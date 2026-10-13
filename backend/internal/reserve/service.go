package reserve

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"golang.org/x/sync/singleflight"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
	"tokenswoop/backend/internal/config"
	"tokenswoop/backend/internal/payments"
	"tokenswoop/backend/internal/security"
	"tokenswoop/backend/internal/store"
)

type Service struct {
	Store     *store.Store
	Config    config.Reserve
	Token     string
	Chain     Chain
	secret    []byte
	refreshes singleflight.Group
}

func New(s *store.Store, c config.Reserve, token string, chain Chain, secret []byte) *Service {
	key := sha256.Sum256(append(append([]byte{}, secret...), []byte(":reserve-approval-v1")...))
	s.ReserveContract = c.Contract
	s.ReserveChainID = c.NetworkID()
	s.ReserveDebtMode = c.DebtMode()
	return &Service{Store: s, Config: c, Token: token, Chain: chain, secret: key[:]}
}

func (s *Service) Refresh(ctx context.Context, userID string, force bool) (Snapshot, error) {
	if force {
		return s.refreshSnapshot(ctx, userID, true)
	}
	result := s.refreshes.DoChan(userID, func() (any, error) {
		check, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		return s.refreshSnapshot(check, userID, false)
	})
	select {
	case <-ctx.Done():
		return Snapshot{}, ctx.Err()
	case v := <-result:
		if v.Err != nil {
			return Snapshot{}, v.Err
		}
		return v.Val.(Snapshot), nil
	}
}
func (s *Service) refreshSnapshot(ctx context.Context, userID string, force bool) (Snapshot, error) {
	var v Snapshot
	if !force {
		e := s.Store.DB.QueryRow(ctx, `SELECT snapshot FROM reserve_accounts WHERE user_id=$1 AND chain_id=$4 AND contract=$2 AND token=$3 AND checked_at>now()-interval '8 seconds'`, userID, s.Config.Contract, s.Token, s.Config.NetworkID()).Scan(&v)
		if e == nil {
			return v, nil
		}
		if e != pgx.ErrNoRows {
			return v, e
		}
	}
	u, e := s.Store.User(ctx, userID)
	if e != nil {
		return v, e
	}
	checked := time.Now()
	v, e = s.Chain.Snapshot(ctx, u.Wallet)
	if e != nil {
		return v, e
	}
	block, e := number(v.Block)
	if e != nil {
		return v, e
	}
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return v, e
	}
	defer tx.Rollback(ctx)
	var previousTier string
	if e = tx.QueryRow(ctx, `SELECT tier FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&previousTier); e != nil {
		return v, e
	}
	var old Snapshot
	var contract, token string
	var previousChecked time.Time
	e = tx.QueryRow(ctx, `SELECT snapshot,contract,token,checked_at FROM reserve_accounts WHERE user_id=$1 AND chain_id=$2 AND contract=$3`, userID, s.Config.NetworkID(), s.Config.Contract).Scan(&old, &contract, &token, &previousChecked)
	if e != nil && e != pgx.ErrNoRows {
		return v, e
	}
	if e == nil {
		if contract != s.Config.Contract || token != s.Token {
			return v, fmt.Errorf("reserve account deployment changed; reconcile first")
		}
		before, err := number(old.Block)
		if err != nil {
			return v, err
		}
		if before.Cmp(block) > 0 || previousChecked.After(checked) {
			return old, nil
		}
		if before.Cmp(block) == 0 && old.BlockHash != v.BlockHash {
			return v, fmt.Errorf("finalized reserve block changed")
		}
	}
	payload, _ := json.Marshal(v)
	if _, e = tx.Exec(ctx, `INSERT INTO reserve_accounts(user_id,chain_id,contract,token,principal,withdrawal_pending,snapshot,block_number,block_hash,checked_at) VALUES($1,$10,$2,$3,$4,$5,$6,$7,$8,$9)
 ON CONFLICT(user_id,chain_id,contract) DO UPDATE SET principal=excluded.principal,withdrawal_pending=excluded.withdrawal_pending,snapshot=excluded.snapshot,block_number=excluded.block_number,block_hash=excluded.block_hash,checked_at=excluded.checked_at`, userID, s.Config.Contract, s.Token, v.Principal, v.Withdrawal.Active, payload, block.String(), v.BlockHash, checked, s.Config.NetworkID()); e != nil {
		return v, e
	}
	if _, e = tx.Exec(ctx, `UPDATE users SET tier=(SELECT id FROM tiers WHERE min_principal IS NOT NULL AND min_principal<=$2::numeric/1000000 ORDER BY min_principal DESC LIMIT 1) WHERE id=$1 AND NOT tier_override`, userID, v.Principal); e != nil {
		return v, e
	}
	if old.Principal != v.Principal || old.Withdrawal != v.Withdrawal || old.Debt != v.Debt || old.DebtEpoch != v.DebtEpoch {
		if _, e = tx.Exec(ctx, `INSERT INTO audit_events(id,actor_id,action,target,details) VALUES($1,$2::uuid,'reserve.synced',$2::text,$3)`, uuid.NewString(), userID, payload); e != nil {
			return v, e
		}
	}
	return v, tx.Commit(ctx)
}

type Clearance struct {
	InFlight      int    `json:"in_flight"`
	Unknown       int    `json:"usage_unknown"`
	OpenBills     int    `json:"open_bills"`
	Unpaid        string `json:"unpaid"`
	RequestUnpaid string `json:"request_unpaid"`
}

func clearance(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, user string) (Clearance, error) {
	var c Clearance
	e := db.QueryRow(ctx, `SELECT unpaid_balance::text,
 (SELECT count(*) FROM requests WHERE user_id=u.id AND state IN('accepted','in_flight')),
 (SELECT count(*) FROM requests WHERE user_id=u.id AND state='usage_unknown'),
 (SELECT count(*) FROM bills WHERE user_id=u.id AND state<>'paid'),
 (SELECT COALESCE(sum(unpaid_amount),0)::text FROM requests WHERE user_id=u.id)
 FROM users u WHERE id=$1`, user).Scan(&c.Unpaid, &c.InFlight, &c.Unknown, &c.OpenBills, &c.RequestUnpaid)
	return c, e
}
func (s *Service) Clearance(ctx context.Context, user string) (Clearance, error) {
	return clearance(ctx, s.Store.DB, user)
}

const approvalColumns = `id::text,user_id::text,wallet,contract,operator,request_id::text,amount_units::text,operator_version::text,approved_until,tx_hash,raw_cipher`

func scanApproval(row pgx.Row) (a Approval, e error) {
	e = row.Scan(&a.ID, &a.UserID, &a.Wallet, &a.Contract, &a.Operator, &a.RequestID, &a.Amount, &a.OperatorVersion, &a.Until, &a.Hash, &a.Cipher)
	return
}

// Tick shares the payment signing lock and persistent pending-transaction gate.
// It never accepts wallet-supplied assertions about fee clearance or principal.
func (s *Service) Tick(ctx context.Context) error {
	if s.Config.DebtMode() {
		return s.tickDebt(ctx)
	}
	lock, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer lock.Rollback(context.Background())
	var acquired bool
	if e = lock.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, store.PlatformSigningLock).Scan(&acquired); e != nil || !acquired {
		return e
	}
	if e = s.Chain.Validate(ctx); e != nil {
		return e
	}
	if _, e = s.Store.DB.Exec(ctx, `INSERT INTO worker_heartbeats(id) VALUES('reserve') ON CONFLICT(id) DO UPDATE SET last_seen=now()`); e != nil {
		return e
	}
	a, e := scanApproval(s.Store.DB.QueryRow(ctx, `SELECT `+approvalColumns+` FROM reserve_approvals WHERE state='pending' ORDER BY created_at LIMIT 1`))
	if e == nil {
		return s.process(ctx, a)
	}
	if e != pgx.ErrNoRows {
		return e
	}
	// Round-robin sampling recovers direct contract operations, including when
	// users never reopen the website. Admission independently forces a fresh read.
	rows, e := s.Store.DB.Query(ctx, `SELECT u.id::text FROM users u LEFT JOIN reserve_accounts r ON r.user_id=u.id AND r.chain_id=$1 AND r.contract=$2 ORDER BY r.checked_at ASC NULLS FIRST,u.created_at LIMIT 8`, s.Config.NetworkID(), s.Config.Contract)
	if e != nil {
		return e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return e
	}
	pendingPayments, e := s.Store.SignerPending(ctx, s.Config.NetworkID(), s.Config.Operator)
	if e != nil {
		return e
	}
	var firstError error
	for _, id := range ids {
		v, err := s.Refresh(ctx, id, true)
		if err != nil {
			if firstError == nil {
				firstError = err
			}
			continue
		}
		if pendingPayments || !v.Withdrawal.Active {
			continue
		}
		if v.Withdrawal.OperatorVersion == v.OperatorVersion && v.Withdrawal.ApprovedUntil > time.Now().Unix()+60 {
			continue
		}
		signed, err := s.approve(ctx, id, v)
		if err != nil {
			return err
		}
		if signed {
			return nil
		}
	}
	return firstError
}
func (s *Service) approve(ctx context.Context, userID string, v Snapshot) (bool, error) {
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return false, e
	}
	defer tx.Rollback(ctx)
	var wallet string
	if e = tx.QueryRow(ctx, `SELECT wallet FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&wallet); e != nil {
		return false, e
	}
	var current Snapshot
	if e = tx.QueryRow(ctx, `SELECT snapshot FROM reserve_accounts WHERE user_id=$1 AND contract=$2 AND withdrawal_pending AND checked_at>now()-interval '20 seconds'`, userID, s.Config.Contract).Scan(&current); e == pgx.ErrNoRows {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if current.Withdrawal != v.Withdrawal || current.OperatorVersion != v.OperatorVersion {
		return false, nil
	}
	c, e := clearance(ctx, tx, userID)
	if e != nil {
		return false, e
	}
	if c.InFlight > 0 || c.Unknown > 0 || c.OpenBills > 0 {
		return false, nil
	}
	debt, e := decimal.NewFromString(c.Unpaid)
	if e != nil {
		return false, e
	}
	requestDebt, e := decimal.NewFromString(c.RequestUnpaid)
	if e != nil {
		return false, e
	}
	if !debt.Equal(requestDebt) {
		return false, fmt.Errorf("reserve clearance ledger mismatch")
	}
	if debt.GreaterThanOrEqual(decimal.RequireFromString("0.000001")) {
		return false, nil
	}
	// Never round fees up to token precision. A sub-micro remainder is waived
	// only at a fully drained exit, preserving both the charge and adjustment.
	if debt.IsPositive() {
		if _, e = tx.Exec(ctx, `UPDATE requests SET waived_amount=waived_amount+unpaid_amount,unpaid_amount=0 WHERE user_id=$1 AND state='completed' AND unpaid_amount>0`, userID); e != nil {
			return false, e
		}
		if _, e = tx.Exec(ctx, `UPDATE users SET unpaid_balance=0 WHERE id=$1`, userID); e != nil {
			return false, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO audit_events(id,actor_id,action,target,details) VALUES($1,$2::uuid,'reserve.dust_waived',$2::text,jsonb_build_object('amount',$3::text,'request_id',$4::text))`, uuid.NewString(), userID, debt.String(), v.Withdrawal.ID); e != nil {
			return false, e
		}
	}
	a := Approval{ID: uuid.NewString(), UserID: userID, Wallet: wallet, Contract: s.Config.Contract, Operator: s.Config.Operator, RequestID: v.Withdrawal.ID, Amount: v.Withdrawal.Amount, OperatorVersion: v.OperatorVersion, Until: time.Now().Unix() + 600}
	signed, e := s.Chain.SignApproval(ctx, a)
	if e != nil {
		return false, e
	}
	cipher, e := security.Encrypt(s.secret, signed.Raw)
	if e != nil {
		return false, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO reserve_approvals(id,user_id,wallet,chain_id,contract,operator,request_id,amount_units,operator_version,approved_until,tx_hash,nonce,raw_cipher) VALUES($1,$2,$3,$13,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, a.ID, userID, wallet, a.Contract, a.Operator, a.RequestID, a.Amount, a.OperatorVersion, a.Until, signed.Hash, signed.Nonce, cipher, s.Config.NetworkID()); e != nil {
		return false, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_events(id,actor_id,action,target,details) VALUES($1,$2,'reserve.approval_signed',$3,jsonb_build_object('request_id',$4::text,'amount_units',$5::text,'tx_hash',$6::text,'approved_until',$7::bigint))`, uuid.NewString(), userID, a.ID, a.RequestID, a.Amount, signed.Hash, a.Until); e != nil {
		return false, e
	}
	if e = tx.Commit(ctx); e != nil {
		return false, e
	}
	a.Hash, a.Cipher = signed.Hash, cipher
	return true, s.process(ctx, a)
}
func (s *Service) process(ctx context.Context, a Approval) error {
	if a.Contract != s.Config.Contract || a.Operator != s.Config.Operator {
		return fmt.Errorf("pending reserve approval deployment mismatch")
	}
	var created time.Time
	if e := s.Store.DB.QueryRow(ctx, `SELECT created_at FROM reserve_approvals WHERE id=$1`, a.ID).Scan(&created); e != nil {
		return e
	}
	winning, c, e := payments.RecoverTransaction(ctx, s.Store, s.secret, s.Chain, payments.TransactionJournal{Hash: a.Hash, Cipher: a.Cipher, Created: created}, func(hash string) (payments.Confirmation, error) {
		copy := a
		copy.Hash = hash
		return s.Chain.Confirm(ctx, copy)
	})
	if e != nil {
		return e
	}
	if c.Finalized {
		state := "reverted"
		if c.Success {
			state = "confirmed"
		}
		if _, e = s.Store.DB.Exec(ctx, `UPDATE reserve_approvals SET state=$2,block_number=$3,block_hash=$4,resolved_tx_hash=$5 WHERE id=$1 AND state='pending'`, a.ID, state, c.Block, c.BlockHash, winning); e != nil {
			return e
		}
		_, e = s.Refresh(ctx, a.UserID, true)
		return e
	}
	return nil
}
func stringsTrimHex(v string) string {
	if len(v) > 2 && v[:2] == "0x" {
		return v[2:]
	}
	return "invalid"
}
