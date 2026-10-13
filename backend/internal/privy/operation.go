package privy

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
)

type Contracts struct{ Token, Payment, Reserve string }
type Operation struct {
	Action string `json:"action"`
	Amount string `json:"amount_units"`
}
type Call struct {
	To    string `json:"to"`
	Data  string `json:"data"`
	Value string `json:"value"`
}

var amountPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,77})$`)

// Construct calldata on the server; never sponsor arbitrary client-supplied calls,
// ETH transfers, gas overrides, authorization lists, or unlimited payment allowances.
func (o Operation) Call(contracts Contracts) (Call, error) {
	bad := errors.New("invalid wallet operation")
	if !amountPattern.MatchString(o.Amount) {
		return Call{}, bad
	}
	n, ok := new(big.Int).SetString(o.Amount, 10)
	if !ok || n.Sign() < 0 || n.BitLen() > 256 {
		return Call{}, bad
	}
	word := strings.Repeat("0", 64-len(n.Text(16))) + n.Text(16)
	call := Call{Value: "0x0"}
	switch o.Action {
	case "payment_approve", "reserve_approve":
		spender := contracts.Payment
		if o.Action == "reserve_approve" {
			spender = contracts.Reserve
		}
		if !addressPattern.MatchString(spender) || !addressPattern.MatchString(contracts.Token) {
			return Call{}, bad
		}
		if o.Action == "payment_approve" && n.Cmp(big.NewInt(100_000_000)) > 0 {
			return Call{}, bad
		}
		call.To = contracts.Token
		call.Data = "0x095ea7b3" + strings.Repeat("0", 24) + strings.ToLower(spender[2:]) + word
	case "reserve_deposit", "reserve_withdraw":
		if n.Sign() == 0 || !addressPattern.MatchString(contracts.Reserve) {
			return Call{}, bad
		}
		call.To = contracts.Reserve
		call.Data = map[string]string{"reserve_deposit": "0xb6b55f25", "reserve_withdraw": "0x2e1a7d4d"}[o.Action] + word
	default:
		return Call{}, bad
	}
	call.To = strings.ToLower(call.To)
	return call, nil
}
func (c Call) Payload(reference string, network ...int64) []byte {
	chain := int64(10143)
	if len(network) > 0 {
		chain = network[0]
	}
	b, _ := json.Marshal(map[string]any{"method": "eth_sendTransaction", "chain_type": "ethereum", "caip2": fmt.Sprintf("eip155:%d", chain), "sponsor": true, "reference_id": reference, "params": map[string]any{"transaction": c}})
	return b
}
