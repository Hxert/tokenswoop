package payments

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"tokenswoop/backend/internal/security"
	"tokenswoop/backend/internal/store"
)

type TransactionJournal struct {
	Hash, Cipher string
	Created      time.Time
}
type replacementChain interface {
	Replace(context.Context, string) (Signed, error)
}
type broadcaster interface {
	Broadcast(context.Context, string) error
}

// RecoverTransaction checks every saved hash before rebroadcasting. A fee bump
// changes only gasPrice: the nonce, destination, value and calldata are fixed.
// The caller holds the shared signing-lane lock through this operation.
func RecoverTransaction(ctx context.Context, db *store.Store, secret []byte, chain broadcaster, root TransactionJournal, confirm func(string) (Confirmation, error)) (string, Confirmation, error) {
	journals := []TransactionJournal{root}
	rows, err := db.DB.Query(ctx, `SELECT tx_hash,raw_cipher,created_at FROM chain_tx_replacements WHERE root_hash=$1 ORDER BY created_at,tx_hash`, root.Hash)
	if err != nil {
		return "", Confirmation{}, err
	}
	for rows.Next() {
		var j TransactionJournal
		if err = rows.Scan(&j.Hash, &j.Cipher, &j.Created); err != nil {
			rows.Close()
			return "", Confirmation{}, err
		}
		journals = append(journals, j)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return "", Confirmation{}, err
	}
	for _, j := range journals {
		c, e := confirm(j.Hash)
		if e != nil {
			return "", c, e
		}
		if c.Finalized {
			return j.Hash, c, nil
		}
	}
	latest := journals[len(journals)-1]
	raw, err := security.Decrypt(secret, latest.Cipher)
	if err != nil {
		return "", Confirmation{}, fmt.Errorf("transaction journal could not be decrypted")
	}
	bytes, err := hex.DecodeString(strings.TrimPrefix(raw, "0x"))
	if err != nil || len(bytes) == 0 || Keccak(bytes) != latest.Hash {
		return "", Confirmation{}, fmt.Errorf("transaction journal hash mismatch")
	}
	if replacer, ok := chain.(replacementChain); ok && time.Since(latest.Created) >= 45*time.Second && len(journals) <= 3 {
		signed, e := replacer.Replace(ctx, raw)
		if e == nil {
			encrypted, e := security.Encrypt(secret, signed.Raw)
			if e != nil {
				return "", Confirmation{}, e
			}
			tx, e := db.DB.Begin(ctx)
			if e != nil {
				return "", Confirmation{}, e
			}
			defer tx.Rollback(ctx)
			if _, e = tx.Exec(ctx, `INSERT INTO chain_tx_replacements(root_hash,tx_hash,raw_cipher) VALUES($1,$2,$3)`, root.Hash, signed.Hash, encrypted); e != nil {
				return "", Confirmation{}, e
			}
			if _, e = tx.Exec(ctx, `UPDATE bills b SET tx_hash=$2 FROM payment_attempts p WHERE p.bill_id=b.id AND p.tx_hash=$1 AND b.state='submitted'`, root.Hash, signed.Hash); e != nil {
				return "", Confirmation{}, e
			}
			if _, e = tx.Exec(ctx, `INSERT INTO audit_events(id,action,target,details) VALUES($1,'chain.fee_bumped',$2,jsonb_build_object('previous_hash',$3::text,'tx_hash',$4::text,'nonce',$5::text))`, uuid.NewString(), root.Hash, latest.Hash, signed.Hash, signed.Nonce); e != nil {
				return "", Confirmation{}, e
			}
			if e = tx.Commit(ctx); e != nil {
				return "", Confirmation{}, e
			}
			raw = signed.Raw
		}
		// A temporary signer/fee failure must not discard the recoverable bytes.
		if e != nil {
			slog.Warn("transaction fee bump deferred", "root_hash", root.Hash, "error", e)
		}
	}
	if len(journals) > 3 && time.Since(latest.Created) >= 45*time.Second {
		slog.Warn("transaction still pending after bounded fee bumps", "root_hash", root.Hash, "age_seconds", int(time.Since(root.Created).Seconds()))
	}
	if err = chain.Broadcast(ctx, raw); err != nil {
		slog.Warn("transaction broadcast will retry", "root_hash", root.Hash, "error", err)
	}
	return "", Confirmation{}, nil
}

