package imap

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"

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

// InboundHandler is called for each new message fetched from IMAP.
// inbox_closer.go registers its pipeline here so the sync job can call it
// without creating an import cycle.
type InboundHandler func(tenantID, accountID bson.ObjectId, msg Message)

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
		"imap_host":              bson.M{"$exists": true, "$ne": ""},
		"timestamps.deleted_at":  nil,
	}).All(&accounts)
	if err != nil || len(accounts) == 0 {
		return
	}

	for _, acct := range accounts {
		if err := syncAccount(acct); err != nil {
			log.Printf("imap: sync failed for %s: %v", acct.EmailAddress, err)
		}
	}
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

	if registeredHandler == nil {
		return nil
	}

	for _, msg := range msgs {
		// Skip messages sent by the account itself
		if strings.EqualFold(msg.FromEmail, acct.EmailAddress) {
			continue
		}
		registeredHandler(acct.TenantID, acct.Id, msg)
	}

	return nil
}

// EncryptCredentials encrypts username+password for storage on InboxAccount.
func EncryptCredentials(username, password string) (string, error) {
	raw, err := json.Marshal(imapCredentials{Username: username, Password: password})
	if err != nil {
		return "", err
	}
	return utils.Encrypt(string(raw))
}
