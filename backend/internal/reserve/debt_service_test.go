package reserve

import (
	"context"
	"fmt"
	"github.com/shopspring/decimal"
	"testing"
	"tokenswoop/backend/internal/payments"
	"tokenswoop/backend/internal/store"
	"tokenswoop/backend/internal/testutil"
)

type fakeDebtChain struct {
	*fakeChain
	report    DebtReport
	confirmed bool
	reverted  bool
}

func (f *fakeDebtChain) SignDebt(_ context.Context, a DebtReport) (payments.Signed, error) {
	f.report = a
	f.signed++
	raw := []byte{byte(f.signed)}
	return payments.Signed{Raw: fmt.Sprintf("0x%02x", f.signed), Hash: payments.Keccak(raw), Nonce: fmt.Sprint(f.signed)}, nil
}
func (f *fakeDebtChain) ConfirmDebt(context.Context, DebtReport) (payments.Confirmation, error) {
	return payments.Confirmation{Finalized: f.confirmed, Success: !f.reverted, Block: "0x10", BlockHash: "0xfinalized"}, nil
}

func TestDebtReportWaitsForSharedMeraTransaction(t *testing.T) {
	s, f, user, model := setup(t)
	s.Config.Mode, s.Store.ReserveDebtMode = "debt", true
	d := &fakeDebtChain{fakeChain: f}
	s.Chain = d
	f.v.DebtEpoch = "0"
	ctx := context.Background()
	id := begin(t, s, user, model)
	if err := s.Store.Finish(ctx, id, "completed", store.Meter{Amount: decimal.RequireFromString("0.2")}, "", 200, "", 1); err != nil {
		t.Fatal(err)
	}
	sql(t, s, `INSERT INTO bills(id,user_id,wallet,chain_id,contract,token,provider,amount,amount_units,state,error_code,insufficient_balance_at) SELECT '0x'||repeat('d',64),id,wallet,10143,'p','t','s',0.2,200000,'blocked','insufficient_allowance',now() FROM users WHERE id=$1`, user)
	meraID := testutil.PendingMera(t, s.Store, user, s.Config.NetworkID(), s.Config.Operator, "submitted")
	tick(t, s)
	if d.signed != 0 {
		t.Fatal("debt report reused a nonce reserved by Mera")
	}
	sql(t, s, `UPDATE mera_transactions SET state='confirmed' WHERE id=$1`, meraID)
	tick(t, s)
	if d.signed != 1 {
		t.Fatal("debt report failed to resume")
	}
}

func TestManagementReleaseWinsAgainstPendingDebtReport(t *testing.T) {
	s, f, u, m := setup(t)
	s.Config.Mode = "debt"
	s.Store.ReserveDebtMode = true
	d := &fakeDebtChain{fakeChain: f}
	s.Chain = d
	f.v.DebtEpoch = "0"
	id := begin(t, s, u, m)
	if err := s.Store.Finish(context.Background(), id, "completed", store.Meter{Amount: decimal.RequireFromString("0.2")}, "", 200, "", 1); err != nil {
		t.Fatal(err)
	}
	sql(t, s, `INSERT INTO bills(id,user_id,wallet,chain_id,contract,token,provider,amount,amount_units,state,error_code,insufficient_balance_at) SELECT '0x'||repeat('e',64),id,wallet,10143,'p','t','s',0.2,200000,'blocked','insufficient_allowance',now() FROM users WHERE id=$1`, u)
	tick(t, s)
	if d.signed != 1 {
		t.Fatal("expected pending report")
	}
	// The manager clears before the signed transaction is included.
	f.v.DebtEpoch = "1"
	d.confirmed, d.reverted = true, true
	tick(t, s)
	tick(t, s)
	if d.signed != 1 {
		t.Fatal("re-reported debt released by management")
	}
}

