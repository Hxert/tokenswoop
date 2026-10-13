package httpapi

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"strconv"
	"time"
)

const opsFilter = ` FROM requests r LEFT JOIN channels ch ON ch.id=r.channel_id LEFT JOIN users u ON u.id=r.user_id WHERE r.created_at >= $1 AND r.created_at < $2 AND ($3='' OR r.model_id=$3) AND ($4='' OR r.user_id::text=$4 OR u.wallet=$4) AND ($5='' OR r.channel_id::text=$5) AND ($6='' OR r.state=$6) AND ($7='' OR r.client_protocol=$7) AND ($8='' OR r.key_id::text=$8) AND ($9='' OR r.id::text=$9)`
const opsFields = `jsonb_build_object('id',r.id,'user_id',r.user_id,'wallet',u.wallet,'model_id',r.model_id,'key_id',r.key_id,'channel_id',r.channel_id,'channel_name',ch.name,'state',r.state,'operation',r.operation,'protocol',r.client_protocol,'upstream_protocol',r.upstream_protocol,'stream',r.is_stream,'input_tokens',r.input_tokens,'output_tokens',r.output_tokens,'latency_ms',r.latency_ms,'first_token_ms',r.first_token_ms,'generation_ms',r.generation_ms,'tokens_per_second',CASE WHEN r.state='completed' AND r.generation_ms>0 THEN round(r.output_tokens*1000.0/r.generation_ms,2) ELSE NULL END,'amount',CASE WHEN r.state='completed' THEN r.amount::text ELSE '0' END,'error_code',r.error_code,'status_code',r.status_code,'created_at',r.created_at,'price_version',r.price_snapshot->'price_version')`

