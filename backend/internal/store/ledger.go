package store

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
	"slices"
	"time"
	"tokenswoop/backend/internal/pricing"
)

// Notifications are only wake-ups; confirmed debt in PostgreSQL remains the
// source of truth. The schema payload isolates development/test databases.
const SettlementNotification = "tokenswoop_settlement"

const paidCallsBlockedQuery = `SELECT EXISTS(SELECT 1 FROM bills WHERE user_id=$1 AND state<>'paid' AND insufficient_balance_at IS NOT NULL)`

// A confirmed funding failure remains blocking through retries and submission.
// A refreshed wallet snapshot is not evidence that the failed bill was paid.
func (s *Store) HasUnpaidSettlementFailure(ctx context.Context, userID string) (bool, error) {
	var blocked bool
	err := s.DB.QueryRow(ctx, paidCallsBlockedQuery, userID).Scan(&blocked)
	return blocked, err
}

func (s *Store) PaidCallsBlocked(ctx context.Context, userID string) (bool, error) {
	blocked, err := s.HasUnpaidSettlementFailure(ctx, userID)
	if err != nil || blocked {
		return blocked, err
	}
	if s.WalletPolicy != nil && s.WalletPolicy.CheckAllowance {
		return s.allowanceBlocked(ctx, s.DB, userID, false)
	}
	return false, nil
}

type Meter struct {
	Input, Output, CacheRead, CacheWrite, CacheWrite1h int64
	Amount                                             decimal.Decimal
	Source                                             string
	Trace                                              *pricing.Trace
	Protocol, UpstreamProtocol, Operation              string
	Stream                                             bool
	FirstTokenMS, GenerationMS                         *int64
	ResponseHandle                                     *ResponseHandle
}

type ResponseHandle struct {
	Digest          string
	ChannelRevision int
}

