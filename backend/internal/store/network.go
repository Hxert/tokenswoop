package store

import (
	"context"
	"fmt"
)

// Each deployment uses a separate database. Changing an environment variable
// must never reinterpret testnet accounts, signed journals or bills as mainnet.
func (s *Store) BindNetwork(ctx context.Context, chainID int64) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(78016345)`); err != nil {
		return err
	}
	var foreign bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bills WHERE chain_id<>$1 UNION ALL SELECT 1 FROM reserve_accounts WHERE chain_id<>$1 UNION ALL SELECT 1 FROM payment_accounts WHERE chain_id<>$1)`, chainID).Scan(&foreign); err != nil {
		return err
	}
	if foreign {
		return fmt.Errorf("database belongs to another network; use a separate database")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO deployment_network(id,chain_id) VALUES(1,$1) ON CONFLICT(id) DO NOTHING`, chainID); err != nil {
		return err
	}
	var stored int64
	if err = tx.QueryRow(ctx, `SELECT chain_id FROM deployment_network WHERE id=1`).Scan(&stored); err != nil {
		return err
	}
	if stored != chainID {
		return fmt.Errorf("database network differs from CHAIN_NETWORK; use a separate database")
	}
	return tx.Commit(ctx)
}
