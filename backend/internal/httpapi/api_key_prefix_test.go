package httpapi

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"tokenswoop/backend/internal/security"
	"tokenswoop/backend/internal/testutil"
)

func TestNewAndLegacyAPIKeyAuthentication(t *testing.T) {
	a := testApp(t)
	c := &testClient{t: t, handler: a.Handler()}
	private, wallet := testutil.Wallet(t)
	u := c.login(private, wallet)["id"].(string)
	created := object(t, c.call("POST", "/api/keys", map[string]any{"name": "New prefix", "budget": nil}, 201))
	token := created["key"].(string)
	if !strings.HasPrefix(token, "sk-") || len(token) < 40 || created["prefix"] != token[:11] {
		t.Fatal("new API key does not use the sk- format")
	}
	legacy := "ts_" + security.Token()
	legacyID := uuid.NewString()
	if _, err := a.Store.DB.Exec(context.Background(), `INSERT INTO api_keys(id,user_id,name,prefix,token_hash,budget) VALUES($1,$2,'Existing key',$3,$4,NULL)`, legacyID, u, legacy[:11], security.Digest(a.Config.Secret, "api-key", legacy)); err != nil {
		t.Fatal(err)
	}
	// All supported client credential formats authenticate the exact original
	// secret, including keys issued before the prefix change.
	for _, key := range []string{token, legacy} {
		for _, mode := range []string{"bearer", "claude", "gemini", "gemini-query"} {
			r := httptest.NewRequest("GET", "/v1/models", nil)
			switch mode {
			case "bearer":
				r.Header.Set("Authorization", "Bearer "+key)
			case "claude":
				r.Header.Set("x-api-key", key)
				r.Header.Set("anthropic-version", "2023-06-01")
			case "gemini":
				r = httptest.NewRequest("GET", "/v1beta/models", nil)
				r.Header.Set("x-goog-api-key", key)
			case "gemini-query":
				r = httptest.NewRequest("GET", "/v1beta/models?key="+key, nil)
			}
			w := httptest.NewRecorder()
			c.handler.ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("%s credential failed with %d", mode, w.Code)
			}
		}
	}
	list := c.call("GET", "/api/keys", nil, 200).Body.String()
	if strings.Contains(list, token) || strings.Contains(list, legacy) {
		t.Fatal("key listing exposed a complete credential")
	}
	// Changing just the prefix must never turn one issued key into another.
	for _, invalid := range []string{"ts_" + token[3:], "sk-" + legacy[3:], "sk-", "ts_", "sk-" + security.Token()} {
		c.key = invalid
		c.call("GET", "/v1/models", nil, 401)
	}
	for _, pair := range []struct{ id, key string }{{created["id"].(string), token}, {legacyID, legacy}} {
		c.call("DELETE", "/api/keys/"+pair.id, nil, 204)
		c.key = pair.key
		c.call("GET", "/v1/models", nil, 401)
	}
}
