package payments

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"tokenswoop/backend/internal/config"
)

func TestReplacementSignerChangesOnlyFee(t *testing.T) {
	cast, err := filepath.Abs("../../../contracts/.toolchain/bin/cast")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(cast); err != nil {
		t.Skip("bundled Foundry signer unavailable")
	}
	dir := t.TempDir()
	password := filepath.Join(dir, "password")
	if err = os.WriteFile(password, []byte("temporary-fixture-only"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(cast, "wallet", "new", dir, "provider", "--json")
	cmd.Env = append(os.Environ(), "CAST_PASSWORD=temporary-fixture-only")
	if _, err = cmd.Output(); err != nil {
		t.Fatal("could not create isolated signer", err)
	}
	key := filepath.Join(dir, "provider")
	os.Chmod(key, 0600)
	out, err := exec.Command(cast, "wallet", "address", "--keystore", key, "--password-file", password).Output()
	if err != nil {
		t.Fatal("could not inspect signer", err)
	}
	provider := strings.TrimSpace(string(out))
	consumed := false
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct{ Method string }
		json.NewDecoder(r.Body).Decode(&q)
		v := "0x0"
		switch q.Method {
		case "eth_chainId":
			v = "0x279f"
		case "eth_getTransactionCount":
			if consumed {
				v = "0x1"
			}
		case "eth_estimateGas":
			v = "0x186a0"
		case "eth_gasPrice":
			v = "0x3b9aca00"
		default:
			t.Errorf("unexpected RPC %s", q.Method)
		}
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": v})
	}))
	defer rpc.Close()
	r := NewChain(config.Payments{ChainID: 10143, RPC: rpc.URL, Cast: cast, Keystore: key, PasswordFile: password, Provider: strings.ToLower(provider)})
	original, err := r.SignContract(context.Background(), "0x1111111111111111111111111111111111111111", calldata("chargeBatch(address,bytes32,uint256)", "0x2222222222222222222222222222222222222222", strings.Repeat("c", 64), "10"))
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := r.Replace(context.Background(), original.Raw)
	if err != nil {
		t.Fatal(err)
	}
	decode := func(raw string) map[string]string {
		t.Helper()
		out, e := r.cast(context.Background(), "decode-transaction", raw, "--json")
		if e != nil {
			t.Fatal(e)
		}
		var envelope struct{ Data string }
		json.Unmarshal([]byte(out), &envelope)
		if envelope.Data != "" {
			out = envelope.Data
		}
		var v map[string]string
		if e = json.Unmarshal([]byte(out), &v); e != nil {
			t.Fatal("could not decode transaction")
		}
		return v
	}
	before, after := decode(original.Raw), decode(replacement.Raw)
	for _, field := range []string{"signer", "type", "chainId", "nonce", "gas", "to", "value", "input"} {
		if before[field] != after[field] {
			t.Fatalf("replacement changed %s", field)
		}
	}
	oldPrice, _ := integer(before["gasPrice"])
	newPrice, _ := integer(after["gasPrice"])
	if newPrice.Cmp(oldPrice) <= 0 || original.Hash == replacement.Hash || original.Nonce != replacement.Nonce {
		t.Fatal("invalid fee replacement")
	}
	consumed = true
	if _, err = r.Replace(context.Background(), replacement.Raw); err == nil {
		t.Fatal("replaced an already consumed nonce")
	}
}
