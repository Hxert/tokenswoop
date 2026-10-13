package httpapi

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	"tokenswoop/backend/internal/config"
	"tokenswoop/backend/internal/merasponsor"
	"tokenswoop/backend/internal/testutil"
)

type meraHTTPChain struct{ approvalErr error }

func (c meraHTTPChain) CheckApproval(context.Context, string, string, string) error {
	return c.approvalErr
}

func (meraHTTPChain) Prepare(context.Context, string) (*int64, error) { return nil, nil }
func (meraHTTPChain) Sign(context.Context, merasponsor.Operation, string, *merasponsor.Authorization) (merasponsor.Signed, error) {
	panic("HTTP must not sign sponsor transactions")
}
func (meraHTTPChain) Broadcast(context.Context, string) error { panic("HTTP must not broadcast") }
func (meraHTTPChain) Confirm(context.Context, merasponsor.Operation) (string, error) {
	panic("HTTP must not confirm")
}
func TestMeraSessionScopeOwnershipAndIdempotentSubmission(t *testing.T) {
	a := testApp(t)
	a.Config.Mera = config.Mera{Enabled: true, Implementation: "0x1111111111111111111111111111111111111111", Sponsor: "0x2222222222222222222222222222222222222222", DailyTransactions: 30}
	a.Mera = &merasponsor.Service{Store: a.Store, Config: a.Config.Mera, ChainID: 10143, Chain: meraHTTPChain{}}
	h := a.Handler()
	key, wallet := testutil.Wallet(t)
	client := &testClient{t: t, handler: h}
	client.login(key, wallet)
	request := map[string]string{"id": uuid.NewString(), "action": "payment_approve", "amount_units": "1000000"}
	client.call("POST", "/api/wallet/mera/transactions", request, 409)
	ch := object(t, client.call("POST", "/api/auth/challenge", map[string]string{"wallet": wallet, "source": "mera"}, 200))
	result := client.call("POST", "/api/auth/login", map[string]string{"id": ch["id"].(string), "signature": testutil.Sign(key, ch["message"].(string))}, 200)
	client.cookies = result.Result().Cookies()
	for _, c := range client.cookies {
		if c.Name == "ts_csrf" {
			client.csrf = c.Value
		}
	}
	a.Mera.Chain = meraHTTPChain{approvalErr: merasponsor.ErrInsufficientBalance}
	failure := object(t, client.call("POST", "/api/wallet/mera/transactions", request, 422))
	if failure["error"].(map[string]any)["code"] != "insufficient_wallet_balance" {
		t.Fatal(failure)
	}
	a.Mera.Chain = meraHTTPChain{}
	prepared := client.call("POST", "/api/wallet/mera/transactions", request, 201)
	var o merasponsor.Operation
	if e := json.Unmarshal(prepared.Body.Bytes(), &o); e != nil {
		t.Fatal(e)
	}
	saved := object(t, client.call("GET", "/api/wallet/mera/pending", nil, 200))
	if saved["id"] != o.ID {
		t.Fatal("cannot recover without localStorage")
	}
	csrf := client.csrf
	client.csrf = "wrong"
	client.call("POST", "/api/wallet/mera/transactions", request, 403)
	client.csrf = csrf
	sig := ecdsa.SignCompact(key, o.Digest(), false)
	body := map[string]any{"signature": "0x" + hex.EncodeToString(append(sig[1:], sig[0])), "authorization": nil}
	client.call("POST", "/api/wallet/mera/transactions/"+o.ID+"/submit", body, 202)
	client.call("POST", "/api/wallet/mera/transactions/"+o.ID+"/submit", body, 202)
	client.call("DELETE", "/api/wallet/mera/transactions/"+o.ID, nil, 409)
	client.call("GET", "/api/wallet/transactions/"+o.ID, nil, 200)
	otherKey, otherWallet := testutil.Wallet(t)
	other := &testClient{t: t, handler: h}
	other.login(otherKey, otherWallet)
	other.call("GET", "/api/wallet/mera/transactions/"+o.ID, nil, 404)
	var count int
	if e := a.Store.DB.QueryRow(context.Background(), `SELECT count(*) FROM mera_transactions`).Scan(&count); e != nil || count != 1 {
		t.Fatal("duplicate operation", count, e)
	}
}
