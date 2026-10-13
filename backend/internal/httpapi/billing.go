package httpapi

import (
	"errors"
	"github.com/gin-gonic/gin"
	"regexp"
	"strconv"
	"tokenswoop/backend/internal/config"
	"tokenswoop/backend/internal/payments"
	"tokenswoop/backend/internal/store"
)

var billID = regexp.MustCompile(`^0x[0-9a-f]{64}$`)

func (a *App) billing(c *gin.Context) {
	network := config.NetworkFor(a.Config.ChainID)
	result := gin.H{"enabled": a.Payments != nil, "chain_id": a.Config.ChainID, "decimals": 6, "symbol": network.Symbol, "network": network.Name, "explorer": network.Explorer, "worker_ready": false}
	if a.Config.ChainID == 10143 {
		result["faucet"] = gin.H{"enabled": a.Faucet.Enabled()}
	}
	if p := a.Payments; p != nil {
		result["contract"] = p.Config.Contract
		result["token"] = p.Config.Token
		result["provider"] = p.Config.Provider
		result["recipient"] = p.Config.RecipientAddress()
		if snapshot, e := p.Refresh(c, currentUser(c).ID, false); e != nil {
			result["chain_error"] = "Wallet balance is temporarily unavailable."
		} else {
			result["wallet"] = snapshot
		}
		blocked, err := a.Store.PaidCallsBlocked(c, currentUser(c).ID)
		if errors.Is(err, store.ErrSettlementUnavailable) {
			blocked = true
			err = nil
		}
		if err != nil {
			fail(c, 503, "database_unavailable", "Payment status unavailable.")
			return
		}
		result["calls_blocked"] = blocked
		tier, err := a.Store.Tier(c, currentUser(c).Tier)
		if err != nil {
			fail(c, 503, "database_unavailable", "Settlement policy unavailable.")
			return
		}
		result["settlement_threshold"] = tier.SettlementThreshold
		result["settlement_window_seconds"] = int(payments.SettlementWindow.Seconds())
		var ready bool
		if e := a.Store.DB.QueryRow(c, `SELECT EXISTS(SELECT 1 FROM worker_heartbeats WHERE id='settlement' AND last_seen>now()-interval '90 seconds')`).Scan(&ready); e != nil {
			fail(c, 503, "database_unavailable", "Payment status unavailable.")
			return
		}
		result["worker_ready"] = ready

	}
	rows, e := a.Store.JSONRows(c, `SELECT jsonb_build_object('outstanding',u.unpaid_balance::text,'paid',COALESCE((SELECT sum(wallet_paid_amount) FROM requests WHERE user_id=u.id),0)::text,'blocked',COALESCE((SELECT sum(amount) FROM bills WHERE user_id=u.id AND state IN('blocked','review')),0)::text,'pending',(u.unpaid_balance-COALESCE((SELECT sum(amount) FROM bills WHERE user_id=u.id AND state IN('blocked','review')),0))::text) FROM users u WHERE id=$1`, currentUser(c).ID)
	if e != nil || len(rows) != 1 {
		fail(c, 503, "database_unavailable", "Billing summary unavailable.")
		return
	}
	result["summary"] = rows[0]
	c.JSON(200, result)
}
func (a *App) paymentAction(c *gin.Context) bool {
	if a.Payments == nil {
		fail(c, 503, "settlement_disabled", "Wallet settlement is not enabled.")
		return false
	}
	ok, e := a.allow(c, "payment:"+currentUser(c).ID, 10)
	if e != nil || !ok {
		fail(c, 429, "payment_rate_limited", "Please wait a minute before checking again.")
		return false
	}
	return true
}
func (a *App) refreshPayment(c *gin.Context) {
	if !a.paymentAction(c) {
		return
	}
	v, e := a.Payments.Refresh(c, currentUser(c).ID, true)
	if e != nil {
		fail(c, 503, "payment_rpc_unavailable", "Could not refresh wallet conditions. No payment was sent.")
		return
	}
	c.JSON(200, v)
}
func (a *App) bills(c *gin.Context) {
	offset, _ := strconv.Atoi(c.Query("offset"))
	if offset < 0 || offset > 100000 {
		fail(c, 400, "invalid_offset", "Invalid page.")
		return
	}
	rows, e := a.Store.JSONRows(c, `SELECT to_jsonb(b)-'next_attempt_at'||jsonb_build_object('amount',b.amount::text,'amount_units',b.amount_units::text,'request_count',(SELECT count(*) FROM bill_items WHERE bill_id=b.id)) FROM bills b WHERE user_id=$1 ORDER BY created_at DESC,id DESC LIMIT 50 OFFSET $2`, currentUser(c).ID, offset)
	if e != nil {
		fail(c, 503, "database_unavailable", "Bills unavailable.")
		return
	}
	c.JSON(200, gin.H{"data": rows, "has_more": len(rows) == 50})
}
func (a *App) billDetail(c *gin.Context) {
	if !billID.MatchString(c.Param("id")) {
		fail(c, 400, "invalid_bill", "Invalid bill.")
		return
	}
	rows, e := a.Store.JSONRows(c, `SELECT to_jsonb(b)-'next_attempt_at'||jsonb_build_object('amount',b.amount::text,'amount_units',b.amount_units::text) FROM bills b WHERE id=$1 AND user_id=$2`, c.Param("id"), currentUser(c).ID)
	if e != nil || len(rows) == 0 {
		fail(c, 404, "bill_not_found", "Bill not found.")
		return
	}
	items, e := a.Store.JSONRows(c, `SELECT jsonb_build_object('request_id',r.id,'model_id',r.model_id,'amount',i.amount::text,'request_amount',r.amount::text,'waived_amount',r.waived_amount::text,'wallet_paid_amount',r.wallet_paid_amount::text,'unpaid_amount',r.unpaid_amount::text,'price_snapshot',r.price_snapshot,'tier_snapshot',r.tier_snapshot,'created_at',r.created_at) FROM bill_items i JOIN requests r ON r.id=i.request_id WHERE i.bill_id=$1 ORDER BY r.created_at,r.id`, c.Param("id"))
	if e != nil {
		fail(c, 503, "database_unavailable", "Bill items unavailable.")
		return
	}
	attempts, e := a.Store.JSONRows(c, `SELECT jsonb_build_object('tx_hash',COALESCE(resolved_tx_hash,(SELECT r.tx_hash FROM chain_tx_replacements r WHERE r.root_hash=p.tx_hash ORDER BY r.created_at DESC LIMIT 1),tx_hash),'original_tx_hash',tx_hash,'state',state,'created_at',created_at) FROM payment_attempts p WHERE bill_id=$1 ORDER BY created_at`, c.Param("id"))
	if e != nil {
		fail(c, 503, "database_unavailable", "Payment attempts unavailable.")
		return
	}
	c.JSON(200, gin.H{"bill": rows[0], "items": items, "attempts": attempts})
}
func (a *App) retryBill(c *gin.Context) {
	if !a.paymentAction(c) {
		return
	}
	if !billID.MatchString(c.Param("id")) {
		fail(c, 400, "invalid_bill", "Invalid bill.")
		return
	}
	if _, e := a.Payments.Bill(c, currentUser(c).ID, c.Param("id")); e != nil {
		fail(c, 404, "bill_not_found", "Bill not found.")
		return
	}
	if e := a.Payments.Retry(c, currentUser(c).ID, c.Param("id")); e != nil {
		fail(c, 409, "bill_not_retryable", "This bill is already paid, confirming, or requires operator review.")
		return
	}
	c.JSON(202, gin.H{"status": "retry_requested", "bill_id": c.Param("id")})
}
