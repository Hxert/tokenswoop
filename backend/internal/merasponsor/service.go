package merasponsor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"tokenswoop/backend/internal/config"
	"tokenswoop/backend/internal/payments"
	"tokenswoop/backend/internal/store"
)

type Service struct {
	Store   *store.Store
	Config  config.Mera
	ChainID int64
	Chain   Chain
}

var ErrPending = errors.New("finish the pending Mera operation first")
var ErrQuota = errors.New("Mera sponsorship daily limit reached")
var ErrInvalid = errors.New("invalid or expired Mera authorization")
var ErrSimulation = errors.New("Mera operation cannot execute")
var ErrInsufficientBalance = errors.New("insufficient wallet balance")

const columns = `id::text,wallet,chain_id,implementation,action,amount_units::text,operation_id,deadline,authorization_nonce,state,COALESCE(tx_hash,''),error_code`

func scan(row pgx.Row) (o Operation, err error) {
	err = row.Scan(&o.ID, &o.Wallet, &o.ChainID, &o.Implementation, &o.Action, &o.Amount, &o.OperationID, &o.Deadline, &o.AuthorizationNonce, &o.State, &o.Hash, &o.Error)
	return
}
func (s *Service) Get(ctx context.Context, id, user string) (Operation, error) {
	if err := s.expirePrepared(ctx, user); err != nil {
		return Operation{}, err
	}
	return scan(s.Store.DB.QueryRow(ctx, `SELECT `+columns+` FROM mera_transactions WHERE id=$1 AND user_id=$2 AND chain_id=$3`, id, user, s.ChainID))
}
func (s *Service) Pending(ctx context.Context, user string) (Operation, error) {
	if err := s.expirePrepared(ctx, user); err != nil {
		return Operation{}, err
	}
	return scan(s.Store.DB.QueryRow(ctx, `SELECT `+columns+` FROM mera_transactions WHERE user_id=$1 AND chain_id=$2 AND state IN ('prepared','queued','submitted','review') ORDER BY created_at LIMIT 1`, user, s.ChainID))
}
func (s *Service) expirePrepared(ctx context.Context, user string) error {
	_, err := s.Store.DB.Exec(ctx, `UPDATE mera_transactions SET state='expired',updated_at=now(),completed_at=now() WHERE user_id=$1 AND chain_id=$2 AND state='prepared' AND deadline<$3`, user, s.ChainID, time.Now().Unix())
	return err
}
func (s *Service) Prepare(ctx context.Context, id, user, wallet, action, amount string) (Operation, error) {
	if _, err := Action(action, amount); err != nil {
		return Operation{}, ErrInvalid
	}
	if old, err := s.Get(ctx, id, user); err == nil {
		if old.Action != action || old.Amount != amount || old.Wallet != wallet {
			return Operation{}, ErrInvalid
		}
		return old, nil
	}
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return Operation{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT id FROM users WHERE id=$1 FOR UPDATE", user); err != nil {
		return Operation{}, err
	}
	// A concurrent retry may have created this ID while waiting for the user lock.
	if old, e := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM mera_transactions WHERE id=$1 AND user_id=$2 AND chain_id=$3`, id, user, s.ChainID)); e == nil {
		if old.Action != action || old.Amount != amount || old.Wallet != wallet {
			return Operation{}, ErrInvalid
		}
		return old, nil
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return Operation{}, e
	}
	if _, err = tx.Exec(ctx, `UPDATE mera_transactions SET state='expired',updated_at=now(),completed_at=now() WHERE user_id=$1 AND state='prepared' AND deadline < $2`, user, time.Now().Unix()); err != nil {
		return Operation{}, err
	}
	var pending bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM mera_transactions WHERE user_id=$1 AND state IN ('prepared','queued','submitted','review'))`, user).Scan(&pending); err != nil {
		return Operation{}, err
	}
	if pending {
		return Operation{}, ErrPending
	}
	if err = s.Chain.CheckApproval(ctx, wallet, action, amount); err != nil {
		return Operation{}, err
	}
	nonce, err := s.Chain.Prepare(ctx, wallet)
	if err != nil {
		return Operation{}, err
	}
	o := Operation{ID: id, Wallet: wallet, ChainID: s.ChainID, Implementation: s.Config.Implementation, Action: action, Amount: amount, OperationID: payments.Keccak([]byte(fmt.Sprintf("mera:%d:%s:%s", s.ChainID, wallet, id))), Deadline: time.Now().Unix() + 600, AuthorizationNonce: nonce, State: "prepared"}
	_, err = tx.Exec(ctx, `INSERT INTO mera_transactions(id,user_id,wallet,chain_id,implementation,sponsor,action,amount_units,operation_id,deadline,authorization_nonce) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, o.ID, user, wallet, o.ChainID, o.Implementation, s.Config.Sponsor, action, amount, o.OperationID, o.Deadline, nonce)
	if err == nil {
		err = tx.Commit(ctx)
	}
	return o, err
}
func (s *Service) Submit(ctx context.Context, id, user, signature string, auth *Authorization) (Operation, error) {
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return Operation{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT id FROM users WHERE id=$1 FOR UPDATE", user); err != nil {
		return Operation{}, err
	}
	o, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM mera_transactions WHERE id=$1 AND user_id=$2 AND chain_id=$3 FOR UPDATE`, id, user, s.ChainID))
	if err != nil {
		return o, err
	}
	if o.State != "prepared" {
		return o, nil
	}
	if o.Deadline < time.Now().Unix() || o.Implementation != s.Config.Implementation || !o.Verify(signature, auth) {
		return o, ErrInvalid
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM mera_transactions WHERE user_id=$1 AND state NOT IN('prepared','cancelled') AND created_at>now()-interval '24 hours'`, user).Scan(&count); err != nil {
		return o, err
	}
	if count >= s.Config.DailyTransactions {
		return o, ErrQuota
	}
	encoded, _ := json.Marshal(auth)
	_, err = tx.Exec(ctx, `UPDATE mera_transactions SET signature=$2,authorization_payload=$3,state='queued',updated_at=now() WHERE id=$1`, id, signature, encoded)
	if err == nil {
		err = tx.Commit(ctx)
	}
	o.State = "queued"
	if err == nil {
		slog.Info("Mera gas sponsorship queued", "operation_id", id, "action", o.Action, "wallet", o.Wallet)
	}
	return o, err
}
func (s *Service) Cancel(ctx context.Context, id, user string) error {
	r, err := s.Store.DB.Exec(ctx, `UPDATE mera_transactions SET state='cancelled',completed_at=now(),updated_at=now() WHERE id=$1 AND user_id=$2 AND state='prepared'`, id, user)
	if err == nil && r.RowsAffected() != 1 {
		return ErrPending
	}
	return err
}
func (s *Service) Tick(ctx context.Context) error {
	lock, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer lock.Rollback(ctx)
	var acquired bool
	lockID := store.DedicatedMeraSigningLock
	if s.Config.SharedSigner {
		lockID = store.PlatformSigningLock
	}
	if err = lock.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, lockID).Scan(&acquired); err != nil || !acquired {
		return err
	}
	o, err := scan(s.Store.DB.QueryRow(ctx, `SELECT `+columns+` FROM mera_transactions WHERE chain_id=$1 AND sponsor=$2 AND state IN ('queued','submitted','review') ORDER BY CASE state WHEN 'review' THEN 0 WHEN 'submitted' THEN 1 ELSE 2 END,created_at LIMIT 1`, s.ChainID, s.Config.Sponsor))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if o.State == "review" {
		return nil
	} // Never reuse a nonce after ambiguous receipt evidence.
	if o.Implementation != s.Config.Implementation {
		return fmt.Errorf("pending Mera implementation differs from configuration")
	}
	if o.State == "queued" && o.Deadline < time.Now().Unix() {
		_, err = s.Store.DB.Exec(ctx, `UPDATE mera_transactions SET state='expired',completed_at=now(),updated_at=now() WHERE id=$1 AND state='queued'`, o.ID)
		return err
	}
	var signature, rawTx string
	var authJSON []byte
	var broadcast *time.Time
	if err = s.Store.DB.QueryRow(ctx, `SELECT signature,authorization_payload,COALESCE(raw_tx,''),broadcast_at FROM mera_transactions WHERE id=$1`, o.ID).Scan(&signature, &authJSON, &rawTx, &broadcast); err != nil {
		return err
	}
	if o.State == "queued" {
		// A signed collection/report may not have reached the RPC yet. Its nonce
		// stays reserved until its own recovery process verifies the outcome.
		pending, e := s.Store.SignerPending(ctx, s.ChainID, s.Config.Sponsor)
		if e != nil {
			return e
		}
		if pending {
			return nil
		}
		var auth *Authorization
		if err = json.Unmarshal(authJSON, &auth); err != nil || !o.Verify(signature, auth) {
			return fmt.Errorf("stored Mera authorization invalid")
		}
		signed, e := s.Chain.Sign(ctx, o, signature, auth)
		if e != nil {
			if errors.Is(e, ErrInvalid) || errors.Is(e, ErrSimulation) || errors.Is(e, ErrInsufficientBalance) {
				code := "operation_cannot_execute"
				if errors.Is(e, ErrInsufficientBalance) {
					code = "insufficient_wallet_balance"
				}
				_, err = s.Store.DB.Exec(ctx, `UPDATE mera_transactions SET state='rejected',error_code=$2,completed_at=now(),updated_at=now() WHERE id=$1 AND state='queued'`, o.ID, code)
				return err
			}
			_, _ = s.Store.DB.Exec(ctx, `UPDATE mera_transactions SET error_code='simulation_or_signer_unavailable',updated_at=now() WHERE id=$1`, o.ID)
			return e
		}
		// Reserve the maximum gas liability before persisting signed bytes or broadcasting.
		var budgetOK bool
		if err = s.Store.DB.QueryRow(ctx, `SELECT COALESCE(sum(gas_budget),0)+$1::numeric <= $2::numeric FROM mera_transactions WHERE sponsor=$3 AND created_at>now()-interval '24 hours'`, signed.Budget, s.Config.DailyGasWei, s.Config.Sponsor).Scan(&budgetOK); err != nil {
			return err
		}
		if !budgetOK {
			_, err = s.Store.DB.Exec(ctx, `UPDATE mera_transactions SET error_code='daily_gas_budget',updated_at=now() WHERE id=$1`, o.ID)
			return err
		}
		_, err = s.Store.DB.Exec(ctx, `UPDATE mera_transactions SET raw_tx=$2,tx_hash=$3,sponsor_nonce=$4,gas_budget=$5,state='submitted',error_code='',updated_at=now() WHERE id=$1 AND state='queued'`, o.ID, signed.Raw, signed.Hash, signed.Nonce, signed.Budget)
		if err != nil {
			return err
		}
		rawTx, o.Hash, o.State = signed.Raw, signed.Hash, "submitted"
	}
	state, err := s.Chain.Confirm(ctx, o)
	if state != "" {
		_, e := s.Store.DB.Exec(ctx, `UPDATE mera_transactions SET state=$2,error_code=$3,completed_at=now(),updated_at=now() WHERE id=$1`, o.ID, state, map[string]string{"reverted": "transaction_reverted", "review": "receipt_requires_review"}[state])
		if e != nil {
			return e
		}
		slog.Info("Mera gas sponsorship completed", "operation_id", o.ID, "state", state, "tx_hash", o.Hash)
		return err
	}
	if err != nil {
		return err
	}
	if broadcast != nil && time.Since(*broadcast) < 5*time.Second {
		return nil
	}
	// Re-broadcast only the journaled signed bytes, even after an unknown submission.
	if err = s.Chain.Broadcast(ctx, rawTx); err != nil {
		return err
	}
	_, err = s.Store.DB.Exec(ctx, `UPDATE mera_transactions SET broadcast_at=now(),updated_at=now() WHERE id=$1`, o.ID)
	return err
}
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(1500 * time.Millisecond)
	defer ticker.Stop()
	var previous string
	for {
		run, cancel := context.WithTimeout(ctx, 35*time.Second)
		err := s.Tick(run)
		cancel()
		message := ""
		if err != nil {
			message = err.Error()
		}
		if message != "" && message != previous && ctx.Err() == nil {
			slog.Warn("Mera sponsorship deferred", "error", message)
		}
		previous = message
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
