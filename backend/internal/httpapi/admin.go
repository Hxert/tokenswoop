package httpapi

import (
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"
	"regexp"
	"strings"
	"time"
	"tokenswoop/backend/internal/store"
)

var modelID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/@-]{0,127}$`)

func (a *App) adminModels(c *gin.Context) {
	m, e := a.Store.Models(c, true)
	if e != nil {
		fail(c, 503, "database_unavailable", "Models unavailable.")
		return
	}
	c.JSON(200, gin.H{"data": m})
}
func (a *App) saveModel(c *gin.Context) {
	var m store.Model
	if c.ShouldBindJSON(&m) != nil || !modelID.MatchString(m.ID) || strings.TrimSpace(m.Name) == "" || len(m.Name) > 100 || len(m.Vendor) < 1 || len(m.Vendor) > 80 {
		fail(c, 400, "invalid_model", "Provide a valid model ID, name and provider.")
		return
	}
	for _, price := range []string{m.InputPrice, m.OutputPrice, m.CacheReadPrice, m.CacheWritePrice, m.CacheWrite1hPrice} {
		if !validDecimal(price, 12, true) {
			fail(c, 400, "invalid_price", "Prices must be non-negative decimal strings with at most 12 decimal places.")
			return
		}
	}
	if e := m.Pricing.Validate(); e != nil {
		fail(c, 400, "invalid_pricing", e.Error())
		return
	}
	saved, e := a.Store.SaveModel(c, currentUser(c).ID, m)
	if e != nil {
		fail(c, 503, "database_unavailable", "Could not save model.")
		return
	}
	c.JSON(200, saved)
}
func (a *App) channels(c *gin.Context) {
	channels, e := a.Store.Channels(c, "", true)
	if e != nil {
		fail(c, 503, "database_unavailable", "Channels unavailable.")
		return
	}
	c.JSON(200, gin.H{"data": channels})
}
func (a *App) users(c *gin.Context) {
	rows, e := a.Store.JSONRows(c, `SELECT jsonb_build_object('id',id,'wallet',wallet,'role',role,'status',status,'tier',tier,'tier_override',tier_override,'unpaid_balance',unpaid_balance::text,'created_at',created_at) FROM users WHERE ($1='' OR wallet ILIKE '%' || $1 || '%') ORDER BY created_at DESC LIMIT 100`, c.Query("wallet"))
	if e != nil {
		fail(c, 503, "database_unavailable", "Users unavailable.")
		return
	}
	c.JSON(200, gin.H{"data": rows})
}
func (a *App) updateUser(c *gin.Context) {
	var in struct {
		Status string `json:"status"`
		Tier   string `json:"tier"`
		Mode   string `json:"tier_mode"`
	}
	id := c.Param("id")
	if !validID(id) || c.ShouldBindJSON(&in) != nil || (in.Status != "active" && in.Status != "suspended") {
		fail(c, 400, "invalid_user", "Invalid user or status.")
		return
	}
	if id == currentUser(c).ID && in.Status == "suspended" {
		fail(c, 409, "self_suspend", "You cannot suspend your current account.")
		return
	}
	if in.Mode != "" && in.Mode != "auto" && in.Mode != "manual" {
		fail(c, 400, "invalid_tier_mode", "Choose automatic or manual tier.")
		return
	}
	if _, e := a.Store.Tier(c, in.Tier); e != nil && in.Mode != "auto" {
		fail(c, 400, "invalid_tier", "Unknown tier.")
		return
	}
	tx, e := a.Store.DB.Begin(c)
	if e != nil {
		fail(c, 503, "database_unavailable", "Could not update user.")
		return
	}
	defer tx.Rollback(c)
	tag, e := tx.Exec(c, `UPDATE users u SET status=$2,tier_override=$4,tier=CASE WHEN $4 THEN $3 ELSE (SELECT id FROM tiers WHERE min_principal<=COALESCE((SELECT principal/1000000 FROM reserve_accounts r WHERE r.user_id=u.id AND r.contract=$5 AND r.chain_id=$6),0) ORDER BY min_principal DESC LIMIT 1) END WHERE id=$1`, id, in.Status, in.Tier, in.Mode != "auto", a.Store.ReserveContract, a.Store.ReserveNetworkID())
	if e != nil || tag.RowsAffected() == 0 {
		fail(c, 404, "not_found", "User not found.")
		return
	}
	details, _ := json.Marshal(in)
	_, e = tx.Exec(c, "INSERT INTO audit_events(id,actor_id,action,target,details) VALUES($1,$2,'user.update',$3,$4)", uuid.NewString(), currentUser(c).ID, id, details)
	if e == nil {
		e = tx.Commit(c)
	}
	if e != nil {
		fail(c, 503, "database_unavailable", "Could not update user.")
		return
	}
	c.Status(204)
}
func (a *App) tiers(c *gin.Context) {
	rows, e := a.Store.JSONRows(c, "SELECT to_jsonb(t)||jsonb_build_object('min_principal',min_principal::text,'settlement_threshold',settlement_threshold::text) FROM tiers t ORDER BY min_principal NULLS LAST,id")
	if e != nil {
		fail(c, 503, "database_unavailable", "Tiers unavailable.")
		return
	}
	c.JSON(200, gin.H{"data": rows, "source": "operator_configuration", "reserve_tiers_enabled": a.Reserve != nil, "max_settlement_threshold": "100"})
}
func (a *App) saveTier(c *gin.Context) {
	var t store.Tier
	if c.ShouldBindJSON(&t) != nil || !regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`).MatchString(t.ID) || t.RPM < 1 || t.RPM > 100000 || t.Concurrency < 1 || t.Concurrency > 1000 || t.DiscountBPS < 0 || t.DiscountBPS > 10000 {
		fail(c, 400, "invalid_tier", "Invalid tier ID, rate limits or discount.")
		return
	}
	if t.MinPrincipal != nil && !validDecimal(*t.MinPrincipal, 6, true) {
		fail(c, 400, "invalid_threshold", "Use a non-negative principal with at most 6 decimal places.")
		return
	}
	if t.MinPrincipal != nil {
		normalized := decimal.RequireFromString(*t.MinPrincipal).String()
		t.MinPrincipal = &normalized
	}
	if t.ID == "free" && (t.MinPrincipal == nil || *t.MinPrincipal != "0") {
		fail(c, 400, "invalid_threshold", "Free tier must start at zero principal.")
		return
	}
	if t.SettlementThreshold == "" {
		// Older clients preserve the saved threshold when editing other fields.
		if previous, err := a.Store.Tier(c, t.ID); err == nil {
			t.SettlementThreshold = previous.SettlementThreshold
		} else {
			t.SettlementThreshold = "0.01"
			switch t.ID {
			case "builder":
				t.SettlementThreshold = "0.05"
			case "scale":
				t.SettlementThreshold = "0.2"
			case "pro":
				t.SettlementThreshold = "1"
			}
		}
	}
	if !validDecimal(t.SettlementThreshold, 6, false) || decimal.RequireFromString(t.SettlementThreshold).GreaterThan(decimal.NewFromInt(100)) {
		fail(c, 400, "invalid_settlement_threshold", "Settlement threshold must be 0.000001–100 kUSDC, with at most 6 decimal places.")
		return
	}
	t.SettlementThreshold = decimal.RequireFromString(t.SettlementThreshold).String()
	t, e := a.Store.SaveTier(c, currentUser(c).ID, t)
	if e != nil {
		var pg *pgconn.PgError
		if errors.As(e, &pg) && pg.Code == "23505" {
			fail(c, 409, "threshold_conflict", "Another tier already uses that principal threshold.")
			return
		}
		fail(c, 503, "database_unavailable", "Could not save tier.")
		return
	}
	c.JSON(200, t)
}
func (a *App) audit(c *gin.Context) {
	rows, e := a.Store.JSONRows(c, "SELECT row_to_json(t) FROM (SELECT id,actor_id,action,target,details,created_at FROM audit_events ORDER BY created_at DESC LIMIT 100) t")
	if e != nil {
		fail(c, 503, "database_unavailable", "Audit unavailable.")
		return
	}
	c.JSON(200, gin.H{"data": rows})
}
func (a *App) unknown(c *gin.Context) {
	rows, e := a.Store.JSONRows(c, `SELECT jsonb_build_object('id',r.id,'wallet',u.wallet,'model_id',r.model_id,'error_code',r.error_code,'created_at',r.created_at) FROM requests r JOIN users u ON u.id=r.user_id WHERE r.state='usage_unknown' ORDER BY r.created_at LIMIT 100`)
	if e != nil {
		fail(c, 503, "database_unavailable", "Requests unavailable.")
		return
	}
	c.JSON(200, gin.H{"data": rows})
}
func (a *App) waive(c *gin.Context) {
	if !validID(c.Param("id")) {
		fail(c, 400, "invalid_id", "Invalid request.")
		return
	}
	if e := a.Store.ResolveUnknown(c, currentUser(c).ID, c.Param("id")); e != nil {
		fail(c, 409, "resolution_failed", "Only an existing unresolved request can be waived.")
		return
	}
	c.Status(204)
}
func (a *App) health(c *gin.Context) {
	var seen *time.Time
	var unknown, pending, unhealthy int
	var requests, failed, latency int64
	e := a.Store.DB.QueryRow(c, "SELECT max(last_seen) FROM worker_heartbeats WHERE id='maintenance'").Scan(&seen)
	if e == nil {
		e = a.Store.DB.QueryRow(c, "SELECT count(*) FROM requests WHERE state='usage_unknown'").Scan(&unknown)
	}
	if e == nil {
		e = a.Store.DB.QueryRow(c, "SELECT count(*) FROM outbox_events WHERE processed_at IS NULL").Scan(&pending)
	}
	if e == nil {
		e = a.Store.DB.QueryRow(c, "SELECT count(*) FROM channels WHERE enabled AND disabled_until>now()").Scan(&unhealthy)
	}
	if e != nil {
		fail(c, 503, "database_unavailable", "Health unavailable.")
		return
	}
	if e = a.Store.DB.QueryRow(c, "SELECT count(*),count(*) FILTER(WHERE state IN('failed','usage_unknown')),COALESCE(round(avg(latency_ms) FILTER(WHERE state='completed')),0)::bigint FROM requests WHERE created_at>now()-interval '24 hours'").Scan(&requests, &failed, &latency); e != nil {
		fail(c, 503, "database_unavailable", "Request metrics unavailable.")
		return
	}
	var settlementSeen *time.Time
	var queuedBills, blockedBills, reviewBills int
	if e = a.Store.DB.QueryRow(c, `SELECT count(*) FILTER(WHERE state IN('queued','submitted')),count(*) FILTER(WHERE state='blocked'),count(*) FILTER(WHERE state='review') FROM bills`).Scan(&queuedBills, &blockedBills, &reviewBills); e != nil {
		fail(c, 503, "database_unavailable", "Billing health unavailable.")
		return
	}
	if e = a.Store.DB.QueryRow(c, `SELECT max(last_seen) FROM worker_heartbeats WHERE id='settlement'`).Scan(&settlementSeen); e != nil {
		fail(c, 503, "database_unavailable", "Billing health unavailable.")
		return
	}
	c.JSON(200, gin.H{"database": "ready", "redis_ready": a.Redis.Ping(c).Err() == nil, "worker_last_seen": seen, "worker_ready": seen != nil && time.Since(*seen) < time.Minute, "unknown_requests": unknown, "outbox_pending": pending, "cooling_channels": unhealthy, "settlement_enabled": a.Payments != nil, "settlement_ready": a.Payments != nil && settlementSeen != nil && time.Since(*settlementSeen) < 90*time.Second, "settlement_last_seen": settlementSeen, "queued_bills": queuedBills, "blocked_bills": blockedBills, "review_bills": reviewBills, "requests_24h": requests, "failed_or_unknown_24h": failed, "avg_latency_ms_24h": latency})
}
