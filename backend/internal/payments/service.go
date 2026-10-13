package payments

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"golang.org/x/sync/singleflight"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
	"tokenswoop/backend/internal/config"
	"tokenswoop/backend/internal/security"
	"tokenswoop/backend/internal/store"
)

type Bill struct {
	ID                    string     `json:"id"`
	UserID                string     `json:"user_id"`
	Wallet                string     `json:"wallet"`
	ChainID               int64      `json:"chain_id"`
	Contract              string     `json:"contract"`
	Token                 string     `json:"token"`
	Provider              string     `json:"provider"`
	Recipient             string     `json:"recipient"`
	Amount                string     `json:"amount"`
	Units                 string     `json:"amount_units"`
	State                 string     `json:"state"`
	Error                 string     `json:"error_code"`
	TxHash                *string    `json:"tx_hash"`
	Block                 *string    `json:"block_number"`
	BlockHash             *string    `json:"block_hash"`
	CreatedAt             time.Time  `json:"created_at"`
	PaidAt                *time.Time `json:"paid_at"`
	InsufficientBalanceAt *time.Time `json:"insufficient_balance_at"`
}

const billColumns = `id,user_id::text,wallet,chain_id,contract,token,provider,recipient,amount::text,amount_units::text,state,error_code,tx_hash,block_number,block_hash,created_at,paid_at,insufficient_balance_at`

func scanBill(row pgx.Row) (b Bill, e error) {
	e = row.Scan(&b.ID, &b.UserID, &b.Wallet, &b.ChainID, &b.Contract, &b.Token, &b.Provider, &b.Recipient, &b.Amount, &b.Units, &b.State, &b.Error, &b.TxHash, &b.Block, &b.BlockHash, &b.CreatedAt, &b.PaidAt, &b.InsufficientBalanceAt)
	return
}
func (b Bill) matches(p config.Payments) bool {
	return b.ChainID == p.NetworkID() && b.Contract == p.Contract && b.Token == p.Token && b.Provider == p.Provider && b.recipientAddress() == p.RecipientAddress()
}

func (b Bill) recipientAddress() string {
	if b.Recipient != "" {
		return b.Recipient
	}
	return b.Provider
}

type Service struct {
	Store     *store.Store
	Config    config.Payments
	Chain     Chain
	secret    []byte
	refreshes singleflight.Group
}

func New(s *store.Store, p config.Payments, chain Chain, secret []byte) *Service {
	key := sha256.Sum256(append(append([]byte{}, secret...), []byte(":payment-transaction-v1")...))
	service := &Service{Store: s, Config: p, Chain: chain, secret: key[:]}
	if p.Enabled {
		s.WalletPolicy = &store.WalletPolicy{CheckAllowance: true, ChainID: p.NetworkID(), Contract: p.Contract, Token: p.Token, Provider: p.Provider}
	}
	return service
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
	if !force {
		var v Snapshot
		e := s.Store.DB.QueryRow(ctx, `SELECT balance::text,allowance::text,block_number FROM payment_accounts WHERE user_id=$1 AND chain_id=$5 AND contract=$2 AND token=$3 AND provider=$4 AND checked_at>now()-interval '2 seconds'`, userID, s.Config.Contract, s.Config.Token, s.Config.Provider, s.Config.NetworkID()).Scan(&v.Balance, &v.Allowance, &v.Block)
		if e == nil {
			return v, nil
		}
		if e != pgx.ErrNoRows {
			return v, e
		}
	}
	user, e := s.Store.User(ctx, userID)
	if e != nil {
		return Snapshot{}, e
	}
	checked := time.Now()
	v, e := s.Chain.Snapshot(ctx, user.Wallet)
	if e != nil {
		return v, e
	}
	_, e = s.Store.DB.Exec(ctx, `INSERT INTO payment_accounts(user_id,chain_id,contract,token,provider,balance,allowance,block_number,checked_at) VALUES($1,$9,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(user_id) DO UPDATE SET chain_id=excluded.chain_id,contract=excluded.contract,token=excluded.token,provider=excluded.provider,balance=excluded.balance,allowance=excluded.allowance,block_number=excluded.block_number,checked_at=excluded.checked_at WHERE payment_accounts.checked_at<=excluded.checked_at`, userID, s.Config.Contract, s.Config.Token, s.Config.Provider, v.Balance, v.Allowance, v.Block, checked, s.Config.NetworkID())
	return v, e
}

