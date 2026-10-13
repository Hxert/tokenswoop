package httpapi

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"os"
	"testing"
	"time"
	"tokenswoop/backend/internal/config"
	"tokenswoop/backend/internal/payments"
	"tokenswoop/backend/internal/reserve"
	"tokenswoop/backend/internal/store"
	"tokenswoop/backend/internal/testutil"
)

// This opt-in suite uses a dedicated, retained database and the temporary stop
// wallet deployment. Each phase is a new process to exercise restart recovery.
func TestFundsLifecycleTestnet(t *testing.T) {
	phase := os.Getenv("FUNDS_TESTNET_PHASE")
	if phase == "" {
		t.Skip("use scripts/funds-backend-testnet.mjs")
	}
	cfg, err := config.Load()
	if err != nil || cfg.ChainID != 10143 || !cfg.Reserve.DebtMode() {
		t.Fatal("testnet debt configuration required", err)
	}
	if os.Getenv("FUNDS_TESTNET_DATABASE") != cfg.DatabaseURL {
		t.Fatal("dedicated funds database required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	s, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	pc := payments.NewChain(cfg.Payments)
	rs := reserve.New(s, cfg.Reserve, cfg.Payments.Token, reserve.NewChain(cfg.Payments, cfg.Reserve), cfg.Secret)
	ps := payments.New(s, cfg.Payments, pc, cfg.Secret)
	if err = pc.Validate(ctx); err != nil {
		t.Fatal(err)
	}
	wallet := os.Getenv("FUNDS_TESTNET_WALLET")
	var user string
	err = s.DB.QueryRow(ctx, `INSERT INTO users(id,wallet) VALUES($1,$2) ON CONFLICT(wallet) DO UPDATE SET wallet=excluded.wallet RETURNING id::text`, uuid.NewString(), wallet).Scan(&user)
	if err != nil {
		t.Fatal(err)
	}
	model, err := s.SaveModel(ctx, user, testutil.Model())
	if err != nil {
		t.Fatal(err)
	}
	require := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	text := func(q string, args ...any) string {
		t.Helper()
		var v string
		require(s.DB.QueryRow(ctx, q, args...).Scan(&v))
		return v
	}
	money := func(q, want string) {
		t.Helper()
		if got := text(q, user); !decimal.RequireFromString(got).Equal(decimal.RequireFromString(want)) {
			t.Fatalf("ledger mismatch got %s want %s", got, want)
		}
	}
	snapshot := func() {
		_, e := ps.Refresh(ctx, user, true)
		require(e)
		_, e = rs.Refresh(ctx, user, true)
		require(e)
	}
	if phase == "prepare" {
		snapshot()
		u, e := s.User(ctx, user)
		require(e)
		tier, e := s.Tier(ctx, u.Tier)
		require(e)
		if text(`SELECT count(*)::text FROM requests WHERE user_id=$1`, user) == "0" {
			first := uuid.NewString()
			require(s.BeginRequest(ctx, first, user, "", model, tier))
			require(s.Finish(ctx, first, "completed", store.Meter{Amount: decimal.RequireFromString("0.0030004"), Source: "confirmed upstream fixture"}, "", 200, "", 1))
			// Accepted before allowance is revoked; it must remain completable.
			require(s.BeginRequest(ctx, uuid.NewString(), user, "", model, tier))
		}
		_, e = ps.BuildBill(ctx, user)
		require(e)
		t.Log("Prepared completed usage and one in-flight request")
	} else if phase == "fail" {
		require(ps.SettleNext(ctx))
		if text(`SELECT error_code FROM bills WHERE user_id=$1 AND state<>'paid'`, user) != "insufficient_allowance" {
			t.Fatal("allowance failure not retained")
		}
		snapshot()
		u, e := s.User(ctx, user)
		require(e)
		tier, e := s.Tier(ctx, u.Tier)
		require(e)
		if e = s.BeginRequest(ctx, uuid.NewString(), user, "", model, tier); e != store.ErrPaymentRequired {
			t.Fatal("revoked allowance did not stop new calls", e)
		}
		id := text(`SELECT id::text FROM requests WHERE user_id=$1 ORDER BY created_at DESC LIMIT 1`, user)
		require(s.Finish(ctx, id, "completed", store.Meter{Amount: decimal.RequireFromString("0.000005"), Source: "confirmed upstream fixture"}, "", 200, "", 1))
		for i := 0; i < 40; i++ {
			require(rs.Tick(ctx))
			v, e := rs.Refresh(ctx, user, true)
			require(e)
			if v.Debt == "3005" {
				t.Log("Revoked allowance blocks new calls; in-flight usage completed; worker froze exact confirmed debt")
				return
			}
			time.Sleep(750 * time.Millisecond)
		}
		t.Fatal("debt report not finalized")
	} else if phase == "submit" {
		snapshot()
		id := text(`SELECT id FROM bills WHERE user_id=$1 AND state<>'paid'`, user)
		require(ps.Retry(ctx, user, id))
		require(ps.SettleNext(ctx))
		money(`SELECT unpaid_balance::text FROM users WHERE id=$1`, "0.0030054")
		t.Log("Payment submitted; deliberately exiting before applying its receipt")
	} else if phase == "recover" {
		for i := 0; i < 50; i++ {
			require(ps.Tick(ctx))
			require(rs.Tick(ctx))
			var open int
			require(s.DB.QueryRow(ctx, `SELECT count(*) FROM bills WHERE user_id=$1 AND state<>'paid'`, user).Scan(&open))
			if open == 0 && decimal.RequireFromString(text(`SELECT unpaid_balance::text FROM users WHERE id=$1`, user)).LessThan(decimal.RequireFromString("0.000001")) {
				v, e := rs.Refresh(ctx, user, true)
				require(e)
				if v.Debt == "0" {
					break
				}
			}
			time.Sleep(750 * time.Millisecond)
		}
		money(`SELECT unpaid_balance::text FROM users WHERE id=$1`, "0.0000004")
		money(`SELECT sum(wallet_paid_amount)::text FROM requests WHERE user_id=$1`, "0.003005")
		if text(`SELECT count(*)::text FROM bills WHERE user_id=$1 AND state='paid'`, user) != "2" {
			t.Fatal("expected two immutable bills")
		}
		for i := 0; i < 3; i++ {
			require(ps.Tick(ctx))
		}
		if text(`SELECT count(*)::text FROM audit_events WHERE actor_id=$1 AND action='bill.paid'`, user) != "2" {
			t.Fatal("duplicate settlement detected")
		}
		v, e := rs.Refresh(ctx, user, true)
		require(e)
		if v.Debt != "0" || v.Principal != "50000000" {
			t.Fatal("reserve release mismatch", v)
		}
		t.Log("Restart recovered mined payment exactly once; request/user/bill totals match; debt released; fractional remainder preserved")
	} else if phase == "reconcile-proof" {
		id := text(`SELECT id FROM bills WHERE user_id=$1 AND state='paid' ORDER BY created_at LIMIT 1`, user)
		b, e := ps.Bill(ctx, user, id)
		require(e)
		from := text(`SELECT reconcile_from::text FROM bills WHERE id=$1`, id)
		for i := 0; i < 30; i++ {
			hash, proof, next, e := pc.FindPayment(ctx, b, from)
			require(e)
			if proof.Finalized && proof.Success {
				if b.TxHash == nil || hash != *b.TxHash {
					t.Fatal("event scan found a different payment")
				}
				t.Log("Actual finalized event scan independently recovered the exact paid bill")
				return
			}
			from = next
		}
		t.Fatal("bounded event scan did not recover payment")
	} else {
		t.Fatal(fmt.Sprintf("unknown funds phase %q", phase))
	}
}
