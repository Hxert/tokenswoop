package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"tokenswoop/backend/internal/config"
	"tokenswoop/backend/internal/payments"
	"tokenswoop/backend/internal/privy"
	"tokenswoop/backend/internal/reserve"
	"tokenswoop/backend/internal/testutil"
)

func privyApp(t *testing.T) (*App, *testutil.PrivyFixture, *testClient) {
	t.Helper()
	a := testApp(t)
	_, wallet := testutil.Wallet(t)
	f := testutil.NewPrivy(t, wallet)
	a.Config.Privy = config.Privy{AppID: f.AppID, AppSecret: "fixture-secret", GasSponsorship: true, DailyTransactions: 2}
	a.Privy = f.Client
	a.Config.Payments = config.Payments{Enabled: true, Token: "0x" + strings.Repeat("11", 20), Contract: "0x" + strings.Repeat("22", 20), Provider: "0x" + strings.Repeat("44", 20)}
	a.Config.Reserve = config.Reserve{Enabled: true, Contract: "0x" + strings.Repeat("33", 20)}
	a.Payments = payments.New(a.Store, a.Config.Payments, walletEvidence{}, a.Config.Secret)
	a.Reserve = reserve.New(a.Store, a.Config.Reserve, a.Config.Payments.Token, &workspaceReserveChain{}, a.Config.Secret)
	return a, f, &testClient{t: t, handler: a.Handler()}
}
func loginPrivy(t *testing.T, c *testClient, f *testutil.PrivyFixture, hint string) map[string]any {
	t.Helper()
	w := c.call("POST", "/api/auth/privy", map[string]string{"access_token": f.Token(nil), "wallet": hint}, 200)
	c.cookies = w.Result().Cookies()
	for _, cookie := range c.cookies {
		if cookie.Name == "ts_csrf" {
			c.csrf = cookie.Value
		}
		if cookie.Name == "ts_session" && !cookie.HttpOnly {
			t.Fatal("insecure session cookie")
		}
	}
	return object(t, w)
}
func TestPrivySelectedWalletIsTheOnlyAccountIdentity(t *testing.T) {
	a, f, c := privyApp(t)
	ctx := context.Background()
	key, wallet := testutil.Wallet(t)
	a.Config.AdminWallets[wallet] = true
	original := c.login(key, wallet)
	id := original["id"].(string)
	apiKey := object(t, c.call("POST", "/api/keys", map[string]any{"name": "keep this key", "budget": nil}, 201))
	embeddedWallet := f.Profile.Accounts[0]
	externalWallet := privy.Wallet{Type: "wallet", ChainType: "ethereum", Address: wallet, ClientType: "metamask", ConnectorType: "injected"}
	f.Profile.Accounts = append(f.Profile.Accounts, externalWallet)
	if _, err := a.Store.DB.Exec(ctx, "UPDATE users SET tier='builder',tier_override=true,unpaid_balance=0.012 WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	// A retained pre-migration identity must never override an explicit wallet selection.
	if _, err := a.Store.DB.Exec(ctx, "INSERT INTO auth_identities(provider,app_id,subject,user_id,wallet_kind) VALUES('privy',$1,$2,$3,'external')", f.AppID, f.Profile.ID, id); err != nil {
		t.Fatal(err)
	}
	c.call("POST", "/api/auth/privy", map[string]string{"access_token": f.Token(nil)}, 409)
	same := loginPrivy(t, c, f, wallet)
	if same["id"] != id || same["wallet"] != wallet || same["role"] != "admin" {
		t.Fatal("same wallet did not reuse account", same)
	}
	if !strings.Contains(c.call("GET", "/api/keys", nil, 200).Body.String(), apiKey["id"].(string)) {
		t.Fatal("existing key lost")
	}
	c.call("POST", "/api/wallet/transactions", map[string]string{"action": "payment_approve", "amount_units": "10"}, 409)
	c.call("POST", "/api/auth/privy", map[string]string{"access_token": f.Token(nil), "wallet": "0x" + strings.Repeat("ff", 20)}, 403)
	previous := &testClient{t: t, handler: c.handler, cookies: c.cookies, csrf: c.csrf}
	selected := loginPrivy(t, c, f, embeddedWallet.Address)
	if selected["id"] == id || selected["wallet"] != embeddedWallet.Address || selected["role"] != "user" {
		t.Fatal("old identity overrode wallet selection", selected)
	}
	previous.call("GET", "/api/me", nil, 401)
	if strings.Contains(c.call("GET", "/api/keys", nil, 200).Body.String(), apiKey["id"].(string)) {
		t.Fatal("API keys crossed wallet accounts")
	}
	c.call("GET", "/api/admin/users", nil, 403)
	u, err := a.Store.User(ctx, id)
	if err != nil || u.Tier != "builder" || !strings.HasPrefix(u.Unpaid, "0.012") {
		t.Fatal("old wallet tier or debt changed", u, err)
	}
	newUser, err := a.Store.User(ctx, selected["id"].(string))
	if err != nil || newUser.Tier == "builder" || !strings.HasPrefix(newUser.Unpaid, "0.000") {
		t.Fatal("old balance or tier carried to new wallet", newUser, err)
	}
	// The old address can be removed from the provider; only the chosen address matters.
	f.Profile.Accounts = []privy.Wallet{embeddedWallet}
	c.call("POST", "/api/auth/privy", map[string]string{"access_token": f.Token(nil), "wallet": wallet}, 403)
	if loginPrivy(t, c, f, "")["id"] != selected["id"] {
		t.Fatal("single verified wallet did not reuse its account")
	}
	// Another verified Privy identity may prove control of the very same address.
	f.Profile.Accounts = []privy.Wallet{externalWallet}
	f.Profile.ID = "did:privy:differentidentity"
	if loginPrivy(t, c, f, wallet)["id"] != id {
		t.Fatal("provider identity incorrectly changed wallet account")
	}
	var count int
	if err := a.Store.DB.QueryRow(ctx, "SELECT count(*) FROM auth_identities").Scan(&count); err != nil || count != 1 {
		t.Fatal("login modified legacy identity bindings", count, err)
	}
	if _, err := a.Store.DB.Exec(ctx, "UPDATE users SET status='suspended' WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	c.call("POST", "/api/auth/privy", map[string]string{"access_token": f.Token(nil), "wallet": wallet}, 403)
	public := c.call("GET", "/api/config", nil, 200).Body.String()
	if strings.Contains(public, "fixture-secret") || !strings.Contains(public, f.AppID) {
		t.Fatal("public auth configuration invalid")
	}
}

func TestSponsorshipRequiresCurrentSessionEmbeddedWalletProof(t *testing.T) {
	a, f, c := privyApp(t)
	key, wallet := testutil.Wallet(t)
	f.Profile.Accounts[0].Address = wallet
	signedIn := loginPrivy(t, c, f, wallet)
	native := &testClient{t: t, handler: c.handler}
	if native.login(key, wallet)["id"] != signedIn["id"] {
		t.Fatal("wallet signature created another account")
	}
	operation := map[string]string{"action": "payment_approve", "amount_units": "10"}
	native.call("POST", "/api/wallet/transactions", operation, 409)
	prepared := object(t, c.call("POST", "/api/wallet/transactions", operation, 201))
	c.call("DELETE", "/api/wallet/transactions/"+prepared["id"].(string), nil, 204)
	// Re-authenticating through EOA signing clears sponsorship proof even at the same address.
	if c.login(key, wallet)["id"] != signedIn["id"] {
		t.Fatal("wallet signature changed account")
	}
	c.call("POST", "/api/wallet/transactions", operation, 409)
	loginPrivy(t, c, f, wallet)
	_, unlinked := testutil.Wallet(t)
	f.Profile.Accounts[0].Address = unlinked
	c.call("POST", "/api/wallet/transactions", operation, 403)
	// A removed wallet cannot retain sponsorship through a stale cookie.
	f.Profile.Accounts[0].Address = wallet
	a.Config.Privy.AppID = "another-application"
	c.call("POST", "/api/wallet/transactions", operation, 409)
}

func TestPrivyNewEmbeddedAccountCannotClaimAdmin(t *testing.T) {
	a, f, c := privyApp(t)
	admin := "0x" + strings.Repeat("ab", 20)
	a.Config.AdminWallets[admin] = true
	c.call("POST", "/api/auth/privy", map[string]string{"access_token": f.Token(nil), "wallet": admin, "role": "admin"}, 403)
	user := loginPrivy(t, c, f, "")
	if user["wallet"] != f.Profile.Accounts[0].Address || user["role"] != "user" {
		t.Fatal("unverified role or wallet accepted", user)
	}
	c.call("GET", "/api/admin/users", nil, 403)
	again := loginPrivy(t, c, f, "")
	if again["id"] != user["id"] {
		t.Fatal("repeat login duplicated user")
	}
}
func TestSponsoredTransactionProtocolOwnershipQuotaAndRetry(t *testing.T) {
	a, f, c := privyApp(t)
	loginPrivy(t, c, f, "")
	prepare := func(action, amount string) map[string]any {
		return object(t, c.call("POST", "/api/wallet/transactions", map[string]any{"action": action, "amount_units": amount, "to": "0xattacker", "sponsor": false}, 201))
	}
	csrf := c.csrf
	c.csrf = "invalid"
	c.call("POST", "/api/wallet/transactions", map[string]string{"action": "payment_approve", "amount_units": "10"}, 403)
	c.csrf = csrf
	c.call("POST", "/api/wallet/transactions", map[string]string{"action": "transfer", "amount_units": "10"}, 400)
	p := prepare("payment_approve", "10")
	id := p["id"].(string)
	auth := p["authorization"].(map[string]any)
	body := auth["body"].(map[string]any)
	tx := body["params"].(map[string]any)["transaction"].(map[string]any)
	if tx["to"] != a.Config.Payments.Token || body["sponsor"] != true || body["caip2"] != "eip155:10143" || body["reference_id"] != id {
		t.Fatal("client overrode pinned operation", body)
	}
	hash := "0x" + strings.Repeat("ab", 32)
	calls := 0
	f.RPC = func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Path != "/v1/wallets/embedded-wallet/rpc" || r.Method != "POST" || r.Header.Get("privy-idempotency-key") != id || r.Header.Get("privy-authorization-signature") != "fixture-user-signature" {
			t.Fatal("wrong RPC authorization")
		}
		raw, _ := io.ReadAll(r.Body)
		var got any
		json.Unmarshal(raw, &got)
		if !reflect.DeepEqual(got, body) {
			t.Fatal("signed body changed")
		}
		headers := auth["headers"].(map[string]any)
		if r.Header.Get("privy-request-expiry") != headers["privy-request-expiry"] {
			t.Fatal("signed expiry changed")
		}
		return testutil.JSONResponse(200, map[string]any{"data": map[string]string{"hash": hash, "transaction_id": "remote-1"}}), nil
	}
	submit := func(id string, status int) map[string]any {
		return object(t, c.call("POST", "/api/wallet/transactions/"+id+"/submit", map[string]string{"signature": "fixture-user-signature"}, status))
	}
	if submit(id, 200)["hash"] != hash || submit(id, 200)["hash"] != hash || calls != 1 {
		t.Fatal("duplicate RPC execution")
	}
	other := &testClient{t: t, handler: c.handler}
	key, wallet := testutil.Wallet(t)
	other.login(key, wallet)
	other.call("GET", "/api/wallet/transactions/"+id, nil, 404)
	other.call("POST", "/api/wallet/transactions/"+id+"/submit", map[string]string{"signature": "fixture-user-signature"}, 404)
	c.call("DELETE", "/api/wallet/transactions/"+id, nil, 409)
	// Lost response is reconciled by the unique reference, even after authorization expiry.
	p = prepare("reserve_deposit", "50")
	id = p["id"].(string)
	f.RPC = func(r *http.Request) (*http.Response, error) {
		if r.Method == "POST" {
			return nil, errors.New("connection lost after sending")
		}
		if r.URL.Query().Get("reference_id") != id {
			t.Fatal("wrong recovery reference")
		}
		return testutil.JSONResponse(200, map[string]any{"transactions": []any{map[string]string{"id": "remote-2", "wallet_id": "embedded-wallet", "caip2": "eip155:10143", "reference_id": id, "transaction_hash": hash, "status": "confirmed"}}}), nil
	}
	if submit(id, 202)["state"] != "pending" {
		t.Fatal("ambiguous transaction discarded")
	}
	c.call("DELETE", "/api/wallet/transactions/"+id, nil, 409)
	_, e := a.Store.DB.Exec(context.Background(), `UPDATE wallet_transactions SET expires_at=now()-interval '1 minute' WHERE id=$1`, id)
	if e != nil {
		t.Fatal(e)
	}
	got := object(t, c.call("GET", "/api/wallet/transactions/"+id, nil, 200))
	if got["hash"] != hash || got["state"] != "submitted" {
		t.Fatal("could not recover lost broadcast", got)
	}
	id = prepare("reserve_approve", "100")["id"].(string)
	submit(id, 429)
	c.call("DELETE", "/api/wallet/transactions/"+id, nil, 204)
}
func TestSponsoredExpiryRejectionAndNeverSubmittingAfterCancel(t *testing.T) {
	a, f, c := privyApp(t)
	loginPrivy(t, c, f, "")
	p := object(t, c.call("POST", "/api/wallet/transactions", map[string]string{"action": "reserve_withdraw", "amount_units": "1"}, 201))
	id := p["id"].(string)
	_, err := a.Store.DB.Exec(context.Background(), `UPDATE wallet_transactions SET expires_at=now()-interval '1 minute' WHERE id=$1`, id)
	if err != nil {
		t.Fatal(err)
	}
	c.call("POST", "/api/wallet/transactions/"+id+"/submit", map[string]string{"signature": "fixture"}, 409)
	c.call("DELETE", "/api/wallet/transactions/"+id, nil, 204)
	c.call("POST", "/api/wallet/transactions/"+id+"/submit", map[string]string{"signature": "fixture"}, 409)
	p = object(t, c.call("POST", "/api/wallet/transactions", map[string]string{"action": "payment_approve", "amount_units": "0"}, 201))
	id = p["id"].(string)
	f.RPC = func(*http.Request) (*http.Response, error) {
		return testutil.JSONResponse(403, map[string]string{"error": "private upstream explanation"}), nil
	}
	response := c.call("POST", "/api/wallet/transactions/"+id+"/submit", map[string]string{"signature": "fixture"}, 202)
	if object(t, response)["state"] != "rejected" || strings.Contains(response.Body.String(), "private upstream explanation") {
		t.Fatal("unsafe rejection handling")
	}
	c.call("GET", "/api/wallet/transactions/"+uuid.NewString(), nil, 404)
}
