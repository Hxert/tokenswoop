package faucet

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"tokenswoop/backend/internal/config"
	"tokenswoop/backend/internal/payments"
	"tokenswoop/backend/internal/security"
	"tokenswoop/backend/internal/store"
)

const Amount = "10000000"

var ErrDisabled = errors.New("testnet faucet unavailable")
var ErrCooldown = errors.New("wallet faucet cooldown")
var ErrQuota = errors.New("faucet daily limit reached")
var ErrEmpty = errors.New("faucet balance unavailable")

type Claim struct {
	ID        string    `json:"id"`
	Wallet    string    `json:"wallet"`
	ChainID   int64     `json:"chain_id"`
	Token     string    `json:"token"`
	Sponsor   string    `json:"-"`
	Amount    string    `json:"amount_units"`
	State     string    `json:"state"`
	Hash      string    `json:"hash"`
	Error     string    `json:"error_code"`
	CreatedAt time.Time `json:"created_at"`
}

const columns = `id::text,wallet,chain_id,token,sponsor,amount_units::text,state,COALESCE(tx_hash,''),error_code,created_at`

func scan(row pgx.Row) (o Claim, err error) {
	err = row.Scan(&o.ID, &o.Wallet, &o.ChainID, &o.Token, &o.Sponsor, &o.Amount, &o.State, &o.Hash, &o.Error, &o.CreatedAt)
	return
}

type Chain interface {
	Sign(context.Context, Claim) (payments.Signed, error)
	Broadcast(context.Context, string) error
	Confirm(context.Context, Claim) (string, error)
}
type Service struct {
	Store   *store.Store
	Config  config.Faucet
	Payment config.Payments
	Secret  []byte
	Verify  Verifier
	Chain   Chain
}

func (s *Service) Enabled() bool {
	return s != nil && s.Config.Enabled && s.Payment.Enabled && s.Payment.NetworkID() == 10143 && !strings.EqualFold(s.Payment.Token, config.MonadUSDC)
}
func (s *Service) Get(ctx context.Context, id, user string) (Claim, error) {
	return scan(s.Store.DB.QueryRow(ctx, `SELECT `+columns+` FROM testnet_faucet_claims WHERE id=$1 AND user_id=$2`, id, user))
}

type Status struct {
	Enabled  bool       `json:"enabled"`
	SiteKey  string     `json:"site_key"`
	Amount   string     `json:"amount_units"`
	Cooldown int64      `json:"cooldown_seconds"`
	Next     *time.Time `json:"next_claim_at"`
	Claim    *Claim     `json:"claim"`
}

