package store

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
)

func (s *Store) SaveTier(ctx context.Context, actor string, t Tier) (Tier, error) {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return t, e
	}
	defer tx.Rollback(ctx)
	// Use the same lock order as admission and principal synchronization.
	if _, e = tx.Exec(ctx, `SELECT id FROM users ORDER BY id FOR UPDATE`); e != nil {
		return t, e
	}
	e = tx.QueryRow(ctx, `INSERT INTO tiers(id,discount_bps,rpm,concurrency,min_principal,settlement_threshold)
 VALUES($1,$2,$3,$4,$5,COALESCE(NULLIF($6,'')::numeric,CASE $1 WHEN 'builder' THEN 0.05 WHEN 'scale' THEN 0.20 WHEN 'pro' THEN 1 ELSE 0.01 END))
 ON CONFLICT(id) DO UPDATE SET discount_bps=excluded.discount_bps,rpm=excluded.rpm,concurrency=excluded.concurrency,min_principal=excluded.min_principal,
 settlement_threshold=COALESCE(NULLIF($6,'')::numeric,tiers.settlement_threshold),version=tiers.version+1
 RETURNING version,settlement_threshold::text`, t.ID, t.DiscountBPS, t.RPM, t.Concurrency, t.MinPrincipal, t.SettlementThreshold).Scan(&t.Version, &t.SettlementThreshold)
	if e != nil {
		return t, e
	}
	payload, _ := json.Marshal(t)
	if _, e = tx.Exec(ctx, `INSERT INTO tier_versions(tier_id,version,snapshot) VALUES($1,$2,$3)`, t.ID, t.Version, payload); e != nil {
		return t, e
	}
	if s.ReserveContract != "" {
		_, e = tx.Exec(ctx, `UPDATE users u SET tier=(SELECT id FROM tiers WHERE min_principal<=COALESCE((SELECT principal/1000000 FROM reserve_accounts r WHERE r.user_id=u.id AND r.contract=$1 AND r.chain_id=$2),0) ORDER BY min_principal DESC LIMIT 1) WHERE NOT tier_override`, s.ReserveContract, s.ReserveNetworkID())
		if e != nil {
			return t, e
		}
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_events(id,actor_id,action,target,details) VALUES($1,$2,'tier.save',$3,$4)`, uuid.NewString(), actor, t.ID, payload); e != nil {
		return t, e
	}
	return t, tx.Commit(ctx)
}
