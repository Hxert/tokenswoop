package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"tokenswoop/backend/internal/security"
	"tokenswoop/backend/internal/store"
	"tokenswoop/backend/internal/testutil"
)

func TestModelsAndRelayHaveNoPlatformTokenCeilings(t *testing.T) {
	a := testApp(t)
	c, uid := operatorClient(t, a)
	m := testutil.Model()
	saved := object(t, c.call("PUT", "/api/admin/models", m, 200))
	if _, ok := saved["max_input_tokens"]; ok {
		t.Fatal("platform input ceiling still exposed")
	}
	if _, ok := saved["max_output_tokens"]; ok {
		t.Fatal("platform output ceiling still exposed")
	}
	ctx := context.Background()
	// Existing installations retain old columns and price snapshots. None of
	// those legacy values may affect future calls or their confirmed charges.
	if _, e := a.Store.DB.Exec(ctx, "UPDATE models SET max_input_tokens=8192,max_output_tokens=2048 WHERE id=$1", m.ID); e != nil {
		t.Fatal(e)
	}
	wire := make(chan map[string]any, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		json.NewDecoder(r.Body).Decode(&request)
		wire <- request
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":500000,"completion_tokens":300000,"total_tokens":800000}}`)
	}))
	defer upstream.Close()
	secret, _ := security.Encrypt(a.Config.Secret, "fixture")
	if e := a.Store.SaveChannel(ctx, uid, store.Channel{ID: uuid.NewString(), Name: "fixture", Protocol: "openai", BaseURL: upstream.URL, SecretCipher: secret, Enabled: true, Models: map[string]string{m.ID: "upstream"}}); e != nil {
		t.Fatal(e)
	}
	body := map[string]any{"model": m.ID, "messages": []map[string]string{{"role": "user", "content": strings.Repeat("long context ", 10000)}}}
	for _, limit := range []int{0, 500000} {
		if limit > 0 {
			body["max_completion_tokens"] = limit
		}
		reply := c.call("POST", "/v1/chat/completions", body, 200)
		request := <-wire
		if limit == 0 {
			if request["max_tokens"] != nil || request["max_completion_tokens"] != nil {
				t.Fatal("default platform output cap injected", request)
			}
		} else if request["max_completion_tokens"] != float64(limit) {
			t.Fatal("caller output parameter changed", request)
		}
		var state, amount string
		if e := a.Store.DB.QueryRow(ctx, "SELECT state,amount::text FROM requests WHERE id=$1", reply.Header().Get("X-Request-ID")).Scan(&state, &amount); e != nil || state != "completed" || amount != "1.100000000000000000" {
			t.Fatalf("usage beyond old ceilings was not billed normally: %s %s %v", state, amount, e)
		}
	}
}
