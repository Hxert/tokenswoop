package store

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// In-flight chain settlements may have already consumed allowance while their
// receipt is still being reconciled. Do not count that bill a second time. No
// estimated cost, wallet balance or reserve principal is used for admission.
func (s *Store) allowanceBlocked(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, user string, requireFresh bool) (bool, error) {
	p := s.WalletPolicy
	var blocked, fresh bool
	err := db.QueryRow(ctx, `SELECT a.allowance=0 OR a.allowance/1000000<=GREATEST(u.unpaid_balance-COALESCE((SELECT sum(amount) FROM bills WHERE user_id=u.id AND state='submitted'),0),0),a.checked_at>now()-interval '5 seconds'
 FROM users u JOIN payment_accounts a ON a.user_id=u.id
 WHERE u.id=$1 AND a.chain_id=$2 AND a.contract=$3 AND a.token=$4 AND a.provider=$5`, user, p.ChainID, p.Contract, p.Token, p.Provider).Scan(&blocked, &fresh)
	if err == pgx.ErrNoRows {
		return true, ErrSettlementUnavailable
	}
	if err != nil {
		return true, err
	}
	if requireFresh && !fresh {
		return true, ErrSettlementUnavailable
	}
	return blocked, nil
}
