// Package privy verifies identity and relays explicitly user-authorized wallet RPCs.
// It never holds a user wallet key or a delegated/session signer.
package privy

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var ErrIdentity = errors.New("invalid Privy identity")
var ErrUnavailable = errors.New("Privy unavailable")
var idPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)
var addressPattern = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

type Wallet struct {
	ID            string `json:"id"`
	Address       string `json:"address"`
	Type          string `json:"type"`
	ChainType     string `json:"chain_type"`
	ClientType    string `json:"wallet_client_type"`
	ConnectorType string `json:"connector_type"`
}

func (w Wallet) Embedded() bool { return w.ClientType == "privy" && w.ConnectorType == "embedded" }

type User struct {
	ID       string   `json:"id"`
	Accounts []Wallet `json:"linked_accounts"`
	Guest    bool     `json:"is_guest"`
	FrozenAt *int64   `json:"frozen_at"`
}

func (u User) Wallets() []Wallet {
	result := []Wallet{}
	for _, w := range u.Accounts {
		if w.Type != "wallet" || w.ChainType != "ethereum" || !addressPattern.MatchString(w.Address) {
			continue
		}
		w.Address = strings.ToLower(w.Address)
		result = append(result, w)
	}
	return result
}

type Client struct {
	AppID, appSecret        string
	ChainID                 int64
	http                    *http.Client
	baseURL, jwksURL        string
	mu                      sync.Mutex
	keys                    map[string]*ecdsa.PublicKey
	keysExpire, lastRefresh time.Time
}

func New(appID, secret string) *Client {
	return &Client{AppID: appID, appSecret: secret, baseURL: "https://api.privy.io/v1", jwksURL: "https://api.privy.io/v1/apps/" + url.PathEscape(appID) + "/jwks.json", http: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// WithHTTPClient supports instrumented transports and isolated protocol tests.
// Production endpoints remain fixed; callers cannot provide a request URL.
func (c *Client) WithHTTPClient(client *http.Client) *Client { c.http = client; return c }

func (c *Client) Authenticate(ctx context.Context, token string) (User, error) {
	if len(token) == 0 || len(token) > 16384 {
		return User{}, ErrIdentity
	}
	var claims struct {
		jwt.RegisteredClaims
		SessionID string `json:"sid"`
	}
	_, err := jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
		kid, ok := t.Header["kid"].(string)
		if (t.Header["kid"] != nil && !ok) || len(kid) > 128 || t.Header["typ"] != "JWT" {
			return nil, ErrIdentity
		}
		return c.key(ctx, kid)
	}, jwt.WithValidMethods([]string{"ES256"}), jwt.WithIssuer("privy.io"), jwt.WithAudience(c.AppID), jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			return User{}, ErrUnavailable
		}
		return User{}, ErrIdentity
	}
	if claims.IssuedAt == nil || claims.SessionID == "" || !strings.HasPrefix(claims.Subject, "did:privy:") || !idPattern.MatchString(strings.TrimPrefix(claims.Subject, "did:privy:")) {
		return User{}, ErrIdentity
	}
	return c.GetUser(ctx, claims.Subject)
}