func (s *Service) Status(ctx context.Context, user string) (Status, error) {
	r := Status{Enabled: s.Enabled(), Amount: Amount, Cooldown: int64(s.Config.Cooldown.Seconds()), SiteKey: s.Config.SiteKey}
	if !r.Enabled {
		return r, nil
	}
	o, e := scan(s.Store.DB.QueryRow(ctx, `SELECT `+columns+` FROM testnet_faucet_claims WHERE user_id=$1 ORDER BY created_at DESC LIMIT 1`, user))
	if errors.Is(e, pgx.ErrNoRows) {
		return r, nil
	}
	if e != nil {
		return r, e
	}
	r.Claim = &o
	var next *time.Time
	e = s.Store.DB.QueryRow(ctx, `SELECT max(created_at)+($2::bigint*interval '1 second') FROM testnet_faucet_claims WHERE user_id=$1 AND state<>'failed'`, user, int64(s.Config.Cooldown.Seconds())).Scan(&next)
	r.Next = next
	return r, e
}
func (s *Service) Claim(ctx context.Context, id, user, wallet, token, ip string) (Claim, error) {
	if !s.Enabled() {
		return Claim{}, ErrDisabled
	}
	if _, err := uuid.Parse(id); err != nil {
		return Claim{}, ErrVerification
	}
	old, err := s.Get(ctx, id, user)
	if err == nil {
		return old, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Claim{}, err
	}
	if s.Verify == nil {
		return Claim{}, ErrVerificationUnavailable
	}
	if err = s.Verify.Verify(ctx, token, ip, id); err != nil {
		return Claim{}, err
	}
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return Claim{}, err
	}
	defer tx.Rollback(ctx)
	// Serialize claims only, not chain operations. Caps remain atomic across all API instances.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(78016357)`); err != nil {
		return Claim{}, err
	}
	if o, e := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM testnet_faucet_claims WHERE id=$1 AND user_id=$2`, id, user)); e == nil {
		return o, nil
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return Claim{}, e
	}
	var blocked bool
	wallet = strings.ToLower(wallet)
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM testnet_faucet_claims WHERE wallet=$1 AND (state IN ('queued','submitted','review') OR state='confirmed' AND created_at>now()-($2::bigint*interval '1 second')))`, wallet, int64(s.Config.Cooldown.Seconds())).Scan(&blocked); err != nil {
		return Claim{}, err
	}
	if blocked {
		return Claim{}, ErrCooldown
	}
	ipDigest := security.Digest(s.Secret, "faucet-ip", ip)
	var ipCount, total int
	if err = tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE ip_digest=$1),count(*) FROM testnet_faucet_claims WHERE created_at>now()-interval '24 hours' AND state<>'failed'`, ipDigest).Scan(&ipCount, &total); err != nil {
		return Claim{}, err
	}
	if ipCount >= s.Config.IPDailyLimit || total >= s.Config.DailyLimit {
		return Claim{}, ErrQuota
	}
	o, err := scan(tx.QueryRow(ctx, `INSERT INTO testnet_faucet_claims(id,user_id,wallet,chain_id,token,sponsor,ip_digest,token_digest) VALUES($1,$2,$3,10143,$4,$5,$6,$7) RETURNING `+columns, id, user, wallet, s.Payment.Token, s.Payment.Provider, ipDigest, security.Digest(s.Secret, "faucet-turnstile", token)))
	if err != nil {
		var pgerr *pgconn.PgError
		if errors.As(err, &pgerr) && pgerr.Code == "23505" {
			return Claim{}, ErrVerification
		}
		return Claim{}, err
	}
	if err = tx.Commit(ctx); err == nil {
		slog.Info("testnet faucet claim queued", "claim_id", id, "wallet", wallet, "amount_units", Amount)
	}
	return o, err
}
func (s *Service) Tick(ctx context.Context) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	lock, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer lock.Rollback(ctx)
	var acquired bool
	if err = lock.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, store.PlatformSigningLock).Scan(&acquired); err != nil || !acquired {
		return err
	}
	if _, err = s.Store.DB.Exec(ctx, `INSERT INTO worker_heartbeats(id) VALUES('faucet') ON CONFLICT(id) DO UPDATE SET last_seen=now()`); err != nil {
		return err
	}
	o, err := scan(s.Store.DB.QueryRow(ctx, `SELECT `+columns+` FROM testnet_faucet_claims WHERE state IN('queued','submitted','review') ORDER BY CASE state WHEN 'review' THEN 0 WHEN 'submitted' THEN 1 ELSE 2 END,created_at LIMIT 1`))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if o.State == "review" {
		return nil
	}
	if o.ChainID != 10143 || o.Token != s.Payment.Token || o.Sponsor != s.Payment.Provider || o.Amount != Amount {
		return fmt.Errorf("faucet claim configuration mismatch")
	}
	var raw string
	var broadcast *time.Time
	if err = s.Store.DB.QueryRow(ctx, `SELECT COALESCE(raw_tx,''),broadcast_at FROM testnet_faucet_claims WHERE id=$1`, o.ID).Scan(&raw, &broadcast); err != nil {
		return err
	}
	if o.State == "queued" {
		if time.Since(o.CreatedAt) > time.Hour {
			_, err = s.Store.DB.Exec(ctx, `UPDATE testnet_faucet_claims SET state='failed',error_code='claim_expired',completed_at=now(),updated_at=now() WHERE id=$1`, o.ID)
			return err
		}
		pending, e := s.Store.SignerPending(ctx, 10143, s.Payment.Provider)
		if e != nil {
			return e
		}
		if pending {
			return nil
		}
		signed, e := s.Chain.Sign(ctx, o)
		if e != nil {
			code := "signer_unavailable"
			if errors.Is(e, ErrEmpty) {
				code = "faucet_empty"
			}
			_, _ = s.Store.DB.Exec(ctx, `UPDATE testnet_faucet_claims SET error_code=$2,updated_at=now() WHERE id=$1`, o.ID, code)
			return e
		}
		if signed.Raw == "" || signed.Hash == "" {
			return fmt.Errorf("empty signed faucet transaction")
		}
		if _, err = s.Store.DB.Exec(ctx, `UPDATE testnet_faucet_claims SET raw_tx=$2,tx_hash=$3,sponsor_nonce=$4,state='submitted',error_code='',updated_at=now() WHERE id=$1 AND state='queued'`, o.ID, signed.Raw, signed.Hash, signed.Nonce); err != nil {
			return err
		}
		raw, o.Hash, o.State = signed.Raw, signed.Hash, "submitted"
	}
	state, err := s.Chain.Confirm(ctx, o)
	if err != nil {
		return err
	}
	if state != "" {
		if state != "confirmed" && state != "failed" && state != "review" {
			return fmt.Errorf("invalid faucet confirmation state")
		}
		_, err = s.Store.DB.Exec(ctx, `UPDATE testnet_faucet_claims SET state=$2,error_code=$3,completed_at=now(),updated_at=now() WHERE id=$1`, o.ID, state, map[string]string{"failed": "transaction_reverted", "review": "receipt_requires_review"}[state])
		if err == nil {
			slog.Info("testnet faucet claim completed", "claim_id", o.ID, "state", state, "tx_hash", o.Hash)
		}
		return err
	}
	if broadcast != nil && time.Since(*broadcast) < 5*time.Second {
		return nil
	}
	// Only the original durable signed bytes may be rebroadcast after a crash or timeout.
	if err = s.Chain.Broadcast(ctx, raw); err != nil {
		return err
	}
	_, err = s.Store.DB.Exec(ctx, `UPDATE testnet_faucet_claims SET broadcast_at=now(),updated_at=now() WHERE id=$1`, o.ID)
	return err
}