// BuildBill serializes with request completion on the account row. The one-open-
// bill constraint prevents allocating a request's unpaid portion twice. A final
// fractional micro-unit stays on its request until enough actual usage accrues.
func (s *Service) BuildBill(ctx context.Context, userID string) (string, error) {
	return s.buildBill(ctx, userID, false)
}

func (s *Service) buildBill(ctx context.Context, userID string, onlyDue bool) (string, error) {
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return "", e
	}
	defer tx.Rollback(ctx)
	var wallet string
	if e = tx.QueryRow(ctx, `SELECT wallet FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&wallet); e != nil {
		return "", e
	}
	var existing string
	e = tx.QueryRow(ctx, `SELECT id FROM bills WHERE user_id=$1 AND state<>'paid'`, userID).Scan(&existing)
	if e == nil {
		return existing, nil
	}
	if e != pgx.ErrNoRows {
		return "", e
	}
	var threshold string
	var deadline time.Time
	if e = tx.QueryRow(ctx, `SELECT t.settlement_threshold::text,now()-$2*interval '1 second' FROM users u JOIN tiers t ON t.id=u.tier WHERE u.id=$1`, userID, SettlementWindow.Seconds()).Scan(&threshold, &deadline); e != nil {
		return "", e
	}
	rows, e := tx.Query(ctx, `SELECT id::text,unpaid_amount::text,COALESCE(completed_at,updated_at) FROM requests WHERE user_id=$1 AND state='completed' AND unpaid_amount>0 ORDER BY created_at,id FOR UPDATE`, userID)
	if e != nil {
		return "", e
	}
	type item struct {
		id     string
		amount decimal.Decimal
	}
	items := []item{}
	total := decimal.Zero
	aged := false
	for rows.Next() {
		var id, amount string
		var completed time.Time
		if e = rows.Scan(&id, &amount, &completed); e != nil {
			rows.Close()
			return "", e
		}
		n, err := decimal.NewFromString(amount)
		if err != nil {
			rows.Close()
			return "", err
		}
		items = append(items, item{id, n})
		total = total.Add(n)
		aged = aged || !completed.After(deadline)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return "", e
	}
	if onlyDue && !aged && total.LessThan(decimal.RequireFromString(threshold)) {
		return "", nil
	}
	amount := total.Truncate(6)
	if !amount.IsPositive() {
		return "", nil
	}
	raw := make([]byte, 32)
	if _, e = rand.Read(raw); e != nil {
		return "", e
	}
	id := "0x" + hex.EncodeToString(raw)
	p := s.Config
	// This snapshot predates publication of the unpredictable bill ID, making
	// it a safe lower bound for finding an externally submitted payment.
	from := "0"
	var anchor string
	if err := tx.QueryRow(ctx, `SELECT block_number FROM payment_accounts WHERE user_id=$1 AND chain_id=$2 AND contract=$3`, userID, p.NetworkID(), p.Contract).Scan(&anchor); err == nil {
		if n, err := integer(anchor); err == nil {
			from = n.String()
		}
	} else if err != pgx.ErrNoRows {
		return "", err
	}
	_, e = tx.Exec(ctx, `INSERT INTO bills(id,user_id,wallet,chain_id,contract,token,provider,amount,amount_units,reconcile_from,recipient) VALUES($1,$2,$3,$9,$4,$5,$6,$7,$8,$10,$11)`, id, userID, wallet, p.Contract, p.Token, p.Provider, amount.String(), amount.Shift(6).String(), p.NetworkID(), from, p.RecipientAddress())
	if e != nil {
		return "", e
	}
	remaining := amount
	for _, it := range items {
		part := decimal.Min(it.amount, remaining)
		if !part.IsPositive() {
			break
		}
		if _, e = tx.Exec(ctx, `INSERT INTO bill_items(bill_id,request_id,amount) VALUES($1,$2,$3)`, id, it.id, part.String()); e != nil {
			return "", e
		}
		remaining = remaining.Sub(part)
	}
	if e = tx.Commit(ctx); e != nil {
		return "", e
	}
	return id, nil
}
func (s *Service) Bill(ctx context.Context, userID, id string) (Bill, error) {
	return scanBill(s.Store.DB.QueryRow(ctx, `SELECT `+billColumns+` FROM bills WHERE id=$1 AND user_id=$2`, id, userID))
}
func (s *Service) Retry(ctx context.Context, userID, id string) error {
	// Rechecking a blocked bill never erases the failure or changes its ID.
	tag, e := s.Store.DB.Exec(ctx, `UPDATE bills SET next_attempt_at=now() WHERE id=$1 AND user_id=$2 AND state IN('queued','blocked')`, id, userID)
	if e != nil {
		return e
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("bill is paid, confirming, under review, or unavailable")
	}
	return nil
}
func (s *Service) deferBill(ctx context.Context, b Bill, state, code string, seconds int) error {
	if code == "insufficient_balance" || code == "insufficient_allowance" {
		tx, err := s.Store.DB.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		// Serialize the failure with admission, without holding a user lock
		// during any chain I/O. Already accepted requests can still finish.
		if _, err = tx.Exec(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, b.UserID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE bills SET state=$2,error_code=$3,insufficient_balance_at=COALESCE(insufficient_balance_at,now()),next_attempt_at=now()+$4*interval '1 second' WHERE id=$1 AND state<>'paid'`, b.ID, state, code, seconds); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	_, e := s.Store.DB.Exec(ctx, `UPDATE bills SET state=$2,error_code=$3,next_attempt_at=now()+$4*interval '1 second' WHERE id=$1 AND state<>'paid'`, b.ID, state, code, seconds)
	return e
}

