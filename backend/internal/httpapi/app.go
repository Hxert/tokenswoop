package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"tokenswoop/backend/internal/config"
	"tokenswoop/backend/internal/faucet"
	"tokenswoop/backend/internal/gateway"
	"tokenswoop/backend/internal/merasponsor"
	"tokenswoop/backend/internal/payments"
	"tokenswoop/backend/internal/privy"
	"tokenswoop/backend/internal/reserve"
	"tokenswoop/backend/internal/security"
	"tokenswoop/backend/internal/store"
)

type App struct {
	Config   config.Config
	Store    *store.Store
	Redis    *redis.Client
	Gateway  *gateway.Gateway
	Payments *payments.Service
	Reserve  *reserve.Service
	Privy    *privy.Client
	ChainRPC *payments.RPCChain
	Mera     *merasponsor.Service
	Faucet   *faucet.Service
}

func (a *App) Handler() http.Handler {
	if a.Privy == nil && a.Config.Privy.Enabled() {
		a.Privy = privy.New(a.Config.Privy.AppID, a.Config.Privy.AppSecret)
		a.Privy.ChainID = a.Config.ChainID
	}
	r := gin.New()
	r.ContextWithFallback = true
	if e := r.SetTrustedProxies(a.Config.TrustedProxies); e != nil {
		panic("invalid TRUSTED_PROXY_CIDRS")
	}
	r.Use(func(c *gin.Context) {
		defer func() {
			if recover() != nil {
				slog.Error("request panic", "request_id", c.GetString("request_id"))
				if !c.Writer.Written() {
					fail(c, 500, "internal_error", "The request could not be completed.")
				}
				c.Abort()
			}
		}()
		c.Next()
	})
	r.Use(func(c *gin.Context) {
		c.Set("request_id", uuid.NewString())
		c.Header("X-Request-ID", c.GetString("request_id"))
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Cache-Control", "no-store")
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1024*1024)
		c.Next()
	})
	r.GET("/health/live", func(c *gin.Context) { c.JSON(200, gin.H{"status": "alive"}) })
	r.GET("/health/ready", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if a.Store.DB.Ping(ctx) != nil || a.Redis.Ping(ctx).Err() != nil {
			fail(c, 503, "not_ready", "Dependencies unavailable.")
			return
		}
		c.JSON(200, gin.H{"status": "ready"})
	})
	api := r.Group("/api", a.origin)
	api.GET("/config", a.publicConfig)
	api.GET("/models", a.models)
	api.GET("/tiers", a.publicTiers)
	api.GET("/about", func(c *gin.Context) {
		c.JSON(200, gin.H{"upstream": "https://github.com/QuantumNous/new-api", "component": "RelayKit, model modifier helpers and provider token estimator", "license": "AGPL-3.0", "source_url": a.Config.SourceURL, "notice": upstreamNotice, "changes": "TokenSwoop independently implements accounts, routing, postpaid pricing, operations and frontend. RelayKit protocol converters are vendored unchanged; model modifier helpers and the provider token estimator are adapted with source notices. Input estimates never determine charges."})
	})
	auth := api.Group("/auth", a.ipLimit)
	auth.POST("/challenge", a.challenge)
	auth.POST("/login", a.login)
	auth.POST("/privy", a.privyLogin)
	member := api.Group("", a.session)
	member.GET("/me", func(c *gin.Context) {
		u := currentUser(c)
		if a.Reserve != nil {
			if _, err := a.Reserve.Refresh(c, u.ID, false); err == nil {
				if updated, err := a.Store.User(c, u.ID); err == nil {
					u = updated
				}
			}
		}
		tier, e := a.Store.Tier(c, u.Tier)
		if e != nil {
			fail(c, 503, "database_unavailable", "Account tier unavailable.")
			return
		}
		c.JSON(200, gin.H{"user": u, "tier": tier, "settlement_enabled": a.Payments != nil})
	})
	member.POST("/auth/logout", a.logout)
	member.POST("/rpc", a.walletRPC)
	member.POST("/wallet/mera/transactions", a.prepareMeraTransaction)
	member.GET("/wallet/mera/pending", a.pendingMeraTransaction)
	member.POST("/wallet/mera/transactions/:id/submit", a.submitMeraTransaction)
	member.GET("/wallet/mera/transactions/:id", a.meraTransaction)
	member.DELETE("/wallet/mera/transactions/:id", a.cancelMeraTransaction)
	member.POST("/wallet/transactions", a.prepareWalletTransaction)
	member.POST("/wallet/transactions/:id/submit", a.submitWalletTransaction)
	member.GET("/wallet/transactions/:id", a.walletTransaction)
	member.DELETE("/wallet/transactions/:id", a.cancelWalletTransaction)
	member.GET("/keys", a.keys)
	member.POST("/keys", a.createKey)
	member.DELETE("/keys/:id", a.revokeKey)
	member.GET("/usage", a.usage)
	member.GET("/requests/:id", a.receipt)
	member.GET("/stats", a.stats)
	member.GET("/reserve", a.reserveStatus)
	member.POST("/reserve/refresh", a.refreshReserve)
	member.GET("/billing", a.billing)
	if a.Config.ChainID == 10143 {
		member.GET("/faucet", a.faucetStatus)
		member.POST("/faucet/claims", a.faucetClaim)
		member.GET("/faucet/claims/:id", a.faucetClaimStatus)
	}
	member.POST("/billing/refresh", a.refreshPayment)
	member.GET("/billing/bills", a.bills)
	member.GET("/billing/bills/:id", a.billDetail)
	member.POST("/billing/bills/:id/retry", a.retryBill)
	member.POST("/playground", func(c *gin.Context) { a.relay(c, types.RelayFormatOpenAI, "") })
	admin := member.Group("/admin", a.admin)
	admin.GET("/users", a.users)
	admin.PATCH("/users/:id", a.updateUser)
	admin.GET("/models", a.adminModels)
	admin.PUT("/models", a.saveModel)
	admin.POST("/pricing/preview", a.previewPrice)
	admin.GET("/operations", a.operations)
	admin.GET("/operations/requests/:id", a.operationDetail)
	admin.GET("/operations/tasks", a.operationTasks)
	admin.POST("/operations/cleanup", a.cleanupOperations)
	admin.GET("/channels", a.channels)
	admin.POST("/channels", a.saveChannel)
	admin.PUT("/channels/:id", a.saveChannel)
	admin.POST("/channels/fetch-models", a.fetchChannelModels)
	admin.POST("/channels/:id/fetch-models", a.fetchChannelModels)
	admin.POST("/channels/:id/test", func(c *gin.Context) {
		if !validID(c.Param("id")) {
			fail(c, 400, "invalid_id", "Invalid channel.")
			return
		}
		a.relay(c, types.RelayFormatOpenAI, c.Param("id"))
	})
	admin.GET("/reserve", a.adminReserves)
	admin.GET("/tiers", a.tiers)
	admin.PUT("/tiers", a.saveTier)
	admin.GET("/audit", a.audit)
	admin.GET("/health", a.health)
	admin.GET("/requests/unknown", a.unknown)
	admin.POST("/requests/:id/waive", a.waive)
	v1 := r.Group("/v1", a.apiKey)
	v1.GET("/models", a.apiModels)
	v1.GET("/models/*model", a.apiModelDetail)
	v1.POST("/messages", func(c *gin.Context) { a.relay(c, types.RelayFormatClaude, "") })
	v1.POST("/responses/compact", func(c *gin.Context) { c.Set("operation", "compact"); a.relay(c, types.RelayFormatOpenAIResponses, "") })
	v1.POST("/alpha/search", func(c *gin.Context) {
		c.Set("operation", "alpha_search")
		a.relay(c, types.RelayFormatOpenAIResponses, "")
	})
	v1beta := r.Group("/v1beta", a.apiKey)
	v1beta.GET("/models", a.apiModels)
	v1beta.GET("/models/*model", a.apiModelDetail)
	v1beta.POST("/models/*action", a.geminiRelay)
	v1.POST("/chat/completions", func(c *gin.Context) { a.relay(c, types.RelayFormatOpenAI, "") })
	v1.POST("/responses", func(c *gin.Context) { a.relay(c, types.RelayFormatOpenAIResponses, "") })
	r.NoRoute(func(c *gin.Context) { fail(c, 404, "not_found", "Endpoint not found.") })
	return r
}

