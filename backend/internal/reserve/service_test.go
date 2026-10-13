package reserve

import (
	"context"
	"errors"
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
	mu                         sync.Mutex
	v                          Snapshot
	signed, sent               int
	final, revert, unavailable bool
	approval                   Approval
}

func (f *fakeChain) Validate(context.Context) error { return nil }
func (f *fakeChain) Snapshot(context.Context, string) (Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unavailable {
		return Snapshot{}, fmt.Errorf("RPC down")
	}
	return f.v, nil
}
func (f *fakeChain) SignApproval(_ context.Context, a Approval) (payments.Signed, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.signed++
	f.approval = a
	raw := []byte{byte(f.signed)}
	return payments.Signed{Raw: fmt.Sprintf("0x%02x", f.signed), Hash: payments.Keccak(raw), Nonce: fmt.Sprint(f.signed)}, nil
}
func (f *fakeChain) Broadcast(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent++
	return fmt.Errorf("timeout after broadcast")
}
func (f *fakeChain) Confirm(context.Context, Approval) (payments.Confirmation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.final && !f.revert {
		f.v.Withdrawal.ApprovedUntil = f.approval.Until
		f.v.Withdrawal.OperatorVersion = f.v.OperatorVersion
	}
	return payments.Confirmation{Finalized: f.final, Success: !f.revert, Block: "0x11", BlockHash: "0xconfirmed"}, nil
}
func setup(t *testing.T) (*Service, *fakeChain, string, store.Model) {
	t.Helper()
	s := testutil.Database(t)
	u := testutil.Account(t, s, "100")
	m, e := s.SaveModel(context.Background(), u, testutil.Model())
	if e != nil {
		t.Fatal(e)
	}
	f := &fakeChain{v: Snapshot{Principal: "50000000", Balance: "100000000", Allowance: "50000000", Block: "0x10", BlockHash: "0xfinalized", OperatorVersion: "1", Withdrawal: Withdrawal{ID: "0", Amount: "0", OperatorVersion: "0"}}}
	r := New(s, config.Reserve{Enabled: true, Contract: "0x1111111111111111111111111111111111111111", Operator: "0x2222222222222222222222222222222222222222"}, "0x3333333333333333333333333333333333333333", f, make([]byte, 32))
	threshold := "50"
	if _, e = s.SaveTier(context.Background(), u, store.Tier{ID: "builder", MinPrincipal: &threshold, DiscountBPS: 300, RPM: 120, Concurrency: 5}); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Refresh(context.Background(), u, true); e != nil {
		t.Fatal(e)
	}
	return r, f, u, m
}
func tick(t *testing.T, s *Service) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	var before string
	s.Store.DB.QueryRow(ctx, `SELECT last_seen::text FROM worker_heartbeats WHERE id='reserve'`).Scan(&before)
	for {
		if e := s.Tick(ctx); e != nil {
			t.Fatal(e)
		}
		var after string
		s.Store.DB.QueryRow(ctx, `SELECT last_seen::text FROM worker_heartbeats WHERE id='reserve'`).Scan(&after)
		if after != "" && after != before {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("signing lock timeout")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func begin(t *testing.T, s *Service, u string, m store.Model) string {
	t.Helper()
	ctx := context.Background()
	account, e := s.Store.User(ctx, u)
	if e != nil {
		t.Fatal(e)
	}
	tier, e := s.Store.Tier(ctx, account.Tier)
	if e != nil {
		t.Fatal(e)
	}
	id := uuid.NewString()
	if e = s.Store.BeginRequest(ctx, id, u, "", m, tier); e != nil {
		t.Fatal(e)
	}
	return id
}
func sql(t *testing.T, s *Service, q string, args ...any) {
	t.Helper()
	if _, e := s.Store.DB.Exec(context.Background(), q, args...); e != nil {
		t.Fatal(e)
	}
}
func TestTierWithdrawalFreezeDrainAndRecovery(t *testing.T) {
	s, f, u, m := setup(t)
	ctx := context.Background()
	user, _ := s.Store.User(ctx, u)
	if user.Tier != "builder" {
		t.Fatalf("principal did not assign builder: %+v", user)
	}
	first := begin(t, s, u, m)
	second := begin(t, s, u, m)
	f.v.Withdrawal = Withdrawal{ID: "1", Amount: "1000000", Active: true, OperatorVersion: "0"}
	if _, e := s.Refresh(ctx, u, true); e != nil {
		t.Fatal(e)
	}
	tier, _ := s.Store.Tier(ctx, "builder")
	if e := s.Store.BeginRequest(ctx, uuid.NewString(), u, "", m, tier); !errors.Is(e, store.ErrWithdrawalPending) {
		t.Fatalf("wallet-funded request bypassed freeze: %v", e)
	}
	tick(t, s)
	if f.signed != 0 {
		t.Fatal("approved with in-flight requests")
	}
	if e := s.Store.Finish(ctx, first, "completed", store.Meter{Amount: decimal.RequireFromString("0.1")}, "", 200, "", 1); e != nil {
		t.Fatal(e)
	}
	if e := s.Store.Finish(ctx, second, "usage_unknown", store.Meter{}, "", 502, "", 1); e != nil {
		t.Fatal(e)
	}
	tick(t, s)
	if f.signed != 0 {
		t.Fatal("approved unresolved usage")
	}
	// Settle the confirmed request before the withdrawal can be approved.
	sql(t, s, `UPDATE requests SET wallet_paid_amount=amount,unpaid_amount=0 WHERE id=$1`, first)
	sql(t, s, `UPDATE users SET unpaid_balance=0 WHERE id=$1`, u)
	if e := s.Store.ResolveUnknown(ctx, u, second); e != nil {
		t.Fatal(e)
	}
	tick(t, s)
	if f.signed != 1 || f.sent != 1 {
		t.Fatal("did not persist and send approval")
	}
	// Restart with the same key and DB. Unknown broadcast outcomes reuse the hash.
	restarted := New(s.Store, s.Config, s.Token, f, make([]byte, 32))
	tick(t, restarted)
	if f.signed != 1 || f.sent != 2 {
		t.Fatal("restart signed a replacement")
	}
	f.final = true
	tick(t, restarted)
	// Approval alone must never unfreeze admission, including after expiry.
	f.v.Withdrawal.ApprovedUntil = time.Now().Unix() - 1
	s.Refresh(ctx, u, true)
	if e := s.Store.BeginRequest(ctx, uuid.NewString(), u, "", m, tier); !errors.Is(e, store.ErrWithdrawalPending) {
		t.Fatalf("expiry lifted freeze: %v", e)
	}
	f.v.Withdrawal.Active = false
	f.v.Principal = "49000000"
	s.Refresh(ctx, u, true)
	user, _ = s.Store.User(ctx, u)
	if user.Tier != "free" {
		t.Fatal("partial withdrawal failed to downgrade")
	}
	begin(t, s, u, m)
	var snapshotTier string
	if e := s.Store.DB.QueryRow(ctx, `SELECT tier_snapshot->>'discount_bps' FROM requests WHERE id=$1`, first).Scan(&snapshotTier); e != nil || snapshotTier != "300" {
		t.Fatalf("original discount snapshot changed: %s %v", snapshotTier, e)
	}
}
func TestClearanceRequiresEveryDebtAndWaivesOnlyDust(t *testing.T) {
	s, f, u, m := setup(t)
	ctx := context.Background()
	id := begin(t, s, u, m)
	if e := s.Store.Finish(ctx, id, "completed", store.Meter{Amount: decimal.RequireFromString("0.0100004")}, "", 200, "", 1); e != nil {
		t.Fatal(e)
	}
	f.v.Withdrawal = Withdrawal{ID: "1", Amount: "50000000", Active: true, OperatorVersion: "0"}
	s.Refresh(ctx, u, true)
	tick(t, s)
	if f.signed != 0 {
		t.Fatal("approved outstanding fees")
	}
	// Model a finalized payment, leaving the exact fractional remainder.
	sql(t, s, `UPDATE requests SET wallet_paid_amount=0.01,unpaid_amount=0.0000004 WHERE id=$1`, id)
	sql(t, s, `UPDATE users SET unpaid_balance=0.0000004 WHERE id=$1`, u)
	tick(t, s)
	if f.signed != 1 {
		t.Fatal("dust prevented exit")
	}
	var unpaid, waived string
	if e := s.Store.DB.QueryRow(ctx, `SELECT unpaid_amount::text,waived_amount::text FROM requests WHERE id=$1`, id).Scan(&unpaid, &waived); e != nil {
		t.Fatal(e)
	}
	if !decimal.RequireFromString(unpaid).IsZero() || !decimal.RequireFromString(waived).Equal(decimal.RequireFromString("0.0000004")) {
		t.Fatal("dust adjustment lost", unpaid, waived)
	}
	var count int
	s.Store.DB.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action='reserve.dust_waived'`).Scan(&count)
	if count != 1 {
		t.Fatal("missing waiver audit")
	}
}
func TestReserveStaleEvidenceOverridesAndPolicyVersions(t *testing.T) {
	s, f, u, m := setup(t)
	ctx := context.Background()
	old, _ := s.Store.Tier(ctx, "builder")
	changed := old
	changed.DiscountBPS = 800
	if _, e := s.Store.SaveTier(ctx, u, changed); e != nil {
		t.Fatal(e)
	}
	if e := s.Store.BeginRequest(ctx, uuid.NewString(), u, "", m, old); !errors.Is(e, store.ErrReserveStale) {
		t.Fatalf("outdated policy admitted: %v", e)
	}
	current, _ := s.Store.Tier(ctx, "builder")
	if current.Version != 2 {
		t.Fatal("tier revision missing")
	}
	sql(t, s, `UPDATE reserve_accounts SET checked_at=now()-interval '30 seconds' WHERE user_id=$1`, u)
	if e := s.Store.BeginRequest(ctx, uuid.NewString(), u, "", m, current); !errors.Is(e, store.ErrReserveStale) {
		t.Fatal("stale reserve admitted", e)
	}
	f.unavailable = true
	if _, e := s.Refresh(ctx, u, true); e == nil {
		t.Fatal("RPC failure ignored")
	}
	f.unavailable = false
	sql(t, s, `UPDATE users SET tier_override=true WHERE id=$1`, u)
	f.v.Principal = "0"
	s.Refresh(ctx, u, true)
	user, _ := s.Store.User(ctx, u)
	if user.Tier != "builder" {
		t.Fatal("operator override overwritten")
	}
	// A late read from an older finalized block cannot remove the pending gate.
	f.v.Block = "0x20"
	f.v.BlockHash = "0xnew"
	f.v.Withdrawal.Active = true
	s.Refresh(ctx, u, true)
	f.v.Block = "0x19"
	f.v.Withdrawal.Active = false
	s.Refresh(ctx, u, true)
	if e := s.Store.BeginRequest(ctx, uuid.NewString(), u, "", m, current); !errors.Is(e, store.ErrWithdrawalPending) {
		t.Fatal("older block removed withdrawal freeze", e)
	}
}
