package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"tokenswoop/backend/internal/payments"
	"tokenswoop/backend/internal/rpclimit"
	"tokenswoop/backend/internal/testutil"
)

func TestWalletRPCSessionScopeAndCredentialRedaction(t *testing.T) {
	a := testApp(t)
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var in struct {
			Method string
			Params []json.RawMessage
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Method == "eth_call" {
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "error": map[string]any{"code": 3, "message": "execution reverted at https://private.invalid/secret-token", "data": "0x12345678"}})
			return
		}
		if in.Method == "eth_sendRawTransaction" && rpcString(in.Params[0]) != "0x12345678" {
			t.Error("signed bytes changed")
		}
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": "0x279f"})
	}))
	defer upstream.Close()
	a.Config.Payments.RPC = upstream.URL + "/private-token"
	a.Config.Payments.Token = "0x" + strings.Repeat("1", 40)
	a.Config.Payments.Contract = "0x" + strings.Repeat("2", 40)
	limit := rpclimit.New(a.Redis, a.Config.Payments.RPC, 40)
	a.ChainRPC = payments.NewChain(a.Config.Payments, limit.Wait)
	client := &testClient{t: t, handler: a.Handler()}
	request := func(method string, params ...any) map[string]any {
		return map[string]any{"jsonrpc": "2.0", "id": "wallet-request", "method": method, "params": params}
	}
	client.call("POST", "/api/rpc", request("eth_chainId"), 401)
	private, wallet := testutil.Wallet(t)
	client.login(private, wallet)
	csrf := client.csrf
	client.csrf = "wrong"
	client.call("POST", "/api/rpc", request("eth_chainId"), 403)
	client.csrf = csrf
	if calls.Load() != 0 {
		t.Fatal("unauthorized request reached upstream")
	}
	response := object(t, client.call("POST", "/api/rpc", request("eth_chainId"), 200))
	if response["id"] != "wallet-request" || response["result"] != "0x279f" {
		t.Fatal(response)
	}
	for _, body := range []any{
		request("debug_traceTransaction", "0x"+strings.Repeat("0", 64)),
		request("eth_sendTransaction", map[string]string{"from": wallet}),
		request("eth_getBalance", a.Config.Payments.Token, "latest"),
		request("eth_getBlockByNumber", "latest", true),
		request("eth_call", map[string]string{"to": wallet}, "latest"),
		request("eth_estimateGas", map[string]string{"to": a.Config.Payments.Token, "from": a.Config.Payments.Contract}),
		request("eth_call", map[string]string{"to": a.Config.Payments.Token}, "latest", map[string]any{}),
	} {
		if object(t, client.call("POST", "/api/rpc", body, 200))["error"] == nil {
			t.Fatal("unrestricted RPC accepted")
		}
	}
	client.call("POST", "/api/rpc", []any{request("eth_chainId"), request("eth_chainId")}, 400)
	if calls.Load() != 1 {
		t.Fatal("disallowed request reached upstream")
	}
	response = object(t, client.call("POST", "/api/rpc", request("eth_call", map[string]string{"to": a.Config.Payments.Token, "data": "0x12345678"}, "latest"), 200))
	errBody := response["error"].(map[string]any)
	if errBody["code"] != float64(3) || errBody["data"] != "0x12345678" || errBody["message"] != "execution reverted" {
		t.Fatal(response)
	}
	client.call("POST", "/api/rpc", request("eth_getBalance", wallet, "latest"), 200)
	client.call("POST", "/api/rpc", request("eth_sendRawTransaction", "0x12345678"), 200)
	if calls.Load() != 4 {
		t.Fatal("wallet request lost")
	}
	a.Redis.Close()
	client.call("POST", "/api/rpc", request("eth_chainId"), 503)
	if calls.Load() != 4 {
		t.Fatal("RPC admitted with unavailable limiter")
	}
}
