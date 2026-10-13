package store

import (
	"context"
	"strings"
)

// PlatformSigningLock coordinates payment, reserve, faucet and platform-sponsored
// Mera transactions. Durable journals reserve a nonce before RPC sees it.
const PlatformSigningLock int64 = 78016347
const DedicatedMeraSigningLock int64 = 78016356

// SignerPending must be checked under the appropriate signing lock before
// creating a fresh transaction. A pending signature can exist even when both
// eth_getTransactionCount(latest) and (pending) return the same number.
func (s *Store) SignerPending(ctx context.Context, chainID int64, signer string) (bool, error) {
	var pending bool
	err := s.DB.QueryRow(ctx, `SELECT EXISTS(
 SELECT 1 FROM payment_attempts WHERE chain_id=$1 AND lower(provider)=$2 AND state='pending'
 UNION ALL SELECT 1 FROM reserve_debt_reports WHERE chain_id=$1 AND lower(operator)=$2 AND state='pending'
 UNION ALL SELECT 1 FROM reserve_approvals WHERE chain_id=$1 AND lower(operator)=$2 AND state='pending'
 UNION ALL SELECT 1 FROM mera_transactions WHERE chain_id=$1 AND lower(sponsor)=$2 AND state IN ('submitted','review')
 UNION ALL SELECT 1 FROM testnet_faucet_claims WHERE chain_id=$1 AND lower(sponsor)=$2 AND state IN ('submitted','review')
 )`, chainID, strings.ToLower(signer)).Scan(&pending)
	return pending, err
}
