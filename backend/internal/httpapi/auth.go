package httpapi

import (
	"crypto/subtle"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"net/http"
	"net/url"
	"strings"
	"time"
	"tokenswoop/backend/internal/security"
	"tokenswoop/backend/internal/store"
)

func (a *App) challenge(c *gin.Context) {
	var in struct {
		Wallet string `json:"wallet"`
		Source string `json:"source"`
	}
	if c.ShouldBindJSON(&in) != nil || !security.WalletPattern.MatchString(in.Wallet) {
		fail(c, 400, "invalid_wallet", "A valid EVM wallet address is required.")
		return
	}
	if in.Source == "" {
		in.Source = "external"
	}
	if in.Source != "external" && in.Source != "mera" {
		fail(c, 400, "invalid_wallet_source", "Invalid wallet sign-in method.")
		return
	}
	id := uuid.NewString()
	wallet := strings.ToLower(in.Wallet)
	now := time.Now().UTC()
	expires := now.Add(5 * time.Minute)
	origin, _ := url.Parse(a.Config.Origin)
	statement := "Sign in to TokenSwoop. This does not authorize any payment."
	if in.Source == "mera" {
		statement = "Sign in to TokenSwoop with your Mera passkey wallet. This does not authorize any payment."
	}
	message := fmt.Sprintf("%s wants you to sign in with your Ethereum account:\n%s\n\n%s\n\nURI: %s\nVersion: 1\nChain ID: %d\nNonce: %s\nIssued At: %s\nExpiration Time: %s", origin.Host, security.ChecksumWallet(wallet), statement, a.Config.Origin, a.Config.ChainID, strings.ReplaceAll(uuid.NewString(), "-", ""), now.Format(time.RFC3339), expires.Format(time.RFC3339))
	_, e := a.Store.DB.Exec(c, "INSERT INTO auth_challenges(id,wallet,message,expires_at,wallet_source) VALUES($1,$2,$3,$4,$5)", id, wallet, message, expires, in.Source)
	if e != nil {
		fail(c, 503, "database_unavailable", "Authentication is temporarily unavailable.")
		return
	}
	c.JSON(200, gin.H{"id": id, "message": message, "expires_at": expires})
}

func (a *App) login(c *gin.Context) {
	var in struct {
		ID        string `json:"id"`
		Signature string `json:"signature"`
	}
	if c.ShouldBindJSON(&in) != nil || !validID(in.ID) {
		fail(c, 400, "invalid_login", "Invalid login request.")
		return
	}
	tx, e := a.Store.DB.Begin(c)
	if e != nil {
		fail(c, 503, "database_unavailable", "Authentication unavailable.")
		return
	}
	defer tx.Rollback(c)
	var wallet, message, source string
	e = tx.QueryRow(c, "SELECT wallet,message,wallet_source FROM auth_challenges WHERE id=$1 AND NOT consumed AND expires_at>now() FOR UPDATE", in.ID).Scan(&wallet, &message, &source)
	if e != nil || !security.VerifyWallet(wallet, message, in.Signature) {
		fail(c, 401, "invalid_signature", "The signature or challenge is invalid or expired. EOA wallets are supported in this release.")
		return
	}
	role := "user"
	if a.Config.AdminWallets[wallet] {
		role = "admin"
	}
	var id, status string
	e = tx.QueryRow(c, "INSERT INTO users(id,wallet,role) VALUES($1,$2,$3) ON CONFLICT(wallet) DO UPDATE SET role=excluded.role RETURNING id::text,status", uuid.NewString(), wallet, role).Scan(&id, &status)
	if e != nil {
		fail(c, 503, "database_unavailable", "Could not create session.")
		return
	}
	if status != "active" {
		fail(c, 403, "account_suspended", "This account is suspended.")
		return
	}
	var session, csrf string
	if _, e = tx.Exec(c, "UPDATE auth_challenges SET consumed=true WHERE id=$1", in.ID); e == nil {
		session, csrf, e = a.createWalletSession(c, tx, id, "", "")
		if e == nil {
			_, e = tx.Exec(c, "UPDATE sessions SET wallet_source=$2 WHERE token_hash=$1", security.Digest(a.Config.Secret, "session", session), source)
		}
	}
	if e == nil {
		e = tx.Commit(c)
	}
	if e != nil {
		fail(c, 503, "database_unavailable", "Could not create session.")
		return
	}
	secure := strings.HasPrefix(a.Config.Origin, "https://")
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie("ts_session", session, int(a.Config.SessionTTL.Seconds()), "/", "", secure, true)
	c.SetCookie("ts_csrf", csrf, int(a.Config.SessionTTL.Seconds()), "/", "", secure, false)
	u, e := a.Store.User(c, id)
	if e != nil {
		fail(c, 503, "database_unavailable", "Could not read account.")
		return
	}
	c.JSON(200, u)
}

