package httpapi

import (
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"tokenswoop/backend/internal/privy"
)

var transactionHash = regexp.MustCompile(`^0x[0-9a-fA-F]{64}$`)

func (a *App) sponsoredWallet(c *gin.Context) bool {
	if a.Privy == nil || !a.Config.Privy.GasSponsorship || a.Payments == nil {
		fail(c, 503, "sponsorship_unavailable", "Sponsored wallet transactions are not enabled.")
		return false
	}
	return true
}
func (a *App) prepareWalletTransaction(c *gin.Context) {
	if !a.sponsoredWallet(c) {
		return
	}
	var op privy.Operation
	if c.ShouldBindJSON(&op) != nil {
		fail(c, 400, "invalid_operation", "Invalid wallet operation.")
		return
	}
	contracts := privy.Contracts{Token: a.Config.Payments.Token, Payment: a.Config.Payments.Contract}
	if a.Reserve != nil {
		contracts.Reserve = a.Config.Reserve.Contract
	}
	call, err := op.Call(contracts)
	if err != nil {
		fail(c, 400, "invalid_operation", "This wallet operation cannot be sponsored.")
		return
	}
	u := currentUser(c)
	if ok, err := a.allow(c, "wallet-prepare:"+u.ID, 10); err != nil || !ok {
		fail(c, 429, "wallet_rate_limit", "Please wait before preparing another wallet transaction.")
		return
	}
	subject, walletID := c.GetString("privy_subject"), c.GetString("privy_wallet_id")
	if c.GetString("privy_app_id") != a.Config.Privy.AppID || subject == "" || walletID == "" {
		fail(c, 409, "embedded_wallet_required", "Sign in with your Privy embedded wallet to use sponsored transactions.")
		return
	}
	// Recheck current ownership when spending app gas; connected wallet hints are not proof.
	profile, err := a.Privy.GetUser(c, subject)
	if err != nil {
		fail(c, 503, "privy_unavailable", "Could not verify the wallet. Try again shortly.")
		return
	}
	verified := false
	for _, w := range profile.Wallets() {
		if w.Address == u.Wallet && w.ID == walletID && w.Embedded() {
			verified = true
		}
	}
	if !verified {
		fail(c, 403, "wallet_not_linked", "Sign in again to verify the selected payment wallet.")
		return
	}
	id := uuid.NewString()
	expires := time.Now().UTC().Add(10 * time.Minute).Truncate(time.Millisecond)
	payload := call.Payload(id, a.Config.ChainID)
	_, err = a.Store.DB.Exec(c, `INSERT INTO wallet_transactions(id,user_id,app_id,wallet_id,wallet,action,amount_units,payload,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, u.ID, a.Config.Privy.AppID, walletID, u.Wallet, op.Action, op.Amount, payload, expires)
	if err != nil {
		fail(c, 503, "database_unavailable", "Could not prepare wallet transaction.")
		return
	}
	c.JSON(201, gin.H{"id": id, "state": "prepared", "authorization": a.Privy.Authorization(walletID, id, payload, expires), "call": call})
}

func (a *App) submitWalletTransaction(c *gin.Context) {
	if !a.sponsoredWallet(c) {
		return
	}
	id := c.Param("id")
	var in struct {
		Signature string `json:"signature"`
	}
	if !validID(id) || c.ShouldBindJSON(&in) != nil || len(in.Signature) == 0 || len(in.Signature) > 2048 {
		fail(c, 400, "invalid_operation", "Invalid wallet authorization.")
		return
	}
	u := currentUser(c)
	if ok, err := a.allow(c, "wallet-submit:"+u.ID, 10); err != nil || !ok {
		fail(c, 429, "wallet_rate_limit", "Please wait before submitting another wallet transaction.")
		return
	}
	tx, err := a.Store.DB.Begin(c)
	if err != nil {
		fail(c, 503, "database_unavailable", "Could not submit wallet transaction.")
		return
	}
	defer tx.Rollback(c)
	// Serialize per-user quota checks and all retries of the same request.
	if _, err = tx.Exec(c, "SELECT id FROM users WHERE id=$1 FOR UPDATE", u.ID); err != nil {
		fail(c, 503, "database_unavailable", "Could not submit wallet transaction.")
		return
	}
	var walletID, wallet, appID, state, hash, remoteID string
	var payload []byte
	var expires time.Time
	err = tx.QueryRow(c, `SELECT wallet_id,wallet,app_id,state,COALESCE(tx_hash,''),COALESCE(privy_transaction_id,''),payload,expires_at FROM wallet_transactions WHERE id=$1 AND user_id=$2 FOR UPDATE`, id, u.ID).Scan(&walletID, &wallet, &appID, &state, &hash, &remoteID, &payload, &expires)
	if err != nil || wallet != u.Wallet || appID != a.Config.Privy.AppID {
		fail(c, 404, "transaction_not_found", "Wallet transaction not found.")
		return
	}
	if state == "submitted" || hash != "" || remoteID != "" {
		c.JSON(200, gin.H{"id": id, "state": state, "hash": hash})
		return
	}
	if state == "rejected" {
		fail(c, 409, "transaction_rejected", "This wallet transaction was rejected. Review its status before starting again.")
		return
	}
	if !time.Now().Before(expires) {
		fail(c, 409, "authorization_expired", "This wallet authorization expired. Check wallet activity before starting another operation.")
		return
	}
	fresh := state == "prepared"
	if fresh {
		var count int
		err = tx.QueryRow(c, `SELECT count(*) FROM wallet_transactions WHERE user_id=$1 AND app_id=$2 AND state<>'prepared' AND error_code<>'cancelled' AND created_at>now()-interval '24 hours'`, u.ID, appID).Scan(&count)
		if err != nil {
			fail(c, 503, "database_unavailable", "Could not check gas sponsorship.")
			return
		}
		if count >= a.Config.Privy.DailyTransactions {
			fail(c, 429, "sponsorship_limit", "Your sponsored transaction limit has been reached. Try again later.")
			return
		}
	}
	// Mark the attempt durably before making a request that could broadcast.
	if _, err = tx.Exec(c, `UPDATE wallet_transactions SET state='pending',updated_at=now() WHERE id=$1`, id); err == nil {
		err = tx.Commit(c)
	}
	if err != nil {
		fail(c, 503, "database_unavailable", "Could not save wallet transaction.")
		return
	}
	result, err := a.Privy.Send(c, walletID, a.Privy.Authorization(walletID, id, payload, expires), in.Signature)
	if err != nil {
		state = "pending"
		code := "submission_unknown"
		var apiError *privy.APIError
		if fresh && errors.As(err, &apiError) && (apiError.Status == 400 || apiError.Status == 401 || apiError.Status == 403 || apiError.Status == 422) {
			state, code = "rejected", "privy_rejected"
		}
		_, _ = a.Store.DB.Exec(c, `UPDATE wallet_transactions SET state=$2,error_code=$3,updated_at=now() WHERE id=$1 AND state='pending' AND tx_hash IS NULL AND privy_transaction_id IS NULL`, id, state, code)
		c.JSON(202, gin.H{"id": id, "state": state, "error_code": code, "hash": ""})
		return
	}
	state = "pending"
	if transactionHash.MatchString(result.Hash) {
		hash = result.Hash
		state = "submitted"
	}
	_, err = a.Store.DB.Exec(c, `UPDATE wallet_transactions SET state=$2,tx_hash=NULLIF($3,''),privy_transaction_id=NULLIF($4,''),error_code='',updated_at=now() WHERE id=$1 AND state='pending' AND tx_hash IS NULL`, id, state, hash, result.ID)
	if err != nil {
		fail(c, 503, "database_unavailable", "Transaction may have been submitted. Check the saved operation before retrying.")
		return
	}
	c.JSON(200, gin.H{"id": id, "state": state, "hash": hash})
}

// Only an operation that has never reached the submission boundary can be discarded.
func (a *App) cancelWalletTransaction(c *gin.Context) {
	if !validID(c.Param("id")) {
		fail(c, 400, "invalid_operation", "Invalid wallet operation.")
		return
	}
	r, err := a.Store.DB.Exec(c, `UPDATE wallet_transactions SET state='rejected',error_code='cancelled',updated_at=now() WHERE id=$1 AND user_id=$2 AND app_id=$3 AND state='prepared'`, c.Param("id"), currentUser(c).ID, a.Config.Privy.AppID)
	if err != nil {
		fail(c, 503, "database_unavailable", "Could not cancel wallet operation.")
		return
	}
	if r.RowsAffected() != 1 {
		fail(c, 409, "submission_pending", "This operation may have been submitted. Resume it to check its status.")
		return
	}
	c.Status(204)
}

func (a *App) walletTransaction(c *gin.Context) {
	if a.Mera != nil && validID(c.Param("id")) {
		if o, err := a.Mera.Get(c, c.Param("id"), currentUser(c).ID); err == nil {
			c.JSON(200, o)
			return
		}
	}
	if a.Privy == nil || !validID(c.Param("id")) {
		fail(c, 404, "transaction_not_found", "Wallet transaction not found.")
		return
	}
	id := c.Param("id")
	var walletID, appID, state, hash, remoteID, code string
	var payload json.RawMessage
	var expires time.Time
	err := a.Store.DB.QueryRow(c, `SELECT wallet_id,app_id,state,COALESCE(tx_hash,''),COALESCE(privy_transaction_id,''),payload,expires_at,error_code FROM wallet_transactions WHERE id=$1 AND user_id=$2`, id, currentUser(c).ID).Scan(&walletID, &appID, &state, &hash, &remoteID, &payload, &expires, &code)
	if err != nil || appID != a.Config.Privy.AppID {
		fail(c, 404, "transaction_not_found", "Wallet transaction not found.")
		return
	}
	if state == "pending" {
		var result privy.Transaction
		var e error
		if remoteID != "" {
			result, e = a.Privy.Transaction(c, remoteID)
		} else {
			result, e = a.Privy.TransactionByReference(c, id, walletID)
		}
		if e == nil {
			remoteID = result.ID
			if transactionHash.MatchString(result.Hash) {
				hash, state, code = result.Hash, "submitted", ""
			} else if result.Status == "failed" || result.Status == "execution_reverted" {
				state, code = "rejected", "transaction_failed"
			}
			_, _ = a.Store.DB.Exec(c, `UPDATE wallet_transactions SET tx_hash=NULLIF($2,''),privy_transaction_id=NULLIF($3,''),state=$4,error_code=$5,updated_at=now() WHERE id=$1 AND state='pending'`, id, hash, remoteID, state, code)
		}
	}
	reply := gin.H{"id": id, "state": state, "hash": hash, "error_code": code, "expires_at": expires, "submitted": state != "prepared"}
	if (state == "prepared" || state == "pending" && remoteID == "") && time.Now().Before(expires) {
		reply["authorization"] = a.Privy.Authorization(walletID, id, payload, expires)
	}
	c.JSON(200, reply)
}
