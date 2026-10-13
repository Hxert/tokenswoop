// Package testutil provisions isolated database schemas for integration tests.
package testutil

import (
	"context"
	"encoding/hex"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"golang.org/x/crypto/sha3"
	"os"
	"strings"
	"testing"
	"tokenswoop/backend/internal/security"
	"tokenswoop/backend/internal/store"
)

func Database(t *testing.T) *store.Store {
	t.Helper()
	s := EmptyDatabase(t)
	if e := s.Migrate(context.Background()); e != nil {
		t.Fatal(e)
	}
	return s
}

func EmptyDatabase(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	root, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	schema := "test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, e = root.Exec(ctx, "CREATE SCHEMA "+schema); e != nil {
		root.Close()
		t.Fatal(e)
	}
	cfg, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		t.Fatal(e)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	s := &store.Store{DB: db}
	t.Cleanup(func() { db.Close(); root.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); root.Close() })
	return s
}
func Account(t *testing.T, s *store.Store, walletBalance string) string {
	t.Helper()
	id := uuid.NewString()
	_, e := s.DB.Exec(context.Background(), "INSERT INTO users(id,wallet) VALUES($1,$2)", id, "0x"+strings.ReplaceAll(id, "-", "")+"00000000")
	if e != nil {
		t.Fatal(e)
	}
	if decimal.RequireFromString(walletBalance).IsPositive() {
		FundWallet(t, s, id, walletBalance)
	}
	return id
}

// PendingMera records a transaction signed before an ambiguous broadcast. This
// must reserve its sponsor's nonce even if the RPC mempool has not seen it yet.
func PendingMera(t *testing.T, s *store.Store, user string, chainID int64, sponsor, state string) string {
	t.Helper()
	id := uuid.NewString()
	_, err := s.DB.Exec(context.Background(), `INSERT INTO mera_transactions
 (id,user_id,wallet,chain_id,implementation,sponsor,action,amount_units,operation_id,deadline,state,raw_tx,tx_hash,sponsor_nonce)
 SELECT $1::uuid,id,wallet,$3,'0x1111111111111111111111111111111111111111',$4,'payment_approve',0,$1::uuid::text,9999999999,$5,'0x1234','0xabc',5 FROM users WHERE id=$2`, id, user, chainID, sponsor, state)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// FundWallet seeds a confirmed wallet snapshot in an isolated test schema.
// It enables settlement without issuing tokens. Snapshots are display/worker
// evidence, not admission limits. Payment tests use their Chain fixtures instead.
func FundWallet(t *testing.T, s *store.Store, userID, balance string) {
	t.Helper()
	if s.WalletPolicy == nil {
		s.WalletPolicy = &store.WalletPolicy{
			ChainID: 10143, Contract: "0x1111111111111111111111111111111111111111",
			Token:    "0x2222222222222222222222222222222222222222",
			Provider: "0x3333333333333333333333333333333333333333",
		}
	}
	p := s.WalletPolicy
	units := decimal.RequireFromString(balance).Shift(6)
	if units.IsNegative() || !units.Equal(units.Truncate(0)) {
		t.Fatal("test wallet balance must have at most 6 decimal places")
	}
	_, err := s.DB.Exec(context.Background(), `INSERT INTO payment_accounts(user_id,chain_id,contract,token,provider,balance,allowance,block_number)
	 VALUES($1,$2,$3,$4,$5,$6,$6,'0x1') ON CONFLICT(user_id) DO UPDATE SET chain_id=EXCLUDED.chain_id,contract=EXCLUDED.contract,token=EXCLUDED.token,provider=EXCLUDED.provider,balance=EXCLUDED.balance,allowance=EXCLUDED.allowance,checked_at=now()`, userID, p.ChainID, p.Contract, p.Token, p.Provider, units.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(context.Background(), `INSERT INTO worker_heartbeats(id,last_seen) VALUES('settlement',now()) ON CONFLICT(id) DO UPDATE SET last_seen=now()`); err != nil {
		t.Fatal(err)
	}
}
func Model() store.Model {
	return store.Model{ID: "test-model", Name: "Fixture model", Vendor: "Fixture", Enabled: true, Tools: true, StructuredOutput: true, InputPrice: "1", OutputPrice: "2", CacheReadPrice: "0.1", CacheWritePrice: "1.25", CacheWrite1hPrice: "2"}
}
func Wallet(t *testing.T) (*secp256k1.PrivateKey, string) {
	t.Helper()
	key, e := secp256k1.GeneratePrivateKey()
	if e != nil {
		t.Fatal(e)
	}
	h := sha3.NewLegacyKeccak256()
	h.Write(key.PubKey().SerializeUncompressed()[1:])
	return key, "0x" + hex.EncodeToString(h.Sum(nil)[12:])
}
func Sign(key *secp256k1.PrivateKey, message string) string {
	sig := ecdsa.SignCompact(key, security.PersonalHash(message), false)
	return "0x" + hex.EncodeToString(append(sig[1:], sig[0]))
}