func TestDebtReportRecoveryRepaymentAndManagementRelease(t *testing.T) {
	s, f, u, m := setup(t)
	ctx := context.Background()
	d := &fakeDebtChain{fakeChain: f}
	s.Chain = d
	s.Config.Mode = "debt"
	s.Store.ReserveDebtMode = true
	f.v.DebtEpoch = "0"
	f.v.Debt = "0"
	f.v.Frozen = "0"
	f.v.Withdrawable = f.v.Principal
	id := begin(t, s, u, m)
	if err := s.Store.Finish(ctx, id, "completed", store.Meter{Amount: decimal.RequireFromString("0.2")}, "", 200, "", 1); err != nil {
		t.Fatal(err)
	}
	// Normal unsettled usage does not freeze collateral.
	tick(t, s)
	if d.signed != 0 {
		t.Fatal("reported normal pending usage")
	}
	sql(t, s, `INSERT INTO bills(id,user_id,wallet,chain_id,contract,token,provider,amount,amount_units,state,error_code,insufficient_balance_at) SELECT '0x'||repeat('a',64),id,wallet,10143,'p','t','s',0.2,200000,'blocked','insufficient_allowance',now() FROM users WHERE id=$1`, u)
	tick(t, s)
	if d.signed != 1 || len(d.report.Items) != 1 || d.report.Items[0].Amount != "200000" {
		t.Fatal("missing exact confirmed debt", d.report)
	}
	first := d.report.Hash
	tick(t, s)
	if d.signed != 1 || d.report.Hash != first {
		t.Fatal("unknown broadcast signed a duplicate")
	}
	d.confirmed = true
	tick(t, s)
	var count int
	s.Store.DB.QueryRow(ctx, `SELECT count(*) FROM reserve_debt_items`).Scan(&count)
	if count != 1 {
		t.Fatal("missing confirmed report")
	}
	// Paying the ledger releases the same request, rather than clearing all debt.
	sql(t, s, `UPDATE requests SET unpaid_amount=0,wallet_paid_amount=amount WHERE id=$1`, id)
	sql(t, s, `UPDATE users SET unpaid_balance=0 WHERE id=$1`, u)
	sql(t, s, `UPDATE bills SET state='paid' WHERE user_id=$1`, u)
	d.confirmed = false
	tick(t, s)
	if d.signed != 2 || d.report.Items[0].Amount != "0" {
		t.Fatal("payment did not release debt", d.report)
	}
	d.confirmed = true
	tick(t, s)

	// A subsequent unpaid request is reported, then cleared by management.
	second := begin(t, s, u, m)
	if err := s.Store.Finish(ctx, second, "completed", store.Meter{Amount: decimal.RequireFromString("0.1")}, "", 200, "", 1); err != nil {
		t.Fatal(err)
	}
	sql(t, s, `INSERT INTO bills(id,user_id,wallet,chain_id,contract,token,provider,amount,amount_units,state,error_code,insufficient_balance_at) SELECT '0x'||repeat('c',64),id,wallet,10143,'p','t','s',0.1,100000,'blocked','insufficient_allowance',now() FROM users WHERE id=$1`, u)
	d.confirmed = false
	tick(t, s)
	if d.signed != 3 {
		t.Fatal("new debt not reported")
	}
	d.confirmed = true
	tick(t, s)
	f.v.DebtEpoch = "1"
	tick(t, s)
	if d.signed != 3 {
		t.Fatal("manager release undone by old request")
	}

}

func TestDebtModeDoesNotFreezeAdmissionForWithdrawals(t *testing.T) {
	s, f, u, m := setup(t)
	s.Store.ReserveDebtMode = true
	f.v.Withdrawal.Active = true
	if _, err := s.Refresh(context.Background(), u, true); err != nil {
		t.Fatal(err)
	}
	begin(t, s, u, m)
}

func TestDebtReportRequiresCollateralAndRejectsDuplicateIDs(t *testing.T) {
	s, f, u, m := setup(t)
	s.Config.Mode = "debt"
	s.Store.ReserveDebtMode = true
	s.Chain = &fakeDebtChain{fakeChain: f}
	f.v.DebtEpoch = "0"
	f.v.Principal = "0"
	id := begin(t, s, u, m)
	if err := s.Store.Finish(context.Background(), id, "completed", store.Meter{Amount: decimal.RequireFromString("0.2")}, "", 200, "", 1); err != nil {
		t.Fatal(err)
	}
	sql(t, s, `INSERT INTO bills(id,user_id,wallet,chain_id,contract,token,provider,amount,amount_units,state,error_code,insufficient_balance_at) SELECT '0x'||repeat('b',64),id,wallet,10143,'p','t','s',0.2,200000,'blocked','insufficient_allowance',now() FROM users WHERE id=$1`, u)
	tick(t, s)
	if f.signed != 0 {
		t.Fatal("reported new debt without collateral")
	}
	item := DebtItem{ID: payments.Keccak([]byte("request")), Amount: "1"}
	if _, err := debtData("0x1111111111111111111111111111111111111111", "0", []DebtItem{item, item}); err == nil {
		t.Fatal("accepted duplicate debt IDs")
	}
}
