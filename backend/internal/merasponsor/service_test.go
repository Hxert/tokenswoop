package merasponsor

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	"tokenswoop/backend/internal/config"
	"tokenswoop/backend/internal/testutil"
)

type fakeChain struct {
	sign, broadcasts      int
	raw                   []string
	state                 string
	signErr, broadcastErr error
	approvalErr           error
	beforeBroadcast       func()
}

func (f *fakeChain) CheckApproval(context.Context, string, string, string) error {
	return f.approvalErr
}

func (*fakeChain) Prepare(context.Context, string) (*int64, error) { return nil, nil }
func (f *fakeChain) Sign(context.Context, Operation, string, *Authorization) (Signed, error) {
	f.sign++
	return Signed{Raw: "0x1234", Hash: "0x" + strings.Repeat("a", 64), Nonce: "5", Budget: "100"}, f.signErr
}
func (f *fakeChain) Broadcast(_ context.Context, raw string) error {
	f.broadcasts++
	f.raw = append(f.raw, raw)
	if f.beforeBroadcast != nil {
		f.beforeBroadcast()
	}
	return f.broadcastErr
}
func (f *fakeChain) Confirm(context.Context, Operation) (string, error) { return f.state, nil }
func signature(key *secp256k1.PrivateKey, o Operation) string {
	sig := ecdsa.SignCompact(key, o.Digest(), false)
	return "0x" + hex.EncodeToString(append(sig[1:], sig[0]))
}
func setup(t *testing.T) (*Service, *fakeChain, *secp256k1.PrivateKey, string, string) {
	t.Helper()
	s := testutil.Database(t)
	key, wallet := testutil.Wallet(t)
	user := uuid.NewString()
	if _, e := s.DB.Exec(context.Background(), `INSERT INTO users(id,wallet) VALUES($1,$2)`, user, wallet); e != nil {
		t.Fatal(e)
	}
	f := &fakeChain{}
	service := &Service{Store: s, Chain: f, ChainID: 10143, Config: config.Mera{Enabled: true, Implementation: "0x" + strings.Repeat("1", 40), Sponsor: "0x" + strings.Repeat("2", 40), DailyTransactions: 30, DailyGasWei: "10000"}}
	return service, f, key, user, wallet
}
func prepare(t *testing.T, s *Service, user, wallet string) Operation {
	t.Helper()
	o, e := s.Prepare(context.Background(), uuid.NewString(), user, wallet, "reserve_deposit", "1000000")
	if e != nil {
		t.Fatal(e)
	}
	return o
}
func TestDurableBroadcastRecoveryAndIdempotence(t *testing.T) {
	s, f, key, user, wallet := setup(t)
	ctx := context.Background()
	o := prepare(t, s, user, wallet)
	other := uuid.NewString()
	if _, e := s.Get(ctx, o.ID, other); e == nil {
		t.Fatal("cross-user operation exposed")
	}
	if _, e := s.Prepare(ctx, other, user, wallet, o.Action, o.Amount); !errors.Is(e, ErrPending) {
		t.Fatal(e)
	}
	if _, e := s.Submit(ctx, o.ID, user, signature(key, o), nil); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Submit(ctx, o.ID, user, signature(key, o), nil); e != nil {
		t.Fatal(e)
	}
	f.beforeBroadcast = func() {
		var raw, hash, state string
		e := s.Store.DB.QueryRow(ctx, `SELECT raw_tx,tx_hash,state FROM mera_transactions WHERE id=$1`, o.ID).Scan(&raw, &hash, &state)
		if e != nil || raw != "0x1234" || hash == "" || state != "submitted" {
			t.Fatal("broadcast before durable record", e)
		}
	}
	f.broadcastErr = errors.New("response lost after submission")
	if e := s.Tick(ctx); e == nil {
		t.Fatal("expected ambiguous broadcast")
	}
	// Simulate a worker restart. The new worker must reuse the saved signed bytes.
	resumed := *s
	f.broadcastErr = nil
	if e := resumed.Tick(ctx); e != nil {
		t.Fatal(e)
	}
	if f.sign != 1 || f.broadcasts != 2 || f.raw[0] != f.raw[1] {
		t.Fatal("rebuilt a transaction during recovery")
	}
	if e := s.Cancel(ctx, o.ID, user); !errors.Is(e, ErrPending) {
		t.Fatal("cancelled a submitted operation", e)
	}
	f.state = "confirmed"
	if e := resumed.Tick(ctx); e != nil {
		t.Fatal(e)
	}
	final, e := s.Get(ctx, o.ID, user)
	if e != nil || final.State != "confirmed" {
		t.Fatal(final, e)
	}
	if _, e = s.Submit(ctx, o.ID, user, signature(key, o), nil); e != nil {
		t.Fatal(e)
	}
	if e = s.Tick(ctx); e != nil || f.sign != 1 {
		t.Fatal("repeated a confirmed operation", e)
	}
}
func TestPreflightRejectionExpiryAndBudget(t *testing.T) {
	for _, mode := range []string{"simulation", "balance", "expired", "quota", "budget", "review"} {
		t.Run(mode, func(t *testing.T) {
			s, f, key, user, wallet := setup(t)
			ctx := context.Background()
			o := prepare(t, s, user, wallet)
			if mode == "expired" {
				_, e := s.Store.DB.Exec(ctx, `UPDATE mera_transactions SET deadline=$2 WHERE id=$1`, o.ID, time.Now().Unix()-1)
				if e != nil {
					t.Fatal(e)
				}
				old, e := s.Get(ctx, o.ID, user)
				if e != nil || old.State != "expired" {
					t.Fatal(old, e)
				}
				if _, e = s.Pending(ctx, user); e == nil {
					t.Fatal("expired draft blocks future operations")
				}
				return
			}
			if mode == "quota" {
				s.Config.DailyTransactions = 0
				if _, e := s.Submit(ctx, o.ID, user, signature(key, o), nil); !errors.Is(e, ErrQuota) {
					t.Fatal(e)
				}
				return
			}
			if _, e := s.Submit(ctx, o.ID, user, signature(key, o), nil); e != nil {
				t.Fatal(e)
			}
			switch mode {
			case "simulation":
				f.signErr = ErrSimulation
			case "balance":
				f.signErr = ErrInsufficientBalance
			case "budget":
				s.Config.DailyGasWei = "99"
			case "review":
				f.state = "review"
			}
			if e := s.Tick(ctx); e != nil {
				t.Fatal(e)
			}
			result, e := s.Get(ctx, o.ID, user)
			if e != nil {
				t.Fatal(e)
			}
			expected := map[string]string{"simulation": "rejected", "balance": "rejected", "budget": "queued", "review": "review"}[mode]
			if result.State != expected || f.broadcasts != 0 {
				t.Fatal(result, f.broadcasts)
			}
			if mode == "balance" && result.Error != "insufficient_wallet_balance" {
				t.Fatal("balance failure must reach the UI", result.Error)
			}
			if mode == "review" {
				if e = s.Tick(ctx); e != nil || f.sign != 1 {
					t.Fatal("continued after ambiguous receipt")
				}
			}
		})
	}
}

