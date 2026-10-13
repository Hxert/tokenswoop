package worker

import (
	"context"
	"log/slog"
	"sync"
	"time"
	"tokenswoop/backend/internal/faucet"
	"tokenswoop/backend/internal/merasponsor"
	"tokenswoop/backend/internal/payments"
	"tokenswoop/backend/internal/reserve"
	"tokenswoop/backend/internal/store"
)

// Sweep flags stale requests for review without charging estimated usage.
func Sweep(ctx context.Context, s *store.Store) error {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var locked bool
	if e = tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock(78016346)").Scan(&locked); e != nil {
		return e
	}
	if !locked {
		return nil
	}
	if _, e = tx.Exec(ctx, `UPDATE requests SET state='usage_unknown',error_code='worker_detected_stale_request',updated_at=now() WHERE state IN('accepted','in_flight') AND ((upstream_deadline_at IS NULL AND updated_at<now()-interval '10 minutes') OR upstream_deadline_at<now())`); e != nil {
		return e
	}
	// Operational notifications have their own cursor. Payments consume durable
	// completed-request balances and their own immutable bill/attempt journal.
	if _, e = tx.Exec(ctx, "UPDATE outbox_events SET processed_at=now() WHERE id IN (SELECT id FROM outbox_events WHERE processed_at IS NULL ORDER BY created_at LIMIT 500)"); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "DELETE FROM sessions WHERE expires_at<now()"); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "DELETE FROM response_handles WHERE expires_at<now()"); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "DELETE FROM runtime_instances WHERE last_seen<now()-interval '7 days'"); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "DELETE FROM auth_challenges WHERE expires_at<now()-interval '1 day'"); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "INSERT INTO worker_heartbeats(id) VALUES('maintenance') ON CONFLICT(id) DO UPDATE SET last_seen=now()"); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func Run(ctx context.Context, s *store.Store, settlement *payments.Service, reserves *reserve.Service, sponsorship *merasponsor.Service, dispenser *faucet.Service) {
	go s.TrackInstance(ctx, "worker")
	settlementWake := make(chan struct{}, 1)
	var tasks sync.WaitGroup
	if settlement != nil {
		tasks.Add(1)
		go func() {
			defer tasks.Done()
			settlement.RunScheduler(ctx, settlementWake)
		}()
	}
	if sponsorship != nil && !sponsorship.Config.SharedSigner {
		tasks.Add(1)
		go func() { defer tasks.Done(); sponsorship.Run(ctx) }()
	}
	type laneTask struct {
		name string
		run  func(context.Context) error
	}
	var lane []laneTask
	if settlement != nil {
		lane = append(lane, laneTask{"wallet settlement", settlement.SettleNext})
	}
	if reserves != nil {
		lane = append(lane, laneTask{"reserve synchronization", reserves.Tick})
	}
	shared := sponsorship != nil && sponsorship.Config.SharedSigner
	if shared {
		lane = append(lane, laneTask{"Mera sponsorship", sponsorship.Tick})
	}
	if dispenser != nil {
		lane = append(lane, laneTask{"testnet faucet", dispenser.Tick})
	}
	turn := 0
	defer tasks.Wait()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		iteration, cancel := context.WithTimeout(ctx, 10*time.Second)
		if e := Sweep(iteration, s); e != nil && ctx.Err() == nil {
			slog.Error("worker maintenance failed", "error", e)
		}
		if e := s.ProcessOperations(iteration); e != nil && ctx.Err() == nil {
			slog.Error("operations cleanup deferred", "error", e)
		}
		cancel()
		// Rotate first access when sharing the platform signer. A constant stream
		// of new collections must not starve user withdrawals or approvals.
		// Each service recovers its durable pending transaction before signing.
		for i := 0; i < len(lane); i++ {
			job := lane[(turn+i)%len(lane)]
			iteration, cancel = context.WithTimeout(ctx, 45*time.Second)
			if e := job.run(iteration); e != nil && ctx.Err() == nil {
				slog.Error(job.name+" deferred", "error", e)
			}
			cancel()
		}
		if shared || dispenser != nil {
			turn = (turn + 1) % len(lane)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-settlementWake:
		}
	}
}