func (a *App) operations(c *gin.Context) {
	from, to, ok := window(c)
	offset, _ := strconv.Atoi(c.Query("offset"))
	if !ok || offset < 0 || offset > 100000 {
		fail(c, 400, "invalid_range", "Select a range of at most 366 days and a valid offset.")
		return
	}
	args := []any{from, to, c.Query("model"), c.Query("user"), c.Query("channel"), c.Query("state"), c.Query("protocol"), c.Query("key"), c.Query("request")}
	for _, v := range args[2:] {
		if len(v.(string)) > 200 {
			fail(c, 400, "invalid_filter", "Filter too long.")
			return
		}
	}
	summary, e := a.Store.JSONRows(c, `SELECT jsonb_build_object('requests',count(*),'completed',count(*) FILTER(WHERE r.state='completed'),'failed',count(*) FILTER(WHERE r.state='failed'),'unknown',count(*) FILTER(WHERE r.state='usage_unknown'),'amount',COALESCE(sum(r.amount) FILTER(WHERE r.state='completed'),0)::text,'input_tokens',COALESCE(sum(r.input_tokens),0),'output_tokens',COALESCE(sum(r.output_tokens),0),'avg_latency_ms',round(avg(r.latency_ms) FILTER(WHERE r.state='completed')),'p95_latency_ms',percentile_cont(0.95) WITHIN GROUP(ORDER BY r.latency_ms) FILTER(WHERE r.state='completed'),'avg_ttft_ms',round(avg(r.first_token_ms) FILTER(WHERE r.state='completed' AND r.is_stream)),'p95_ttft_ms',percentile_cont(0.95) WITHIN GROUP(ORDER BY r.first_token_ms) FILTER(WHERE r.state='completed' AND r.is_stream),'tokens_per_second',round(sum(r.output_tokens) FILTER(WHERE r.state='completed' AND r.generation_ms>0)*1000.0/NULLIF(sum(r.generation_ms) FILTER(WHERE r.state='completed' AND r.generation_ms>0),0),2))`+opsFilter, args...)
	if e != nil {
		fail(c, 503, "database_unavailable", "Operational summary unavailable.")
		return
	}
	rows, e := a.Store.JSONRows(c, "SELECT "+opsFields+opsFilter+" ORDER BY r.created_at DESC,r.id DESC LIMIT 51 OFFSET $10", append(args, offset)...)
	if e != nil {
		fail(c, 503, "database_unavailable", "Operational logs unavailable.")
		return
	}
	more := len(rows) > 50
	if more {
		rows = rows[:50]
	}
	series, e := a.Store.JSONRows(c, `SELECT jsonb_build_object('date',to_char(r.created_at AT TIME ZONE 'UTC','YYYY-MM-DD'),'requests',count(*),'completed',count(*) FILTER(WHERE r.state='completed'),'amount',COALESCE(sum(r.amount) FILTER(WHERE r.state='completed'),0)::text,'avg_ttft_ms',round(avg(r.first_token_ms) FILTER(WHERE r.state='completed'))) `+opsFilter+` GROUP BY to_char(r.created_at AT TIME ZONE 'UTC','YYYY-MM-DD') ORDER BY to_char(r.created_at AT TIME ZONE 'UTC','YYYY-MM-DD')`, args...)
	if e != nil {
		fail(c, 503, "database_unavailable", "Operational timeseries unavailable.")
		return
	}
	instances, e := a.Store.JSONRows(c, `SELECT jsonb_build_object('id',id,'role',role,'started_at',started_at,'last_seen',last_seen,'ready',last_seen>now()-interval '90 seconds') FROM runtime_instances WHERE last_seen>now()-interval '1 day' ORDER BY role,started_at DESC`)
	if e != nil {
		fail(c, 503, "database_unavailable", "Runtime status unavailable.")
		return
	}
	c.JSON(200, gin.H{"data": rows, "summary": summary[0], "daily": series, "instances": instances, "has_more": more, "offset": offset, "limit": 50, "from": from, "to": to})
}
func (a *App) operationDetail(c *gin.Context) {
	if !validID(c.Param("id")) {
		fail(c, 400, "invalid_id", "Invalid request ID.")
		return
	}
	rows, e := a.Store.JSONRows(c, `SELECT `+opsFields+` || jsonb_build_object('pricing_trace',r.pricing_trace,'price_snapshot',r.price_snapshot,'tier_snapshot',r.tier_snapshot,'usage_source',r.usage_source,'tool_calls',r.tool_calls,'cache_read_tokens',r.cache_read_tokens,'cache_write_tokens',r.cache_write_tokens,'cache_write_1h_tokens',r.cache_write_1h_tokens) FROM requests r LEFT JOIN channels ch ON ch.id=r.channel_id LEFT JOIN users u ON u.id=r.user_id WHERE r.id=$1`, c.Param("id"))
	if e != nil || len(rows) == 0 {
		fail(c, 404, "not_found", "Request not found.")
		return
	}
	attempts, e := a.Store.JSONRows(c, `SELECT jsonb_build_object('channel_id',a.channel_id,'channel_name',ch.name,'status',a.status_code,'outcome',a.outcome,'diagnostics',a.diagnostics,'created_at',a.created_at) FROM request_attempts a LEFT JOIN channels ch ON ch.id=a.channel_id WHERE a.request_id=$1 ORDER BY a.created_at,a.id`, c.Param("id"))
	if e != nil {
		fail(c, 503, "database_unavailable", "Request diagnostics unavailable.")
		return
	}
	c.JSON(200, gin.H{"request": rows[0], "attempts": attempts})
}
func (a *App) operationTasks(c *gin.Context) {
	rows, e := a.Store.JSONRows(c, `SELECT to_jsonb(t) FROM operations_tasks t ORDER BY created_at DESC LIMIT 50`)
	if e != nil {
		fail(c, 503, "database_unavailable", "Task history unavailable.")
		return
	}
	c.JSON(200, gin.H{"data": rows})
}
func (a *App) cleanupOperations(c *gin.Context) {
	var in struct {
		Days int `json:"retain_days"`
	}
	if c.ShouldBindJSON(&in) != nil || in.Days < 7 || in.Days > 3650 {
		fail(c, 400, "invalid_retention", "Retain 7–3650 days of completed request diagnostics.")
		return
	}
	tx, e := a.Store.DB.Begin(c)
	if e != nil {
		fail(c, 503, "database_unavailable", "Could not queue cleanup.")
		return
	}
	defer tx.Rollback(c)
	var active bool
	if e = tx.QueryRow(c, `SELECT pg_advisory_xact_lock(78016348) IS NULL`).Scan(&active); e != nil {
		fail(c, 503, "database_unavailable", "Could not queue cleanup.")
		return
	}
	if e = tx.QueryRow(c, `SELECT EXISTS(SELECT 1 FROM operations_tasks WHERE state IN('queued','running'))`).Scan(&active); e != nil || active {
		fail(c, 409, "cleanup_pending", "A cleanup task is already pending.")
		return
	}
	id := uuid.NewString()
	params, _ := json.Marshal(gin.H{"before": time.Now().UTC().AddDate(0, 0, -in.Days), "retain_days": in.Days})
	if _, e = tx.Exec(c, `INSERT INTO operations_tasks(id,actor_id,kind,parameters) VALUES($1,$2,'attempt_diagnostics',$3)`, id, currentUser(c).ID, params); e == nil {
		_, e = tx.Exec(c, `INSERT INTO audit_events(id,actor_id,action,target,details) VALUES($1,$2,'operations.cleanup',$3,$4)`, uuid.NewString(), currentUser(c).ID, id, params)
	}
	if e != nil || tx.Commit(c) != nil {
		fail(c, 503, "database_unavailable", "Could not queue cleanup.")
		return
	}
	c.JSON(202, gin.H{"id": id, "state": "queued", "preserves": "request ledger, pricing snapshots, financial entries and unresolved requests"})
}
