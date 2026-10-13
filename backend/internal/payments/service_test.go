package payments_test

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"sync"
	"testing"
	"time"
	"tokenswoop/backend/internal/config"
	"tokenswoop/backend/internal/payments"
	"tokenswoop/backend/internal/store"
	"tokenswoop/backend/internal/testutil"
)

type fakeChain struct {
	mu                 sync.Mutex
	balance, allowance string
	signed, broadcast  int
	finalized, revert  bool
	ids                []string
	beforeSend         func()
	validateGate       <-chan struct{}
	validated          chan struct{}
}

func (f *fakeChain) Validate(ctx context.Context) error {
	if f.validated != nil {
		select {
		case f.validated <- struct{}{}:
		default:
		}
	}
	if f.validateGate != nil {
		select {
		case <-f.validateGate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
func (f *fakeChain) Snapshot(context.Context, string) (payments.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return payments.Snapshot{Balance: f.balance, Allowance: f.allowance, Block: "0x10"}, nil
}
func (f *fakeChain) IsCharged(context.Context, payments.Bill) (bool, error) { return false, nil }
func (f *fakeChain) Sign(_ context.Context, b payments.Bill) (payments.Signed, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw := fmt.Sprintf("0x%02x", f.signed+1)
	hash := payments.Keccak([]byte{byte(f.signed + 1)})
	nonce := fmt.Sprint(f.signed)
	f.signed++
	f.ids = append(f.ids, b.ID)
	return payments.Signed{Raw: raw, Hash: hash, Nonce: nonce}, nil
}
func (f *fakeChain) Broadcast(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.beforeSend != nil {
		f.beforeSend()
	}
	f.broadcast++
	return fmt.Errorf("timeout after send")
}
func (f *fakeChain) Confirm(context.Context, payments.Bill, string) (payments.Confirmation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return payments.Confirmation{Finalized: f.finalized, Success: !f.revert, Block: "0x11", BlockHash: "0xconfirmed"}, nil
}
func setup(t *testing.T) (*payments.Service, *fakeChain, string, store.Model) {
	t.Helper()
	s := testutil.Database(t)
	u := testutil.Account(t, s, "0")
	m, e := s.SaveModel(context.Background(), u, testutil.Model())
	if e != nil {
		t.Fatal(e)
	}
	p := config.Payments{Enabled: true, Contract: "0x1111111111111111111111111111111111111111", Token: "0x2222222222222222222222222222222222222222", Provider: "0x3333333333333333333333333333333333333333", Recipient: "0x7777777777777777777777777777777777777777"}
	chain := &fakeChain{balance: "1000000", allowance: "1000000"}
	service := payments.New(s, p, chain, make([]byte, 32))
	tick(t, service)
	if _, e = service.Refresh(context.Background(), u, true); e != nil {
		t.Fatal(e)
	}
	return service, chain, u, m
}
func finish(t *testing.T, s *payments.Service, u string, m store.Model, amount string) string {
	t.Helper()
	id := uuid.NewString()
	ctx := context.Background()
	if e := s.Store.BeginRequest(ctx, id, u, "", m, store.Tier{Concurrency: 10}); e != nil {
		t.Fatal(e)
	}
	if e := s.Store.Finish(ctx, id, "completed", store.Meter{Amount: decimal.RequireFromString(amount), Source: "confirmed fixture"}, "", 200, "", 1); e != nil {
		t.Fatal(e)
	}
	return id
}

func TestCollectionWaitsForSharedMeraTransaction(t *testing.T) {
	for _, state := range []string{"submitted", "review"} {
		t.Run(state, func(t *testing.T) {
			s, chain, user, model := setup(t)
			ctx := context.Background()
			finish(t, s, user, model, "0.01")
			id := testutil.PendingMera(t, s.Store, user, s.Config.NetworkID(), s.Config.Provider, state)
			tick(t, s)
			if chain.signed != 0 {
				t.Fatal("collection reused a nonce reserved by Mera")
			}
			if _, err := s.Store.DB.Exec(ctx, `UPDATE mera_transactions SET state='confirmed' WHERE id=$1`, id); err != nil {
				t.Fatal(err)
			}
			tick(t, s)
			if chain.signed != 1 {
				t.Fatal("collection failed to resume")
			}
		})
	}
}
func tick(t *testing.T, s *payments.Service) {
	t.Helper()
	// Tick legitimately skips when another package or the local development
	// worker owns the database-wide signing lock. Wait for this isolated
	// schema's heartbeat to advance before asserting that its work ran.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var before string
	s.Store.DB.QueryRow(ctx, "SELECT last_seen::text FROM worker_heartbeats WHERE id='settlement'").Scan(&before)
	for {
		if e := s.Tick(ctx); e != nil {
			t.Fatal(e)
		}
		var after string
		s.Store.DB.QueryRow(ctx, "SELECT last_seen::text FROM worker_heartbeats WHERE id='settlement'").Scan(&after)
		if after != "" && after != before {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("settlement fixture could not acquire the signing lane")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func value(t *testing.T, s *payments.Service, query string, args ...any) string {
	t.Helper()
	var out string
	if e := s.Store.DB.QueryRow(context.Background(), query, args...).Scan(&out); e != nil {
		t.Fatal(e)
	}
	return out
}
func money(t *testing.T, got, want string) {
	t.Helper()
	if !decimal.RequireFromString(got).Equal(decimal.RequireFromString(want)) {
		t.Fatalf("got %s want %s", got, want)
	}
}

func admission(t *testing.T, s *payments.Service, user string, model store.Model, want error) {
	t.Helper()
	if _, err := s.Refresh(context.Background(), user, true); err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	ctx := context.Background()
	if err := s.Store.BeginRequest(ctx, id, user, "", model, store.Tier{Concurrency: 10}); err != want {
		t.Fatalf("admission got %v, want %v", err, want)
	}
	if want == nil {
		if err := s.Store.Finish(ctx, id, "failed", store.Meter{}, "", 502, "fixture", 1); err != nil {
			t.Fatal(err)
		}
	}
}
func TestDurablePostpaidSettlementAndFractionalCarry(t *testing.T) {
	s, f, u, m := setup(t)
	ctx := context.Background()
	first := finish(t, s, u, m, "0.0000006")
	tick(t, s)
	if value(t, s, `SELECT count(*)::text FROM bills`) != "0" {
		t.Fatal("rounded a sub-micro request up")
	}
	second := finish(t, s, u, m, "0.0000006")
	var wg sync.WaitGroup
	ids := make(chan string, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, e := s.BuildBill(ctx, u)
			if e != nil {
				t.Error(e)
			}
			ids <- id
		}()
	}
	wg.Wait()
	close(ids)
	id := ""
	for v := range ids {
		if id == "" {
			id = v
		}
		if v != id {
			t.Fatal("concurrent issuance duplicated debt")
		}
	}
	b, e := s.Bill(ctx, u, id)
	if e != nil {
		t.Fatal(e)
	}
	money(t, b.Amount, "0.000001")
	if b.Recipient != s.Config.Recipient || b.Recipient == b.Provider {
		t.Fatal("bill lost separate revenue destination")
	}
	if _, e = s.Store.DB.Exec(ctx, "UPDATE bills SET recipient=provider WHERE id=$1", id); e == nil {
		t.Fatal("bill recipient is mutable")
	}
	if _, e = s.Store.DB.Exec(ctx, `UPDATE bills SET amount=2,amount_units=2000000 WHERE id=$1`, id); e == nil {
		t.Fatal("mutable bill terms")
	}
	money(t, value(t, s, `SELECT unpaid_balance::text FROM users WHERE id=$1`, u), "0.0000012")
	// Merely queued debt must not block the next call or debit/hold funds.
	admitted := uuid.NewString()
	if e = s.Store.BeginRequest(ctx, admitted, u, "", m, store.Tier{Concurrency: 10}); e != nil {
		t.Fatal(e)
	}
	if e = s.Store.Finish(ctx, admitted, "failed", store.Meter{}, "", 502, "upstream", 1); e != nil {
		t.Fatal(e)
	}
	f.beforeSend = func() {
		if value(t, s, `SELECT count(*)::text FROM payment_attempts WHERE bill_id=$1 AND raw_cipher NOT LIKE '0x%'`, id) != "1" {
			t.Fatal("broadcast before encrypted journal commit")
		}
	}
	tick(t, s)
	if f.signed != 1 || f.broadcast != 1 {
		t.Fatal("payment not sent")
	}
	// A fresh process recovers from an unknown RPC outcome using the same bytes.
	recovered := payments.New(s.Store, s.Config, f, make([]byte, 32))
	tick(t, recovered)
	if f.signed != 1 || f.broadcast != 2 {
		t.Fatal("recovery signed a replacement")
	}
	money(t, value(t, s, `SELECT unpaid_balance::text FROM users WHERE id=$1`, u), "0.0000012")
	f.finalized = true
	tick(t, recovered)
	tick(t, recovered)
	b, e = s.Bill(ctx, u, id)
	if e != nil || b.State != "paid" {
		t.Fatal("bill not paid", e)
	}
	money(t, value(t, s, `SELECT unpaid_balance::text FROM users WHERE id=$1`, u), "0.0000002")
	money(t, value(t, s, `SELECT wallet_paid_amount::text FROM requests WHERE id=$1`, first), "0.0000006")
	money(t, value(t, s, `SELECT unpaid_amount::text FROM requests WHERE id=$1`, second), "0.0000002")
	if f.signed != 1 {
		t.Fatal("paid bill charged twice")
	}
	f.beforeSend = nil
	f.finalized = false
	if _, e = s.Refresh(ctx, u, true); e != nil {
		t.Fatal(e)
	}
	finish(t, s, u, m, "0.0000008")
	ageDebt(t, s, u)
	tick(t, s)
	f.finalized = true
	tick(t, s)
	money(t, value(t, s, `SELECT unpaid_balance::text FROM users WHERE id=$1`, u), "0")
	money(t, value(t, s, `SELECT sum(wallet_paid_amount)::text FROM requests WHERE user_id=$1`, u), "0.000002")
	if f.signed != 2 || f.ids[0] == f.ids[1] {
		t.Fatal("incorrect carry settlement")
	}
}
func TestFailedAllowanceRecoveryAndFinalizedRevertReuseBill(t *testing.T) {
	s, f, u, m := setup(t)
	ctx := context.Background()
	finish(t, s, u, m, "0.00003")
	ageDebt(t, s, u)
	f.allowance = "0"
	tick(t, s)
	id := value(t, s, `SELECT id FROM bills WHERE user_id=$1`, u)
	b, _ := s.Bill(ctx, u, id)
	if b.State != "blocked" || b.Error != "insufficient_allowance" || f.signed != 0 {
		t.Fatal("missing allowance was charged")
	}
	admission(t, s, u, m, store.ErrSettlementFailed)
	f.allowance = "1000"
	if _, e := s.Refresh(ctx, u, true); e != nil {
		t.Fatal(e)
	}
	if e := s.Retry(ctx, u, id); e != nil {
		t.Fatal(e)
	}
	admission(t, s, u, m, store.ErrSettlementFailed)
	tick(t, s)
	admission(t, s, u, m, store.ErrSettlementFailed)
	f.finalized = true
	f.revert = true
	tick(t, s)
	b, _ = s.Bill(ctx, u, id)
	if b.Error != "transaction_reverted" {
		t.Fatal("revert not recorded")
	}
	admission(t, s, u, m, store.ErrSettlementFailed)
	money(t, value(t, s, `SELECT unpaid_balance::text FROM users WHERE id=$1`, u), "0.00003")
	f.finalized = false
	f.revert = false
	if e := s.Retry(ctx, u, id); e != nil {
		t.Fatal(e)
	}
	tick(t, s)
	f.finalized = true
	tick(t, s)
	if f.signed != 2 || f.ids[0] != f.ids[1] {
		t.Fatal("revert changed bill identity")
	}
	if value(t, s, `SELECT count(*)::text FROM payment_attempts WHERE state='reverted'`) != "1" {
		t.Fatal("revert history lost")
	}
	money(t, value(t, s, `SELECT unpaid_balance::text FROM users WHERE id=$1`, u), "0")
	admission(t, s, u, m, nil)
}
func TestCallsContinueBeforeAndDuringSettlement(t *testing.T) {
	s, f, u, m := setup(t)
	ctx := context.Background()
	// Confirmed outstanding usage is covered by allowance; wallet balance and
	// worker heartbeat do not determine admission.
	f.allowance = "10000000"
	finish(t, s, u, m, "2")
	if _, e := s.Store.DB.Exec(ctx, `UPDATE payment_accounts SET balance=0,allowance=0,checked_at=now()-interval '1 minute'; DELETE FROM worker_heartbeats`); e != nil {
		t.Fatal(e)
	}
	admission(t, s, u, m, nil)
	id, err := s.BuildBill(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	admission(t, s, u, m, nil)
	f.balance, f.allowance = "10000000", "10000000"
	tick(t, s)
	b, err := s.Bill(ctx, u, id)
	if err != nil || b.State != "submitted" || b.InsufficientBalanceAt != nil {
		t.Fatal("ordinary settlement did not submit", b, err)
	}
	admission(t, s, u, m, nil)
	// Even an empty wallet during an unconfirmed transfer is not a failure.
	f.balance = "0"
	if _, err = s.Refresh(ctx, u, true); err != nil {
		t.Fatal(err)
	}
	admission(t, s, u, m, nil)
	finish(t, s, u, m, "0.2")
	f.finalized = true
	tick(t, s)
	admission(t, s, u, m, nil)
	money(t, value(t, s, `SELECT unpaid_balance::text FROM users WHERE id=$1`, u), "0.2")
}

func TestFundingFailureBlocksNewCallsUntilFailedBillIsPaid(t *testing.T) {
	s, f, u, m := setup(t)
	ctx := context.Background()
	finish(t, s, u, m, "0.02")
	inFlight := uuid.NewString()
	if err := s.Store.BeginRequest(ctx, inFlight, u, "", m, store.Tier{Concurrency: 10}); err != nil {
		t.Fatal(err)
	}
	f.balance = "0"
	tick(t, s)
	id := value(t, s, `SELECT id FROM bills WHERE user_id=$1`, u)
	b, err := s.Bill(ctx, u, id)
	if err != nil || b.Error != "insufficient_balance" || b.InsufficientBalanceAt == nil || f.signed != 0 {
		t.Fatal("balance failure not persisted", b, err)
	}
	admission(t, s, u, m, store.ErrSettlementFailed)
	// Accounts and zero-priced models remain independent of the failed bill.
	other := testutil.Account(t, s.Store, "0")
	admission(t, s, other, m, nil)
	free := m
	free.InputPrice, free.OutputPrice, free.CacheReadPrice, free.CacheWritePrice, free.CacheWrite1hPrice = "0", "0", "0", "0", "0"
	admission(t, s, u, free, nil)
	for i := 0; i < 2; i++ {
		if err = s.Store.Finish(ctx, inFlight, "completed", store.Meter{Amount: decimal.RequireFromString("0.03"), Source: "confirmed fixture"}, "", 200, "", 1); err != nil {
			t.Fatal("failure interrupted an accepted request", err)
		}
	}
	money(t, value(t, s, `SELECT unpaid_balance::text FROM users WHERE id=$1`, u), "0.05")
	f.balance, f.allowance = "1000000", "0"
	if _, err = s.Refresh(ctx, u, true); err != nil {
		t.Fatal(err)
	}
	if err = s.Retry(ctx, u, id); err != nil {
		t.Fatal(err)
	}
	admission(t, s, u, m, store.ErrSettlementFailed)
	tick(t, s) // A different error must not erase the balance failure.
	b, _ = s.Bill(ctx, u, id)
	if b.Error != "insufficient_allowance" || b.InsufficientBalanceAt == nil {
		t.Fatal("retry lost the original failure", b)
	}
	f.allowance = "1000000"
	recovered := payments.New(s.Store, s.Config, f, make([]byte, 32))
	if err = recovered.Retry(ctx, u, id); err != nil {
		t.Fatal(err)
	}
	admission(t, recovered, u, m, store.ErrSettlementFailed)
	tick(t, recovered)
	b, _ = recovered.Bill(ctx, u, id)
	if b.State != "submitted" || b.Error != "" || b.InsufficientBalanceAt == nil {
		t.Fatal("failed bill recovery did not persist through submission", b)
	}
	admission(t, recovered, u, m, store.ErrSettlementFailed)
	f.finalized = true
	tick(t, recovered)
	admission(t, recovered, u, m, nil)
	money(t, value(t, s, `SELECT unpaid_balance::text FROM users WHERE id=$1`, u), "0.03")
	money(t, value(t, s, `SELECT sum(wallet_paid_amount)::text FROM requests WHERE user_id=$1`, u), "0.02")
	if f.signed != 1 || f.ids[0] != id {
		t.Fatal("recovery duplicated the bill")
	}
}

func TestFinalizedRevertWithInsufficientBalanceBlocksNewCalls(t *testing.T) {
	s, f, u, m := setup(t)
	ctx := context.Background()
	finish(t, s, u, m, "0.02")
	tick(t, s)
	admission(t, s, u, m, nil)
	f.balance, f.finalized, f.revert = "0", true, true
	tick(t, s)
	id := value(t, s, `SELECT id FROM bills WHERE user_id=$1`, u)
	b, err := s.Bill(ctx, u, id)
	if err != nil || b.Error != "insufficient_balance" || b.InsufficientBalanceAt == nil {
		t.Fatal("finalized failure lost insufficient-balance evidence", b, err)
	}
	admission(t, s, u, m, store.ErrSettlementFailed)
}

func TestManyTinyRequestsDoNotStarveAggregation(t *testing.T) {
	s, _, u, m := setup(t)
	ctx := context.Background()
	// Six hundred charges together exceed one micro-unit, even though the first
	// five hundred do not. A fixed row batch must not strand the account's debt.
	if _, e := s.Store.DB.Exec(ctx, `INSERT INTO requests(id,user_id,model_id,state,amount,unpaid_amount,price_snapshot,tier_snapshot) SELECT gen_random_uuid(),$1,$2,'completed',0.0000000018,0.0000000018,'{}','{}' FROM generate_series(1,600)`, u, m.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Store.DB.Exec(ctx, `UPDATE users SET unpaid_balance=0.00000108 WHERE id=$1`, u); e != nil {
		t.Fatal(e)
	}
	id, e := s.BuildBill(ctx, u)
	if e != nil || id == "" {
		t.Fatal("tiny charges stranded", e)
	}
	b, e := s.Bill(ctx, u, id)
	if e != nil {
		t.Fatal(e)
	}
	money(t, b.Amount, "0.000001")
}
