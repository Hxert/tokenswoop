package httpapi

import (
	"github.com/gin-gonic/gin"
	"tokenswoop/backend/internal/config"
)

func (a *App) publicTiers(c *gin.Context) {
	rows, e := a.Store.JSONRows(c, `SELECT to_jsonb(t)||jsonb_build_object('min_principal',min_principal::text,'settlement_threshold',settlement_threshold::text) FROM tiers t WHERE min_principal IS NOT NULL ORDER BY min_principal`)
	if e != nil {
		fail(c, 503, "database_unavailable", "Tier policy unavailable.")
		return
	}
	c.JSON(200, gin.H{"data": rows, "reserve_enabled": a.Reserve != nil})
}
func (a *App) reserveStatus(c *gin.Context) { a.reserveResponse(c, false) }
func (a *App) refreshReserve(c *gin.Context) {
	if a.Reserve == nil {
		fail(c, 503, "reserve_disabled", "Reserve is not configured.")
		return
	}
	ok, e := a.allow(c, "reserve-refresh:"+currentUser(c).ID, 20)
	if e != nil || !ok {
		fail(c, 429, "rate_limited", "Please wait before refreshing again.")
		return
	}
	a.reserveResponse(c, true)
}
func (a *App) reserveResponse(c *gin.Context, force bool) {
	s := a.Reserve
	if s == nil {
		c.JSON(200, gin.H{"enabled": false})
		return
	}
	u := currentUser(c)
	v, e := s.Refresh(c, u.ID, force)
	if e != nil {
		fail(c, 503, "reserve_unavailable", "Could not verify finalized reserve state. Try again shortly.")
		return
	}
	u, e = a.Store.User(c, u.ID)
	if e != nil {
		fail(c, 503, "database_unavailable", "Account unavailable.")
		return
	}
	tier, e := a.Store.Tier(c, u.Tier)
	if e != nil {
		fail(c, 503, "database_unavailable", "Tier unavailable.")
		return
	}
	clearance, e := s.Clearance(c, u.ID)
	if e != nil {
		fail(c, 503, "database_unavailable", "Clearance unavailable.")
		return
	}
	var worker bool
	if e = a.Store.DB.QueryRow(c, `SELECT EXISTS(SELECT 1 FROM worker_heartbeats WHERE id='reserve' AND last_seen>now()-interval '90 seconds')`).Scan(&worker); e != nil {
		fail(c, 503, "database_unavailable", "Worker status unavailable.")
		return
	}
	network := config.NetworkFor(a.Config.ChainID)
	reports, e := a.Store.JSONRows(c, `SELECT jsonb_build_object('tx_hash',COALESCE(resolved_tx_hash,tx_hash),'state',state,'created_at',created_at,'items',items) FROM reserve_debt_reports WHERE user_id=$1 AND contract=$2 AND chain_id=$3 ORDER BY created_at DESC LIMIT 20`, u.ID, s.Config.Contract, s.Config.NetworkID())
	if e != nil {
		fail(c, 503, "database_unavailable", "Debt report history unavailable.")
		return
	}
	c.JSON(200, gin.H{"enabled": true, "mode": s.Config.Mode, "custody": s.Config.Custody, "manager": s.Config.Manager, "reports": reports, "chain_id": network.ChainID, "symbol": network.Symbol, "network": network.Name, "testnet_mock": network.Testnet && s.Config.Custody != "vault", "contract": s.Config.Contract, "token": s.Token, "operator": s.Config.Operator, "snapshot": v, "tier": tier, "tier_override": u.TierOverride, "clearance": clearance, "worker_ready": worker})
}
func (a *App) adminReserves(c *gin.Context) {
	rows, e := a.Store.JSONRows(c, `SELECT jsonb_build_object('user_id',u.id,'wallet',u.wallet,'tier',u.tier,'tier_override',u.tier_override,'unpaid',u.unpaid_balance::text,'principal_units',r.principal::text,'snapshot',r.snapshot,'checked_at',r.checked_at,'in_flight',(SELECT count(*) FROM requests WHERE user_id=u.id AND state IN('accepted','in_flight')),'usage_unknown',(SELECT count(*) FROM requests WHERE user_id=u.id AND state='usage_unknown'),'open_bills',(SELECT count(*) FROM bills WHERE user_id=u.id AND state<>'paid')) FROM reserve_accounts r JOIN users u ON u.id=r.user_id WHERE r.contract=$1 AND r.chain_id=$2 ORDER BY r.withdrawal_pending DESC,r.principal DESC,u.created_at LIMIT 100`, a.Store.ReserveContract, a.Store.ReserveNetworkID())
	if e != nil {
		fail(c, 503, "database_unavailable", "Reserve accounts unavailable.")
		return
	}
	c.JSON(200, gin.H{"data": rows, "enabled": a.Reserve != nil})
}
