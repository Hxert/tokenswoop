package httpapi

import (
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"tokenswoop/backend/internal/faucet"
	"tokenswoop/backend/internal/security"
)

func (a *App) faucetAvailable(c *gin.Context) bool {
	if a.Config.ChainID != 10143 || !a.Faucet.Enabled() {
		fail(c, 503, "faucet_unavailable", "The testnet faucet is temporarily unavailable.")
		return false
	}
	return true
}
func (a *App) faucetStatus(c *gin.Context) {
	if !a.faucetAvailable(c) {
		return
	}
	status, err := a.Faucet.Status(c, currentUser(c).ID)
	if err != nil {
		fail(c, 503, "faucet_unavailable", "Could not check your faucet claim.")
		return
	}
	c.JSON(200, status)
}
func (a *App) faucetClaim(c *gin.Context) {
	if !a.faucetAvailable(c) {
		return
	}
	var in struct {
		ID    string `json:"id"`
		Token string `json:"turnstile_token"`
	}
	if c.ShouldBindJSON(&in) != nil || !validID(in.ID) || len(in.Token) > 2048 {
		fail(c, 400, "invalid_claim", "Invalid faucet claim.")
		return
	}
	// Existing claims are safe to recover without consuming another one-use captcha.
	if old, err := a.Faucet.Get(c, in.ID, currentUser(c).ID); err == nil {
		c.JSON(200, old)
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		fail(c, 503, "faucet_unavailable", "Could not read your claim.")
		return
	}
	ok, err := a.allow(c, "faucet:"+security.Digest(a.Config.Secret, "ip", c.ClientIP()), 10)
	if err != nil {
		fail(c, 503, "faucet_unavailable", "Please try again later.")
		return
	}
	if !ok {
		c.Header("Retry-After", "60")
		fail(c, 429, "faucet_rate_limited", "Please wait a minute before trying again.")
		return
	}
	user := currentUser(c)
	claim, err := a.Faucet.Claim(c, in.ID, user.ID, user.Wallet, in.Token, c.ClientIP())
	switch {
	case err == nil:
		c.JSON(202, claim)
	case errors.Is(err, faucet.ErrVerification):
		fail(c, 403, "turnstile_failed", "Verification expired or failed. Please verify again.")
	case errors.Is(err, faucet.ErrVerificationUnavailable):
		fail(c, 503, "turnstile_unavailable", "Verification is temporarily unavailable. Please try again.")
	case errors.Is(err, faucet.ErrCooldown):
		fail(c, 429, "faucet_cooldown", "You already have a claim. Check its status or wait for the next claim time.")
	case errors.Is(err, faucet.ErrQuota):
		fail(c, 429, "faucet_daily_limit", "The faucet's daily limit has been reached. Please try again later.")
	default:
		fail(c, 503, "faucet_unavailable", "Could not accept your claim. Retry to check the same request.")
	}
}
func (a *App) faucetClaimStatus(c *gin.Context) {
	if !a.faucetAvailable(c) {
		return
	}
	if !validID(c.Param("id")) {
		fail(c, 400, "invalid_claim", "Invalid claim.")
		return
	}
	claim, err := a.Faucet.Get(c, c.Param("id"), currentUser(c).ID)
	if errors.Is(err, pgx.ErrNoRows) {
		fail(c, 404, "claim_not_found", "Claim not found.")
		return
	}
	if err != nil {
		fail(c, 503, "faucet_unavailable", "Could not read your claim.")
		return
	}
	c.JSON(200, claim)
}