func fail(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, errorBody(c, status, code, message))
}

var rateScript = redis.NewScript(`local n=redis.call('INCR',KEYS[1]); if n==1 then redis.call('PEXPIRE',KEYS[1],ARGV[1]) end; return n`)

func (a *App) allow(ctx context.Context, key string, limit int) (bool, error) {
	n, e := rateScript.Run(ctx, a.Redis, []string{"tokenswoop:rate:" + security.Digest(a.Config.Secret, "redis", a.Config.Origin)[:16] + ":" + key}, 60000).Int()
	return n <= limit, e
}
func (a *App) ipLimit(c *gin.Context) {
	ok, e := a.allow(c, "auth:"+security.Digest(a.Config.Secret, "ip", c.ClientIP()), 20)
	if e != nil {
		fail(c, 503, "limiter_unavailable", "Please try again later.")
		return
	}
	if !ok {
		c.Header("Retry-After", "60")
		fail(c, 429, "rate_limited", "Too many authentication attempts.")
		return
	}
	c.Next()
}

func (a *App) apiKey(c *gin.Context) {
	header := c.GetHeader("Authorization")
	credential := strings.TrimPrefix(header, "Bearer ")
	for _, native := range []string{c.GetHeader("x-api-key"), c.GetHeader("x-goog-api-key"), c.Query("key")} {
		if native != "" {
			if credential != "" && credential != native {
				fail(c, 401, "ambiguous_api_key", "Supply one API key.")
				return
			}
			credential = native
		}
	}
	// New keys use sk-. Keep existing ts_ keys valid under their original hash.
	validPrefix := strings.HasPrefix(credential, "sk-") || strings.HasPrefix(credential, "ts_")
	if !validPrefix || len(credential) > 128 || header != "" && !strings.HasPrefix(header, "Bearer ") {
		fail(c, 401, "invalid_api_key", "A valid TokenSwoop API key is required.")
		return
	}
	var id, userID string
	var models []string
	e := a.Store.DB.QueryRow(c, "SELECT id::text,user_id::text,allowed_models FROM api_keys WHERE token_hash=$1 AND status='active' AND (expires_at IS NULL OR expires_at>now())", security.Digest(a.Config.Secret, "api-key", credential)).Scan(&id, &userID, &models)
	if e != nil {
		fail(c, 401, "invalid_api_key", "This API key is invalid, expired, or revoked.")
		return
	}
	u, e := a.Store.User(c, userID)
	if e != nil || u.Status != "active" {
		fail(c, 403, "account_unavailable", "Account unavailable.")
		return
	}
	c.Set("user", u)
	c.Set("key_id", id)
	c.Set("key_models", models)
	c.Next()
}

