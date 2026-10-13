package payments

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"tokenswoop/backend/internal/store"
)

const SettlementWindow = 5 * time.Second

// QueueDueBills uses the account's current tier. The age of the oldest confirmed
// unpaid request is durable across restarts and never reset by later requests.
func (s *Service) QueueDueBills(ctx context.Context) (int, error) {
	rows, err := s.Store.DB.Query(ctx, `SELECT u.id::text FROM users u JOIN tiers t ON t.id=u.tier
 WHERE u.unpaid_balance>=0.000001
 AND NOT EXISTS(SELECT 1 FROM bills b WHERE b.user_id=u.id AND b.state<>'paid')
 AND (u.unpaid_balance>=t.settlement_threshold OR EXISTS(
   SELECT 1 FROM requests r WHERE r.user_id=u.id AND r.state='completed' AND r.unpaid_amount>0
   AND COALESCE(r.completed_at,r.updated_at)<=now()-$1*interval '1 second'))
 ORDER BY u.created_at,u.id LIMIT 20`, SettlementWindow.Seconds())
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return 0, err
	}
	queued := 0
	for _, id := range ids {
		bill, err := s.buildBill(ctx, id, true)
		if err != nil {
			return queued, err
		}
		if bill != "" {
			queued++
		}
	}
	return queued, nil
}

// RunScheduler separates local bill scheduling from slow RPC/signing operations. A
// post-commit notification handles amount triggers promptly; the short DB-only
// sweep handles five-second deadlines and repairs missed notifications.
func (s *Service) RunScheduler(ctx context.Context, pay chan<- struct{}) {
	wake := make(chan struct{}, 1)
	var tasks sync.WaitGroup
	tasks.Add(1)
	go func() {
		defer tasks.Done()
		s.listen(ctx, wake)
	}()
	defer tasks.Wait()
	sweep := time.NewTicker(250 * time.Millisecond)
	defer sweep.Stop()
	for {
		iteration, cancel := context.WithTimeout(ctx, 10*time.Second)
		queued, err := s.QueueDueBills(iteration)
		cancel()
		if queued > 0 {
			select {
			case pay <- struct{}{}:
			default:
			}
		}
		if err != nil && ctx.Err() == nil {
			slog.Error("bill scheduling deferred", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-sweep.C:
		}
	}
}

func (s *Service) listen(ctx context.Context, wake chan<- struct{}) {
	for ctx.Err() == nil {
		connection, err := pgx.ConnectConfig(ctx, s.Store.DB.Config().ConnConfig.Copy())
		if err == nil {
			var schema string
			err = connection.QueryRow(ctx, "SELECT current_schema()").Scan(&schema)
			if err == nil {
				_, err = connection.Exec(ctx, "LISTEN "+store.SettlementNotification)
			}
			for err == nil {
				n, e := connection.WaitForNotification(ctx)
				err = e
				if err == nil && n.Payload == schema {
					select {
					case wake <- struct{}{}:
					default:
					}
				}
			}
			closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			_ = connection.Close(closeCtx)
			cancel()
		}
		// Periodic DB sweeps remain active throughout reconnects.
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}
