package httpapi

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"strconv"
	"strings"
	"time"
	"tokenswoop/backend/internal/security"
)

func (a *App) keys(c *gin.Context) {
	rows, e := a.Store.JSONRows(c, `SELECT jsonb_build_object('id',id,'name',name,'prefix',prefix,'status',status,'allowed_models',allowed_models,'budget',budget::text,'spent',spent::text,'expires_at',expires_at,'created_at',created_at) FROM api_keys WHERE user_id=$1 ORDER BY (status='active') DESC,created_at DESC LIMIT 100`, currentUser(c).ID)
	if e != nil {
		fail(c, 503, "database_unavailable", "Keys unavailable.")
		return
	}
	c.JSON(200, gin.H{"data": rows})
}
func (a *App) createKey(c *gin.Context) {
	var in struct {
		Name    string     `json:"name"`
		Models  []string   `json:"allowed_models"`
		Budget  *string    `json:"budget"`
		Expires *time.Time `json:"expires_at"`
	}
	if c.ShouldBindJSON(&in) != nil || len(strings.TrimSpace(in.Name)) < 1 || len(in.Name) > 80 || len(in.Models) > 100 || (in.Budget != nil && !validDecimal(*in.Budget, 18, false)) || (in.Expires != nil && !in.Expires.After(time.Now())) {
		fail(c, 400, "invalid_key", "Provide a name, a positive budget or null for unlimited, and optional future expiry.")
		return
	}
	for _, model := range in.Models {
		if _, e := a.Store.Model(c, model); e != nil {
			fail(c, 400, "invalid_model", "Unknown or disabled model in key permissions.")
			return
		}
	}
	if in.Models == nil {
		in.Models = []string{}
	}
	tx, e := a.Store.DB.Begin(c)
	if e != nil {
		fail(c, 503, "database_unavailable", "Could not create key.")
		return
	}
	defer tx.Rollback(c)
	user := currentUser(c)
	var active int
	if _, e = tx.Exec(c, "SELECT id FROM users WHERE id=$1 FOR UPDATE", user.ID); e == nil {
		e = tx.QueryRow(c, "SELECT count(*) FROM api_keys WHERE user_id=$1 AND status='active'", user.ID).Scan(&active)
	}
	if e != nil {
		fail(c, 503, "database_unavailable", "Could not create key.")
		return
	}
	if active >= 50 {
		fail(c, 409, "key_limit", "Revoke an existing key before creating another (limit 50).")
		return
	}
	id, token := uuid.NewString(), "sk-"+security.Token()
	prefix := token[:11]
	_, e = tx.Exec(c, "INSERT INTO api_keys(id,user_id,name,prefix,token_hash,allowed_models,budget,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", id, user.ID, strings.TrimSpace(in.Name), prefix, security.Digest(a.Config.Secret, "api-key", token), in.Models, in.Budget, in.Expires)
	if e == nil {
		_, e = tx.Exec(c, "INSERT INTO audit_events(id,actor_id,action,target) VALUES($1,$2,'key.create',$3)", uuid.NewString(), user.ID, id)
	}
	if e == nil {
		e = tx.Commit(c)
	}
	if e != nil {
		fail(c, 503, "database_unavailable", "Could not create key.")
		return
	}
	c.JSON(201, gin.H{"id": id, "key": token, "prefix": prefix, "notice": "Copy this key now. It will not be shown again."})
}
func (a *App) revokeKey(c *gin.Context) {
	if !validID(c.Param("id")) {
		fail(c, 400, "invalid_id", "Invalid key.")
		return
	}
	tx, e := a.Store.DB.Begin(c)
	if e != nil {
		fail(c, 503, "database_unavailable", "Could not revoke key.")
		return
	}
	defer tx.Rollback(c)
	tag, e := tx.Exec(c, "UPDATE api_keys SET status='revoked' WHERE id=$1 AND user_id=$2", c.Param("id"), currentUser(c).ID)
	if e != nil || tag.RowsAffected() == 0 {
		fail(c, 404, "key_not_found", "Key not found.")
		return
	}
	_, e = tx.Exec(c, "INSERT INTO audit_events(id,actor_id,action,target) VALUES($1,$2,'key.revoke',$3)", uuid.NewString(), currentUser(c).ID, c.Param("id"))
	if e == nil {
		e = tx.Commit(c)
	}
	if e != nil {
		fail(c, 503, "database_unavailable", "Could not revoke key.")
		return
	}
	c.Status(204)
}

