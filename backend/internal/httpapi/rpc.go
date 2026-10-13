package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"tokenswoop/backend/internal/security"
)

var rpcHex = regexp.MustCompile(`^0x(?:[0-9a-fA-F]{2})*$`)
var rpcQuantity = regexp.MustCompile(`^0x(?:0|[1-9a-fA-F][0-9a-fA-F]{0,63})$`)
var rpcHash = regexp.MustCompile(`^0x[0-9a-fA-F]{64}$`)

func rpcString(v json.RawMessage) string { var s string; _ = json.Unmarshal(v, &s); return s }
func rpcBlock(v json.RawMessage) bool {
	s := rpcString(v)
	return s == "latest" || s == "pending" || s == "finalized" || s == "safe" || s == "earliest" || rpcQuantity.MatchString(s)
}

func (a *App) walletRPC(c *gin.Context) {
	if a.ChainRPC == nil {
		fail(c, 503, "rpc_unavailable", "Wallet RPC is unavailable.")
		return
	}
	var in struct {
		Version string            `json:"jsonrpc"`
		ID      json.RawMessage   `json:"id"`
		Method  string            `json:"method"`
		Params  []json.RawMessage `json:"params"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32768)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	var trailing any
	if decoder.Decode(&in) != nil || decoder.Decode(&trailing) != io.EOF || in.Version != "2.0" || len(in.ID) == 0 || len(in.ID) > 128 || string(in.ID) == "null" {
		fail(c, 400, "invalid_rpc", "Send one JSON-RPC request with an ID.")
		return
	}
	var id any
	if json.Unmarshal(in.ID, &id) != nil {
		fail(c, 400, "invalid_rpc", "Invalid request ID.")
		return
	}
	switch id.(type) {
	case string, float64:
	default:
		fail(c, 400, "invalid_rpc", "Invalid request ID.")
		return
	}
	if !a.walletRPCAllowed(in.Method, in.Params, currentUser(c).Wallet) {
		c.JSON(200, gin.H{"jsonrpc": "2.0", "id": in.ID, "error": gin.H{"code": -32602, "message": "RPC method or parameters are not allowed."}})
		return
	}
	allowed, err := a.allow(c, "wallet-rpc:"+currentUser(c).ID, 600)
	if err != nil {
		fail(c, 503, "limiter_unavailable", "Wallet RPC is temporarily unavailable.")
		return
	}
	if !allowed {
		c.Header("Retry-After", "60")
		fail(c, 429, "rpc_rate_limited", "Too many wallet RPC requests.")
		return
	}
	if in.Method == "eth_sendRawTransaction" {
		allowed, err = a.allow(c, "wallet-rpc-send:"+currentUser(c).ID, 30)
		if err != nil {
			fail(c, 503, "limiter_unavailable", "Wallet RPC is temporarily unavailable.")
			return
		}
		if !allowed {
			c.Header("Retry-After", "60")
			fail(c, 429, "rpc_rate_limited", "Too many wallet transaction broadcasts.")
			return
		}
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	result, rpcError, err := a.ChainRPC.Request(ctx, in.Method, in.Params)
	if err != nil {
		fail(c, 503, "rpc_unavailable", "Wallet RPC is temporarily unavailable. Retry shortly.")
		return
	}
	if rpcError != nil {
		c.JSON(200, gin.H{"jsonrpc": "2.0", "id": in.ID, "error": rpcError})
		return
	}
	c.JSON(200, gin.H{"jsonrpc": "2.0", "id": in.ID, "result": result})
}

func (a *App) walletRPCAllowed(method string, p []json.RawMessage, wallet string) bool {
	contract := func(address string) bool {
		return security.WalletPattern.MatchString(address) && (strings.EqualFold(address, a.Config.Payments.Token) || strings.EqualFold(address, a.Config.Payments.Contract) || a.Config.Reserve.Enabled && strings.EqualFold(address, a.Config.Reserve.Contract))
	}
	switch method {
	case "eth_chainId", "eth_blockNumber", "eth_gasPrice", "eth_maxPriorityFeePerGas":
		return len(p) == 0
	case "eth_getBalance", "eth_getTransactionCount":
		return len(p) == 2 && strings.EqualFold(rpcString(p[0]), wallet) && rpcBlock(p[1])
	case "eth_getCode":
		return len(p) == 2 && contract(rpcString(p[0])) && rpcBlock(p[1])
	case "eth_getBlockByNumber":
		return len(p) == 2 && rpcBlock(p[0]) && string(p[1]) == "false"
	case "eth_getTransactionReceipt", "eth_getTransactionByHash":
		return len(p) == 1 && rpcHash.MatchString(rpcString(p[0]))
	case "eth_sendRawTransaction":
		return len(p) == 1 && len(rpcString(p[0])) >= 4 && len(rpcString(p[0])) <= 16386 && rpcHex.MatchString(rpcString(p[0]))
	case "eth_feeHistory":
		if len(p) != 3 || !rpcQuantity.MatchString(rpcString(p[0])) || !rpcBlock(p[1]) {
			return false
		}
		n, err := strconv.ParseUint(strings.TrimPrefix(rpcString(p[0]), "0x"), 16, 64)
		var rewards []float64
		if err != nil || n < 1 || n > 20 || json.Unmarshal(p[2], &rewards) != nil || len(rewards) > 10 {
			return false
		}
		for i, v := range rewards {
			if v < 0 || v > 100 || i > 0 && v < rewards[i-1] {
				return false
			}
		}
		return true
	case "eth_call", "eth_estimateGas":
		if len(p) < 1 || len(p) > 2 || method == "eth_call" && len(p) != 2 || len(p) == 2 && !rpcBlock(p[1]) {
			return false
		}
		var tx map[string]json.RawMessage
		if json.Unmarshal(p[0], &tx) != nil || !contract(rpcString(tx["to"])) {
			return false
		}
		for key, value := range tx {
			s := rpcString(value)
			switch key {
			case "to":
			case "from":
				if !strings.EqualFold(s, wallet) {
					return false
				}
			case "data", "input":
				if len(s) > 8194 || !rpcHex.MatchString(s) {
					return false
				}
			case "value":
				if s != "0x0" {
					return false
				}
			case "gas", "gasPrice", "maxFeePerGas", "maxPriorityFeePerGas", "nonce", "type":
				if !rpcQuantity.MatchString(s) {
					return false
				}
			default:
				return false
			}
		}
		return true
	}
	return false
}
