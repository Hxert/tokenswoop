package reserve

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"tokenswoop/backend/internal/payments"
	"tokenswoop/backend/internal/security"
	"tokenswoop/backend/internal/store"
)

const debtReportColumns = `id::text,user_id::text,wallet,chain_id,contract,operator,epoch::text,items,calldata,tx_hash,raw_cipher`

func scanDebtReport(row pgx.Row) (a DebtReport, err error) {
	err = row.Scan(&a.ID, &a.UserID, &a.Wallet, &a.ChainID, &a.Contract, &a.Operator, &a.Epoch, &a.Items, &a.Data, &a.Hash, &a.Cipher)
	return
}

func (s *Service) tickDebt(ctx context.Context) error {
	chain, ok := s.Chain.(DebtChain)
	if !ok {
		return fmt.Errorf("reserve chain does not support debt reporting")
	}
	lock, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer lock.Rollback(context.Background())
	var acquired bool
	if err = lock.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, store.PlatformSigningLock).Scan(&acquired); err != nil || !acquired {
		return err
	}
	if err = s.Chain.Validate(ctx); err != nil {
		return err
	}
	if _, err = s.Store.DB.Exec(ctx, `INSERT INTO worker_heartbeats(id) VALUES('reserve') ON CONFLICT(id) DO UPDATE SET last_seen=now()`); err != nil {
		return err
	}
	a, err := scanDebtReport(s.Store.DB.QueryRow(ctx, `SELECT `+debtReportColumns+` FROM reserve_debt_reports WHERE state='pending' ORDER BY created_at LIMIT 1`))
	if err == nil {
		return s.processDebt(ctx, chain, a)
	}
	if err != pgx.ErrNoRows {
		return err
	}
	signingBusy, err := s.Store.SignerPending(ctx, s.Config.NetworkID(), s.Config.Operator)
	if err != nil {
		return err
	}
	if signingBusy {
		return nil
	}
	rows, err := s.Store.DB.Query(ctx, `SELECT u.id::text FROM users u LEFT JOIN reserve_accounts r ON r.user_id=u.id AND r.chain_id=$1 AND r.contract=$2
 ORDER BY (EXISTS(SELECT 1 FROM bills WHERE user_id=u.id AND state<>'paid' AND insufficient_balance_at IS NOT NULL)
 OR EXISTS(SELECT 1 FROM reserve_debt_items WHERE user_id=u.id AND amount_units>0)) DESC,
 r.checked_at ASC NULLS FIRST,u.created_at LIMIT 8`, s.Config.NetworkID(), s.Config.Contract)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		v, err := s.Refresh(ctx, id, true)
		if err != nil {
			return err
		}
		signed, err := s.reportDebt(ctx, chain, id, v)
		if err != nil {
			return err
		}
		if signed {
			return nil
		}
	}
	return nil
}

