package httpapi

import (
	"context"
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"
	"tokenswoop/backend/internal/config"
	"tokenswoop/backend/internal/faucet"
	"tokenswoop/backend/internal/testutil"
)

type faucetVerifier struct{ err error }

func (v faucetVerifier) Verify(context.Context, string, string, string) error { return v.err }
func TestTestnetFaucetRequiresSessionCSRFAndCaptchaAndBindsRecipient(t *testing.T) {
	a := testApp(t)
	a.Faucet = &faucet.Service{Store: a.Store, Config: config.Faucet{Enabled: true, SiteKey: "public-site-key", SecretKey: "never-expose-this", Cooldown: 24 * time.Hour, IPDailyLimit: 10, DailyLimit: 100}, Payment: config.Payments{Enabled: true, ChainID: 10143, Token: "0x2222222222222222222222222222222222222222", Provider: "0x3333333333333333333333333333333333333333"}, Secret: a.Config.Secret, Verify: faucetVerifier{}}
	client := &testClient{t: t, handler: a.Handler()}
	id := uuid.NewString()
	body := map[string]any{"id": id, "turnstile_token": "token", "wallet": "0x4444444444444444444444444444444444444444", "amount_units": "9999999999999"}
	client.call("POST", "/api/faucet/claims", body, 401)
	key, wallet := testutil.Wallet(t)
	client.login(key, wallet)
	csrf := client.csrf
	client.csrf = "bad"
	client.call("POST", "/api/faucet/claims", body, 403)
	client.csrf = csrf
	a.Faucet.Verify = faucetVerifier{faucet.ErrVerification}
	client.call("POST", "/api/faucet/claims", body, 403)
	a.Faucet.Verify = faucetVerifier{}
	out := object(t, client.call("POST", "/api/faucet/claims", body, 202))
	if out["amount_units"] != faucet.Amount || out["wallet"] != wallet {
		t.Fatal("client controlled transfer", out)
	}
	a.Faucet.Verify = faucetVerifier{faucet.ErrVerification}
	client.call("POST", "/api/faucet/claims", body, 200)
	status := client.call("GET", "/api/faucet", nil, 200)
	if strings.Contains(status.Body.String(), a.Faucet.Config.SecretKey) {
		t.Fatal("verification secret exposed")
	}
	fields := object(t, status)
	if fields["site_key"] != "public-site-key" || fields["secret_key"] != nil {
		t.Fatal(fields)
	}
	otherKey, otherWallet := testutil.Wallet(t)
	other := &testClient{t: t, handler: a.Handler()}
	other.login(otherKey, otherWallet)
	other.call("GET", "/api/faucet/claims/"+id, nil, 404)
	a.Config.ChainID = 143
	mainnet := &testClient{t: t, handler: a.Handler(), cookies: client.cookies, csrf: client.csrf}
	mainnet.call("POST", "/api/faucet/claims", body, 404)
	mainnet.call("GET", "/api/faucet", nil, 404)
}
