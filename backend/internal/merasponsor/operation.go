package merasponsor

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math/big"
	"regexp"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"tokenswoop/backend/internal/payments"
)

type Authorization struct {
	Address string `json:"address"`
	ChainID int64  `json:"chainId"`
	Nonce   uint64 `json:"nonce"`
	R       string `json:"r"`
	S       string `json:"s"`
	YParity uint8  `json:"yParity"`
}
type Operation struct {
	ID                 string `json:"id"`
	Wallet             string `json:"wallet"`
	ChainID            int64  `json:"chain_id"`
	Implementation     string `json:"implementation"`
	Action             string `json:"action"`
	Amount             string `json:"amount_units"`
	OperationID        string `json:"operation_id"`
	Deadline           int64  `json:"deadline"`
	AuthorizationNonce *int64 `json:"authorization_nonce"`
	State              string `json:"state"`
	Hash               string `json:"hash"`
	Error              string `json:"error_code"`
}

var hex32 = regexp.MustCompile(`^0x[0-9a-fA-F]{64}$`)

func Action(action, amount string) (uint8, error) {
	code, ok := map[string]uint8{"payment_approve": 0, "reserve_approve": 1, "reserve_deposit": 2, "reserve_withdraw": 3}[action]
	n, valid := new(big.Int).SetString(amount, 10)
	if !ok || !valid || n.String() != amount || n.Sign() < 0 || n.BitLen() > 256 || code >= 2 && n.Sign() == 0 || code == 0 && n.Cmp(big.NewInt(100_000_000)) > 0 {
		return 0, fmt.Errorf("invalid operation")
	}
	return code, nil
}
func raw(hexValue string) []byte {
	b, _ := hex.DecodeString(strings.TrimPrefix(hexValue, "0x"))
	return b
}
func hash(b []byte) []byte         { return raw(payments.Keccak(b)) }
func word(b []byte) []byte         { return append(make([]byte, 32-len(b)), b...) }
func number(n int64) []byte        { return word(big.NewInt(n).Bytes()) }
func join(values ...[]byte) []byte { return bytes.Join(values, nil) }
func (o Operation) Digest() []byte {
	action, _ := Action(o.Action, o.Amount)
	n, _ := new(big.Int).SetString(o.Amount, 10)
	domain := hash(join(hash([]byte("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)")), hash([]byte("TokenSwoop Mera")), hash([]byte("1")), number(o.ChainID), word(raw(o.Wallet))))
	body := hash(join(hash([]byte("WalletOperation(uint8 action,uint256 amount,bytes32 id,uint256 deadline,address implementation)")), number(int64(action)), word(n.Bytes()), raw(o.OperationID), number(o.Deadline), word(raw(o.Implementation))))
	return hash(join([]byte{0x19, 0x01}, domain, body))
}
func verify(wallet string, digest, signature []byte) bool {
	if len(signature) != 65 || len(digest) != 32 {
		return false
	}
	v := signature[64]
	if v >= 27 {
		v -= 27
	}
	if v > 1 {
		return false
	}
	half, _ := new(big.Int).SetString("7fffffffffffffffffffffffffffffff5d576e7357a4501ddfe92f46681b20a0", 16)
	if new(big.Int).SetBytes(signature[32:64]).Cmp(half) > 0 {
		return false
	}
	pub, _, err := ecdsa.RecoverCompact(append([]byte{27 + v}, signature[:64]...), digest)
	return err == nil && strings.EqualFold(wallet, "0x"+hex.EncodeToString(hash(pub.SerializeUncompressed()[1:])[12:]))
}
func (o Operation) Verify(signature string, auth *Authorization) bool {
	if _, err := Action(o.Action, o.Amount); err != nil {
		return false
	}
	if len(raw(o.Wallet)) != 20 || len(raw(o.Implementation)) != 20 || !hex32.MatchString(o.OperationID) || o.ChainID <= 0 || o.Deadline <= 0 {
		return false
	}
	if !verify(o.Wallet, o.Digest(), raw(signature)) {
		return false
	}
	if o.AuthorizationNonce == nil {
		return auth == nil
	}
	if auth == nil || auth.ChainID != o.ChainID || auth.Nonce != uint64(*o.AuthorizationNonce) || !strings.EqualFold(auth.Address, o.Implementation) || !hex32.MatchString(auth.R) || !hex32.MatchString(auth.S) || auth.YParity > 1 {
		return false
	}
	digest := hash(append([]byte{5}, rlpList(rlp(big.NewInt(auth.ChainID).Bytes()), rlp(raw(auth.Address)), rlp(new(big.Int).SetUint64(auth.Nonce).Bytes()))...))
	return verify(o.Wallet, digest, join(raw(auth.R), raw(auth.S), []byte{auth.YParity}))
}
func (o Operation) Calldata(signature string) string {
	action, _ := Action(o.Action, o.Amount)
	n, _ := new(big.Int).SetString(o.Amount, 10)
	sig := raw(signature)
	padding := make([]byte, (32-len(sig)%32)%32)
	return "0x" + hex.EncodeToString(join(hash([]byte("execute(uint8,uint256,bytes32,uint256,bytes)"))[:4], number(int64(action)), word(n.Bytes()), raw(o.OperationID), number(o.Deadline), number(160), number(int64(len(sig))), sig, padding))
}
func rlp(b []byte) []byte {
	if len(b) == 1 && b[0] < 128 {
		return b
	}
	return rlpEnvelope(b, 128, 183)
}
func rlpList(items ...[]byte) []byte { return rlpEnvelope(join(items...), 192, 247) }
func rlpEnvelope(b []byte, short, long byte) []byte {
	if len(b) <= 55 {
		return append([]byte{short + byte(len(b))}, b...)
	}
	size := big.NewInt(int64(len(b))).Bytes()
	return join([]byte{long + byte(len(size))}, size, b)
}
func (a Authorization) Encoded() string {
	return "0x" + hex.EncodeToString(rlpList(rlp(big.NewInt(a.ChainID).Bytes()), rlp(raw(a.Address)), rlp(new(big.Int).SetUint64(a.Nonce).Bytes()), rlp(big.NewInt(int64(a.YParity)).Bytes()), rlp(new(big.Int).SetBytes(raw(a.R)).Bytes()), rlp(new(big.Int).SetBytes(raw(a.S)).Bytes())))
}
func (a Authorization) RPC() map[string]any {
	return map[string]any{"chainId": fmt.Sprintf("0x%x", a.ChainID), "address": a.Address, "nonce": fmt.Sprintf("0x%x", a.Nonce), "r": a.R, "s": a.S, "yParity": fmt.Sprintf("0x%x", a.YParity)}
}
