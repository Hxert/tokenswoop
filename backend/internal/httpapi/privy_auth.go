package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"tokenswoop/backend/internal/config"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"tokenswoop/backend/internal/privy"
	"tokenswoop/backend/internal/security"
)

func (a *App) privyLogin(c *gin.Context) {
	if a.Privy == nil {
		fail(c, 503, "privy_disabled", "Email and embedded wallet sign-in is not configured.")
		return
	}
	var in struct {
		AccessToken string `json:"access_token"`
		Wallet      string `json:"wallet"`
	}
	if c.ShouldBindJSON(&in) != nil || (in.Wallet != "" && !security.WalletPattern.MatchString(in.Wallet)) {
		fail(c, 400, "invalid_login", "Invalid login request.")
		return
	}
	profile, err := a.Privy.Authenticate(c, in.AccessToken)
	if err != nil {
		if errors.Is(err, privy.ErrIdentity) {
			fail(c, 401, "invalid_privy_token", "Sign in again to verify your Privy account.")
		} else {
			fail(c, 503, "privy_unavailable", "Wallet sign-in is temporarily unavailable.")
		}
		return
	}
	wallets := profile.Wallets()
	if len(wallets) == 0 {
		fail(c, 409, "wallet_not_ready", "Finish creating or linking your Ethereum wallet, then sign in again.")
		return
	}
	verified := map[string]privy.Wallet{}
	for _, w := range wallets {
		verified[w.Address] = w
	}
	if in.Wallet != "" && verified[strings.ToLower(in.Wallet)].Address == "" {
		fail(c, 403, "wallet_not_linked", "The selected wallet is not verified on your Privy account.")
		return
	}
	// Only the selected, provider-verified wallet chooses the workspace.
	wallet := strings.ToLower(in.Wallet)
	if wallet == "" {
		if len(verified) != 1 {
			fail(c, 409, "account_selection_required", "Select the wallet you want to sign in with.")
			return
		}
		for address := range verified {
			wallet = address
		}
	}
	tx, err := a.Store.DB.Begin(c)
	if err != nil {
		fail(c, 503, "database_unavailable", "Could not create session.")
		return
	}
	defer tx.Rollback(c)
	role := "user"
	if a.Config.AdminWallets[wallet] {
		role = "admin"
	}
	var id, status string
	err = tx.QueryRow(c, `INSERT INTO users(id,wallet,role) VALUES($1,$2,$3) ON CONFLICT(wallet) DO UPDATE SET role=excluded.role RETURNING id::text,status`, uuid.NewString(), wallet, role).Scan(&id, &status)
	if err != nil {
		fail(c, 503, "database_unavailable", "Could not read account.")
		return
	}
	if status != "active" {
		fail(c, 403, "account_suspended", "This account is suspended.")
		return
	}
	w := verified[wallet]
	walletID := ""
	if w.Embedded() {
		walletID = w.ID
	}
	session, csrf, err := a.createWalletSession(c, tx, id, profile.ID, walletID)
	if err == nil {
		err = tx.Commit(c)
	}
	if err != nil {
		fail(c, 503, "database_unavailable", "Could not create session.")
		return
	}
	secure := strings.HasPrefix(a.Config.Origin, "https://")
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie("ts_session", session, int(a.Config.SessionTTL.Seconds()), "/", "", secure, true)
	c.SetCookie("ts_csrf", csrf, int(a.Config.SessionTTL.Seconds()), "/", "", secure, false)
	u, err := a.Store.User(c, id)
	if err != nil {
		fail(c, 503, "database_unavailable", "Could not read account.")
		return
	}
	c.JSON(200, u)
}

func (a *App) publicConfig(c *gin.Context) {
	appID, auth := "", "eoa-signature"
	if a.Privy != nil {
		appID, auth = a.Config.Privy.AppID, "privy"
	}
	c.JSON(200, gin.H{"chain_id": a.Config.ChainID, "settlement_enabled": a.Payments != nil, "reserve_enabled": a.Reserve != nil, "auth": auth, "network": config.NetworkFor(a.Config.ChainID), "environment": a.Config.Environment, "mera": gin.H{"gas_sponsorship": a.Mera != nil && a.Config.Mera.Enabled, "implementation": a.Config.Mera.Implementation, "token": a.Config.Payments.Token, "payment": a.Config.Payments.Contract, "reserve": a.Config.Reserve.Contract}, "privy": gin.H{"enabled": a.Privy != nil, "app_id": appID, "gas_sponsorship": a.Privy != nil && a.Config.Privy.GasSponsorship}})
}