func (a *App) models(c *gin.Context) {
	m, e := a.Store.Models(c, false)
	if e != nil {
		fail(c, 503, "database_unavailable", "Models unavailable.")
		return
	}
	c.JSON(200, gin.H{"data": m, "currency": "USDC", "price_unit": "million_tokens"})
}
func (a *App) apiModels(c *gin.Context) {
	models, e := a.Store.Models(c, false)
	if e != nil {
		fail(c, 503, "database_unavailable", "Models unavailable.")
		return
	}
	allowed := c.MustGet("key_models").([]string)
	items := []gin.H{}
	for _, m := range models {
		if len(allowed) == 0 || slices.Contains(allowed, m.ID) {
			items = append(items, modelObject(c, m))
		}
	}
	if strings.HasPrefix(c.Request.URL.Path, "/v1beta/") {
		c.JSON(200, gin.H{"models": items})
		return
	}
	if claudeCatalog(c) {
		var first, last any
		if len(items) > 0 {
			first = items[0]["id"]
			last = items[len(items)-1]["id"]
		}
		c.JSON(200, gin.H{"data": items, "has_more": false, "first_id": first, "last_id": last})
		return
	}
	c.JSON(200, gin.H{"object": "list", "data": items})
}

func (a *App) relay(c *gin.Context, format types.RelayFormat, testChannel string) {
	started := time.Now()
	u := currentUser(c)
	id := c.GetString("request_id")
	raw, e := io.ReadAll(c.Request.Body)
	if e != nil {
		fail(c, 413, "request_too_large", "Request body exceeds 1 MiB.")
		return
	}
	var input struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(raw, &input) != nil || input.Model == "" {
		fail(c, 400, "invalid_request", "A model and a valid JSON body are required.")
		return
	}
	model, e := a.Store.Model(c, input.Model)
	if e != nil {
		fail(c, 404, "model_unavailable", "Model is unavailable.")
		return
	}
	if v, ok := c.Get("key_models"); ok {
		allowed := v.([]string)
		if len(allowed) > 0 && !slices.Contains(allowed, model.ID) {
			fail(c, 403, "model_not_allowed", "This key does not permit that model.")
			return
		}
	}
	if a.Payments != nil {
		// Persisted settlement failures take precedence over wallet/RPC refresh.
		// BeginRequest checks again under the account lock to cover a failure
		// committed while this request is preparing its upstream plan.
		if model.Pricing.Billable(model.Rates()) {
			failed, err := a.Store.HasUnpaidSettlementFailure(c, u.ID)
			if err != nil {
				fail(c, 503, "database_unavailable", "Payment status unavailable.")
				return
			}
			if failed {
				tier, err := a.Store.Tier(c, u.Tier)
				if err != nil {
					fail(c, 503, "tier_unavailable", "Account limits unavailable.")
					return
				}
				if tier.DiscountBPS < 10000 {
					fail(c, 402, "payment_required", store.ErrSettlementFailed.Error())
					return
				}
			}
		}
		if _, err := a.Payments.Refresh(c, u.ID, false); err != nil {
			fail(c, 503, "payment_rpc_unavailable", "Could not verify payment allowance. Try again shortly.")
			return
		}
	}
	if a.Reserve != nil {
		if _, err := a.Reserve.Refresh(c, u.ID, !a.Reserve.Config.DebtMode()); err != nil {
			fail(c, 503, "reserve_unavailable", "Could not verify reserve state. Try again shortly.")
			return
		}
		var err error
		u, err = a.Store.User(c, u.ID)
		if err != nil {
			fail(c, 503, "account_unavailable", "Account unavailable.")
			return
		}
	}
	tier, e := a.Store.Tier(c, u.Tier)
	if e != nil {
		fail(c, 503, "tier_unavailable", "Account limits unavailable.")
		return
	}
	plan, e := gateway.BuildOperation(raw, model, tier, format, c.GetString("operation"))
	if e != nil {
		fail(c, 400, "unsupported_request", e.Error())
		return
	}
	plan.RoutingScope = u.ID
	plan.AllowStreamRestart = c.FullPath() == "/api/playground"
	if format == types.RelayFormatClaude {
		plan.ClientHeaders = map[string]string{}
		for _, key := range []string{"anthropic-version", "anthropic-beta"} {
			if value := c.GetHeader(key); value != "" {
				if len(value) > 1024 || strings.ContainsAny(value, "\r\n\x00") {
					fail(c, 400, "invalid_header", "Invalid Anthropic compatibility header.")
					return
				}
				plan.ClientHeaders[key] = value
			}
		}
	}
	plan.StartedAt = started
	channels, e := a.Store.Channels(c, model.ID, testChannel != "")
	if e != nil {
		fail(c, 503, "database_unavailable", "Channels unavailable.")
		return
	}
	if testChannel != "" {
		channels = slices.DeleteFunc(channels, func(ch store.Channel) bool { return ch.ID != testChannel })
	}
	if plan.PreviousResponseID != "" {
		var channelID string
		var revision int
		err := a.Store.DB.QueryRow(c, `SELECT channel_id::text,channel_revision FROM response_handles WHERE digest=$1 AND user_id=$2 AND model_id=$3 AND expires_at>now()`, security.Digest(a.Config.Secret, "response-handle", u.ID+":"+model.ID+":"+plan.PreviousResponseID), u.ID, model.ID).Scan(&channelID, &revision)
		if err != nil {
			fail(c, 400, "context_unavailable", "Previous response is unavailable for this account and model, or has expired.")
			return
		}
		channels = slices.DeleteFunc(channels, func(ch store.Channel) bool {
			return ch.ID != channelID || ch.Revision != revision || !ch.Options.AllowStoredResponses
		})
	}
	channels = slices.DeleteFunc(channels, func(ch store.Channel) bool {
		return (plan.Stored || plan.PreviousResponseID != "") && (ch.Protocol != "openai_responses" || !ch.Options.AllowStoredResponses) || plan.Operation == "compact" && (ch.Protocol != "openai_responses" || !ch.Options.SupportsCompact) || plan.Operation == "alpha_search" && (!ch.Options.SupportsAlphaSearch || (ch.Protocol != "openai" && ch.Protocol != "openai_responses"))
	})
	if len(channels) == 0 {
		fail(c, 503, "no_available_channel", "No configured channel can serve this model.")
		return
	}
	ok, e := a.allow(c, "account:"+u.ID, tier.RPM)
	if e != nil {
		fail(c, 503, "limiter_unavailable", "Rate limiter unavailable.")
		return
	}
	if !ok {
		c.Header("Retry-After", "60")
		fail(c, 429, "rate_limited", "Account requests per minute limit reached.")
		return
	}
	metadata := store.Meter{Protocol: string(plan.Format), Operation: plan.Operation, Stream: plan.Stream}
	e = a.Store.BeginRequest(c, id, u.ID, c.GetString("key_id"), model, tier, metadata)
	// Settlement can invalidate the allowance snapshot between Refresh and
	// admission. Refresh that evidence and retry the uncommitted admission,
	// without rejecting an otherwise funded call or charging RPM twice.
	for retry := 0; retry < 3 && errors.Is(e, store.ErrSettlementUnavailable) && a.Payments != nil; retry++ {
		if _, err := a.Payments.Refresh(c, u.ID, false); err != nil {
			break
		}
		e = a.Store.BeginRequest(c, id, u.ID, c.GetString("key_id"), model, tier, metadata)
	}
	if e != nil {
		switch {
		case errors.Is(e, store.ErrSettlementUnavailable):
			fail(c, 503, "settlement_disabled", e.Error())
		case errors.Is(e, store.ErrPaymentRequired), errors.Is(e, store.ErrSettlementFailed):
			fail(c, 402, "payment_required", e.Error())
		case errors.Is(e, store.ErrBudget):
			fail(c, 402, "key_budget_exceeded", e.Error())
		case errors.Is(e, store.ErrUnavailable):
			fail(c, 403, "account_unavailable", e.Error())
		case errors.Is(e, store.ErrWithdrawalPending):
			fail(c, 409, "withdrawal_pending", e.Error())
		case errors.Is(e, store.ErrReserveStale):
			fail(c, 503, "reserve_changed", e.Error())
		case errors.Is(e, store.ErrConcurrency):
			fail(c, 429, "concurrency_limited", "Finish pending requests before starting another.")
		default:
			fail(c, 503, "admission_failed", "Could not admit the request.")
		}
		return
	}
	streaming := false
	emit := func(event string, data []byte) error {
		if c.Request.Context().Err() != nil {
			return c.Request.Context().Err()
		}
		if !streaming {
			c.Header("Content-Type", "text/event-stream")
			c.Header("X-Accel-Buffering", "no")
			c.Status(200)
			streaming = true
		}
		// Comment frames keep proxies alive without becoming model tokens or
		// protocol events. All writes run in the relay goroutine.
		if data == nil {
			if _, e := fmt.Fprint(c.Writer, ": keep-alive\n\n"); e != nil {
				return e
			}
			c.Writer.Flush()
			return nil
		}
		if event != "" {
			if _, e := fmt.Fprintf(c.Writer, "event: %s\n", event); e != nil {
				return e
			}
		}
		if _, e := fmt.Fprintf(c.Writer, "data: %s\n\n", data); e != nil {
			return e
		}
		c.Writer.Flush()
		return nil
	}
	result := a.Gateway.Run(c.Request.Context(), id, plan, channels, emit)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if e = a.Store.Finish(ctx, id, result.State, result.Meter, result.Channel, result.Status, result.Code, time.Since(started).Milliseconds()); e != nil {
		result.State = "usage_unknown"
		result.Status = 503
		result.Code = "ledger_unavailable"
	}
	if testChannel != "" {
		a.Store.Audit(ctx, u.ID, "channel.test", testChannel, gin.H{"request_id": id, "state": result.State})
	}
	if result.State != "completed" {
		message := "The upstream request could not complete."
		if result.State == "usage_unknown" {
			message = "Usage could not be confirmed. This request awaits review and has not been charged."
			if result.Code == "ledger_unavailable" {
				// A lost COMMIT acknowledgement can leave a successful charge in
				// the database. Do not promise a refund or invite a blind retry.
				message = "Billing confirmation is unavailable. Check this request's receipt before retrying."
			}
		}
		if streaming {
			b, _ := json.Marshal(errorBody(c, result.Status, result.Code, message))
			emit("error", b)
			return
		}
		status := result.Status
		if status < 400 || status > 599 {
			status = 502
		}
		fail(c, status, result.Code, message)
		return
	}
	if plan.Stream {
		for _, event := range result.FinalEvents {
			if emit(event.Event, event.Data) != nil {
				return
			}
		}
		if format == types.RelayFormatOpenAI {
			emit("", []byte("[DONE]"))
		}
		return
	}
	c.Data(200, "application/json", result.Body)
}
