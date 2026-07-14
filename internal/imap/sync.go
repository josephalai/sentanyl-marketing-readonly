package imap

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/josephalai/sentanyl/marketing-service/internal/mailoauth"
	"github.com/josephalai/sentanyl/pkg/db"
	"github.com/josephalai/sentanyl/pkg/jobs"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/utils"
	"gopkg.in/mgo.v2/bson"
)

type imapCredentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// InboundHandler is called for each new message fetched from a mailbox.
// inbox_closer.go registers its pipeline here so the sync job can call it
// without creating an import cycle. A returned error dead-letters the
// message onto the durable job kernel (COM-EM-003).
type InboundHandler func(tenantID, accountID bson.ObjectId, msg Message) error

var registeredHandler InboundHandler

// RegisterHandler wires the inbox processing pipeline into the sync loop.
func RegisterHandler(h InboundHandler) {
	registeredHandler = h
}

const syncSweepJobType = "imap.sync.sweep"

// StartSyncLoop polls all active IMAP inbox accounts every interval.
// Call once from main.
//
// OPS-001/OPS-004: the pass runs as a durable self-rescheduling job on
// pkg/jobs (was an in-process ticker), so it survives restarts, panics are
// retried/dead-lettered by the worker, and the job lease guarantees a single
// syncer across replicas.
func StartSyncLoop(interval time.Duration) {
	jobs.Register(syncSweepJobType, func(ctx context.Context, job *jobs.Job) error {
		// Re-arm the chain FIRST so a crash mid-sync never stalls the loop.
		if err := jobs.EnqueueSweep(syncSweepJobType, time.Now().Add(interval), interval); err != nil {
			return err
		}
		syncAllAccounts()
		if job.RunAt.Unix()%3600 < int64(interval/time.Second) {
			jobs.PruneSucceeded(syncSweepJobType, 24*time.Hour)
		}
		return nil
	})
	if err := jobs.EnqueueSweep(syncSweepJobType, time.Now(), interval); err != nil {
		log.Printf("imap: bootstrap sweep enqueue failed: %v", err)
	}
	log.Printf("imap: durable sync sweep registered (interval=%s)", interval)
}

func syncAllAccounts() {
	var accounts []pkgmodels.InboxAccount
	err := db.GetCollection(pkgmodels.InboxAccountCollection).Find(bson.M{
		"$or": []bson.M{
			{"imap_host": bson.M{"$exists": true, "$ne": ""}},
			{"provider": bson.M{"$in": []string{"gmail", "microsoft"}}},
		},
		"timestamps.deleted_at": nil,
	}).All(&accounts)
	if err != nil || len(accounts) == 0 {
		return
	}

	now := time.Now()
	for _, acct := range accounts {
		if acct.AuthState == "reauth_required" || acct.AuthState == "disconnected" {
			continue // needs a tenant reconnect; do not crash-loop against the provider
		}
		if acct.NextRetryAt != nil && acct.NextRetryAt.After(now) {
			continue // backing off after consecutive failures
		}
		if err := syncAccount(acct); err != nil {
			log.Printf("imap: sync failed for %s: %v", acct.EmailAddress, err)
			recordSyncFailure(acct, err)
		} else {
			clearSyncFailure(acct)
		}
	}
}

// recordSyncFailure sets error health with exponential backoff so a dead
// mailbox degrades to periodic re-checks instead of hammering the provider.
func recordSyncFailure(acct pkgmodels.InboxAccount, cause error) {
	failures := acct.ConsecutiveFailures + 1
	backoff := time.Duration(1<<uint(min(failures, 6))) * time.Minute // 2m..64m
	next := time.Now().Add(backoff)
	_ = db.GetCollection(pkgmodels.InboxAccountCollection).UpdateId(acct.Id, bson.M{"$set": bson.M{
		"sync_status":          "error",
		"consecutive_failures": failures,
		"next_retry_at":        next,
		"last_error":           cause.Error(),
	}})
}

func clearSyncFailure(acct pkgmodels.InboxAccount) {
	if acct.ConsecutiveFailures == 0 && acct.NextRetryAt == nil {
		return
	}
	_ = db.GetCollection(pkgmodels.InboxAccountCollection).UpdateId(acct.Id, bson.M{"$unset": bson.M{
		"consecutive_failures": "", "next_retry_at": "", "last_error": "",
	}})
}

