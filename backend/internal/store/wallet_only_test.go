package store_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"tokenswoop/backend/internal/config"
	"tokenswoop/backend/internal/payments"
	"tokenswoop/backend/internal/store"
	"tokenswoop/backend/internal/testutil"
)

func TestWalletOnlyMigrationPreservesHistoricalPayments(t *testing.T) {
	s := testutil.EmptyDatabase(t)
	ctx := context.Background()
	execSQL := func(q string, args ...any) {
		t.Helper()
		if _, err := s.DB.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Build the immediately preceding schema with its original migrations.
	files := []string{"schema.sql", "002_cache_write_breakdown.sql", "003_postpaid_billing.sql", "004_wallet_billing.sql", "005_channel_catalog.sql", "006_llm_compatibility.sql", "007_pricing_observability.sql", "008_provider_token_limits.sql", "009_unlimited_key_budget.sql", "010_reserve_tiers.sql"}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		execSQL(string(data))
	}
	u := testutil.Account(t, s, "0")
	m, err := s.SaveModel(ctx, u, testutil.Model())
	if err != nil {
		t.Fatal(err)
	}
	key := newKey(t, s, u, "10")
	mixed, covered, unpaid, inFlight := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	execSQL(`UPDATE users SET credit_balance=1.5,unpaid_balance=1.75 WHERE id=$1`, u)
	execSQL(`UPDATE api_keys SET spent=4.25 WHERE id=$1`, key)
	for _, r := range []struct{ id, state, cost, credit, paid, due string }{
		{mixed, "completed", "3", "1", "1", "1"},
		{covered, "completed", "0.5", "0.5", "0", "0"},
		{unpaid, "completed", "0.75", "0", "0", "0.75"},
		{inFlight, "in_flight", "0", "0", "0", "0"},
	} {
		execSQL(`INSERT INTO requests(id,user_id,key_id,model_id,state,amount,credit_amount,wallet_paid_amount,unpaid_amount,price_snapshot,tier_snapshot) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'{}','{}')`, r.id, u, key, m.ID, r.state, r.cost, r.credit, r.paid, r.due)
	}
	execSQL(`INSERT INTO credit_entries(id,user_id,amount,kind,reference) VALUES($1,$2,3,'introductory','legacy-grant')`, uuid.NewString(), u)
	for _, r := range []struct{ id, debit string }{{mixed, "-1"}, {covered, "-0.5"}} {
		execSQL(`INSERT INTO credit_entries(id,user_id,request_id,amount,kind,reference) VALUES($1,$2,$3,$4,'usage',$5)`, uuid.NewString(), u, r.id, r.debit, "request:"+r.id)
	}
	bill := "0x" + strings.Repeat("a", 64)
	execSQL(`INSERT INTO bills(id,user_id,wallet,chain_id,contract,token,provider,amount,amount_units,state,tx_hash,paid_at) SELECT $1,id,wallet,10143,'contract','token','provider',1,1000000,'paid','original-payment',now() FROM users WHERE id=$2`, bill, u)
	execSQL(`INSERT INTO bill_items(bill_id,request_id,amount) VALUES($1,$2,1)`, bill, mixed)
	for i := 0; i < 2; i++ {
		if err = s.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	value := func(q string, args ...any) string {
		t.Helper()
		var out string
		if err := s.DB.QueryRow(ctx, q, args...).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	equalMoney(t, value(`SELECT balance::text FROM legacy_credit_balances WHERE user_id=$1`, u), "1.5")
	equalMoney(t, value(`SELECT sum(amount)::text FROM legacy_credit_entries WHERE user_id=$1`, u), "1.5")
	equalMoney(t, value(`SELECT sum(waived_amount)::text FROM requests WHERE user_id=$1`, u), "1.5")
	equalMoney(t, value(`SELECT unpaid_balance::text FROM users WHERE id=$1`, u), "1.75")
	equalMoney(t, value(`SELECT spent::text FROM api_keys WHERE id=$1`, key), "4.25")
	if value(`SELECT count(*)::text FROM audit_events WHERE action='migration.retire_credit'`) != "2" {
		t.Fatal("historical adjustments were lost or applied twice")
	}
	if value(`SELECT recipient FROM bills WHERE id=$1`, bill) != "provider" {
		t.Fatal("migration lost historical recipient")
	}
	if value(`SELECT state||':'||tx_hash FROM bills WHERE id=$1`, bill) != "paid:original-payment" {
		t.Fatal("historical payment changed")
	}
	if value(`SELECT count(*)::text FROM information_schema.columns WHERE table_schema=current_schema() AND column_name IN('credit_balance','credit_amount')`) != "0" {
		t.Fatal("active credit columns remain")
	}
	// Unused archived balances cannot admit calls or offset a completion that
	// was already in flight at upgrade time.
	if err = s.BeginRequest(ctx, uuid.NewString(), u, key, m, store.Tier{Concurrency: 3}); !errors.Is(err, store.ErrSettlementUnavailable) {
		t.Fatal("archived credit bypassed wallet payment", err)
	}
	if err = s.Finish(ctx, inFlight, "completed", store.Meter{Amount: decimal.RequireFromString("0.25"), Source: "upstream:fixture"}, "", 200, "", 1); err != nil {
		t.Fatal(err)
	}
	equalMoney(t, value(`SELECT unpaid_balance::text FROM users WHERE id=$1`, u), "2")
	equalMoney(t, value(`SELECT spent::text FROM api_keys WHERE id=$1`, key), "4.5")
	service := payments.New(s, config.Payments{Enabled: true, Contract: "contract", Token: "token", Provider: "provider"}, nil, make([]byte, 32))
	newBill, err := service.BuildBill(ctx, u)
	if err != nil || newBill == "" || newBill == bill {
		t.Fatal("could not aggregate remaining actual debt", err)
	}
	equalMoney(t, value(`SELECT amount::text FROM bills WHERE id=$1`, newBill), "2")
	if value(`SELECT count(*)::text FROM bill_items WHERE bill_id=$1 AND request_id=$2`, newBill, covered) != "0" {
		t.Fatal("historically covered request billed again")
	}
}

func TestPaidAdmissionDoesNotDependOnWalletEvidence(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	u := testutil.Account(t, s, "0")
	m, err := s.SaveModel(ctx, u, testutil.Model())
	if err != nil {
		t.Fatal(err)
	}
	admit := func(want error) {
		t.Helper()
		id := uuid.NewString()
		if err := s.BeginRequest(ctx, id, u, "", m, store.Tier{Concurrency: 1}); !errors.Is(err, want) {
			t.Fatalf("admission got %v, want %v", err, want)
		}
		if want == nil {
			if err := s.Finish(ctx, id, "failed", store.Meter{}, "", 502, "fixture", 1); err != nil {
				t.Fatal(err)
			}
		}
	}
	admit(store.ErrSettlementUnavailable)
	testutil.FundWallet(t, s, u, "0")
	admit(nil)
	for _, q := range []string{
		`UPDATE payment_accounts SET allowance=0`,
		`UPDATE payment_accounts SET checked_at=now()-interval '1 minute'`,
		`UPDATE worker_heartbeats SET last_seen=now()-interval '2 minutes'`,
		`UPDATE payment_accounts SET contract='wrong-deployment'`,
		`DELETE FROM payment_accounts`,
		`DELETE FROM worker_heartbeats`,
	} {
		testutil.FundWallet(t, s, u, "1")
		if _, err = s.DB.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
		admit(nil)
	}
	testutil.FundWallet(t, s, u, "0.000001")
	admit(nil)
	user, err := s.User(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	equalMoney(t, user.Unpaid, "0")
}
