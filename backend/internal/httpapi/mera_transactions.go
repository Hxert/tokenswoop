package httpapi

import (
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"tokenswoop/backend/internal/merasponsor"
)

func (a *App) pendingMeraTransaction(c *gin.Context) {
	if a.Mera == nil {
		c.JSON(200, nil)
		return
	}
	o, err := a.Mera.Pending(c, currentUser(c).ID)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(200, nil)
		return
	}
	if err != nil {
		a.meraError(c, err)
		return
	}
	c.JSON(200, o)
}

func (a *App) meraWallet(c *gin.Context) bool {
	if a.Mera == nil || !a.Config.Mera.Enabled {
		fail(c, 503, "sponsorship_unavailable", "Mera gas sponsorship is unavailable.")
		return false
	}
	// This is a session-bound choice of client, not remote attestation of a passkey.
	// Every operation also requires the wallet owner's cryptographic signatures.
	if c.GetString("wallet_source") != "mera" {
		fail(c, 409, "mera_wallet_required", "Sign in with your Mera passkey to use gas sponsorship.")
		return false
	}
	return true
}
func (a *App) meraError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, merasponsor.ErrInsufficientBalance):
		fail(c, 422, "insufficient_wallet_balance", err.Error())
	case errors.Is(err, merasponsor.ErrInvalid):
		fail(c, 400, "invalid_authorization", err.Error())
	case errors.Is(err, merasponsor.ErrQuota):
		fail(c, 429, "sponsorship_limit", err.Error())
	case errors.Is(err, merasponsor.ErrPending):
		fail(c, 409, "wallet_operation_pending", err.Error())
	default:
		fail(c, 503, "mera_unavailable", "Could not prepare this Mera operation. Resume any pending operation or try again shortly.")
	}
}
func (a *App) prepareMeraTransaction(c *gin.Context) {
	if !a.meraWallet(c) {
		return
	}
	var in struct {
		ID     string `json:"id"`
		Action string `json:"action"`
		Amount string `json:"amount_units"`
	}
	if c.ShouldBindJSON(&in) != nil || !validID(in.ID) {
		fail(c, 400, "invalid_operation", "Invalid Mera operation.")
		return
	}
	u := currentUser(c)
	if ok, err := a.allow(c, "mera-prepare:"+u.ID, 10); err != nil || !ok {
		fail(c, 429, "wallet_rate_limit", "Please wait before trying again.")
		return
	}
	o, err := a.Mera.Prepare(c, in.ID, u.ID, u.Wallet, in.Action, in.Amount)
	if err != nil {
		a.meraError(c, err)
		return
	}
	c.JSON(201, o)
}
func (a *App) submitMeraTransaction(c *gin.Context) {
	if !a.meraWallet(c) {
		return
	}
	var in struct {
		Signature     string                     `json:"signature"`
		Authorization *merasponsor.Authorization `json:"authorization"`
	}
	if !validID(c.Param("id")) || c.ShouldBindJSON(&in) != nil || len(in.Signature) != 132 {
		fail(c, 400, "invalid_operation", "Invalid Mera authorization.")
		return
	}
	u := currentUser(c)
	if ok, err := a.allow(c, "mera-submit:"+u.ID, 10); err != nil || !ok {
		fail(c, 429, "wallet_rate_limit", "Please wait before trying again.")
		return
	}
	o, err := a.Mera.Submit(c, c.Param("id"), u.ID, in.Signature, in.Authorization)
	if err != nil {
		a.meraError(c, err)
		return
	}
	c.JSON(202, o)
}
func (a *App) meraTransaction(c *gin.Context) {
	if a.Mera == nil || !validID(c.Param("id")) {
		fail(c, 404, "transaction_not_found", "Wallet operation not found.")
		return
	}
	o, err := a.Mera.Get(c, c.Param("id"), currentUser(c).ID)
	if err != nil {
		fail(c, 404, "transaction_not_found", "Wallet operation not found.")
		return
	}
	c.JSON(200, o)
}
func (a *App) cancelMeraTransaction(c *gin.Context) {
	if !a.meraWallet(c) {
		return
	}
	if !validID(c.Param("id")) {
		fail(c, 400, "invalid_operation", "Invalid operation.")
		return
	}
	if err := a.Mera.Cancel(c, c.Param("id"), currentUser(c).ID); err != nil {
		a.meraError(c, err)
		return
	}
	c.Status(204)
}
