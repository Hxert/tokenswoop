package payments_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"tokenswoop/backend/internal/payments"
	"tokenswoop/backend/internal/store"
)

func ageDebt(t *testing.T, s *payments.Service, user string) {
	t.Helper()
	if _, err := s.Store.DB.Exec(context.Background(), `UPDATE requests SET completed_at=now()-interval '5 seconds' WHERE user_id=$1 AND state='completed' AND unpaid_amount>0`, user); err != nil {
		t.Fatal(err)
	}
}

func queue(t *testing.T, s *payments.Service, want int) {
	t.Helper()
	got, err := s.QueueDueBills(context.Background())
	if err != nil || got != want {
		t.Fatalf("queued %d, want %d: %v", got, want, err)
	}
}

func TestSettlementAmountTriggersByTier(t *testing.T) {
	for _, tc := range []struct{ tier, amount string }{{"free", "0.01"}, {"builder", "0.05"}, {"scale", "0.2"}, {"pro", "1"}} {
		t.Run(tc.tier, func(t *testing.T) {
			s, _, user, model := setup(t)
			ctx := context.Background()
			if tc.tier != "free" {
				if _, err := s.Store.SaveTier(ctx, user, store.Tier{ID: tc.tier, RPM: 30, Concurrency: 10}); err != nil {
					t.Fatal(err)
				}
				if _, err := s.Store.DB.Exec(ctx, `UPDATE users SET tier=$2 WHERE id=$1`, user, tc.tier); err != nil {
					t.Fatal(err)
				}
			}
			tier, err := s.Store.Tier(ctx, tc.tier)
			if err != nil {
				t.Fatal(err)
			}
			money(t, tier.SettlementThreshold, tc.amount)
			finish(t, s, user, model, decimal.RequireFromString(tc.amount).Sub(decimal.RequireFromString("0.000001")).String())
			queue(t, s, 0)
			finish(t, s, user, model, "0.000001")
			queue(t, s, 1)
			money(t, value(t, s, `SELECT amount::text FROM bills WHERE user_id=$1`, user), tc.amount)
			if value(t, s, `SELECT count(*)::text FROM bill_items`) != "2" {
				t.Fatal("amount trigger did not combine both completed requests")
			}
			queue(t, s, 0)
		})
	}
}

func TestSettlementDeadlineStartsAtCompletionAndSurvivesRestart(t *testing.T) {
	s, _, user, model := setup(t)
	ctx := context.Background()
	id := uuid.NewString()
	if err := s.Store.BeginRequest(ctx, id, user, "", model, store.Tier{Concurrency: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.DB.Exec(ctx, `UPDATE requests SET created_at=now()-interval '1 hour' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Finish(ctx, id, "completed", store.Meter{Amount: decimal.RequireFromString("0.003")}, "", 200, "", 1); err != nil {
		t.Fatal(err)
	}
	queue(t, s, 0)
	ageDebt(t, s, user)
	finish(t, s, user, model, "0.002")
	// A fresh scheduler must honor the first completion even though a newer
	// request arrived and the amount is still below the tier threshold.
	restarted := payments.New(s.Store, s.Config, s.Chain, make([]byte, 32))
	queue(t, restarted, 1)
	money(t, value(t, s, `SELECT amount::text FROM bills WHERE user_id=$1`, user), "0.005")
}

func TestConcurrentTriggersKeepExistingBillImmutable(t *testing.T) {
	s, chain, user, model := setup(t)
	finish(t, s, user, model, "0.01")
	var jobs sync.WaitGroup
	for i := 0; i < 10; i++ {
		jobs.Add(1)
		go func() {
			defer jobs.Done()
			if _, err := s.QueueDueBills(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	jobs.Wait()
	if value(t, s, `SELECT count(*)::text FROM bills`) != "1" {
		t.Fatal("concurrent amount triggers duplicated a bill")
	}
	first := value(t, s, `SELECT id FROM bills`)
	finish(t, s, user, model, "0.02")
	ageDebt(t, s, user)
	queue(t, s, 0)
	money(t, value(t, s, `SELECT amount::text FROM bills WHERE id=$1`, first), "0.01")
	tick(t, s)
	chain.finalized = true
	tick(t, s)
	queue(t, s, 1)
	money(t, value(t, s, `SELECT amount::text FROM bills WHERE state='queued'`), "0.02")
}

func TestSchedulerQueuesWhileRPCIsBlockedAndSignalsSettlement(t *testing.T) {
	s, chain, user, model := setup(t)
	gate := make(chan struct{})
	chain.validateGate = gate
	chain.validated = make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	wake := make(chan struct{}, 1)
	go func() { defer close(done); s.RunScheduler(ctx, wake) }()
	signingDone := make(chan struct{})
	go func() { defer close(signingDone); _ = s.SettleNext(ctx) }()
	defer func() { cancel(); <-done; <-signingDone }()
	select {
	case <-chain.validated:
	case <-time.After(3 * time.Second):
		t.Fatal("scheduler did not start")
	}
	finish(t, s, user, model, "0.01")
	deadline := time.After(3 * time.Second)
	for value(t, s, `SELECT count(*)::text FROM bills`) != "1" {
		select {
		case <-deadline:
			t.Fatal("amount trigger waited for RPC or the five-second window")
		case <-time.After(10 * time.Millisecond):
		}
	}
	select {
	case <-wake:
	case <-deadline:
		t.Fatal("amount trigger did not signal the worker")
	}
	close(gate)
	for {
		chain.mu.Lock()
		signed := chain.signed
		chain.mu.Unlock()
		if signed == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("queued amount trigger did not wake the signer")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