func validDecimal(v string, scale int, zero bool) bool {
	if len(v) > 50 {
		return false
	}
	n, e := decimal.NewFromString(v)
	return e == nil && !n.IsNegative() && (zero || n.IsPositive()) && n.Exponent() >= int32(-scale) && n.LessThanOrEqual(decimal.NewFromInt(1000000000000))
}
func window(c *gin.Context) (time.Time, time.Time, bool) {
	until := time.Now().Add(time.Second)
	since := until.AddDate(0, 0, -30)
	var e error
	if v := c.Query("from"); v != "" {
		since, e = time.Parse(time.RFC3339, v)
		if e != nil {
			return since, until, false
		}
	}
	if v := c.Query("to"); v != "" {
		until, e = time.Parse(time.RFC3339, v)
		if e != nil {
			return since, until, false
		}
	}
	return since, until, since.Before(until) && until.Sub(since) <= 366*24*time.Hour
}
func (a *App) usage(c *gin.Context) {
	from, to, ok := window(c)
	if !ok {
		fail(c, 400, "invalid_range", "Use ISO timestamps for a range of at most 366 days.")
		return
	}
	offset, _ := strconv.Atoi(c.Query("offset"))
	if offset < 0 || offset > 100000 {
		fail(c, 400, "invalid_offset", "Invalid offset.")
		return
	}
	rows, e := a.Store.JSONRows(c, `SELECT jsonb_build_object('id',id,'model_id',model_id,'key_id',key_id,'state',state,'amount',CASE WHEN state='completed' THEN amount::text ELSE '0' END,'wallet_paid_amount',wallet_paid_amount::text,'unpaid_amount',unpaid_amount::text,'waived_amount',waived_amount::text,'input_tokens',input_tokens,'output_tokens',output_tokens,'cache_read_tokens',cache_read_tokens,'cache_write_tokens',cache_write_tokens,'cache_write_1h_tokens',cache_write_1h_tokens,'price_version',price_snapshot->'price_version','first_token_ms',first_token_ms,'generation_ms',generation_ms,'pricing_trace',pricing_trace,'latency_ms',latency_ms,'error_code',error_code,'created_at',created_at) FROM requests WHERE user_id=$1 AND ($2='' OR model_id=$2) AND ($3='' OR key_id::text=$3) AND created_at>=$4 AND created_at<$5 ORDER BY created_at DESC,id DESC LIMIT 50 OFFSET $6`, currentUser(c).ID, c.Query("model"), c.Query("key"), from, to, offset)
	if e != nil {
		fail(c, 503, "database_unavailable", "Usage unavailable.")
		return
	}
	c.JSON(200, gin.H{"data": rows, "offset": offset, "limit": 50, "has_more": len(rows) == 50})
}

func (a *App) receipt(c *gin.Context) {
	if !validID(c.Param("id")) {
		fail(c, 400, "invalid_id", "Invalid request.")
		return
	}
	rows, e := a.Store.JSONRows(c, `SELECT jsonb_build_object('id',id,'model_id',model_id,'state',state,'amount',CASE WHEN state='completed' THEN amount::text ELSE '0' END,'wallet_paid_amount',wallet_paid_amount::text,'unpaid_amount',unpaid_amount::text,'waived_amount',waived_amount::text,'input_tokens',input_tokens,'output_tokens',output_tokens,'first_token_ms',first_token_ms,'generation_ms',generation_ms,'pricing_trace',pricing_trace,'latency_ms',latency_ms,'created_at',created_at,'bills',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',b.id,'state',b.state,'amount',i.amount::text,'tx_hash',b.tx_hash)) FROM bill_items i JOIN bills b ON b.id=i.bill_id WHERE i.request_id=requests.id),'[]'::jsonb)) FROM requests WHERE id=$1 AND user_id=$2`, c.Param("id"), currentUser(c).ID)
	if e != nil || len(rows) == 0 {
		fail(c, 404, "not_found", "Request not found.")
		return
	}
	attempts, e := a.Store.JSONRows(c, `SELECT jsonb_build_object('status',status_code,'outcome',outcome,'diagnostics',diagnostics,'created_at',created_at) FROM request_attempts WHERE request_id=$1 ORDER BY created_at,id`, c.Param("id"))
	if e != nil {
		fail(c, 503, "database_unavailable", "Request diagnostics unavailable.")
		return
	}
	var receipt map[string]any
	if json.Unmarshal(rows[0], &receipt) != nil {
		fail(c, 503, "database_unavailable", "Receipt unavailable.")
		return
	}
	receipt["attempts"] = attempts
	c.JSON(200, receipt)
}
func (a *App) stats(c *gin.Context) {
	from, to, ok := window(c)
	if !ok {
		fail(c, 400, "invalid_range", "Invalid time range.")
		return
	}
	rows, e := a.Store.JSONRows(c, `SELECT jsonb_build_object('requests',count(*),'completed',count(*) FILTER(WHERE state='completed'),'pending',count(*) FILTER(WHERE state IN('accepted','in_flight','usage_unknown')),'spent',COALESCE(sum(amount) FILTER(WHERE state='completed'),0)::text,'waived',COALESCE(sum(waived_amount),0)::text,'wallet_paid',COALESCE(sum(wallet_paid_amount),0)::text,'unpaid',COALESCE(sum(unpaid_amount),0)::text,'input_tokens',COALESCE(sum(input_tokens),0),'output_tokens',COALESCE(sum(output_tokens),0),'avg_latency_ms',COALESCE(round(avg(latency_ms) FILTER(WHERE state='completed')),0)) FROM requests WHERE user_id=$1 AND created_at>=$2 AND created_at<$3 AND ($4='' OR model_id=$4) AND ($5='' OR key_id::text=$5)`, currentUser(c).ID, from, to, c.Query("model"), c.Query("key"))
	if e != nil {
		fail(c, 503, "database_unavailable", "Statistics unavailable.")
		return
	}
	daily, e := a.Store.JSONRows(c, `SELECT jsonb_build_object('date',to_char(created_at AT TIME ZONE 'UTC','YYYY-MM-DD'),'requests',count(*),'spent',COALESCE(sum(amount) FILTER(WHERE state='completed'),0)::text,'input_tokens',COALESCE(sum(input_tokens),0),'output_tokens',COALESCE(sum(output_tokens),0)) FROM requests WHERE user_id=$1 AND created_at>=$2 AND created_at<$3 AND ($4='' OR model_id=$4) AND ($5='' OR key_id::text=$5) GROUP BY 1::integer, to_char(created_at AT TIME ZONE 'UTC','YYYY-MM-DD') ORDER BY to_char(created_at AT TIME ZONE 'UTC','YYYY-MM-DD')`, currentUser(c).ID, from, to, c.Query("model"), c.Query("key"))
	if e != nil {
		fail(c, 503, "database_unavailable", "Daily statistics unavailable.")
		return
	}
	c.JSON(200, gin.H{"summary": rows[0], "daily": daily, "from": from, "to": to})
}