// BeginRequest records admission and price snapshots without changing funds.
func (s *Store) BeginRequest(ctx context.Context, id, userID, keyID string, m Model, t Tier, metadata ...Meter) error {
	billable := m.Pricing.Billable(m.Rates())
	for _, price := range []string{m.InputPrice, m.OutputPrice, m.CacheReadPrice, m.CacheWritePrice, m.CacheWrite1hPrice} {
		n, err := decimal.NewFromString(price)
		if err != nil || n.IsNegative() {
			return fmt.Errorf("invalid model price")
		}
	}
	billable = billable && t.DiscountBPS < 10000
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var status string
	if e = tx.QueryRow(ctx, "SELECT status FROM users WHERE id=$1 FOR UPDATE", userID).Scan(&status); e != nil {
		return e
	}
	if status != "active" {
		return ErrUnavailable
	}
	if s.ReserveContract != "" {
		var pending, fresh bool
		e = tx.QueryRow(ctx, `SELECT withdrawal_pending, checked_at>now()-interval '20 seconds' FROM reserve_accounts WHERE user_id=$1 AND chain_id=$3 AND contract=$2`, userID, s.ReserveContract, s.ReserveNetworkID()).Scan(&pending, &fresh)
		if e == pgx.ErrNoRows {
			return ErrReserveStale
		}
		if e != nil {
			return e
		}
		if pending && !s.ReserveDebtMode {
			return ErrWithdrawalPending
		}
		if !fresh {
			return ErrReserveStale
		}
		var valid bool
		if e = tx.QueryRow(ctx, `SELECT t.id=$2 AND t.version=$3 FROM users u JOIN tiers t ON t.id=u.tier WHERE u.id=$1 FOR SHARE OF t`, userID, t.ID, t.Version).Scan(&valid); e != nil {
			return e
		}
		if !valid {
			return ErrReserveStale
		}
	}
	var count int
	if e = tx.QueryRow(ctx, "SELECT count(*) FROM requests WHERE user_id=$1 AND state IN('accepted','in_flight')", userID).Scan(&count); e != nil {
		return e
	}
	if count >= t.Concurrency {
		return ErrConcurrency
	}
	if billable {
		if s.WalletPolicy == nil {
			return ErrSettlementUnavailable
		}
		// Finalized usage and settlement recovery use this same account lock.
		// Reject an unpaid funding failure even when allowance is sufficient.
		// Ordinary queued/submitted bills do not interrupt new calls.
		var blocked bool
		if e = tx.QueryRow(ctx, paidCallsBlockedQuery, userID).Scan(&blocked); e != nil {
			return e
		}
		if blocked {
			return ErrSettlementFailed
		}
		if s.WalletPolicy.CheckAllowance {
			blocked, e = s.allowanceBlocked(ctx, tx, userID, true)
			if e != nil {
				return e
			}
			if blocked {
				return ErrPaymentRequired
			}
		}
	}
	var key any
	if keyID != "" {
		key = keyID
		var budget *string
		var spent, keyStatus string
		var expires *time.Time
		var models []string
		e = tx.QueryRow(ctx, "SELECT budget::text,spent::text,status,expires_at,allowed_models FROM api_keys WHERE id=$1 AND user_id=$2 FOR UPDATE", keyID, userID).Scan(&budget, &spent, &keyStatus, &expires, &models)
		if e != nil {
			return e
		}
		if keyStatus != "active" || (expires != nil && !expires.After(time.Now())) || (len(models) > 0 && !slices.Contains(models, m.ID)) {
			return ErrUnavailable
		}
		if budget != nil {
			budgetN, e := decimal.NewFromString(*budget)
			if e != nil {
				return e
			}
			spentN, e := decimal.NewFromString(spent)
			if e != nil {
				return e
			}
			if billable && spentN.GreaterThanOrEqual(budgetN) {
				return ErrBudget
			}
		}
	}
	price, e := json.Marshal(m)
	if e != nil {
		return e
	}
	tier, e := json.Marshal(t)
	if e != nil {
		return e
	}
	meta := Meter{Operation: "generate"}
	if len(metadata) > 0 {
		meta = metadata[0]
	}
	if _, e = tx.Exec(ctx, "INSERT INTO requests(id,user_id,key_id,model_id,state,price_snapshot,tier_snapshot,client_protocol,operation,is_stream) VALUES($1,$2,$3,$4,'accepted',$5,$6,$7,$8,$9)", id, userID, key, m.ID, price, tier, meta.Protocol, meta.Operation, meta.Stream); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// Finish charges confirmed usage once. Previously admitted concurrent requests
// still settle in full after wallet funds or a key budget is exhausted. Uncertain
// usage is kept for review without a debit, hold or guessed fee.
func (s *Store) Finish(ctx context.Context, id, state string, m Meter, channel string, status int, code string, latency int64) error {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var userID, previous string
	var keyID *string
	e = tx.QueryRow(ctx, "SELECT user_id::text,key_id::text FROM requests WHERE id=$1", id).Scan(&userID, &keyID)
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "SELECT id FROM users WHERE id=$1 FOR UPDATE", userID); e != nil {
		return e
	}
	if keyID != nil {
		if _, e = tx.Exec(ctx, "SELECT id FROM api_keys WHERE id=$1 FOR UPDATE", *keyID); e != nil {
			return e
		}
	}
	if e = tx.QueryRow(ctx, "SELECT state FROM requests WHERE id=$1 FOR UPDATE", id).Scan(&previous); e != nil {
		return e
	}
	if previous == "completed" || previous == "failed" {
		return nil
	}
	if state != "completed" && state != "failed" && state != "usage_unknown" {
		return fmt.Errorf("invalid request transition")
	}
	cost := decimal.Zero
	if state == "completed" {
		if m.Amount.IsNegative() || !m.Amount.Equal(m.Amount.Round(18)) {
			return fmt.Errorf("invalid actual usage charge")
		}
		cost = m.Amount
		if _, e = tx.Exec(ctx, "UPDATE users SET unpaid_balance=unpaid_balance+$2 WHERE id=$1", userID, cost.String()); e != nil {
			return e
		}
		if keyID != nil {
			if _, e = tx.Exec(ctx, "UPDATE api_keys SET spent=spent+$2 WHERE id=$1", *keyID, cost.String()); e != nil {
				return e
			}
		}
	}
	var channelID any
	if channel != "" {
		channelID = channel
	}
	_, e = tx.Exec(ctx, "UPDATE requests SET state=$2,amount=$3,unpaid_amount=$3,input_tokens=$4,output_tokens=$5,cache_read_tokens=$6,cache_write_tokens=$7,cache_write_1h_tokens=$8,usage_source=$9,channel_id=COALESCE($10,channel_id),status_code=$11,error_code=$12,latency_ms=$13,updated_at=now(),completed_at=CASE WHEN $2='completed' THEN now() ELSE NULL END WHERE id=$1", id, state, cost.String(), m.Input, m.Output, m.CacheRead, m.CacheWrite, m.CacheWrite1h, m.Source, channelID, status, code, latency)
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE requests SET client_protocol=$2,upstream_protocol=$3,operation=$4,is_stream=$5,first_token_ms=$6,generation_ms=$7,pricing_trace=$8,tool_calls=$9 WHERE id=$1`, id, m.Protocol, m.UpstreamProtocol, m.Operation, m.Stream, m.FirstTokenMS, m.GenerationMS, m.Trace, toolFacts(m)); e != nil {
		return e
	}
	if state == "completed" && m.ResponseHandle != nil {
		if _, e = tx.Exec(ctx, `INSERT INTO response_handles(digest,user_id,request_id,model_id,channel_id,channel_revision,expires_at) SELECT $2,user_id,id,model_id,channel_id,$3,now()+interval '7 days' FROM requests WHERE id=$1 ON CONFLICT(digest) DO NOTHING`, id, m.ResponseHandle.Digest, m.ResponseHandle.ChannelRevision); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(ctx, "INSERT INTO outbox_events(id,kind,request_id) VALUES($1,$2,$3)", uuid.NewString(), "request."+state, id); e != nil {
		return e
	}
	if cost.IsPositive() {
		if _, e = tx.Exec(ctx, "SELECT pg_notify($1,current_schema())", SettlementNotification); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}

func toolFacts(m Meter) map[string]int64 {
	if m.Trace != nil && m.Trace.Facts.Tools != nil {
		return m.Trace.Facts.Tools
	}
	return map[string]int64{}
}

func (s *Store) ResolveUnknown(ctx context.Context, actor, id string) error {
	// Match the account -> request lock order used by completion.
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var userID, state string
	if e = tx.QueryRow(ctx, "SELECT user_id::text FROM requests WHERE id=$1", id).Scan(&userID); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "SELECT id FROM users WHERE id=$1 FOR UPDATE", userID); e != nil {
		return e
	}
	if e = tx.QueryRow(ctx, "SELECT state FROM requests WHERE id=$1 FOR UPDATE", id).Scan(&state); e != nil {
		return e
	}
	if state != "usage_unknown" {
		return fmt.Errorf("only unknown requests can be waived")
	}
	if _, e = tx.Exec(ctx, "UPDATE requests SET state='failed',amount=0,error_code='waived_by_operator',updated_at=now() WHERE id=$1", id); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "INSERT INTO audit_events(id,actor_id,action,target) VALUES($1,$2,'request.waive',$3)", uuid.NewString(), actor, id); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

func IsMissing(err error) bool { return err == pgx.ErrNoRows }
