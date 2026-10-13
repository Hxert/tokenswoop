package faucet

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestTurnstileRequiresSuccessHostActionIDAndFreshToken(t *testing.T) {
	for _, mode := range []string{"valid", "false", "hostname", "action", "id", "expired", "future", "network", "http", "json", "empty", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			v := NewTurnstile("private-fixture-secret", "localhost")
			v.HTTP.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() != "https://challenges.cloudflare.com/turnstile/v0/siteverify" || r.Method != "POST" {
					t.Fatal("incorrect verifier destination")
				}
				var body map[string]string
				json.NewDecoder(r.Body).Decode(&body)
				_, keyErr := uuid.Parse(body["idempotency_key"])
				if body["secret"] != v.Secret || body["response"] != "valid-token" || keyErr != nil || body["remoteip"] != "127.0.0.1" {
					t.Fatal("missing verifier binding")
				}
				result := map[string]any{"success": true, "hostname": "localhost", "action": Action, "cdata": "claim-id", "challenge_ts": time.Now().UTC().Format(time.RFC3339)}
				switch mode {
				case "false":
					result["success"] = false
				case "hostname":
					result["hostname"] = "evil.example"
				case "action":
					result["action"] = "login"
				case "id":
					result["cdata"] = "another-id"
				case "expired":
					result["challenge_ts"] = time.Now().Add(-6 * time.Minute).Format(time.RFC3339)
				case "future":
					result["challenge_ts"] = time.Now().Add(time.Hour).Format(time.RFC3339)
				case "network":
					return nil, errors.New("disconnected")
				}
				b, _ := json.Marshal(result)
				code := 200
				if mode == "http" {
					code = 500
				}
				if mode == "json" {
					b = []byte("invalid")
				}
				return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(string(b)))}, nil
			})
			token := "valid-token"
			if mode == "empty" {
				token = ""
			}
			if mode == "oversize" {
				token = strings.Repeat("a", 2049)
			}
			err := v.Verify(context.Background(), token, "127.0.0.1", "claim-id")
			if (mode == "valid") != (err == nil) {
				t.Fatal(mode, err)
			}
			if (mode == "empty" || mode == "oversize") && calls != 0 {
				t.Fatal("invalid token made HTTP call")
			}
		})
	}
}

func TestFreshChallengeDoesNotReuseFailedVerificationKey(t *testing.T) {
	v := NewTurnstile("private-fixture-secret", "localhost")
	var keys []string
	v.HTTP.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, body["idempotency_key"])
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"success":false}`))}, nil
	})
	for _, token := range []string{"first-token", "first-token", "fresh-token"} {
		if err := v.Verify(context.Background(), token, "127.0.0.1", "claim-id"); !errors.Is(err, ErrVerification) {
			t.Fatal(err)
		}
	}
	if keys[0] != keys[1] || keys[1] == keys[2] {
		t.Fatal("incorrect validation retry isolation")
	}
}
