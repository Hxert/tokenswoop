package faucet

// Explicitly opted-in smoke test. No real RPC, signer, or transfer is used by
// ordinary test runs. CAPTCHA validation is tested separately in turnstile_test.go.
import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"tokenswoop/backend/internal/config"
	"tokenswoop/backend/internal/payments"
	"tokenswoop/backend/internal/testutil"
)

type smokeChain struct {
	*RPCChain
	path     string
	baseline string
	lost     bool
}
type smokeJournal struct {
	Claim    Claim
	Signed   payments.Signed
	Baseline string
}

func (c *smokeChain) Sign(ctx context.Context, o Claim) (payments.Signed, error) {
	if data, err := os.ReadFile(c.path); err == nil {
		var j smokeJournal
		if json.Unmarshal(data, &j) != nil || j.Claim.Wallet != o.Wallet || j.Claim.Token != o.Token || j.Claim.Sponsor != o.Sponsor || j.Claim.Amount != o.Amount || j.Claim.ChainID != o.ChainID {
			return payments.Signed{}, errors.New("smoke journal mismatch")
		}
		c.baseline = j.Baseline
		return j.Signed, nil
	} else if !os.IsNotExist(err) {
		return payments.Signed{}, err
	}
	signed, err := c.RPCChain.Sign(ctx, o)
	if err != nil {
		return signed, err
	}
	data, _ := json.Marshal(smokeJournal{o, signed, c.baseline})
	return signed, os.WriteFile(c.path, data, 0600)
}
func (c *smokeChain) Broadcast(ctx context.Context, raw string) error {
	if err := c.RPCChain.Broadcast(ctx, raw); err != nil {
		return err
	}
	if !c.lost {
		c.lost = true
		return errors.New("simulated lost broadcast response")
	}
	return nil
}

func TestLiveTestnetFaucetSmoke(t *testing.T) {
	file := os.Getenv("FAUCET_SMOKE_CONFIG_FILE")
	if file == "" {
		t.Skip("explicit testnet smoke configuration required")
	}
	var cfg struct {
		Payment                 config.Payments
		Wallet, Journal, Report string
	}
	data, err := os.ReadFile(file)
	if err != nil || json.Unmarshal(data, &cfg) != nil {
		t.Fatal("invalid private smoke configuration")
	}
	if cfg.Payment.ChainID != 10143 || strings.EqualFold(cfg.Payment.Token, config.MonadUSDC) {
		t.Fatal("testnet only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	rpc := payments.NewChain(cfg.Payment)
	if err = rpc.Validate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = rpc.ValidateSigner(ctx); err != nil {
		t.Fatal(err)
	}
	balance := func() string {
		var out string
		if err := rpc.RPC(ctx, "eth_call", []any{map[string]string{"to": cfg.Payment.Token, "data": "0x70a08231" + word(cfg.Wallet)}, "finalized"}, &out); err != nil {
			t.Fatal(err)
		}
		n, ok := new(big.Int).SetString(strings.TrimPrefix(out, "0x"), 16)
		if !ok {
			t.Fatal("invalid token balance")
		}
		return n.String()
	}
	c := &smokeChain{RPCChain: &RPCChain{RPC: rpc}, path: cfg.Journal, baseline: balance()}
	db := testutil.Database(t)
	user := uuid.NewString()
	cfg.Wallet = strings.ToLower(cfg.Wallet)
	if _, err = db.DB.Exec(ctx, `INSERT INTO users(id,wallet) VALUES($1,$2)`, user, cfg.Wallet); err != nil {
		t.Fatal(err)
	}
	s := &Service{Store: db, Config: config.Faucet{Enabled: true, Cooldown: 24 * time.Hour, IPDailyLimit: 10, DailyLimit: 100}, Payment: cfg.Payment, Secret: make([]byte, 32), Verify: verifyStub{}, Chain: c}
	o, err := s.Claim(ctx, uuid.NewString(), user, cfg.Wallet, uuid.NewString(), "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	for ctx.Err() == nil {
		_ = s.Tick(ctx) // A successful send deliberately loses its HTTP response once.
		stored, e := s.Get(ctx, o.ID, user)
		if e != nil {
			t.Fatal(e)
		}
		if stored.State == "failed" || stored.State == "review" {
			t.Fatal("unexpected terminal claim", stored.State)
		}
		if stored.State == "confirmed" {
			before, _ := new(big.Int).SetString(c.baseline, 10)
			after, _ := new(big.Int).SetString(balance(), 10)
			if new(big.Int).Sub(after, before).Cmp(big.NewInt(10000000)) != 0 {
				t.Fatal("recipient did not receive exactly 10 kUSDC")
			}
			if _, e = s.Claim(ctx, stored.ID, user, cfg.Wallet, "consumed-token", "127.0.0.1"); e != nil {
				t.Fatal(e)
			}
			if e = s.Tick(ctx); e != nil {
				t.Fatal(e)
			}
			if pending, e := db.SignerPending(ctx, 10143, cfg.Payment.Provider); e != nil || pending {
				t.Fatal("confirmed nonce not released")
			}
			report, _ := json.MarshalIndent(map[string]any{"claim": stored, "recipient_delta_units": "10000000", "same_id_retry": "passed", "broadcast_recovery": "passed", "turnstile": "validated separately; live challenge not exercised", "checked_at": time.Now().UTC()}, "", "  ")
			if e = os.WriteFile(cfg.Report, report, 0600); e != nil {
				t.Fatal(e)
			}
			t.Log("finalized testnet faucet transfer", stored.Hash)
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatal("smoke test timed out; retain private transaction journal for recovery")
}