// Tick serializes the provider signing lane using a database transaction lock.
// Signed bytes are committed before sending. Unknown broadcast outcomes always
// reuse that nonce. Only finalized evidence of no payment permits a new nonce.
func (s *Service) Tick(ctx context.Context) error {
	if _, e := s.QueueDueBills(ctx); e != nil {
		return e
	}
	return s.SettleNext(ctx)
}

// SettleNext owns the signing lane only. Queuing due bills must not wait for a
// previous chain transaction or a reserve approval to finish confirming.
func (s *Service) SettleNext(ctx context.Context) error {
	lock, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer lock.Rollback(context.Background())
	var acquired bool
	if e = lock.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, store.PlatformSigningLock).Scan(&acquired); e != nil {
		return e
	}
	if !acquired {
		return nil
	}
	if e = s.Chain.Validate(ctx); e != nil {
		return e
	}
	if _, e = s.Store.DB.Exec(ctx, `INSERT INTO worker_heartbeats(id) VALUES('settlement') ON CONFLICT(id) DO UPDATE SET last_seen=now()`); e != nil {
		return e
	}
	// A previous signed payment always takes precedence over a fresh nonce.
	b, e := scanBill(s.Store.DB.QueryRow(ctx, `SELECT `+billColumns+` FROM bills WHERE state='submitted' ORDER BY created_at LIMIT 1`))
	if e == nil {
		return s.process(ctx, b)
	}
	if e != pgx.ErrNoRows {
		return e
	}
	// Check every journal for this signer, including Mera transactions whose
	// broadcast outcome is unknown or whose receipt needs review.
	reservePending, e := s.Store.SignerPending(ctx, s.Config.NetworkID(), s.Config.Provider)
	if e != nil {
		return e
	}
	if reservePending {
		return nil
	}
	b, e = scanBill(s.Store.DB.QueryRow(ctx, `SELECT `+billColumns+` FROM bills WHERE (state IN('queued','blocked') OR state='review' AND error_code='external_payment_requires_review') AND next_attempt_at<=now() ORDER BY next_attempt_at,created_at LIMIT 1`))
	if e == pgx.ErrNoRows {
		return nil
	}
	if e != nil {
		return e
	}
	return s.process(ctx, b)
}
func (s *Service) process(ctx context.Context, b Bill) error {
	if !b.matches(s.Config) {
		return s.deferBill(ctx, b, "review", "configuration_changed", 60)
	}
	var hash, cipher string
	var nonce string
	var created time.Time
	e := s.Store.DB.QueryRow(ctx, `SELECT tx_hash,raw_cipher,nonce::text,created_at FROM payment_attempts WHERE bill_id=$1 AND state='pending'`, b.ID).Scan(&hash, &cipher, &nonce, &created)
	if e != nil && e != pgx.ErrNoRows {
		return e
	}
	paid, err := s.Chain.IsCharged(ctx, b)
	if err != nil {
		return err
	}
	if paid {
		// Prefer durable hashes over log scans; a long outage can span millions
		// of blocks while the saved receipt still identifies the exact payment.
		if hash != "" {
			hashes := []string{hash}
			rows, err := s.Store.DB.Query(ctx, `SELECT tx_hash FROM chain_tx_replacements WHERE root_hash=$1 ORDER BY created_at`, hash)
			if err != nil {
				return err
			}
			for rows.Next() {
				var h string
				if err = rows.Scan(&h); err != nil {
					rows.Close()
					return err
				}
				hashes = append(hashes, h)
			}
			rows.Close()
			if err = rows.Err(); err != nil {
				return err
			}
			for _, h := range hashes {
				c, err := s.Chain.Confirm(ctx, b, h)
				if err != nil {
					return err
				}
				if c.Finalized && c.Success {
					return s.applyPaid(ctx, b, h, c)
				}
			}
		}
		if recovery, ok := s.Chain.(interface {
			FindPayment(context.Context, Bill, string) (string, Confirmation, string, error)
		}); ok {
			var from string
			if err = s.Store.DB.QueryRow(ctx, `SELECT reconcile_from::text FROM bills WHERE id=$1`, b.ID).Scan(&from); err != nil {
				return err
			}
			h, c, next, err := recovery.FindPayment(ctx, b, from)
			if err != nil {
				return err
			}
			if c.Finalized && c.Success {
				return s.applyPaid(ctx, b, h, c)
			}
			if _, err = s.Store.DB.Exec(ctx, `UPDATE bills SET reconcile_from=$2 WHERE id=$1 AND state<>'paid'`, b.ID, next); err != nil {
				return err
			}
		}
		if hash != "" {
			return nil
		}
		return s.deferBill(ctx, b, "review", "external_payment_requires_review", 5)
	}
	if e == pgx.ErrNoRows {
		if b.State == "review" {
			// An unfinalized charged flag can disappear after a reorg. Retry the
			// original ID: contract-level replay protection still applies.
			return s.deferBill(ctx, b, "queued", "", 0)
		}
		v, err := s.Refresh(ctx, b.UserID, true)
		if err != nil {
			return err
		}
		need := decimal.RequireFromString(b.Units)
		if decimal.RequireFromString(v.Allowance).LessThan(need) {
			return s.deferBill(ctx, b, "blocked", "insufficient_allowance", 30)
		}
		if decimal.RequireFromString(v.Balance).LessThan(need) {
			return s.deferBill(ctx, b, "blocked", "insufficient_balance", 30)
		}
		signed, err := s.Chain.Sign(ctx, b)
		if err != nil {
			_ = s.deferBill(ctx, b, "queued", "signer_unavailable", 30)
			return err
		}
		cipher, err = security.Encrypt(s.secret, signed.Raw)
		if err != nil {
			return err
		}
		tx, err := s.Store.DB.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if _, err = tx.Exec(ctx, `INSERT INTO payment_attempts(id,bill_id,tx_hash,chain_id,provider,nonce,raw_cipher) VALUES($1,$2,$3,$4,$5,$6,$7)`, uuid.NewString(), b.ID, signed.Hash, b.ChainID, b.Provider, signed.Nonce, cipher); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE bills SET state='submitted',error_code='',tx_hash=$2 WHERE id=$1 AND state IN('queued','blocked')`, b.ID, signed.Hash); err != nil {
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		hash, nonce, created = signed.Hash, signed.Nonce, time.Now()
	} else if e != nil {
		return e
	}
	winning, confirmation, e := RecoverTransaction(ctx, s.Store, s.secret, s.Chain, TransactionJournal{hash, cipher, created}, func(h string) (Confirmation, error) { return s.Chain.Confirm(ctx, b, h) })
	if e != nil {
		return e
	}
	if !confirmation.Finalized && time.Since(created) >= 45*time.Second {
		if recovery, ok := s.Chain.(interface {
			ConsumedUncharged(context.Context, Bill, string) (Confirmation, error)
		}); ok {
			confirmation, e = recovery.ConsumedUncharged(ctx, b, nonce)
			if e != nil {
				return e
			}
		}
	}
	if confirmation.Finalized {
		if confirmation.Success {
			return s.applyPaid(ctx, b, winning, confirmation)
		}
		// A balance can change after signing. A finalized revert alone is
		// not proof of insufficient funds; check fresh chain evidence.
		fundingFailure := ""
		if v, err := s.Refresh(ctx, b.UserID, true); err == nil {
			need := decimal.RequireFromString(b.Units)
			if decimal.RequireFromString(v.Allowance).LessThan(need) {
				fundingFailure = "insufficient_allowance"
			} else if decimal.RequireFromString(v.Balance).LessThan(need) {
				fundingFailure = "insufficient_balance"
			}
		}
		tx, e := s.Store.DB.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		if _, e = tx.Exec(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, b.UserID); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE payment_attempts SET state='reverted',resolved_tx_hash=NULLIF($2,'') WHERE tx_hash=$1 AND state='pending'`, hash, winning); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE bills SET state='blocked',error_code=CASE WHEN $2<>'' THEN $2 ELSE 'transaction_reverted' END,insufficient_balance_at=CASE WHEN $2<>'' THEN COALESCE(insufficient_balance_at,now()) ELSE insufficient_balance_at END,next_attempt_at=now()+interval '30 seconds' WHERE id=$1`, b.ID, fundingFailure); e != nil {
			return e
		}
		return tx.Commit(ctx)
	}
	return nil
}
func (s *Service) applyPaid(ctx context.Context, b Bill, hash string, c Confirmation) error {
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, b.UserID); e != nil {
		return e
	}
	var state string
	if e = tx.QueryRow(ctx, `SELECT state FROM bills WHERE id=$1 FOR UPDATE`, b.ID).Scan(&state); e != nil {
		return e
	}
	if state == "paid" {
		return nil
	}
	var count int64
	var sum string
	if e = tx.QueryRow(ctx, `SELECT count(*),sum(amount)::text FROM bill_items WHERE bill_id=$1`, b.ID).Scan(&count, &sum); e != nil {
		return e
	}
	if !decimal.RequireFromString(sum).Equal(decimal.RequireFromString(b.Amount)) {
		return fmt.Errorf("bill allocation mismatch")
	}
	tag, e := tx.Exec(ctx, `UPDATE requests r SET unpaid_amount=r.unpaid_amount-i.amount,wallet_paid_amount=r.wallet_paid_amount+i.amount FROM bill_items i WHERE i.bill_id=$1 AND i.request_id=r.id AND r.user_id=$2 AND r.state='completed' AND r.unpaid_amount>=i.amount`, b.ID, b.UserID)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != count {
		return fmt.Errorf("bill request balance mismatch")
	}
	// The per-request ledger is authoritative. Repair a stale aggregate inside
	// the same transaction as applying this exact, finalized bill once.
	var previous, remaining string
	if e = tx.QueryRow(ctx, `SELECT unpaid_balance::text,(SELECT COALESCE(sum(unpaid_amount),0)::text FROM requests WHERE user_id=$1) FROM users WHERE id=$1`, b.UserID).Scan(&previous, &remaining); e != nil {
		return e
	}
	if !decimal.RequireFromString(previous).Sub(decimal.RequireFromString(b.Amount)).Equal(decimal.RequireFromString(remaining)) {
		if _, e = tx.Exec(ctx, `INSERT INTO audit_events(id,actor_id,action,target,details) VALUES($1,$2,'bill.balance_reconciled',$3,jsonb_build_object('previous_unpaid',$4::text,'remaining_unpaid',$5::text))`, uuid.NewString(), b.UserID, b.ID, previous, remaining); e != nil {
			return e
		}
	}
	tag, e = tx.Exec(ctx, `UPDATE users SET unpaid_balance=$2 WHERE id=$1`, b.UserID, remaining)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("bill account balance mismatch")
	}
	if _, e = tx.Exec(ctx, `UPDATE bills SET state='paid',error_code='',tx_hash=$2,block_number=$3,block_hash=$4,paid_at=now() WHERE id=$1`, b.ID, hash, c.Block, c.BlockHash); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE payment_attempts SET state='paid',resolved_tx_hash=$1 WHERE bill_id=$2 AND state='pending'`, hash, b.ID); e != nil {
		return e
	}
	// Refresh the wallet display after transfer. Admission depends on the
	// failed bill being paid, never on the age or amount of this snapshot.
	if _, e = tx.Exec(ctx, `DELETE FROM payment_accounts WHERE user_id=$1`, b.UserID); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_events(id,actor_id,action,target,details) VALUES($1,$2,'bill.paid',$3,jsonb_build_object('tx_hash',$4::text,'amount',$5::text))`, uuid.NewString(), b.UserID, b.ID, hash, b.Amount); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
