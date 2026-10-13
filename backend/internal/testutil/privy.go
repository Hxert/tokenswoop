package testutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"tokenswoop/backend/internal/privy"
)

// PrivyFixture verifies the actual JWT and HTTP protocol without contacting Privy.
type PrivyFixture struct {
	T                    *testing.T
	AppID                string
	Key                  *ecdsa.PrivateKey
	Client               *privy.Client
	Profile              privy.User
	JWKSReads, UserReads int
	RPC                  func(*http.Request) (*http.Response, error)
}
type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func JSONResponse(status int, v any) *http.Response {
	b, _ := json.Marshal(v)
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(b)))}
}
func NewPrivy(t *testing.T, wallet string) *PrivyFixture {
	t.Helper()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	f := &PrivyFixture{T: t, AppID: "testprivyapplication", Key: key, Profile: privy.User{ID: "did:privy:testidentity", Accounts: []privy.Wallet{{ID: "embedded-wallet", Address: wallet, Type: "wallet", ChainType: "ethereum", ClientType: "privy", ConnectorType: "embedded"}}}}
	f.Client = privy.New(f.AppID, "fixture-secret").WithHTTPClient(&http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "api.privy.io" {
			t.Fatalf("unexpected Privy host: %s", r.URL)
		}
		if r.URL.Path == "/v1/apps/"+f.AppID+"/jwks.json" {
			f.JWKSReads++
			if r.Header.Get("Authorization") != "" {
				t.Fatal("credentials sent to public JWKS endpoint")
			}
			return JSONResponse(200, map[string]any{"keys": []any{map[string]string{"kid": "fixture-key", "kty": "EC", "crv": "P-256", "alg": "ES256", "use": "sig", "x": base64.RawURLEncoding.EncodeToString(key.X.FillBytes(make([]byte, 32))), "y": base64.RawURLEncoding.EncodeToString(key.Y.FillBytes(make([]byte, 32)))}}}), nil
		}
		id, secret, ok := r.BasicAuth()
		if !ok || id != f.AppID || secret != "fixture-secret" || r.Header.Get("privy-app-id") != f.AppID {
			t.Fatal("wrong Privy application credentials")
		}
		if strings.HasPrefix(r.URL.Path, "/v1/users/") {
			f.UserReads++
			return JSONResponse(200, f.Profile), nil
		}
		if f.RPC != nil {
			return f.RPC(r)
		}
		t.Errorf("unexpected Privy request: %s %s", r.Method, r.URL.Path)
		return JSONResponse(500, map[string]string{"error": "unhandled fixture"}), nil
	})})
	return f
}
func (f *PrivyFixture) Token(overrides jwt.MapClaims) string {
	f.T.Helper()
	claims := jwt.MapClaims{"aud": f.AppID, "iss": "privy.io", "sub": f.Profile.ID, "sid": "fixture-session", "iat": time.Now().Add(-time.Minute).Unix(), "exp": time.Now().Add(time.Hour).Unix()}
	for k, v := range overrides {
		if v == nil {
			delete(claims, k)
		} else {
			claims[k] = v
		}
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["kid"] = "fixture-key"
	s, e := token.SignedString(f.Key)
	if e != nil {
		f.T.Fatal(e)
	}
	return s
}
