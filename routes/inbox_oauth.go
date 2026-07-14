package routes

import (
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	imapsync "github.com/josephalai/sentanyl/marketing-service/internal/imap"
	"github.com/josephalai/sentanyl/marketing-service/internal/mailoauth"
	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/utils"
)

// Tenant self-service mailbox OAuth (COM-EM-003): "Connect Gmail/Microsoft"
// in the admin UI round-trips through the provider consent screen and lands
// the mailbox on an InboxAccount owned by EXACTLY the initiating tenant —
// the binding travels inside the HMAC-signed state, never a query parameter.

// RegisterInboxOAuthTenantRoutes mounts the authenticated halves.
func RegisterInboxOAuthTenantRoutes(rg *gin.RouterGroup) {
	rg.GET("/inbox/oauth/:provider/authorize-url", handleInboxOAuthAuthorizeURL)
	rg.POST("/inbox/accounts/:id/disconnect", handleInboxOAuthDisconnect)
}

// RegisterInboxOAuthPublicRoutes mounts the provider redirect target.
func RegisterInboxOAuthPublicRoutes(r *gin.Engine) {
	r.GET("/api/marketing/inbox/oauth/callback", handleInboxOAuthCallback)
}

func oauthRedirectURI() string {
	return publicBaseURL() + "/api/marketing/inbox/oauth/callback"
}

func handleInboxOAuthAuthorizeURL(c *gin.Context) {
	tenantID, ok := tenantIDFromContext(c)
	if !ok {
		return
	}
	provider := c.Param("provider")
	cfg, err := mailoauth.ForProvider(provider)
	if err != nil {
		// Unconfigured platform credentials are an owner-visible condition,
		// not a silent failure.
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		return
	}
	state, challenge, err := mailoauth.NewState(tenantID.Hex(), auth.GetAccountUserID(c), provider)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "state mint failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"url":      mailoauth.AuthorizeURL(cfg, oauthRedirectURI(), state, challenge),
		"provider": provider,
	})
}

func handleInboxOAuthCallback(c *gin.Context) {
	redirectErr := func(msg string) {
		c.Redirect(http.StatusFound, "/inbox-closer?mailbox_error="+url.QueryEscape(msg))
	}
	if e := c.Query("error"); e != "" {
		redirectErr(e)
		return
	}
	code := c.Query("code")
	tenantHex, connectedBy, provider, verifier, err := mailoauth.VerifyState(c.Query("state"))
	if err != nil || code == "" || !bson.IsObjectIdHex(tenantHex) {
		redirectErr("invalid or expired authorization state")
		return
	}
	cfg, err := mailoauth.ForProvider(provider)
	if err != nil {
		redirectErr(err.Error())
		return
	}
	tok, err := mailoauth.Exchange(cfg, oauthRedirectURI(), code, verifier)
	if err != nil {
		log.Printf("[inbox oauth] exchange failed: %v", err)
		redirectErr("token exchange failed")
		return
	}
	// Mailbox identity comes from the PROVIDER, never from the browser.
	email, err := mailoauth.Identity(cfg, tok.AccessToken)
	if err != nil {
		log.Printf("[inbox oauth] identity failed: %v", err)
		redirectErr("could not verify mailbox identity")
		return
	}
	encAccess, err1 := utils.Encrypt(tok.AccessToken)
	encRefresh, err2 := utils.Encrypt(tok.RefreshToken)
	if err1 != nil || err2 != nil || tok.RefreshToken == "" {
		redirectErr("provider did not grant offline access; remove the app's prior consent and retry")
		return
	}
	tenantID := bson.ObjectIdHex(tenantHex)
	expires := time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)

	col := db.GetCollection(pkgmodels.InboxAccountCollection)
	var existing pkgmodels.InboxAccount
	if err := col.Find(bson.M{
		"tenant_id":             tenantID,
		"provider":              provider,
		"email_address":         email,
		"timestamps.deleted_at": nil,
	}).One(&existing); err == nil {
		// Reconnect: refresh credentials + health on the same account row.
		_ = col.UpdateId(existing.Id, bson.M{"$set": bson.M{
			"access_token_encrypted":  encAccess,
			"refresh_token_encrypted": encRefresh,
			"token_expires_at":        expires,
			"auth_state":              "connected",
			"sync_status":             "pending",
			"connected_by":            connectedBy,
			"last_error":              "",
			"consecutive_failures":    0,
			"timestamps.updated_at":   time.Now(),
		}})
		go func() { _ = imapsync.SyncAccountNow(existing.Id) }()
		c.Redirect(http.StatusFound, "/inbox-closer?mailbox_connected="+url.QueryEscape(email))
		return
	}

	account := pkgmodels.NewInboxAccount(tenantID, provider, email)
	account.AccessTokenEncrypted = encAccess
	account.RefreshTokenEncrypted = encRefresh
	account.TokenExpiresAt = &expires
	account.AuthState = "connected"
	account.SyncStatus = "pending"
	account.ConnectedBy = connectedBy
	if err := col.Insert(account); err != nil {
		redirectErr("failed to save mailbox connection")
		return
	}
	// Bootstrap the cursor immediately (forward-only sync from now).
	go func() { _ = imapsync.SyncAccountNow(account.Id) }()
	c.Redirect(http.StatusFound, "/inbox-closer?mailbox_connected="+url.QueryEscape(email))
}

// handleInboxOAuthDisconnect revokes the provider authorization where the
// provider supports it and deletes every stored token — the account row
// remains (auditable) but can no longer read or send mail.
func handleInboxOAuthDisconnect(c *gin.Context) {
	tenantID, ok := tenantIDFromContext(c)
	if !ok {
		return
	}
	var account pkgmodels.InboxAccount
	if err := findByIDOrPublic(pkgmodels.InboxAccountCollection, tenantID, c.Param("id"), &account); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "inbox account not found"})
		return
	}
	if cfg, err := mailoauth.ForProvider(account.Provider); err == nil {
		if account.RefreshTokenEncrypted != "" {
			if refresh, derr := utils.Decrypt(account.RefreshTokenEncrypted); derr == nil {
				_ = mailoauth.Revoke(cfg, refresh)
			}
		}
	}
	_ = db.GetCollection(pkgmodels.InboxAccountCollection).UpdateId(account.Id, bson.M{
		"$set": bson.M{
			"auth_state":            "disconnected",
			"sync_status":           "disconnected",
			"timestamps.updated_at": time.Now(),
		},
		"$unset": bson.M{
			"access_token_encrypted":  "",
			"refresh_token_encrypted": "",
			"token_expires_at":        "",
			"sync_cursor":             "",
		},
	})
	c.JSON(http.StatusOK, gin.H{"status": "disconnected"})
}
