package faucet

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"strings"
	"sync"
	"testing"
	"time"
	"tokenswoop/backend/internal/config"
	"tokenswoop/backend/internal/payments"
	"tokenswoop/backend/internal/testutil"
)

type verifyStub struct{ err error }

func (v verifyStub) Verify(context.Context, string, string, string) error { return v.err }

type chainStub struct {
	sign, broadcast int
	err             error
	state           string
	raw             []string
	before          func()
}

func (c *chainStub) Sign(context.Context, Claim) (payments.Signed, error) {
	c.sign++
	return payments.Signed{Raw: "0x1234", Hash: "0x" + strings.Repeat("a", 64), Nonce: "7"}, nil
}
func (c *chainStub) Broadcast(_ context.Context, raw string) error {
	c.broadcast++
	c.raw = append(c.raw, raw)
	if c.before != nil {
		c.before()
	}
	return c.err
}
func (c *chainStub) Confirm(context.Context, Claim) (string, error) { return c.state, nil }
func setup(t *testing.T) (*Service, *chainStub, string, string) {
	t.Helper()
	db := testutil.Database(t)
	_, wallet := testutil.Wallet(t)
	user := uuid.NewString()
	if _, err := db.DB.Exec(context.Background(), `INSERT INTO users(id,wallet) VALUES($1,$2)`, user, wallet); err != nil {
		t.Fatal(err)
	}
	chain := &chainStub{}
	return &Service{Store: db, Config: config.Faucet{Enabled: true, Cooldown: 24 * time.Hour, IPDailyLimit: 10, DailyLimit: 100}, Payment: config.Payments{Enabled: true, ChainID: 10143, Token: "0x" + strings.Repeat("2", 40), Provider: "0x" + strings.Repeat("3", 40)}, Secret: make([]byte, 32), Verify: verifyStub{}, Chain: chain}, chain, user, wallet
}
func TestClaimIsFixedWalletAmountIdempotentAndVerificationGated(t *testing.T) {
	s, _, user, wallet := setup(t)
	ctx := context.Background()
	id := uuid.NewString()
	s.Verify = verifyStub{ErrVerification}
	if _, err := s.Claim(ctx, id, user, wallet, "bad", "127.0.0.1"); !errors.Is(err, ErrVerification) {
		t.Fatal(err)
	}
	var n int
	s.Store.DB.QueryRow(ctx, `SELECT count(*) FROM testnet_faucet_claims`).Scan(&n)
	if n != 0 {
		t.Fatal("invalid token queued a transfer")
	}
	s.Verify = verifyStub{}
	o, err := s.Claim(ctx, id, user, wallet, "token", "127.0.0.1")
	if err != nil || o.Amount != Amount || o.Wallet != wallet || o.ChainID != 10143 {
		t.Fatal(o, err)
	}
	s.Verify = verifyStub{ErrVerification}
	again, err := s.Claim(ctx, id, user, wallet, "used-token", "127.0.0.1")
	if err != nil || again.ID != id {
		t.Fatal("idempotent recovery demanded a new captcha", err)
	}
	s.Verify = verifyStub{}
	if _, err = s.Claim(ctx, uuid.NewString(), user, wallet, "new-token", "127.0.0.1"); !errors.Is(err, ErrCooldown) {
		t.Fatal(err)
	}
	s.Payment.ChainID = 143
	if _, err = s.Claim(ctx, uuid.NewString(), user, wallet, "new-token", "127.0.0.1"); !errors.Is(err, ErrDisabled) {
		t.Fatal("mainnet faucet accepted", err)
	}
	if err = s.Tick(ctx); !errors.Is(err, ErrDisabled) {
		t.Fatal("mainnet worker enabled", err)
	}
}
func TestConcurrentClaimsAndTokenReplayCannotIssueTwice(t *testing.T) {
	s, _, user, wallet := setup(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := uuid.NewString()
			_, err := s.Claim(ctx, id, user, wallet, id, "127.0.0.1")
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrCooldown) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatal(success)
	}
	var tokenDigest string
	s.Store.DB.QueryRow(ctx, `SELECT token_digest FROM testnet_faucet_claims`).Scan(&tokenDigest)
	if len(tokenDigest) == 0 {
		t.Fatal("no replay digest")
	}
	// A failed on-chain transfer permits a new claim, but never reuses its captcha.
	s2, _, u2, w2 := setup(t)
	id := uuid.NewString()
	s2.Claim(ctx, id, u2, w2, "same-token", "127.0.0.1")
	s2.Store.DB.Exec(ctx, `UPDATE testnet_faucet_claims SET state='failed' WHERE id=$1`, id)
	if _, err := s2.Claim(ctx, uuid.NewString(), u2, w2, "same-token", "127.0.0.1"); !errors.Is(err, ErrVerification) {
		t.Fatal("captcha replay accepted", err)
	}
}
func TestDurableFaucetRecoveryUsesSameSignedBytes(t *testing.T) {
	s, c, user, wallet := setup(t)
	ctx := context.Background()
	o, err := s.Claim(ctx, uuid.NewString(), user, wallet, "token", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	c.before = func() {
		var state, raw string
		s.Store.DB.QueryRow(ctx, `SELECT state,raw_tx FROM testnet_faucet_claims WHERE id=$1`, o.ID).Scan(&state, &raw)
		if state != "submitted" || raw != "0x1234" {
			t.Fatal("broadcast before durable write")
		}
	}
	c.err = errors.New("lost response")
	if err = s.Tick(ctx); err == nil {
		t.Fatal("missing broadcast failure")
	}
	pending, err := s.Store.SignerPending(ctx, 10143, s.Payment.Provider)
	if err != nil || !pending {
		t.Fatal("faucet nonce was not reserved", err)
	}
	restarted := *s
	c.err = nil
	if err = restarted.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if c.sign != 1 || c.broadcast != 2 || c.raw[0] != c.raw[1] {
		t.Fatal("recovery re-signed the transfer")
	}
	c.state = "confirmed"
	if err = restarted.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	final, err := s.Get(ctx, o.ID, user)
	if err != nil || final.State != "confirmed" {
		t.Fatal(final, err)
	}
	if err = s.Tick(ctx); err != nil || c.sign != 1 {
		t.Fatal("confirmed claim repeated", err)
	}
	pending, err = s.Store.SignerPending(ctx, 10143, s.Payment.Provider)
	if err != nil || pending {
		t.Fatal("final nonce not released", err)
	}
}
func TestFaucetCapsAndCooldownPersist(t *testing.T) {
	s, _, user, wallet := setup(t)
	ctx := context.Background()
	id := uuid.NewString()
	if _, err := s.Claim(ctx, id, user, wallet, "token1", "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	s.Store.DB.Exec(ctx, `UPDATE testnet_faucet_claims SET state='confirmed' WHERE id=$1`, id)
	s2 := *s
	if _, err := s2.Claim(ctx, uuid.NewString(), user, wallet, "token2", "other-ip"); !errors.Is(err, ErrCooldown) {
		t.Fatal(err)
	}
	s.Store.DB.Exec(ctx, `UPDATE testnet_faucet_claims SET created_at=now()-interval '25 hours' WHERE id=$1`, id)
	if _, err := s2.Claim(ctx, uuid.NewString(), user, wallet, "token2", "other-ip"); err != nil {
		t.Fatal(err)
	}
	_, wallet2 := testutil.Wallet(t)
	user2 := uuid.NewString()
	s.Store.DB.Exec(ctx, `INSERT INTO users(id,wallet) VALUES($1,$2)`, user2, wallet2)
	s.Config.DailyLimit = 1
	if _, err := s.Claim(ctx, uuid.NewString(), user2, wallet2, "token3", "new-ip"); !errors.Is(err, ErrQuota) {
		t.Fatal("global quota not enforced", err)
	}
	s.Config.DailyLimit = 100
	s.Config.IPDailyLimit = 1
	if _, err := s.Claim(ctx, uuid.NewString(), user2, wallet2, "token3", "other-ip"); !errors.Is(err, ErrQuota) {
		t.Fatal("IP quota not enforced", err)
	}
}

func TestFaucetWaitsForPlatformSponsorAndRetainsReviewReservation(t *testing.T) {
	s, chain, user, wallet := setup(t)
	ctx := context.Background()
	o, err := s.Claim(ctx, uuid.NewString(), user, wallet, "token", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	meraID := testutil.PendingMera(t, s.Store, user, 10143, s.Payment.Provider, "submitted")
	if err = s.Tick(ctx); err != nil || chain.sign != 0 {
		t.Fatal("faucet used a reserved sponsor nonce", err)
	}
	if _, err = s.Store.DB.Exec(ctx, `UPDATE mera_transactions SET state='confirmed' WHERE id=$1`, meraID); err != nil {
		t.Fatal(err)
	}
	if err = s.Tick(ctx); err != nil || chain.sign != 1 {
		t.Fatal("faucet did not resume", err)
	}
	chain.state = "review"
	if err = s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if pending, e := s.Store.SignerPending(ctx, 10143, s.Payment.Provider); e != nil || !pending {
		t.Fatal("ambiguous receipt released the nonce", e)
	}
	if _, err = s.Claim(ctx, uuid.NewString(), user, wallet, "token-2", "127.0.0.1"); !errors.Is(err, ErrCooldown) {
		t.Fatal("ambiguous claim could repeat", err)
	}
	if err = s.Tick(ctx); err != nil || chain.sign != 1 {
		t.Fatal("review claim was re-signed", err)
	}
	stored, err := s.Get(ctx, o.ID, user)
	if err != nil || stored.State != "review" {
		t.Fatal(stored, err)
	}
}