func (c *Client) key(ctx context.Context, kid string) (*ecdsa.PublicKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if k := selectKey(c.keys, kid); k != nil && now.Before(c.keysExpire) {
		return k, nil
	}
	// Prevent untrusted kids from forcing an unbounded stream of discovery requests.
	if now.Sub(c.lastRefresh) < 30*time.Second {
		if !now.Before(c.keysExpire) {
			return nil, ErrUnavailable
		}
		return nil, ErrIdentity
	}
	c.lastRefresh = now
	req, _ := http.NewRequestWithContext(ctx, "GET", c.jwksURL, nil)
	req.Header.Set("privy-app-id", c.AppID)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer resp.Body.Close()
	var set struct {
		Keys []struct{ Kid, Kty, Crv, X, Y, Alg, Use string } `json:"keys"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(&set) != nil {
		return nil, ErrUnavailable
	}
	keys := map[string]*ecdsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "EC" || k.Crv != "P-256" || k.Kid == "" || (k.Alg != "" && k.Alg != "ES256") || (k.Use != "" && k.Use != "sig") {
			continue
		}
		x, ex := base64.RawURLEncoding.DecodeString(k.X)
		y, ey := base64.RawURLEncoding.DecodeString(k.Y)
		if ex != nil || ey != nil || len(x) != 32 || len(y) != 32 {
			continue
		}
		key := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
		if key.Curve.IsOnCurve(key.X, key.Y) {
			keys[k.Kid] = key
		}
	}
	if len(keys) == 0 {
		return nil, ErrUnavailable
	}
	c.keys, c.keysExpire = keys, now.Add(time.Hour)
	if key := selectKey(keys, kid); key != nil {
		return key, nil
	}
	return nil, ErrIdentity
}

// JOSE permits an absent kid when there is exactly one eligible verification key.
func selectKey(keys map[string]*ecdsa.PublicKey, kid string) *ecdsa.PublicKey {
	if kid != "" {
		return keys[kid]
	}
	if len(keys) == 1 {
		for _, key := range keys {
			return key
		}
	}
	return nil
}

func (c *Client) request(ctx context.Context, method, path string, body []byte, headers map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return ErrUnavailable
	}
	req.SetBasicAuth(c.AppID, c.appSecret)
	req.Header.Set("privy-app-id", c.AppID)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Do not expose upstream bodies, credentials, user data or authorization signatures.
		return &APIError{Status: resp.StatusCode}
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out) != nil {
		return ErrUnavailable
	}
	return nil
}

type APIError struct{ Status int }

func (e *APIError) Error() string { return fmt.Sprintf("Privy request failed (%d)", e.Status) }

func (c *Client) GetUser(ctx context.Context, subject string) (User, error) {
	if !strings.HasPrefix(subject, "did:privy:") || !idPattern.MatchString(strings.TrimPrefix(subject, "did:privy:")) {
		return User{}, ErrIdentity
	}
	var user User
	if err := c.request(ctx, "GET", "/users/"+url.PathEscape(subject), nil, nil, &user); err != nil {
		return user, ErrUnavailable
	}
	if user.ID != subject || user.Guest || user.FrozenAt != nil {
		return User{}, ErrIdentity
	}
	return user, nil
}

type Authorization struct {
	Version int               `json:"version"`
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Body    json.RawMessage   `json:"body"`
	Headers map[string]string `json:"headers"`
}

func (c *Client) Authorization(walletID, id string, body []byte, expires time.Time) Authorization {
	return Authorization{Version: 1, Method: "POST", URL: "https://api.privy.io/v1/wallets/" + walletID + "/rpc", Body: body, Headers: map[string]string{"privy-app-id": c.AppID, "privy-idempotency-key": id, "privy-request-expiry": fmt.Sprint(expires.UnixMilli())}}
}

type Transaction struct {
	Hash   string `json:"hash"`
	ID     string `json:"transaction_id"`
	Status string `json:"status"`
}

func (c *Client) Send(ctx context.Context, walletID string, auth Authorization, signature string) (Transaction, error) {
	if !idPattern.MatchString(walletID) || len(signature) == 0 || len(signature) > 2048 || strings.ContainsAny(signature, "\r\n") {
		return Transaction{}, ErrIdentity
	}
	headers := map[string]string{"privy-authorization-signature": signature}
	for k, v := range auth.Headers {
		headers[k] = v
	}
	var result struct {
		Data Transaction `json:"data"`
		ID   string      `json:"transaction_id"`
	}
	err := c.request(ctx, "POST", "/wallets/"+walletID+"/rpc", auth.Body, headers, &result)
	if result.Data.ID == "" {
		result.Data.ID = result.ID
	}
	return result.Data, err
}
func (c *Client) Transaction(ctx context.Context, id string) (Transaction, error) {
	if !idPattern.MatchString(id) {
		return Transaction{}, ErrIdentity
	}
	var result struct {
		Hash   string `json:"transaction_hash"`
		Status string `json:"status"`
	}
	err := c.request(ctx, "GET", "/transactions/"+id, nil, nil, &result)
	return Transaction{ID: id, Hash: result.Hash, Status: result.Status}, err
}

// Recover a broadcast even if its RPC response was lost, including after request expiry.
func (c *Client) TransactionByReference(ctx context.Context, reference, walletID string) (Transaction, error) {
	if !idPattern.MatchString(reference) || !idPattern.MatchString(walletID) {
		return Transaction{}, ErrIdentity
	}
	var result struct {
		Transactions []struct {
			ID        string `json:"id"`
			WalletID  string `json:"wallet_id"`
			Chain     string `json:"caip2"`
			Reference string `json:"reference_id"`
			Hash      string `json:"transaction_hash"`
			Status    string `json:"status"`
		} `json:"transactions"`
	}
	if err := c.request(ctx, "GET", "/transactions?reference_id="+url.QueryEscape(reference), nil, nil, &result); err != nil {
		return Transaction{}, err
	}
	if len(result.Transactions) != 1 {
		return Transaction{}, ErrUnavailable
	}
	r := result.Transactions[0]
	if r.WalletID != walletID || r.Chain != fmt.Sprintf("eip155:%d", c.networkID()) || r.Reference != reference {
		return Transaction{}, ErrUnavailable
	}
	return Transaction{ID: r.ID, Hash: r.Hash, Status: r.Status}, nil
}

func (c *Client) networkID() int64 {
	if c.ChainID == 0 {
		return 10143
	}
	return c.ChainID
}
