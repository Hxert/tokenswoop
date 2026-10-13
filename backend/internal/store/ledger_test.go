package store_test

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"os"
	"sync"
	"testing"
	"tokenswoop/backend/internal/store"
	"tokenswoop/backend/internal/testutil"
	"tokenswoop/backend/internal/worker"
)

func equalMoney(t *testing.T, got, want string) {
	t.Helper()
	if !decimal.RequireFromString(got).Equal(decimal.RequireFromString(want)) {
		t.Fatalf("amount %s, want %s", got, want)
	}
}
func newKey(t *testing.T, s *store.Store, user, budget string) string {
	t.Helper()
	id := uuid.NewString()
	_, err := s.DB.Exec(context.Background(), "INSERT INTO api_keys(id,user_id,name,prefix,token_hash,budget) VALUES($1,$2,'test','ts_test',$4,$3)", id, user, budget, id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func TestConcurrentAdmissionAndPostpaidCompletion(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	u := testutil.Account(t, s, "0.00005")
	m, err := s.SaveModel(ctx, u, testutil.Model())
	if err != nil {
		t.Fatal(err)
	}
	tier := store.Tier{ID: "free", RPM: 100, Concurrency: 3}
	keys := []string{newKey(t, s, u, "0.00001"), newKey(t, s, u, "0.00001")}
	accepted := make(chan string, 20)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := uuid.NewString()
			if e := s.BeginRequest(ctx, id, u, keys[i%2], m, tier); e == nil {
				accepted <- id
			} else if !errors.Is(e, store.ErrConcurrency) {
				t.Error(e)
			}
		}(i)
	}
	wg.Wait()
	close(accepted)
	if len(accepted) != 3 {
		t.Fatalf("accepted %d, want account concurrency 3", len(accepted))
	}
	user, err := s.User(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	equalMoney(t, user.Unpaid, "0")
	var keySpent, billed string
	if err = s.DB.QueryRow(ctx, "SELECT sum(spent)::text FROM api_keys WHERE user_id=$1", u).Scan(&keySpent); err != nil {
		t.Fatal(err)
	}
	if err = s.DB.QueryRow(ctx, "SELECT sum(amount)::text FROM requests WHERE user_id=$1", u).Scan(&billed); err != nil {
		t.Fatal(err)
	}
	equalMoney(t, keySpent, "0")
	equalMoney(t, billed, "0")
	// All admitted requests settle even after balance or budget is exhausted.
	// Race duplicate completion calls to prove each request is charged once.
	for id := range accepted {
		for repeat := 0; repeat < 4; repeat++ {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				if e := s.Finish(ctx, id, "completed", store.Meter{Input: 20, Output: 5, Amount: decimal.RequireFromString("0.00003"), Source: "upstream:test"}, "", 200, "", 1); e != nil {
					t.Error(e)
				}
			}(id)
		}
	}
	wg.Wait()
	user, err = s.User(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	equalMoney(t, user.Unpaid, "0.00009")
	var due string
	var completed int
	if err = s.DB.QueryRow(ctx, "SELECT sum(amount)::text,sum(unpaid_amount)::text,count(*) FILTER(WHERE state='completed') FROM requests WHERE user_id=$1", u).Scan(&billed, &due, &completed); err != nil {
		t.Fatal(err)
	}
	equalMoney(t, billed, "0.00009")
	equalMoney(t, due, "0.00009")
	if completed != 3 {
		t.Fatal("completed request lost")
	}
	if err = s.DB.QueryRow(ctx, "SELECT sum(spent)::text FROM api_keys WHERE user_id=$1", u).Scan(&keySpent); err != nil {
		t.Fatal(err)
	}
	equalMoney(t, keySpent, "0.00009")
	var completions int
	if err = s.DB.QueryRow(ctx, "SELECT count(*) FROM outbox_events WHERE kind='request.completed'").Scan(&completions); err != nil || completions != 3 {
		t.Fatal("completion charged more than once", err)
	}
	if e := s.BeginRequest(ctx, uuid.NewString(), u, "", m, tier); e != nil {
		t.Fatalf("unsettled usage interrupted new calls: %v", e)
	}
}
func TestSnapshotsAndUnknownUsageDoNotHoldFundsOrConcurrency(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	u := testutil.Account(t, s, "1")
	m, err := s.SaveModel(ctx, u, testutil.Model())
	if err != nil {
		t.Fatal(err)
	}
	tier := store.Tier{ID: "free", Concurrency: 1}
	id := uuid.NewString()
	if err = s.BeginRequest(ctx, id, u, "", m, tier); err != nil {
		t.Fatal(err)
	}
	m.InputPrice = "9"
	if _, err = s.SaveModel(ctx, u, m); err != nil {
		t.Fatal(err)
	}
	var price string
	if err = s.DB.QueryRow(ctx, "SELECT price_snapshot->>'input_price' FROM requests WHERE id=$1", id).Scan(&price); err != nil {
		t.Fatal(err)
	}
	equalMoney(t, price, "1")
	if err = s.Finish(ctx, id, "usage_unknown", store.Meter{}, "", 502, "cutoff", 20); err != nil {
		t.Fatal(err)
	}
	user, err := s.User(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	equalMoney(t, user.Unpaid, "0")
	next := uuid.NewString()
	if err = s.BeginRequest(ctx, next, u, "", m, tier); err != nil {
		t.Fatalf("unknown request occupies concurrency: %v", err)
	}
	if err = s.Finish(ctx, next, "failed", store.Meter{}, "", 429, "upstream_rejected", 1); err != nil {
		t.Fatal(err)
	}
	if err = s.ResolveUnknown(ctx, u, id); err != nil {
		t.Fatal(err)
	}
	if err = s.ResolveUnknown(ctx, u, id); err == nil {
		t.Fatal("waived twice")
	}
	if err = s.Finish(ctx, id, "completed", store.Meter{Amount: decimal.RequireFromString("0.5")}, "", 200, "", 1); err != nil {
		t.Fatal(err)
	}
	user, err = s.User(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	equalMoney(t, user.Unpaid, "0")
}
func TestKeyBudgetIsCheckedAgainstCompletedSpend(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	u := testutil.Account(t, s, "1")
	m, err := s.SaveModel(ctx, u, testutil.Model())
	if err != nil {
		t.Fatal(err)
	}
	tier := store.Tier{Concurrency: 2}
	key := newKey(t, s, u, "0.00001")
	id := uuid.NewString()
	if err = s.BeginRequest(ctx, id, u, key, m, tier); err != nil {
		t.Fatal(err)
	}
	if err = s.Finish(ctx, id, "completed", store.Meter{Input: 20, Output: 5, Amount: decimal.RequireFromString("0.00003"), Source: "upstream:test"}, "", 200, "", 1); err != nil {
		t.Fatal(err)
	}
	if err = s.BeginRequest(ctx, uuid.NewString(), u, key, m, tier); !errors.Is(err, store.ErrBudget) {
		t.Fatalf("spent key still admitted: %v", err)
	}
	user, err := s.User(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	equalMoney(t, user.Unpaid, "0.00003")
}
func TestWorkerMarksStaleWithoutCharge(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	u := testutil.Account(t, s, "1")
	m, err := s.SaveModel(ctx, u, testutil.Model())
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	tier := store.Tier{Concurrency: 1}
	if err = s.BeginRequest(ctx, id, u, "", m, tier); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, "UPDATE requests SET updated_at=now()-interval '11 minutes' WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	if err = worker.Sweep(ctx, s); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = s.DB.QueryRow(ctx, "SELECT state FROM requests WHERE id=$1", id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "usage_unknown" {
		t.Fatal("stale request not reviewed")
	}
	user, err := s.User(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	equalMoney(t, user.Unpaid, "0")
	if err = s.BeginRequest(ctx, uuid.NewString(), u, "", m, tier); err != nil {
		t.Fatal(err)
	}
}
func TestActualChargeValidationAndCacheBreakdown(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	u := testutil.Account(t, s, "1")
	m, err := s.SaveModel(ctx, u, testutil.Model())
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	if err = s.BeginRequest(ctx, id, u, "", m, store.Tier{Concurrency: 1}); err != nil {
		t.Fatal(err)
	}
	for _, amount := range []string{"-1", "0.0000000000000000001"} {
		if err = s.Finish(ctx, id, "completed", store.Meter{Amount: decimal.RequireFromString(amount)}, "", 200, "", 1); err == nil {
			t.Fatal("invalid actual charge accepted")
		}
	}
	if err = s.Finish(ctx, id, "completed", store.Meter{Amount: decimal.RequireFromString("0.2"), CacheWrite: 20, CacheWrite1h: 12}, "", 200, "", 1); err != nil {
		t.Fatal(err)
	}
	var hour int
	if err = s.DB.QueryRow(ctx, "SELECT cache_write_1h_tokens FROM requests WHERE id=$1", id).Scan(&hour); err != nil || hour != 12 {
		t.Fatal("cache breakdown lost", err)
	}
	user, err := s.User(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	equalMoney(t, user.Unpaid, "0.2")
}
func TestFreeModelDoesNotRequireWalletFunds(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	u := testutil.Account(t, s, "0")
	m := testutil.Model()
	m.InputPrice = "0"
	m.OutputPrice = "0"
	m.CacheReadPrice = "0"
	m.CacheWritePrice = "0"
	m.CacheWrite1hPrice = "0"
	m, err := s.SaveModel(ctx, u, m)
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	if err = s.BeginRequest(ctx, id, u, "", m, store.Tier{Concurrency: 1}); err != nil {
		t.Fatal(err)
	}
	if err = s.Finish(ctx, id, "completed", store.Meter{Input: 20, Amount: decimal.Zero, Source: "upstream:test"}, "", 200, "", 1); err != nil {
		t.Fatal(err)
	}
}
func TestMigrationRemovesLegacyHoldsWithoutRefundingCredit(t *testing.T) {
	s := testutil.EmptyDatabase(t)
	ctx := context.Background()
	schema, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, string(schema)); err != nil {
		t.Fatal(err)
	}
	u := uuid.NewString()
	if _, err = s.DB.Exec(ctx, "INSERT INTO users(id,wallet,credit_balance) VALUES($1,$2,1)", u, "0x"+u); err != nil {
		t.Fatal(err)
	}
	key := newKey(t, s, u, "1")
	// Build a version-1 fixture with version-1 SQL, independently of today's store methods.
	m := testutil.Model()
	if _, err = s.DB.Exec(ctx, `INSERT INTO models(id,name,vendor,enabled,max_input_tokens,max_output_tokens,input_price,output_price,cache_read_price,cache_write_price,cache_write_1h_price) VALUES($1,$2,$3,true,$4,$5,$6,$7,$8,$9,$10)`, m.ID, m.Name, m.Vendor, 8192, 2048, m.InputPrice, m.OutputPrice, m.CacheReadPrice, m.CacheWritePrice, m.CacheWrite1hPrice); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, `INSERT INTO model_prices(model_id,version,snapshot) SELECT id,1,jsonb_build_object('id',id,'input_price',input_price::text) FROM models`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, "UPDATE users SET credit_reserved=0.3 WHERE id=$1", u); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, "UPDATE api_keys SET reserved=0.3,spent=0.1 WHERE id=$1", key); err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	done := uuid.NewString()
	if _, err = s.DB.Exec(ctx, "INSERT INTO requests(id,user_id,key_id,model_id,state,reserved,price_snapshot,tier_snapshot) VALUES($1,$2,$3,$4,'reserved',0.3,'{}','{}')", id, u, key, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, "INSERT INTO requests(id,user_id,key_id,model_id,state,reserved,amount,credit_amount,price_snapshot,tier_snapshot) VALUES($1,$2,$3,$4,'completed',0.2,0.1,0.1,'{}','{}')", done, u, key, m.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = s.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	migrated, err := s.Model(ctx, m.ID)
	if err != nil || !migrated.Enabled || !migrated.PricingConfigured || migrated.PriceVersion != 1 {
		t.Fatal("existing model changed during catalog migration", err)
	}
	equalMoney(t, migrated.InputPrice, "1")
	user, err := s.User(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	equalMoney(t, user.Unpaid, "0")
	var state, spent, amount string
	if err = s.DB.QueryRow(ctx, "SELECT state FROM requests WHERE id=$1", id).Scan(&state); err != nil || state != "accepted" {
		t.Fatal("pending request lost", err)
	}
	if err = s.DB.QueryRow(ctx, "SELECT spent::text FROM api_keys WHERE id=$1", key).Scan(&spent); err != nil {
		t.Fatal(err)
	}
	if err = s.DB.QueryRow(ctx, "SELECT amount::text FROM requests WHERE id=$1", done).Scan(&amount); err != nil {
		t.Fatal(err)
	}
	equalMoney(t, spent, "0.1")
	equalMoney(t, amount, "0.1")
	var archived, waived, due string
	if err = s.DB.QueryRow(ctx, "SELECT balance::text FROM legacy_credit_balances WHERE user_id=$1", u).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if err = s.DB.QueryRow(ctx, "SELECT waived_amount::text,unpaid_amount::text FROM requests WHERE id=$1", done).Scan(&waived, &due); err != nil {
		t.Fatal(err)
	}
	equalMoney(t, archived, "1")
	equalMoney(t, waived, "0.1")
	equalMoney(t, due, "0")
	var legacyColumns, audits int
	if err = s.DB.QueryRow(ctx, "SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND column_name IN('reserved','credit_reserved','credit_balance','credit_amount')").Scan(&legacyColumns); err != nil || legacyColumns != 0 {
		t.Fatal("reservation columns remain", err)
	}
	if err = s.DB.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE action='billing.remove_legacy_hold'").Scan(&audits); err != nil || audits != 3 {
		t.Fatal("legacy holds not audited", err)
	}
}
