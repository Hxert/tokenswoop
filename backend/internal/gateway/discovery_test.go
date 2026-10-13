package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDiscoveryProtocolsAndPagination(t *testing.T) {
	for _, protocol := range []string{"openai", "openai_responses", "claude", "gemini"} {
		t.Run(protocol, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				if r.Method != "GET" || r.URL.Path != "/v1/models" {
					t.Errorf("unexpected metadata request %s %s", r.Method, r.URL.Path)
				}
				if strings.Contains(r.URL.RawQuery, "fixture-key") {
					t.Error("credential in query")
				}
				switch protocol {
				case "claude":
					if r.Header.Get("x-api-key") != "fixture-key" || r.Header.Get("anthropic-version") != "2023-06-01" || r.Header.Get("Authorization") != "" {
						t.Error("Claude auth headers")
					}
					if n == 1 {
						fmt.Fprint(w, `{"data":[{"id":"z-model","display_name":"Zeta","max_input_tokens":200000,"max_tokens":8192}],"has_more":true,"last_id":"z-model"}`)
					} else {
						if r.URL.Query().Get("after_id") != "z-model" {
							t.Error("missing Claude cursor")
						}
						fmt.Fprint(w, `{"data":[{"id":"a-model","display_name":"Alpha"},{"id":"z-model"}],"has_more":false}`)
					}
				case "gemini":
					if r.Header.Get("x-goog-api-key") != "fixture-key" || r.Header.Get("Authorization") != "" {
						t.Error("Gemini auth headers")
					}
					if n == 1 {
						fmt.Fprint(w, `{"models":[{"name":"models/z-model","displayName":"Zeta","inputTokenLimit":200000,"outputTokenLimit":8192,"supportedGenerationMethods":["generateContent"]},{"name":"models/embedding","supportedGenerationMethods":["embedContent"]}],"nextPageToken":"next+page"}`)
					} else {
						if r.URL.Query().Get("pageToken") != "next+page" {
							t.Error("missing encoded Gemini cursor")
						}
						fmt.Fprint(w, `{"models":[{"name":"models/a-model"},{"name":"models/z-model"},{"name":"models/invalid id"}]}`)
					}
				default:
					if r.Header.Get("Authorization") != "Bearer fixture-key" {
						t.Error("OpenAI auth header")
					}
					fmt.Fprint(w, `{"object":"list","data":[{"id":"z-model","owned_by":"Fixture"},{"id":"a-model"},{"id":"z-model"}]}`)
				}
			}))
			defer upstream.Close()
			g := New(nil, nil, true)
			result, e := g.DiscoverModels(context.Background(), protocol, upstream.URL+"/v1/", "fixture-key")
			if e != nil {
				t.Fatal(e)
			}
			if len(result.Models) != 2 || result.Models[0].ID != "a-model" || result.Models[1].ID != "z-model" {
				t.Fatalf("normalized models: %+v", result)
			}
			if protocol == "gemini" && (result.Skipped != 2 || result.Models[1].Vendor != "Google") {
				t.Fatalf("Gemini filtering: %+v", result)
			}
			if protocol == "claude" || protocol == "gemini" {
				if calls.Load() != 2 || result.Models[1].MaxInput != 200000 {
					t.Fatalf("pagination/metadata: %+v calls=%d", result, calls.Load())
				}
			}
		})
	}
}

func TestDiscoveryBoundsAndSanitizedErrors(t *testing.T) {
	for _, test := range []struct {
		name, body, code string
		status           int
	}{
		{"auth", `{"error":"secret-fixture-key"}`, "upstream_auth_failed", 401},
		{"missing", `secret-fixture-key`, "model_list_unavailable", 404},
		{"rate", `secret-fixture-key`, "upstream_rate_limited", 429},
		{"server", `secret-fixture-key`, "upstream_rejected", 500},
		{"redirect", `secret-fixture-key`, "upstream_rejected", 302},
		{"html", `<html>secret-fixture-key</html>`, "invalid_model_list", 200},
		{"null", `{"data":null}`, "invalid_model_list", 200},
		{"object", `{"data":{}}`, "invalid_model_list", 200},
		{"missing array", `{"error":"secret-fixture-key"}`, "invalid_model_list", 200},
		{"loop", `{"data":[{"id":"test"}],"has_more":true,"last_id":"same"}`, "invalid_pagination", 200},
		{"no cursor", `{"data":[],"has_more":true}`, "invalid_pagination", 200},
		{"oversize", strings.Repeat("x", (2<<20)+1), "model_list_too_large", 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", "/redirected")
				w.WriteHeader(test.status)
				fmt.Fprint(w, test.body)
			}))
			defer upstream.Close()
			_, e := New(nil, nil, true).DiscoverModels(context.Background(), "openai", upstream.URL, "secret-fixture-key")
			var d *DiscoveryError
			if !errors.As(e, &d) || d.Code != test.code || strings.Contains(e.Error(), "secret-fixture-key") {
				t.Fatalf("unsafe/wrong error: %v", e)
			}
			if test.name == "redirect" && calls.Load() != 1 {
				t.Fatal("followed a redirect with credentials")
			}
			if calls.Load() > 2 {
				t.Fatal("pagination was not bounded")
			}
		})
	}
	t.Run("empty is valid", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"data":[]}`) }))
		defer server.Close()
		out, e := New(nil, nil, true).DiscoverModels(context.Background(), "openai", server.URL, "key")
		if e != nil || out.Models == nil || len(out.Models) != 0 {
			t.Fatalf("empty: %+v %v", out, e)
		}
	})
	t.Run("private address blocked at dial", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
		defer server.Close()
		_, e := New(nil, nil, false).DiscoverModels(context.Background(), "openai", server.URL, "key")
		if e == nil || calls.Load() != 0 {
			t.Fatal("discovery bypassed relay address restrictions")
		}
	})
}