func (r *RPCChain) Replace(ctx context.Context, raw string) (Signed, error) {
	var out Signed
	if err := r.chain(ctx); err != nil {
		return out, err
	}
	decoded, err := r.cast(ctx, "decode-transaction", raw, "--json")
	if err != nil {
		return out, err
	}
	// Foundry versions either emit the object directly or wrap it as JSON data.
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal([]byte(decoded), &envelope) == nil && len(envelope.Data) > 0 {
		var inner string
		if json.Unmarshal(envelope.Data, &inner) == nil {
			decoded = inner
		} else {
			decoded = string(envelope.Data)
		}
	}
	var tx struct{ Signer, Type, ChainID, Nonce, GasPrice, Gas, To, Value, Input string }
	if json.Unmarshal([]byte(decoded), &tx) != nil {
		return out, fmt.Errorf("cannot decode saved transaction")
	}
	cid, e := integer(tx.ChainID)
	if e != nil || cid.Cmp(big.NewInt(r.Config.NetworkID())) != 0 || tx.Type != "0x0" || tx.Value != "0x0" || !strings.EqualFold(tx.Signer, r.Config.Provider) || !hexData.MatchString(tx.Input) || len(tx.Input) < 10 {
		return out, fmt.Errorf("saved transaction scope mismatch")
	}
	nonce, e := integer(tx.Nonce)
	if e != nil {
		return out, e
	}
	gas, e := integer(tx.Gas)
	if e != nil {
		return out, e
	}
	price, e := integer(tx.GasPrice)
	if e != nil {
		return out, e
	}
	var count, market string
	if e = r.RPC(ctx, "eth_getTransactionCount", []any{r.Config.Provider, "latest"}, &count); e != nil {
		return out, e
	}
	n, e := integer(count)
	if e != nil || n.Cmp(nonce) > 0 {
		return out, fmt.Errorf("saved nonce already consumed; awaiting reconciliation")
	}
	if e = r.RPC(ctx, "eth_gasPrice", []any{}, &market); e != nil {
		return out, e
	}
	current, e := integer(market)
	if e != nil {
		return out, e
	}
	price.Mul(price, big.NewInt(125))
	price.Div(price, big.NewInt(100))
	price.Add(price, big.NewInt(1))
	if price.Cmp(current) < 0 {
		price.Set(current)
	}
	if gas.Sign() <= 0 || new(big.Int).Mul(gas, price).Cmp(big.NewInt(50_000_000_000_000_000)) >= 0 {
		return out, fmt.Errorf("replacement transaction gas cap exceeded")
	}
	signed, e := r.cast(ctx, "mktx", tx.To, tx.Input, "--chain", fmt.Sprint(r.Config.NetworkID()), "--legacy", "--nonce", nonce.String(), "--gas-limit", gas.String(), "--gas-price", price.String(), "--keystore", r.Config.Keystore, "--password-file", r.Config.PasswordFile, "--from", r.Config.Provider)
	if e != nil {
		return out, e
	}
	bytes, e := hex.DecodeString(strings.TrimPrefix(signed, "0x"))
	if e != nil || len(bytes) < 100 {
		return out, fmt.Errorf("invalid replacement signature")
	}
	return Signed{signed, Keccak(bytes), nonce.String()}, nil
}

// FindPayment scans a bounded finalized range and returns only exact receipt
// evidence. A boolean isCharged flag alone never marks a database bill paid.
func (r *RPCChain) FindPayment(ctx context.Context, b Bill, from string) (string, Confirmation, string, error) {
	start, ok := new(big.Int).SetString(from, 10)
	if !ok || start.Sign() < 0 {
		return "", Confirmation{}, from, fmt.Errorf("invalid reconciliation cursor")
	}
	var final *block
	if err := r.RPC(ctx, "eth_getBlockByNumber", []any{"finalized", false}, &final); err != nil {
		return "", Confirmation{}, from, err
	}
	if final == nil {
		return "", Confirmation{}, from, fmt.Errorf("finality unavailable")
	}
	end, err := integer(final.Number)
	if err != nil {
		return "", Confirmation{}, from, err
	}
	if start.Cmp(end) > 0 {
		return "", Confirmation{}, from, nil
	}
	limit := new(big.Int).Add(start, big.NewInt(999))
	if end.Cmp(limit) > 0 {
		end = limit
	}
	var logs []struct {
		Hash string `json:"transactionHash"`
	}
	filter := map[string]any{"address": b.Contract, "fromBlock": "0x" + start.Text(16), "toBlock": "0x" + end.Text(16), "topics": []any{[]string{Keccak([]byte("BatchCharged(address,address,bytes32,uint256)")), Keccak([]byte("Charged(address,address,bytes32,uint256)"))}, "0x" + word(b.Wallet), "0x" + word(b.Provider), b.ID}}
	if err = r.RPC(ctx, "eth_getLogs", []any{filter}, &logs); err != nil {
		return "", Confirmation{}, from, err
	}
	for _, l := range logs {
		c, e := r.Confirm(ctx, b, l.Hash)
		if e != nil {
			return "", c, from, e
		}
		if c.Finalized && c.Success {
			return l.Hash, c, from, nil
		}
	}
	return "", Confirmation{}, end.Add(end, big.NewInt(1)).String(), nil
}

// A finalized consumed nonce with an uncharged bill can be retried under the
// same bill ID. This never treats a missing receipt alone as a failed transfer.
func (r *RPCChain) ConsumedUncharged(ctx context.Context, b Bill, nonce string) (Confirmation, error) {
	var final *block
	if e := r.RPC(ctx, "eth_getBlockByNumber", []any{"finalized", false}, &final); e != nil {
		return Confirmation{}, e
	}
	if final == nil {
		return Confirmation{}, fmt.Errorf("finality unavailable")
	}
	var count string
	if e := r.RPC(ctx, "eth_getTransactionCount", []any{b.Provider, final.Number}, &count); e != nil {
		return Confirmation{}, e
	}
	n, e := integer(count)
	saved, ok := new(big.Int).SetString(nonce, 10)
	if e != nil || !ok {
		return Confirmation{}, fmt.Errorf("invalid nonce evidence")
	}
	if n.Cmp(saved) <= 0 {
		return Confirmation{}, nil
	}
	charged, e := r.call(ctx, b.Contract, calldata("isCharged(address,bytes32)", b.Wallet, b.ID), final.Number)
	if e != nil {
		return Confirmation{}, e
	}
	if charged != "0x"+word("0") {
		return Confirmation{}, nil
	}
	return Confirmation{Finalized: true, Success: false, Block: final.Number, BlockHash: final.Hash}, nil
}
