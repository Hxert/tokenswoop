package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"tokenswoop/backend/internal/security"
	"tokenswoop/backend/internal/store"
	"tokenswoop/backend/internal/testutil"
)

func TestOnlyUpstreamUsageDeterminesTokenCharges(t *testing.T) {
	a := testApp(t)
	c, uid := operatorClient(t, a)
	ctx := context.Background()
	m := testutil.Model()
	model := c.call("PUT", "/api/admin/models", m, 200)
	if strings.Contains(model.Body.String(), "tokenizer") {
		t.Fatal("retired local tokenizer configuration exposed")
	}
	// Historical configuration is inert, even when it is no longer valid.
	if _, err := a.Store.DB.Exec(ctx, "UPDATE models SET tokenizer='retired-estimator' WHERE id=$1", m.ID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/tokens/estimate", "/v1/tokens/estimate", "/v1/messages/count_tokens", "/v1beta/models/test-model:countTokens"} {
		c.call("POST", path, map[string]string{"model": m.ID}, 404)
	}
	catalog := c.call("GET", "/v1beta/models", nil, 200)
	if strings.Contains(catalog.Body.String(), "countTokens") {
		t.Fatal("catalog advertises local token counting")
	}
	var confirmed atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Stream bool }
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("invalid upstream request")
			w.WriteHeader(400)
			return
		}
		usage := map[string]int{"prompt_tokens": 7, "completion_tokens": 3, "total_tokens": 10}
		if request.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"An answer with much more than three tokens in plain text.\"},\"finish_reason\":null}]}\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
			if confirmed.Load() {
				data, _ := json.Marshal(map[string]any{"choices": []any{}, "usage": usage})
				fmt.Fprintf(w, "data: %s\n\n", data)
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		body := map[string]any{"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": "An answer with much more than three tokens in plain text."}, "finish_reason": "stop"}}}
		if confirmed.Load() {
			body["usage"] = usage
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(body)
	}))
	defer upstream.Close()
	secret, err := security.Encrypt(a.Config.Secret, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Store.SaveChannel(ctx, uid, store.Channel{ID: uuid.NewString(), Name: "fixture", Protocol: "openai", BaseURL: upstream.URL, SecretCipher: secret, Enabled: true, Models: map[string]string{m.ID: "upstream"}}); err != nil {
		t.Fatal(err)
	}
	for _, hasUsage := range []bool{true, false} {
		confirmed.Store(hasUsage)
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("usage=%v/stream=%v", hasUsage, stream), func(t *testing.T) {
				// Each case independently exercises usage semantics, including
				// after a prior case exhausts retries and cools down the fixture.
				if _, err := a.Store.DB.Exec(ctx, "UPDATE channels SET failure_count=0,disabled_until=NULL,last_error=''"); err != nil {
					t.Fatal(err)
				}
				before, _ := a.Store.User(ctx, uid)
				body := map[string]any{"model": m.ID, "stream": stream, "messages": []map[string]string{{"role": "user", "content": strings.Repeat("long input ", 10000)}}}
				status := 200
				if !hasUsage && !stream {
					status = 502
				}
				response := c.call("POST", "/v1/chat/completions", body, status)
				id := response.Header().Get("X-Request-ID")
				var state, amount, source string
				var input, output int64
				var noEstimate bool
				err := a.Store.DB.QueryRow(ctx, `SELECT state,amount::text,usage_source,input_tokens,output_tokens,input_estimate IS NULL AND estimate_method='' FROM requests WHERE id=$1`, id).Scan(&state, &amount, &source, &input, &output, &noEstimate)
				if err != nil || !noEstimate {
					t.Fatal("local token estimate still recorded", err)
				}
				if hasUsage {
					if state != "completed" || input != 7 || output != 3 || amount != "0.000013000000000000" || !strings.HasPrefix(source, "upstream:") {
						t.Fatalf("billing did not use exact supplier counts: state=%s input=%d output=%d amount=%s source=%s", state, input, output, amount, source)
					}
				} else if state != "usage_unknown" || input != 0 || output != 0 || !decimal.RequireFromString(amount).IsZero() {
					t.Fatal("missing usage was replaced with local counts or a charge")
				}
				after, _ := a.Store.User(ctx, uid)
				if !decimal.RequireFromString(after.Unpaid).Sub(decimal.RequireFromString(before.Unpaid)).Equal(decimal.RequireFromString(amount)) {
					t.Fatal("wallet payable differs from confirmed charge")
				}
				for _, path := range []string{"/api/usage", "/api/requests/" + id, "/api/admin/operations", "/api/admin/operations/requests/" + id} {
					data := c.call("GET", path, nil, 200).Body.String()
					if strings.Contains(data, "input_estimate") || strings.Contains(data, "estimate_method") {
						t.Error("local estimate still exposed in usage or operations")
					}
				}
			})
		}
	}
}
