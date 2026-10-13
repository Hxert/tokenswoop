package store_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"tokenswoop/backend/internal/testutil"
)

func TestSettlementAdmissionMigrationPreservesBills(t *testing.T) {
	s := testutil.EmptyDatabase(t)
	ctx := context.Background()
	for _, file := range []string{"schema.sql", "002_cache_write_breakdown.sql", "003_postpaid_billing.sql", "004_wallet_billing.sql", "005_channel_catalog.sql", "006_llm_compatibility.sql", "007_pricing_observability.sql", "008_provider_token_limits.sql", "009_unlimited_key_budget.sql", "010_reserve_tiers.sql", "011_wallet_only.sql", "012_tier_settlement.sql"} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.DB.Exec(ctx, string(data)); err != nil {
			t.Fatal(err)
		}
	}
	users := []string{}
	for i, state := range []struct{ state, code string }{
		{"blocked", "insufficient_balance"},
		{"blocked", "insufficient_allowance"},
		{"submitted", ""},
		{"review", "configuration_changed"},
		{"paid", ""},
	} {
		u := testutil.Account(t, s, "0")
		users = append(users, u)
		if _, err := s.DB.Exec(ctx, `INSERT INTO bills(id,user_id,wallet,chain_id,contract,token,provider,amount,amount_units,state,error_code) SELECT $1,id,wallet,10143,'contract','token','provider',0.01,10000,$3,$4 FROM users WHERE id=$2`, fmt.Sprintf("0x%064x", i+1), u, state.state, state.code); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := s.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for i, u := range users {
		blocked, err := s.PaidCallsBlocked(ctx, u)
		if err != nil || blocked != (i <= 1) {
			t.Fatal("migration lost a balance failure or blocked another bill state", i, blocked, err)
		}
	}
	var total string
	if err := s.DB.QueryRow(ctx, `SELECT sum(amount)::text FROM bills`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	equalMoney(t, total, "0.05")
}