func (a *App) createWalletSession(c *gin.Context, tx pgx.Tx, userID, subject, walletID string) (string, string, error) {
	token, csrf := security.Token(), security.Token()
	appID := ""
	if subject != "" {
		appID = a.Config.Privy.AppID
	}
	_, err := tx.Exec(c, `INSERT INTO sessions(token_hash,user_id,csrf_hash,expires_at,privy_app_id,privy_subject,privy_wallet_id) VALUES($1,$2,$3,$4,$5,$6,$7)`, security.Digest(a.Config.Secret, "session", token), userID, security.Digest(a.Config.Secret, "csrf", csrf), time.Now().Add(a.Config.SessionTTL), appID, subject, walletID)
	if err == nil {
		if previous, e := c.Cookie("ts_session"); e == nil && previous != "" {
			_, err = tx.Exec(c, "DELETE FROM sessions WHERE token_hash=$1", security.Digest(a.Config.Secret, "session", previous))
		}
	}
	return token, csrf, err
}

func (a *App) session(c *gin.Context) {
	token, e := c.Cookie("ts_session")
	if e != nil {
		fail(c, 401, "unauthorized", "Sign in to continue.")
		return
	}
	var id, csrf, privyApp, privySubject, privyWallet, source string
	e = a.Store.DB.QueryRow(c, "SELECT user_id::text,csrf_hash,privy_app_id,privy_subject,privy_wallet_id,wallet_source FROM sessions WHERE token_hash=$1 AND expires_at>now()", security.Digest(a.Config.Secret, "session", token)).Scan(&id, &csrf, &privyApp, &privySubject, &privyWallet, &source)
	if e != nil {
		fail(c, 401, "unauthorized", "Your session has expired.")
		return
	}
	if c.Request.Method != "GET" && c.Request.Method != "HEAD" {
		value := security.Digest(a.Config.Secret, "csrf", c.GetHeader("X-CSRF-Token"))
		if subtle.ConstantTimeCompare([]byte(value), []byte(csrf)) != 1 {
			fail(c, 403, "csrf_failed", "Refresh the page and try again.")
			return
		}
	}
	u, e := a.Store.User(c, id)
	if e != nil || u.Status != "active" {
		fail(c, 403, "account_unavailable", "This account is unavailable.")
		return
	}
	c.Set("user", u)
	c.Set("privy_app_id", privyApp)
	c.Set("privy_subject", privySubject)
	c.Set("privy_wallet_id", privyWallet)
	c.Set("wallet_source", source)
	c.Next()
}
func (a *App) admin(c *gin.Context) {
	if currentUser(c).Role != "admin" || !a.Config.AdminWallets[currentUser(c).Wallet] {
		fail(c, 403, "forbidden", "Administrator access required.")
		return
	}
	c.Next()
}
func (a *App) origin(c *gin.Context) {
	if c.Request.Method != "GET" && c.Request.Method != "HEAD" && c.GetHeader("Origin") != a.Config.Origin {
		fail(c, 403, "origin_rejected", "Request origin is not allowed.")
		return
	}
	c.Next()
}
func (a *App) logout(c *gin.Context) {
	t, _ := c.Cookie("ts_session")
	if _, e := a.Store.DB.Exec(c, "DELETE FROM sessions WHERE token_hash=$1", security.Digest(a.Config.Secret, "session", t)); e != nil {
		fail(c, 503, "database_unavailable", "Could not revoke session.")
		return
	}
	c.SetSameSite(http.SameSiteStrictMode)
	secure := strings.HasPrefix(a.Config.Origin, "https://")
	c.SetCookie("ts_session", "", -1, "/", "", secure, true)
	c.SetCookie("ts_csrf", "", -1, "/", "", secure, false)
	c.Status(204)
}
func currentUser(c *gin.Context) store.User { return c.MustGet("user").(store.User) }
func validID(v string) bool                 { _, e := uuid.Parse(v); return e == nil }