func (s *Service) reportDebt(ctx context.Context, chain DebtChain, user string, v Snapshot) (bool, error) {
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var wallet string
	if err = tx.QueryRow(ctx, `SELECT wallet FROM users WHERE id=$1 FOR UPDATE`, user).Scan(&wallet); err != nil {
		return false, err
	}
	var failed bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bills WHERE user_id=$1 AND state<>'paid' AND insufficient_balance_at IS NOT NULL)`, user).Scan(&failed); err != nil {
		return false, err
	}
	// New reports require actual collection failure and existing collateral.
	// Paid requests reduce their own prior report; manager-cleared epochs are
	// excluded, so old debt cannot be automatically frozen again.
	rows, err := tx.Query(ctx, `SELECT r.id::text,trunc(r.unpaid_amount*1000000)::text
 FROM requests r LEFT JOIN reserve_debt_items d ON d.user_id=r.user_id AND d.request_id=r.id AND d.chain_id=$2 AND d.contract=$3
 WHERE r.user_id=$1 AND r.state='completed' AND (
 (d.request_id IS NULL AND $4 AND r.unpaid_amount>=0.000001)
 OR (d.epoch=$5 AND d.amount_units<>trunc(r.unpaid_amount*1000000)))
 ORDER BY r.created_at,r.id LIMIT 64`, user, s.Config.NetworkID(), s.Config.Contract, failed && v.Principal != "0", v.DebtEpoch)
	if err != nil {
		return false, err
	}
	items := []DebtItem{}
	for rows.Next() {
		var it DebtItem
		if err = rows.Scan(&it.RequestID, &it.Amount); err != nil {
			rows.Close()
			return false, err
		}
		it.ID = payments.Keccak([]byte("tokenswoop:request:" + it.RequestID))
		items = append(items, it)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return false, err
	}
	if len(items) == 0 {
		return false, nil
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	a := DebtReport{ID: uuid.NewString(), UserID: user, Wallet: wallet, ChainID: s.Config.NetworkID(), Contract: s.Config.Contract, Operator: s.Config.Operator, Epoch: v.DebtEpoch, Items: items}
	a.Data, err = debtData(wallet, a.Epoch, items)
	if err != nil {
		return false, err
	}
	signed, err := chain.SignDebt(ctx, a)
	if err != nil {
		return false, err
	}
	a.Hash = signed.Hash
	a.Cipher, err = security.Encrypt(s.secret, signed.Raw)
	if err != nil {
		return false, err
	}
	payload, _ := json.Marshal(items)
	if _, err = tx.Exec(ctx, `INSERT INTO reserve_debt_reports(id,user_id,wallet,chain_id,contract,operator,epoch,items,calldata,tx_hash,nonce,raw_cipher) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, a.ID, user, wallet, a.ChainID, a.Contract, a.Operator, a.Epoch, payload, a.Data, a.Hash, signed.Nonce, a.Cipher); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(id,actor_id,action,target,details) VALUES($1,$2,'reserve.debt_signed',$3,jsonb_build_object('tx_hash',$4::text,'items',$5::jsonb,'epoch',$6::text))`, uuid.NewString(), user, a.ID, a.Hash, payload, a.Epoch); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, s.processDebt(ctx, chain, a)
}

func (s *Service) processDebt(ctx context.Context, chain DebtChain, a DebtReport) error {
	expected, err := debtData(a.Wallet, a.Epoch, a.Items)
	if err != nil || a.ChainID != s.Config.NetworkID() || a.Contract != s.Config.Contract || a.Operator != s.Config.Operator || expected != a.Data {
		return fmt.Errorf("pending debt report scope mismatch")
	}
	var created time.Time
	if err = s.Store.DB.QueryRow(ctx, `SELECT created_at FROM reserve_debt_reports WHERE id=$1`, a.ID).Scan(&created); err != nil {
		return err
	}
	winning, proof, err := payments.RecoverTransaction(ctx, s.Store, s.secret, s.Chain, payments.TransactionJournal{Hash: a.Hash, Cipher: a.Cipher, Created: created}, func(hash string) (payments.Confirmation, error) {
		copy := a
		copy.Hash = hash
		return chain.ConfirmDebt(ctx, copy)
	})
	if err != nil {
		return err
	}
	if proof.Finalized {
		// A manager release may win the race against an already signed report.
		// Remember those request IDs in their old epoch even when the transaction
		// reverts, otherwise the next tick could report them again in the new epoch.
		cleared := false
		if !proof.Success {
			v, err := s.Chain.Snapshot(ctx, a.Wallet)
			if err != nil {
				return err
			}
			current, currentOK := new(big.Int).SetString(v.DebtEpoch, 10)
			reported, reportedOK := new(big.Int).SetString(a.Epoch, 10)
			if !currentOK || !reportedOK {
				return fmt.Errorf("invalid debt epoch")
			}
			cleared = current.Cmp(reported) > 0
		}
		tx, err := s.Store.DB.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if _, err = tx.Exec(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, a.UserID); err != nil {
			return err
		}
		state := "reverted"
		if proof.Success {
			state = "confirmed"
		}
		if proof.Success || cleared {
			for _, it := range a.Items {
				amount := it.Amount
				if cleared {
					amount = "0"
				}
				if _, err = tx.Exec(ctx, `INSERT INTO reserve_debt_items(user_id,request_id,chain_id,contract,debt_id,epoch,amount_units) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(user_id,chain_id,contract,request_id) DO UPDATE SET amount_units=excluded.amount_units WHERE reserve_debt_items.epoch=excluded.epoch`, a.UserID, it.RequestID, a.ChainID, a.Contract, it.ID, a.Epoch, amount); err != nil {
					return err
				}
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE reserve_debt_reports SET state=$2,block_number=$3,block_hash=$4,resolved_tx_hash=$5 WHERE id=$1 AND state='pending'`, a.ID, state, proof.Block, proof.BlockHash, winning); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO audit_events(id,actor_id,action,target,details) VALUES($1,$2,$3,$4,jsonb_build_object('tx_hash',$5::text,'block_number',$6::text,'block_hash',$7::text,'manager_cleared',$8::boolean))`, uuid.NewString(), a.UserID, "reserve.debt_"+state, a.ID, winning, proof.Block, proof.BlockHash, cleared); err != nil {
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		_, err = s.Refresh(ctx, a.UserID, true)
		return err
	}
	return nil
}
