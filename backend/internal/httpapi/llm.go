package httpapi

import (
	"bytes"
	"encoding/json"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"io"
	"slices"
	"strings"
	"tokenswoop/backend/internal/pricing"
	"tokenswoop/backend/internal/store"
)

func errorBody(c *gin.Context, status int, code, message string) gin.H {
	id := c.GetString("request_id")
	if strings.HasPrefix(c.Request.URL.Path, "/v1beta/") {
		return gin.H{"error": gin.H{"code": status, "status": strings.ToUpper(code), "message": message, "request_id": id}}
	}
	if strings.HasPrefix(c.Request.URL.Path, "/v1/messages") {
		return gin.H{"type": "error", "error": gin.H{"type": "api_error", "code": code, "message": message}, "request_id": id}
	}
	return gin.H{"error": gin.H{"type": "tokenswoop_error", "code": code, "message": message, "request_id": id}}
}
func claudeCatalog(c *gin.Context) bool { return c.GetHeader("anthropic-version") != "" }
func modelObject(c *gin.Context, m store.Model) gin.H {
	if strings.HasPrefix(c.Request.URL.Path, "/v1beta/") {
		return gin.H{"name": "models/" + m.ID, "displayName": m.Name, "supportedGenerationMethods": []string{"generateContent", "streamGenerateContent"}}
	}
	if claudeCatalog(c) {
		return gin.H{"id": m.ID, "type": "model", "display_name": m.Name, "created_at": "1970-01-01T00:00:00Z"}
	}
	return gin.H{"id": m.ID, "object": "model", "created": 0, "owned_by": m.Vendor}
}
func modelAllowed(c *gin.Context, id string) bool {
	if v, ok := c.Get("key_models"); ok {
		allowed := v.([]string)
		return len(allowed) == 0 || slices.Contains(allowed, id)
	}
	return true
}
func (a *App) apiModelDetail(c *gin.Context) {
	id := strings.TrimPrefix(c.Param("model"), "/")
	m, e := a.Store.Model(c, id)
	if e != nil || !modelAllowed(c, id) {
		fail(c, 404, "model_unavailable", "Model is unavailable.")
		return
	}
	c.JSON(200, modelObject(c, m))
}
func (a *App) geminiRelay(c *gin.Context) {
	path := strings.TrimPrefix(c.Param("action"), "/")
	index := strings.LastIndex(path, ":")
	if index < 1 {
		fail(c, 404, "unsupported_operation", "Expected a model generation action.")
		return
	}
	model, action := path[:index], path[index+1:]
	if action != "generateContent" && action != "streamGenerateContent" {
		fail(c, 404, "unsupported_operation", "Unsupported model action.")
		return
	}
	raw, e := io.ReadAll(c.Request.Body)
	if e != nil {
		fail(c, 413, "request_too_large", "Request exceeds 1 MiB.")
		return
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil || body == nil {
		fail(c, 400, "invalid_request", "Expected a JSON object.")
		return
	}
	if v := body["model"]; v != nil {
		var existing string
		json.Unmarshal(v, &existing)
		if existing != model {
			fail(c, 400, "model_mismatch", "Body model must match URL model.")
			return
		}
	}
	body["model"], _ = json.Marshal(model)
	body["stream"], _ = json.Marshal(action == "streamGenerateContent")
	raw, _ = json.Marshal(body)
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	a.relay(c, types.RelayFormatGemini, "")
}
func (a *App) previewPrice(c *gin.Context) {
	var in struct {
		Model    store.Model   `json:"model"`
		Facts    pricing.Facts `json:"facts"`
		Discount int           `json:"discount_bps"`
	}
	if c.ShouldBindJSON(&in) != nil {
		fail(c, 400, "invalid_preview", "Provide model pricing and hypothetical usage.")
		return
	}
	trace, e := pricing.Charge(in.Model.Pricing, in.Model.Rates(), in.Facts, in.Discount)
	if e != nil {
		fail(c, 400, "invalid_pricing", e.Error())
		return
	}
	c.JSON(200, gin.H{"trace": trace, "preview": true, "currency": "USDC"})
}
