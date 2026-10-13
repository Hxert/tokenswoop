package payments_test

import (
	"context"
	"strings"
	"testing"
	"tokenswoop/backend/internal/payments"
)

type recoverableChain struct {
	*fakeChain
	replacements      int
	charged, consumed bool
	confirmedHash     string
	proof             payments.Confirmation
	seenFrom          string
}

func (f *recoverableChain) Replace(_ context.Context, raw string) (payments.Signed, error) {
	f.replacements++
	return payments.Signed{Raw: "0x80", Hash: payments.Keccak([]byte{128}), Nonce: "0"}, nil
}
func (f *recoverableChain) IsCharged(context.Context, payments.Bill) (bool, error) {
	return f.charged, nil
}
func (f *recoverableChain) FindPayment(_ context.Context, _ payments.Bill, from string) (string, payments.Confirmation, string, error) {
	f.seenFrom = from
	return f.confirmedHash, f.proof, from, nil
}
func (f *recoverableChain) Confirm(_ context.Context, _ payments.Bill, hash string) (payments.Confirmation, error) {
	if hash == f.confirmedHash {
		return f.proof, nil
	}
	return payments.Confirmation{}, nil
}
func (f *recoverableChain) ConsumedUncharged(context.Context, payments.Bill, string) (payments.Confirmation, error) {
	return payments.Confirmation{Finalized: f.consumed, Success: false, Block: "0x12", BlockHash: "0xconsumed"}, nil
}

func TestFeeReplacementPreservesOriginalWinnerAndRepairsAggregate(t *testing.T) {
	s, f, u, m := setup(t)
	r := &recoverableChain{fakeChain: f}
	s.Chain = r
	ctx := context.Background()
	finish(t, s, u, m, "0.02")
	tick(t, s)
	id := value(t, s, `SELECT id FROM bills`)
	original := value(t, s, `SELECT tx_hash FROM payment_attempts`)
	if _, err := s.Store.DB.Exec(ctx, `UPDATE payment_attempts SET created_at=now()-interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	tick(t, s)
	if r.replacements != 1 || f.signed != 1 || value(t, s, `SELECT count(*)::text FROM payment_attempts`) != "1" || value(t, s, `SELECT count(*)::text FROM chain_tx_replacements`) != "1" {
		t.Fatal("replacement allocated another bill/nonce")
	}
	// The original can win even after its replacement was accepted by an RPC.
	r.confirmedHash = original
	r.proof = payments.Confirmation{Finalized: true, Success: true, Block: "0x12", BlockHash: "0xfinal"}
	if _, err := s.Store.DB.Exec(ctx, `UPDATE users SET unpaid_balance=0 WHERE id=$1`, u); err != nil {
		t.Fatal(err)
	}
	restarted := payments.New(s.Store, s.Config, r, make([]byte, 32))
	tick(t, restarted)
	tick(t, restarted)
	b, err := s.Bill(ctx, u, id)
	if err != nil || b.State != "paid" || *b.TxHash != original {
		t.Fatal("lost original winner", b, err)
	}
	money(t, value(t, s, `SELECT sum(wallet_paid_amount)::text FROM requests WHERE user_id=$1`, u), "0.02")
	money(t, value(t, s, `SELECT unpaid_balance::text FROM users WHERE id=$1`, u), "0")
	if value(t, s, `SELECT count(*)::text FROM audit_events WHERE action='bill.paid'`) != "1" || value(t, s, `SELECT count(*)::text FROM audit_events WHERE action='bill.balance_reconciled'`) != "1" {
		t.Fatal("missing once-only reconciliation audit")
	}
}

func TestExternallyPaidBillReconcilesWithoutSigningOrChargingAgain(t *testing.T) {
	s, f, u, m := setup(t)
	r := &recoverableChain{fakeChain: f}
	s.Chain = r
	ctx := context.Background()
	finish(t, s, u, m, "0.01")
	id, err := s.BuildBill(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	r.charged = true
	r.confirmedHash = "0x" + strings.Repeat("c", 64)
	r.proof = payments.Confirmation{Finalized: true, Success: true, Block: "0x11", BlockHash: "0xcanonical"}
	tick(t, s)
	tick(t, s)
	b, err := s.Bill(ctx, u, id)
	if err != nil || b.State != "paid" || f.signed != 0 || f.broadcast != 0 || r.seenFrom != "16" {
		t.Fatal("external payment not reconciled", b, err, r.seenFrom)
	}
	money(t, value(t, s, `SELECT unpaid_balance::text FROM users WHERE id=$1`, u), "0")
}

func TestUnprovenChargedFlagNeverDebitsLedger(t *testing.T) {
	s, f, u, m := setup(t)
	r := &recoverableChain{fakeChain: f, charged: true}
	s.Chain = r
	finish(t, s, u, m, "0.01")
	tick(t, s)
	if value(t, s, `SELECT state FROM bills`) != "review" || f.signed != 0 {
		t.Fatal("unproven flag treated as payment")
	}
	money(t, value(t, s, `SELECT unpaid_balance::text FROM users WHERE id=$1`, u), "0.01")
}

func TestConsumedNonceRetriesSameBillOnlyWithFinalizedUnchargedProof(t *testing.T) {
	s, f, u, m := setup(t)
	r := &recoverableChain{fakeChain: f}
	s.Chain = r
	ctx := context.Background()
	finish(t, s, u, m, "0.02")
	tick(t, s)
	id := value(t, s, `SELECT id FROM bills`)
	if _, err := s.Store.DB.Exec(ctx, `UPDATE payment_attempts SET created_at=now()-interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	r.consumed = true
	tick(t, s)
	if value(t, s, `SELECT state FROM payment_attempts`) != "reverted" {
		t.Fatal("consumed nonce not retired")
	}
	if err := s.Retry(ctx, u, id); err != nil {
		t.Fatal(err)
	}
	r.consumed = false
	tick(t, s)
	if f.signed != 2 || f.ids[0] != f.ids[1] {
		t.Fatal("nonce recovery changed bill identity")
	}
	money(t, value(t, s, `SELECT unpaid_balance::text FROM users WHERE id=$1`, u), "0.02")
}