func TestInsufficientApprovalCannotCreateDraftButExistingOperationRemainsRecoverable(t *testing.T) {
	s, f, _, user, wallet := setup(t)
	ctx := context.Background()
	id := uuid.NewString()
	f.approvalErr = ErrInsufficientBalance
	if _, err := s.Prepare(ctx, id, user, wallet, "payment_approve", "10000000"); !errors.Is(err, ErrInsufficientBalance) {
		t.Fatal(err)
	}
	var count int
	if err := s.Store.DB.QueryRow(ctx, `SELECT count(*) FROM mera_transactions`).Scan(&count); err != nil || count != 0 {
		t.Fatal("created an unpayable approval", count, err)
	}
	f.approvalErr = nil
	o, err := s.Prepare(ctx, id, user, wallet, "payment_approve", "10000000")
	if err != nil {
		t.Fatal(err)
	}
	f.approvalErr = ErrInsufficientBalance
	recovered, err := s.Prepare(ctx, id, user, wallet, o.Action, o.Amount)
	if err != nil || recovered.ID != o.ID {
		t.Fatal("balance changes must not hide an existing operation", recovered, err)
	}
}
func TestSignatureBindsWalletActionAmountChainAndOperation(t *testing.T) {
	key, wallet := testutil.Wallet(t)
	o := Operation{Wallet: wallet, Implementation: "0x" + strings.Repeat("1", 40), ChainID: 143, Action: "payment_approve", Amount: "1000000", OperationID: "0x" + strings.Repeat("2", 64), Deadline: 1234567}
	sig := signature(key, o)
	if !o.Verify(sig, nil) {
		t.Fatal("valid signature rejected")
	}
	mutations := []func(*Operation){func(o *Operation) { o.ChainID = 10143 }, func(o *Operation) { o.Amount = "2000000" }, func(o *Operation) { o.Action = "reserve_approve" }, func(o *Operation) { o.Implementation = "0x" + strings.Repeat("3", 40) }, func(o *Operation) { o.OperationID = "0x" + strings.Repeat("4", 64) }, func(o *Operation) { o.Deadline++ }, func(o *Operation) { o.Amount = "invalid" }}
	for _, mutate := range mutations {
		copy := o
		mutate(&copy)
		if copy.Verify(sig, nil) {
			t.Fatal("tampered operation accepted")
		}
	}
	for _, amount := range []string{"-1", "01", "+1", "100000001"} {
		if _, e := Action("payment_approve", amount); e == nil {
			t.Fatal(amount)
		}
	}
	if _, e := Action("transfer", "1"); e == nil {
		t.Fatal("arbitrary transfer enabled")
	}
}