// SyncAccountNow runs one immediate sync pass for a single account — the
// "Sync now" button's real backend (the endpoint previously answered with a
// manual_ready stub while the actual sync only ran on the background sweep).
func SyncAccountNow(accountID bson.ObjectId) error {
	var acct pkgmodels.InboxAccount
	if err := db.GetCollection(pkgmodels.InboxAccountCollection).FindId(accountID).One(&acct); err != nil {
		return err
	}
	return syncAccount(acct)
}

func syncAccount(acct pkgmodels.InboxAccount) error {
	switch acct.Provider {
	case "gmail", "microsoft":
		return syncOAuthAccount(acct)
	}
	if acct.CredentialsEncrypted == "" {
		return nil
	}

	raw, err := utils.Decrypt(acct.CredentialsEncrypted)
	if err != nil {
		return err
	}
	var creds imapCredentials
	if err := json.Unmarshal([]byte(raw), &creds); err != nil {
		return err
	}

	msgs, maxUID, err := FetchNew(acct.IMAPHost, acct.IMAPPort, creds.Username, creds.Password, acct.IMAPLastUID)
	if err != nil {
		// Mark as error but don't hard-fail the loop
		_ = db.GetCollection(pkgmodels.InboxAccountCollection).UpdateId(acct.Id, bson.M{"$set": bson.M{
			"sync_status":           "error",
			"timestamps.updated_at": time.Now(),
		}})
		return err
	}

	now := time.Now()
	updateFields := bson.M{
		"last_synced_at":        now,
		"sync_status":           "synced",
		"timestamps.updated_at": now,
	}
	if maxUID > acct.IMAPLastUID {
		updateFields["imap_last_uid"] = maxUID
	}
	_ = db.GetCollection(pkgmodels.InboxAccountCollection).UpdateId(acct.Id, bson.M{"$set": updateFields})

	deliverMessages(acct, msgs)
	return nil
}

// deliverMessages hands each fetched message to the inbound pipeline with
// per-message failure isolation (COM-EM-003): one poisoned message is
// dead-lettered onto the durable job kernel instead of aborting the sweep or
// being silently dropped, and the rest of the batch still processes.
func deliverMessages(acct pkgmodels.InboxAccount, msgs []Message) {
	if registeredHandler == nil {
		return
	}
	for _, msg := range msgs {
		// Skip messages sent by the account itself
		if strings.EqualFold(msg.FromEmail, acct.EmailAddress) {
			continue
		}
		if err := deliverOne(acct, msg); err != nil {
			log.Printf("imap: message %q failed processing; dead-lettering: %v", msg.MessageID, err)
			enqueueMessageRetry(acct, msg)
		}
	}
}

func deliverOne(acct pkgmodels.InboxAccount, msg Message) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic in inbound handler: %v", r)
		}
	}()
	return registeredHandler(acct.TenantID, acct.Id, msg)
}

const messageRetryJobType = "inbox.message.retry"

// enqueueMessageRetry persists a failed message as a durable job — the
// kernel retries with backoff and dead-letters after max attempts, keeping
// the failure visible to operators.
func enqueueMessageRetry(acct pkgmodels.InboxAccount, msg Message) {
	payload, err := json.Marshal(msg)
	if err != nil {
		return
	}
	key := messageRetryJobType + ":" + acct.Id.Hex() + ":" + msg.MessageID
	_ = jobs.Enqueue(jobs.NewJob(messageRetryJobType, key, jobs.Envelope{
		TenantID: acct.TenantID, Actor: "imap-sync", Subject: msg.MessageID, Version: 1,
	}, bson.M{"account_id": acct.Id.Hex(), "message": string(payload)}))
}

// RegisterMessageRetryJob wires the dead-letter consumer for failed inbound
// messages. Call from main after RegisterHandler.
func RegisterMessageRetryJob() {
	jobs.Register(messageRetryJobType, func(ctx context.Context, job *jobs.Job) error {
		if registeredHandler == nil {
			return fmt.Errorf("no inbound handler registered")
		}
		acctHex, _ := job.Payload["account_id"].(string)
		raw, _ := job.Payload["message"].(string)
		if !bson.IsObjectIdHex(acctHex) || raw == "" {
			return nil
		}
		var acct pkgmodels.InboxAccount
		if err := db.GetCollection(pkgmodels.InboxAccountCollection).FindId(bson.ObjectIdHex(acctHex)).One(&acct); err != nil {
			return nil // account gone
		}
		var msg Message
		if err := json.Unmarshal([]byte(raw), &msg); err != nil {
			return nil
		}
		return deliverOne(acct, msg)
	})
}

// syncOAuthAccount fetches new mail for a Gmail/Microsoft account using the
// stored OAuth tokens (refreshing + rotating as needed) and the provider's
// incremental cursor. A revoked authorization flips the account to
// reauth_required health instead of crash-looping.
func syncOAuthAccount(acct pkgmodels.InboxAccount) error {
	token, err := freshAccessToken(&acct)
	if err != nil {
		if err == mailoauth.ErrReauthRequired {
			_ = db.GetCollection(pkgmodels.InboxAccountCollection).UpdateId(acct.Id, bson.M{"$set": bson.M{
				"auth_state":  "reauth_required",
				"sync_status": "error",
				"last_error":  err.Error(),
			}})
			return nil
		}
		return err
	}

	var msgs []Message
	var nextCursor string
	switch acct.Provider {
	case "gmail":
		msgs, nextCursor, err = FetchNewGmail(token, acct.SyncCursor)
	case "microsoft":
		msgs, nextCursor, err = FetchNewGraph(token, acct.SyncCursor)
	}
	if err != nil {
		if strings.Contains(err.Error(), "unauthorized") {
			_ = db.GetCollection(pkgmodels.InboxAccountCollection).UpdateId(acct.Id, bson.M{"$set": bson.M{
				"auth_state":  "reauth_required",
				"sync_status": "error",
				"last_error":  err.Error(),
			}})
			return nil
		}
		_ = db.GetCollection(pkgmodels.InboxAccountCollection).UpdateId(acct.Id, bson.M{"$set": bson.M{
			"sync_status": "error",
		}})
		return err
	}

	now := time.Now()
	update := bson.M{
		"last_synced_at":        now,
		"sync_status":           "synced",
		"auth_state":            "connected",
		"timestamps.updated_at": now,
	}
	if nextCursor != "" {
		update["sync_cursor"] = nextCursor
	}
	_ = db.GetCollection(pkgmodels.InboxAccountCollection).UpdateId(acct.Id, bson.M{"$set": update})

	deliverMessages(acct, msgs)
	return nil
}

// freshAccessToken returns a currently valid access token, refreshing (and
// persisting rotated tokens) when the stored one is expired or near expiry.
func freshAccessToken(acct *pkgmodels.InboxAccount) (string, error) {
	if acct.AccessTokenEncrypted != "" && acct.TokenExpiresAt != nil && time.Until(*acct.TokenExpiresAt) > 2*time.Minute {
		return utils.Decrypt(acct.AccessTokenEncrypted)
	}
	if acct.RefreshTokenEncrypted == "" {
		return "", mailoauth.ErrReauthRequired
	}
	refresh, err := utils.Decrypt(acct.RefreshTokenEncrypted)
	if err != nil {
		return "", err
	}
	cfg, err := mailoauth.ForProvider(acct.Provider)
	if err != nil {
		return "", err
	}
	tok, err := mailoauth.Refresh(cfg, refresh)
	if err != nil {
		return "", err
	}
	encAccess, err := utils.Encrypt(tok.AccessToken)
	if err != nil {
		return "", err
	}
	expires := time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	update := bson.M{
		"access_token_encrypted": encAccess,
		"token_expires_at":       expires,
	}
	if tok.RefreshToken != "" && tok.RefreshToken != refresh {
		if encRefresh, err := utils.Encrypt(tok.RefreshToken); err == nil {
			update["refresh_token_encrypted"] = encRefresh // rotation
		}
	}
	_ = db.GetCollection(pkgmodels.InboxAccountCollection).UpdateId(acct.Id, bson.M{"$set": update})
	acct.AccessTokenEncrypted = encAccess
	acct.TokenExpiresAt = &expires
	return tok.AccessToken, nil
}

// EncryptCredentials encrypts username+password for storage on InboxAccount.
func EncryptCredentials(username, password string) (string, error) {
	raw, err := json.Marshal(imapCredentials{Username: username, Password: password})
	if err != nil {
		return "", err
	}
	return utils.Encrypt(string(raw))
}
