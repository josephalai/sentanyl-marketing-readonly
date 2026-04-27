package routes

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/josephalai/sentanyl/marketing-service/internal/ai"
	imapsync "github.com/josephalai/sentanyl/marketing-service/internal/imap"
	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/utils"
	"gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"
)

type inboxClassification struct {
	PrimaryCategory     string   `json:"primary_category"`
	SecondaryCategories []string `json:"secondary_categories"`
	Intent              string   `json:"intent"`
	Urgency             string   `json:"urgency"`
	EmotionalTone       string   `json:"emotional_tone"`
	BuyerReadiness      string   `json:"buyer_readiness"`
	RiskLevel           string   `json:"risk_level"`
	ConfidenceScore     float64  `json:"confidence_score"`
	RecommendedAction   string   `json:"recommended_action"`
}

// EnsureInboxIndexes creates the tenant and queue indexes the inbox closer
// needs. Safe to call at startup; mgo treats existing compatible indexes as a
// no-op.
func EnsureInboxIndexes() {
	indexes := map[pkgmodels.MGCollection][]mgo.Index{
		pkgmodels.InboxAccountCollection: {
			{Key: []string{"tenant_id", "email_address"}, Background: true},
		},
		pkgmodels.InboxAgentCollection: {
			{Key: []string{"tenant_id", "inbox_account_id"}, Background: true},
		},
		pkgmodels.EmailThreadCollection: {
			{Key: []string{"tenant_id", "inbox_account_id", "provider_thread_id"}, Background: true},
			{Key: []string{"tenant_id", "-last_message_at"}, Background: true},
		},
		pkgmodels.EmailMessageCollection: {
			{Key: []string{"tenant_id", "email_thread_id", "received_at"}, Background: true},
			{Key: []string{"tenant_id", "provider_message_id"}, Background: true},
		},
		pkgmodels.AIReplyDraftCollection: {
			{Key: []string{"tenant_id", "status", "-created_at"}, Background: true},
			{Key: []string{"tenant_id", "send_after_at"}, Background: true},
		},
		pkgmodels.ContactMemoryCollection: {
			{Key: []string{"tenant_id", "contact_id"}, Unique: true, Background: true},
		},
		pkgmodels.BusinessBrainChunkCollection: {
			{Key: []string{"tenant_id", "business_brain_id"}, Background: true},
		},
	}
	for collection, list := range indexes {
		col := db.GetCollection(collection)
		for _, idx := range list {
			if err := col.EnsureIndex(idx); err != nil {
				log.Printf("inbox closer: failed ensuring index on %s: %v", collection, err)
			}
		}
	}
}

// RegisterInboxCloserRoutes mounts the tenant-facing Inbox Closer API. The
// caller must wrap the group in RequireTenantAuth.
func RegisterInboxCloserRoutes(rg *gin.RouterGroup) {
	rg.POST("/inbox/connect", handleInboxConnect)
	rg.GET("/inbox/accounts", handleInboxListAccounts)
	rg.DELETE("/inbox/accounts/:id", handleInboxDeleteAccount)
	rg.POST("/inbox/accounts/:id/sync", handleInboxSyncAccount)

	rg.POST("/inbox-agents", handleInboxCreateAgent)
	rg.GET("/inbox-agents", handleInboxListAgents)
	rg.GET("/inbox-agents/:id", handleInboxGetAgent)
	rg.PUT("/inbox-agents/:id", handleInboxUpdateAgent)
	rg.DELETE("/inbox-agents/:id", handleInboxDeleteAgent)

	rg.GET("/inbox/threads", handleInboxListThreads)
	rg.GET("/inbox/threads/:id", handleInboxGetThread)
	rg.GET("/inbox/messages/:id", handleInboxGetMessage)
	rg.POST("/inbox/process-inbound", handleInboxProcessInbound)

	rg.GET("/inbox/drafts", handleInboxListDrafts)
	rg.PUT("/inbox/drafts/:id", handleInboxUpdateDraft)
	rg.POST("/inbox/drafts/:id/approve", handleInboxApproveDraft)
	rg.POST("/inbox/drafts/:id/reject", handleInboxRejectDraft)
	rg.POST("/inbox/drafts/:id/regenerate", handleInboxRegenerateDraft)
	rg.POST("/inbox/drafts/:id/send", handleInboxSendDraft)

	rg.GET("/business-brain", handleInboxGetBusinessBrain)
	rg.POST("/business-brain/regenerate", handleInboxRegenerateBusinessBrain)
	rg.GET("/business-brain/status", handleInboxBusinessBrainStatus)

	rg.GET("/sales-playbooks", handleInboxListPlaybooks)
	rg.POST("/sales-playbooks", handleInboxCreatePlaybook)
	rg.GET("/sales-playbooks/:id", handleInboxGetPlaybook)
	rg.PUT("/sales-playbooks/:id", handleInboxUpdatePlaybook)
	rg.DELETE("/sales-playbooks/:id", handleInboxDeletePlaybook)

	rg.POST("/inbox/feedback", handleInboxFeedback)
	rg.POST("/inbox/drafts/:id/feedback", handleInboxDraftFeedback)

	rg.POST("/inbox-agents/:id/generate-voice-profile", handleGenerateVoiceProfile)
	rg.GET("/inbox-agents/:id/context-packs", handleInboxAgentListContextPacks)
	rg.PUT("/inbox-agents/:id/context-packs", handleInboxAgentSetContextPacks)
}

func tenantIDFromContext(c *gin.Context) (bson.ObjectId, bool) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return "", false
	}
	return tenantID, true
}

func findByIDOrPublic(collection pkgmodels.MGCollection, tenantID bson.ObjectId, raw string, out interface{}) error {
	q := bson.M{"tenant_id": tenantID, "timestamps.deleted_at": nil}
	if bson.IsObjectIdHex(raw) {
		q["_id"] = bson.ObjectIdHex(raw)
	} else {
		q["public_id"] = raw
	}
	return db.GetCollection(collection).Find(q).One(out)
}

func handleInboxConnect(c *gin.Context) {
	tenantID, ok := tenantIDFromContext(c)
	if !ok {
		return
	}
	var req struct {
		Provider     string `json:"provider"`
		EmailAddress string `json:"email_address" binding:"required"`
		DisplayName  string `json:"display_name"`
		// IMAP/SMTP fields — optional; omit for manual/dev mode
		IMAPHost string `json:"imap_host"`
		IMAPPort int    `json:"imap_port"`
		SMTPHost string `json:"smtp_host"`
		SMTPPort int    `json:"smtp_port"`
		Password string `json:"password"` // never stored; encrypted below
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	emailAddr := strings.ToLower(strings.TrimSpace(req.EmailAddress))
	provider := req.Provider
	if provider == "" {
		if req.IMAPHost != "" {
			provider = "imap"
		} else {
			provider = "manual"
		}
	}

	account := pkgmodels.NewInboxAccount(tenantID, provider, emailAddr)
	account.DisplayName = req.DisplayName

	if req.IMAPHost != "" {
		account.IMAPHost = req.IMAPHost
		account.IMAPPort = req.IMAPPort
		if account.IMAPPort == 0 {
			account.IMAPPort = 993
		}
		account.SMTPHost = req.SMTPHost
		account.SMTPPort = req.SMTPPort
		if account.SMTPPort == 0 {
			account.SMTPPort = 587
		}
		if req.Password != "" {
			enc, err := imapsync.EncryptCredentials(emailAddr, req.Password)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to encrypt credentials"})
				return
			}
			account.CredentialsEncrypted = enc
		}
		account.SyncStatus = "connected"
	}

	account.SetCreated()
	if err := db.GetCollection(pkgmodels.InboxAccountCollection).Insert(account); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save inbox account"})
		return
	}
	c.JSON(http.StatusCreated, account)
}

func handleInboxListAccounts(c *gin.Context) {
	tenantID, ok := tenantIDFromContext(c)
	if !ok {
		return
	}
	var accounts []pkgmodels.InboxAccount
	if err := db.GetCollection(pkgmodels.InboxAccountCollection).Find(bson.M{"tenant_id": tenantID, "timestamps.deleted_at": nil}).All(&accounts); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list inbox accounts"})
		return
	}
	if accounts == nil {
		accounts = []pkgmodels.InboxAccount{}
	}
	c.JSON(http.StatusOK, gin.H{"accounts": accounts})
}

func handleInboxDeleteAccount(c *gin.Context) {
	tenantID, ok := tenantIDFromContext(c)
	if !ok {
		return
	}
	var account pkgmodels.InboxAccount
	if err := findByIDOrPublic(pkgmodels.InboxAccountCollection, tenantID, c.Param("id"), &account); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "inbox account not found"})
		return
	}
	now := time.Now()
	if err := db.GetCollection(pkgmodels.InboxAccountCollection).UpdateId(account.Id, bson.M{"$set": bson.M{"timestamps.deleted_at": now, "sync_status": "disconnected"}}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete inbox account"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

func handleInboxSyncAccount(c *gin.Context) {
	tenantID, ok := tenantIDFromContext(c)
	if !ok {
		return
	}
	var account pkgmodels.InboxAccount
	if err := findByIDOrPublic(pkgmodels.InboxAccountCollection, tenantID, c.Param("id"), &account); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "inbox account not found"})
		return
	}
	now := time.Now()
	_ = db.GetCollection(pkgmodels.InboxAccountCollection).UpdateId(account.Id, bson.M{"$set": bson.M{"last_synced_at": now, "sync_status": "manual_ready"}})
	c.JSON(http.StatusAccepted, gin.H{"status": "manual_ready", "message": "Provider sync is not wired yet; use /inbox/process-inbound for dev ingestion."})
}

func handleInboxCreateAgent(c *gin.Context) {
	tenantID, ok := tenantIDFromContext(c)
	if !ok {
		return
	}
	var req struct {
		InboxAccountID     string `json:"inbox_account_id" binding:"required"`
		Name               string `json:"name"`
		ReplyIdentity      string `json:"reply_identity"`
		SendMode           string `json:"send_mode"`
		TimerMinutes       int    `json:"timer_minutes"`
		DoubleCheckEnabled *bool  `json:"double_check_enabled"`
		AutoSendEnabled    *bool  `json:"auto_send_enabled"`
		Status             string `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var account pkgmodels.InboxAccount
	if err := findByIDOrPublic(pkgmodels.InboxAccountCollection, tenantID, req.InboxAccountID, &account); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "inbox account not found"})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Inbox Closer"
	}
	agent := pkgmodels.NewInboxAgent(tenantID, account.Id, name)
	agent.ReplyIdentity = req.ReplyIdentity
	if agent.ReplyIdentity == "" {
		agent.ReplyIdentity = account.EmailAddress
	}
	if req.SendMode != "" {
		agent.SendMode = req.SendMode
	}
	if req.TimerMinutes > 0 {
		agent.TimerMinutes = req.TimerMinutes
	}
	if req.DoubleCheckEnabled != nil {
		agent.DoubleCheckEnabled = *req.DoubleCheckEnabled
	}
	if req.AutoSendEnabled != nil {
		agent.AutoSendEnabled = *req.AutoSendEnabled
	}
	if req.Status != "" {
		agent.Status = req.Status
	}
	agent.SetCreated()
	if err := db.GetCollection(pkgmodels.InboxAgentCollection).Insert(agent); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create inbox agent"})
		return
	}
	settings := pkgmodels.NewInboxAgentSettings(tenantID, agent.Id)
	settings.SetCreated()
	_ = db.GetCollection(pkgmodels.InboxAgentSettingsCollection).Insert(settings)
	c.JSON(http.StatusCreated, gin.H{"agent": agent, "settings": settings})
}

func handleInboxListAgents(c *gin.Context) {
	tenantID, ok := tenantIDFromContext(c)
	if !ok {
		return
	}
	var agents []pkgmodels.InboxAgent
	if err := db.GetCollection(pkgmodels.InboxAgentCollection).Find(bson.M{"tenant_id": tenantID, "timestamps.deleted_at": nil}).All(&agents); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list inbox agents"})
		return
	}
	if agents == nil {
		agents = []pkgmodels.InboxAgent{}
	}
	c.JSON(http.StatusOK, gin.H{"agents": agents})
}

func handleInboxGetAgent(c *gin.Context) {
	tenantID, ok := tenantIDFromContext(c)
	if !ok {
		return
	}
	var agent pkgmodels.InboxAgent
	if err := findByIDOrPublic(pkgmodels.InboxAgentCollection, tenantID, c.Param("id"), &agent); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "inbox agent not found"})
		return
	}
	var settings pkgmodels.InboxAgentSettings
	_ = db.GetCollection(pkgmodels.InboxAgentSettingsCollection).Find(bson.M{"tenant_id": tenantID, "inbox_agent_id": agent.Id, "timestamps.deleted_at": nil}).One(&settings)
	c.JSON(http.StatusOK, gin.H{"agent": agent, "settings": settings})
}

func handleInboxUpdateAgent(c *gin.Context) {
	tenantID, ok := tenantIDFromContext(c)
	if !ok {
		return
	}
	var agent pkgmodels.InboxAgent
	if err := findByIDOrPublic(pkgmodels.InboxAgentCollection, tenantID, c.Param("id"), &agent); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "inbox agent not found"})
		return
	}
	var req map[string]interface{}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	allowed := map[string]bool{"name": true, "reply_identity": true, "send_mode": true, "timer_minutes": true, "double_check_enabled": true, "auto_send_enabled": true, "status": true}
	update := bson.M{"timestamps.updated_at": time.Now()}
	for k, v := range req {
		if allowed[k] {
			update[k] = v
		}
	}
	if err := db.GetCollection(pkgmodels.InboxAgentCollection).UpdateId(agent.Id, bson.M{"$set": update}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update inbox agent"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"updated": true})
}

func handleInboxDeleteAgent(c *gin.Context) {
	tenantID, ok := tenantIDFromContext(c)
	if !ok {
		return
	}
	var agent pkgmodels.InboxAgent
	if err := findByIDOrPublic(pkgmodels.InboxAgentCollection, tenantID, c.Param("id"), &agent); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "inbox agent not found"})
		return
	}
	now := time.Now()
	if err := db.GetCollection(pkgmodels.InboxAgentCollection).UpdateId(agent.Id, bson.M{"$set": bson.M{"timestamps.deleted_at": now, "status": "deleted"}}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete inbox agent"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

func handleInboxListThreads(c *gin.Context) {
	tenantID, ok := tenantIDFromContext(c)
	if !ok {
		return
	}
	var threads []pkgmodels.EmailThread
	q := db.GetCollection(pkgmodels.EmailThreadCollection).Find(bson.M{"tenant_id": tenantID, "timestamps.deleted_at": nil}).Sort("-last_message_at").Limit(100)
	if err := q.All(&threads); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list threads"})
		return
	}
	if threads == nil {
		threads = []pkgmodels.EmailThread{}
	}
	c.JSON(http.StatusOK, gin.H{"threads": threads})
}

func handleInboxGetThread(c *gin.Context) {
	tenantID, ok := tenantIDFromContext(c)
	if !ok {
		return
	}
	var thread pkgmodels.EmailThread
	if err := findByIDOrPublic(pkgmodels.EmailThreadCollection, tenantID, c.Param("id"), &thread); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "thread not found"})
		return
	}
	var messages []pkgmodels.EmailMessage
	_ = db.GetCollection(pkgmodels.EmailMessageCollection).Find(bson.M{"tenant_id": tenantID, "email_thread_id": thread.Id, "timestamps.deleted_at": nil}).Sort("received_at", "sent_at").All(&messages)
	var drafts []pkgmodels.AIReplyDraft
	_ = db.GetCollection(pkgmodels.AIReplyDraftCollection).Find(bson.M{"tenant_id": tenantID, "email_thread_id": thread.Id, "timestamps.deleted_at": nil}).Sort("-created_at").All(&drafts)
	c.JSON(http.StatusOK, gin.H{"thread": thread, "messages": messages, "drafts": drafts})
}

func handleInboxGetMessage(c *gin.Context) {
	tenantID, ok := tenantIDFromContext(c)
	if !ok {
		return
	}
	var msg pkgmodels.EmailMessage
	if err := findByIDOrPublic(pkgmodels.EmailMessageCollection, tenantID, c.Param("id"), &msg); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "message not found"})
		return
	}
	c.JSON(http.StatusOK, msg)
}

func handleInboxProcessInbound(c *gin.Context) {
	tenantID, ok := tenantIDFromContext(c)
	if !ok {
		return
	}
	var req struct {
		AgentID           string   `json:"agent_id"`
		InboxAccountID    string   `json:"inbox_account_id"`
		ProviderThreadID  string   `json:"provider_thread_id"`
		ProviderMessageID string   `json:"provider_message_id"`
		FromEmail         string   `json:"from_email" binding:"required"`
		FromName          string   `json:"from_name"`
		To                []string `json:"to"`
		Subject           string   `json:"subject"`
		BodyText          string   `json:"body_text" binding:"required"`
		BodyHTML          string   `json:"body_html"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	agent, account, err := resolveInboxAgentForInbound(tenantID, req.AgentID, req.InboxAccountID, req.To)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	contact, err := findOrCreateInboxContact(tenantID, req.FromEmail, req.FromName)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to match contact"})
		return
	}
	thread, err := findOrCreateThread(tenantID, account.Id, req.ProviderThreadID, req.Subject, req.FromEmail, account.EmailAddress)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save thread"})
		return
	}
	msg := pkgmodels.NewEmailMessage(tenantID, thread.Id, "inbound")
	msg.ProviderMessageID = req.ProviderMessageID
	msg.FromEmail = strings.ToLower(strings.TrimSpace(req.FromEmail))
	msg.FromName = req.FromName
	msg.ToJSON = req.To
	msg.Subject = req.Subject
	msg.BodyText = req.BodyText
	msg.BodyHTML = req.BodyHTML
	now := time.Now()
	msg.ReceivedAt = &now
	msg.SetCreated()
	if err := db.GetCollection(pkgmodels.EmailMessageCollection).Insert(msg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save message"})
		return
	}
	_ = db.GetCollection(pkgmodels.EmailThreadCollection).UpdateId(thread.Id, bson.M{"$set": bson.M{"last_message_at": now}})

	classification := classifyInboundEmail(req.Subject + "\n" + req.BodyText)
	retrieved := retrieveInboxContextForAgent(tenantID, agent.Id, req.BodyText)
	reply := generateInboxDraftReply(tenantID, agent, contact, classification, req.BodyText, retrieved)

	// Double-check pass when enabled
	var audit *pkgmodels.AIReplyAudit
	if agent.DoubleCheckEnabled {
		if dc := doubleCheckDraft(reply, req.BodyText, classification); dc != nil {
			if dc.SuggestedRevision != "" {
				reply = dc.SuggestedRevision
			}
			if dc.FinalRiskLevel != "" {
				classification.RiskLevel = dc.FinalRiskLevel
			}
			if dc.FinalConfidenceScore > 0 {
				classification.ConfidenceScore = dc.FinalConfidenceScore
			}
			audit = dc
		}
	}

	draft := pkgmodels.NewAIReplyDraft(tenantID, agent.Id, thread.Id, msg.Id, contact.Id)
	draft.DraftBody = reply
	draft.Category = classification.PrimaryCategory
	draft.RiskLevel = classification.RiskLevel
	draft.ConfidenceScore = classification.ConfidenceScore
	draft.RecommendedAction = decideInboxAction(agent, classification)
	draft.ReasoningSummary = "Draft generated from inbound email, customer context, active agent settings, and available Business Brain/context chunks."
	if draft.RecommendedAction == "escalate" {
		draft.Status = pkgmodels.AIReplyDraftStatusEscalated
	}
	if draft.RecommendedAction == "timer_send" {
		t := now.Add(time.Duration(agent.TimerMinutes) * time.Minute)
		draft.SendAfterAt = &t
		draft.Status = pkgmodels.AIReplyDraftStatusTimer
	}
	draft.SetCreated()
	if err := db.GetCollection(pkgmodels.AIReplyDraftCollection).Insert(draft); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save draft"})
		return
	}
	if audit == nil {
		audit = createAuditForDraft(tenantID, draft, classification)
	} else {
		audit.Id = bson.NewObjectId()
		audit.PublicId = utils.GeneratePublicId()
		audit.TenantID = tenantID
		audit.AIReplyDraftID = draft.Id
		audit.SetCreated()
	}
	_ = db.GetCollection(pkgmodels.AIReplyAuditCollection).Insert(audit)
	logInboxActivity(tenantID, agent.Id, "email_received", thread.Id, msg.Id, contact.Id, bson.M{"category": classification.PrimaryCategory})
	logInboxActivity(tenantID, agent.Id, "reply_generated", thread.Id, msg.Id, contact.Id, bson.M{"draft_id": draft.Id.Hex(), "action": draft.RecommendedAction})
	updateContactMemoryFromDraft(tenantID, contact.Id, msg.Id, classification, req.BodyText)

	if draft.RecommendedAction == "auto_send" {
		_ = sendDraftNow(agent, account, *draft)
	}

	c.JSON(http.StatusCreated, gin.H{
		"classification": classification,
		"thread":         thread,
		"message":        msg,
		"contact":        contact,
		"draft":          draft,
		"audit":          audit,
		"retrieved":      retrieved,
	})
}

func handleInboxListDrafts(c *gin.Context) {
	tenantID, ok := tenantIDFromContext(c)
	if !ok {
		return
	}
	status := c.Query("status")
	query := bson.M{"tenant_id": tenantID, "timestamps.deleted_at": nil}
	if status != "" {
		query["status"] = status
	}
	var drafts []pkgmodels.AIReplyDraft
	if err := db.GetCollection(pkgmodels.AIReplyDraftCollection).Find(query).Sort("-created_at").Limit(200).All(&drafts); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list drafts"})
		return
	}
	if drafts == nil {
		drafts = []pkgmodels.AIReplyDraft{}
	}
	c.JSON(http.StatusOK, gin.H{"drafts": drafts})
}

func handleInboxUpdateDraft(c *gin.Context) {
	tenantID, ok := tenantIDFromContext(c)
	if !ok {
		return
	}
	var draft pkgmodels.AIReplyDraft
	if err := findByIDOrPublic(pkgmodels.AIReplyDraftCollection, tenantID, c.Param("id"), &draft); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "draft not found"})
		return
	}
	var req struct {
		DraftBody string `json:"draft_body" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	err := db.GetCollection(pkgmodels.AIReplyDraftCollection).UpdateId(draft.Id, bson.M{"$set": bson.M{
		"draft_body":            req.DraftBody,
		"timestamps.updated_at": time.Now(),
	}})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update draft"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"updated": true})
}

func handleInboxApproveDraft(c *gin.Context) {
	markDraft(c, pkgmodels.AIReplyDraftStatusApproved, "approved_at")
}

func handleInboxRejectDraft(c *gin.Context) {
	markDraft(c, pkgmodels.AIReplyDraftStatusRejected, "rejected_at")
}

func markDraft(c *gin.Context, status, dateField string) {
	tenantID, ok := tenantIDFromContext(c)
	if !ok {
		return
	}
	var draft pkgmodels.AIReplyDraft
	if err := findByIDOrPublic(pkgmodels.AIReplyDraftCollection, tenantID, c.Param("id"), &draft); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "draft not found"})
		return
	}
	now := time.Now()
	if err := db.GetCollection(pkgmodels.AIReplyDraftCollection).UpdateId(draft.Id, bson.M{"$set": bson.M{"status": status, dateField: now, "timestamps.updated_at": now}}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update draft"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": status})
}

func handleInboxRegenerateDraft(c *gin.Context) {
	tenantID, ok := tenantIDFromContext(c)
	if !ok {
		return
	}
	var draft pkgmodels.AIReplyDraft
	if err := findByIDOrPublic(pkgmodels.AIReplyDraftCollection, tenantID, c.Param("id"), &draft); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "draft not found"})
		return
	}
	var agent pkgmodels.InboxAgent
	var message pkgmodels.EmailMessage
	var contact pkgmodels.User
	if err := db.GetCollection(pkgmodels.InboxAgentCollection).FindId(draft.InboxAgentID).One(&agent); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "agent not found"})
		return
	}
	_ = db.GetCollection(pkgmodels.EmailMessageCollection).FindId(draft.EmailMessageID).One(&message)
	_ = db.GetCollection(pkgmodels.UserCollection).FindId(draft.ContactID).One(&contact)
	classification := classifyInboundEmail(message.Subject + "\n" + message.BodyText)
	retrieved := retrieveInboxContext(tenantID, message.BodyText)
	next := generateInboxDraftReply(tenantID, &agent, &contact, classification, message.BodyText, retrieved)
	now := time.Now()
	if err := db.GetCollection(pkgmodels.AIReplyDraftCollection).UpdateId(draft.Id, bson.M{"$set": bson.M{
		"draft_body":            next,
		"status":                pkgmodels.AIReplyDraftStatusDraft,
		"confidence_score":      classification.ConfidenceScore,
		"risk_level":            classification.RiskLevel,
		"category":              classification.PrimaryCategory,
		"timestamps.updated_at": now,
	}}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to regenerate draft"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"draft_body": next, "classification": classification})
}

func handleInboxSendDraft(c *gin.Context) {
	tenantID, ok := tenantIDFromContext(c)
	if !ok {
		return
	}
	var draft pkgmodels.AIReplyDraft
	if err := findByIDOrPublic(pkgmodels.AIReplyDraftCollection, tenantID, c.Param("id"), &draft); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "draft not found"})
		return
	}
	var agent pkgmodels.InboxAgent
	var account pkgmodels.InboxAccount
	if err := db.GetCollection(pkgmodels.InboxAgentCollection).FindId(draft.InboxAgentID).One(&agent); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "agent not found"})
		return
	}
	_ = db.GetCollection(pkgmodels.InboxAccountCollection).FindId(agent.InboxAccountID).One(&account)
	if draft.RiskLevel == pkgmodels.InboxRiskHigh {
		c.JSON(http.StatusConflict, gin.H{"error": "high-risk drafts require human handling and cannot be sent from this endpoint"})
		return
	}
	if err := sendDraftNow(&agent, &account, draft); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"sent": true})
}

func handleInboxGetBusinessBrain(c *gin.Context) {
	tenantID, ok := tenantIDFromContext(c)
	if !ok {
		return
	}
	brain, err := latestBusinessBrain(tenantID)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"brain": nil})
		return
	}
	c.JSON(http.StatusOK, gin.H{"brain": brain})
}

func handleInboxRegenerateBusinessBrain(c *gin.Context) {
	tenantID, ok := tenantIDFromContext(c)
	if !ok {
		return
	}
	brain, chunks, err := regenerateBusinessBrain(tenantID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to regenerate business brain"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"brain": brain, "chunks": len(chunks)})
}

func handleInboxBusinessBrainStatus(c *gin.Context) {
	tenantID, ok := tenantIDFromContext(c)
	if !ok {
		return
	}
	brain, err := latestBusinessBrain(tenantID)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"status": "missing"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": brain.Status, "generated_at": brain.GeneratedAt, "version": brain.Version})
}

func handleInboxListPlaybooks(c *gin.Context) {
	tenantID, ok := tenantIDFromContext(c)
	if !ok {
		return
	}
	var custom []pkgmodels.SalesPlaybook
	_ = db.GetCollection(pkgmodels.SalesPlaybookCollection).Find(bson.M{"tenant_id_nullable": tenantID, "timestamps.deleted_at": nil}).All(&custom)
	c.JSON(http.StatusOK, gin.H{"playbooks": append(builtinPlaybooks(), custom...)})
}

func handleInboxCreatePlaybook(c *gin.Context) {
	tenantID, ok := tenantIDFromContext(c)
	if !ok {
		return
	}
	var req pkgmodels.SalesPlaybook
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name required"})
		return
	}
	playbook := pkgmodels.NewSalesPlaybook(tenantID, req.Name)
	playbook.Description = req.Description
	playbook.Instructions = req.Instructions
	playbook.ObjectionRulesJSON = req.ObjectionRulesJSON
	playbook.ClosingRulesJSON = req.ClosingRulesJSON
	playbook.LengthRulesJSON = req.LengthRulesJSON
	playbook.RiskRulesJSON = req.RiskRulesJSON
	playbook.ExampleRepliesJSON = req.ExampleRepliesJSON
	playbook.SetCreated()
	if err := db.GetCollection(pkgmodels.SalesPlaybookCollection).Insert(playbook); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create playbook"})
		return
	}
	c.JSON(http.StatusCreated, playbook)
}

func handleInboxGetPlaybook(c *gin.Context) {
	tenantID, ok := tenantIDFromContext(c)
	if !ok {
		return
	}
	for _, p := range builtinPlaybooks() {
		if p.PublicId == c.Param("id") {
			c.JSON(http.StatusOK, p)
			return
		}
	}
	var playbook pkgmodels.SalesPlaybook
	if err := db.GetCollection(pkgmodels.SalesPlaybookCollection).Find(bson.M{"tenant_id_nullable": tenantID, "$or": []bson.M{{"public_id": c.Param("id")}, {"_id": objectIDIfHex(c.Param("id"))}}}).One(&playbook); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "playbook not found"})
		return
	}
	c.JSON(http.StatusOK, playbook)
}

func handleInboxUpdatePlaybook(c *gin.Context) {
	tenantID, ok := tenantIDFromContext(c)
	if !ok {
		return
	}
	var playbook pkgmodels.SalesPlaybook
	if err := findCustomPlaybook(tenantID, c.Param("id"), &playbook); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "custom playbook not found"})
		return
	}
	var req map[string]interface{}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	allowed := map[string]bool{"name": true, "description": true, "instructions": true, "objection_rules_json": true, "closing_rules_json": true, "length_rules_json": true, "risk_rules_json": true, "example_replies_json": true}
	update := bson.M{"timestamps.updated_at": time.Now()}
	for k, v := range req {
		if allowed[k] {
			update[k] = v
		}
	}
	if err := db.GetCollection(pkgmodels.SalesPlaybookCollection).UpdateId(playbook.Id, bson.M{"$set": update}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update playbook"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"updated": true})
}

func handleInboxDeletePlaybook(c *gin.Context) {
	tenantID, ok := tenantIDFromContext(c)
	if !ok {
		return
	}
	var playbook pkgmodels.SalesPlaybook
	if err := findCustomPlaybook(tenantID, c.Param("id"), &playbook); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "custom playbook not found"})
		return
	}
	now := time.Now()
	_ = db.GetCollection(pkgmodels.SalesPlaybookCollection).UpdateId(playbook.Id, bson.M{"$set": bson.M{"timestamps.deleted_at": now}})
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

func handleInboxFeedback(c *gin.Context) {
	createFeedback(c, "")
}

func handleInboxDraftFeedback(c *gin.Context) {
	createFeedback(c, c.Param("id"))
}

func createFeedback(c *gin.Context, draftIDParam string) {
	tenantID, ok := tenantIDFromContext(c)
	if !ok {
		return
	}
	var req struct {
		AIReplyDraftID string `json:"ai_reply_draft_id"`
		FeedbackType   string `json:"feedback_type" binding:"required"`
		OriginalReply  string `json:"original_reply"`
		EditedReply    string `json:"edited_reply"`
		Notes          string `json:"notes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	raw := draftIDParam
	if raw == "" {
		raw = req.AIReplyDraftID
	}
	event := pkgmodels.AIFeedbackEvent{
		Id:            bson.NewObjectId(),
		PublicId:      utils.GeneratePublicId(),
		TenantID:      tenantID,
		FeedbackType:  req.FeedbackType,
		OriginalReply: req.OriginalReply,
		EditedReply:   req.EditedReply,
		Notes:         req.Notes,
	}
	if raw != "" {
		var draft pkgmodels.AIReplyDraft
		if err := findByIDOrPublic(pkgmodels.AIReplyDraftCollection, tenantID, raw, &draft); err == nil {
			event.AIReplyDraftID = draft.Id
			event.ContactID = draft.ContactID
		}
	}
	event.SetCreated()
	if err := db.GetCollection(pkgmodels.AIFeedbackEventCollection).Insert(&event); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save feedback"})
		return
	}
	c.JSON(http.StatusCreated, event)
}

func resolveInboxAgentForInbound(tenantID bson.ObjectId, agentID, accountID string, recipients []string) (*pkgmodels.InboxAgent, *pkgmodels.InboxAccount, error) {
	var agent pkgmodels.InboxAgent
	if agentID != "" {
		if err := findByIDOrPublic(pkgmodels.InboxAgentCollection, tenantID, agentID, &agent); err != nil {
			return nil, nil, fmt.Errorf("agent not found")
		}
	} else {
		query := bson.M{"tenant_id": tenantID, "timestamps.deleted_at": nil}
		if accountID != "" && bson.IsObjectIdHex(accountID) {
			query["inbox_account_id"] = bson.ObjectIdHex(accountID)
		}
		if err := db.GetCollection(pkgmodels.InboxAgentCollection).Find(query).Sort("-created_at").One(&agent); err != nil {
			return createDefaultInboxAgent(tenantID, recipients)
		}
	}
	var account pkgmodels.InboxAccount
	if err := db.GetCollection(pkgmodels.InboxAccountCollection).FindId(agent.InboxAccountID).One(&account); err != nil {
		return nil, nil, fmt.Errorf("agent inbox account not found")
	}
	return &agent, &account, nil
}

func createDefaultInboxAgent(tenantID bson.ObjectId, recipients []string) (*pkgmodels.InboxAgent, *pkgmodels.InboxAccount, error) {
	email := "inbox@example.com"
	if len(recipients) > 0 && strings.TrimSpace(recipients[0]) != "" {
		email = strings.ToLower(strings.TrimSpace(recipients[0]))
	}
	account := pkgmodels.NewInboxAccount(tenantID, "manual", email)
	account.DisplayName = "Manual Inbox"
	account.SetCreated()
	if err := db.GetCollection(pkgmodels.InboxAccountCollection).Insert(account); err != nil {
		return nil, nil, err
	}
	agent := pkgmodels.NewInboxAgent(tenantID, account.Id, "Inbox Closer")
	agent.ReplyIdentity = email
	agent.Status = pkgmodels.InboxAgentStatusActive
	agent.SetCreated()
	if err := db.GetCollection(pkgmodels.InboxAgentCollection).Insert(agent); err != nil {
		return nil, nil, err
	}
	settings := pkgmodels.NewInboxAgentSettings(tenantID, agent.Id)
	settings.SetCreated()
	_ = db.GetCollection(pkgmodels.InboxAgentSettingsCollection).Insert(settings)
	return agent, account, nil
}

func findOrCreateInboxContact(tenantID bson.ObjectId, emailAddress, name string) (*pkgmodels.User, error) {
	emailAddress = strings.ToLower(strings.TrimSpace(emailAddress))
	var contact pkgmodels.User
	err := db.GetCollection(pkgmodels.UserCollection).Find(bson.M{"tenant_id": tenantID, "email": emailAddress, "timestamps.deleted_at": nil}).One(&contact)
	if err == nil {
		return &contact, nil
	}
	contact = *pkgmodels.NewUser()
	contact.PublicId = utils.GeneratePublicId()
	contact.TenantID = tenantID
	contact.Email = pkgmodels.EmailAddress(emailAddress)
	contact.Subscribed = true
	parts := strings.Fields(name)
	if len(parts) > 0 {
		contact.Name.First = parts[0]
	}
	if len(parts) > 1 {
		contact.Name.Last = strings.Join(parts[1:], " ")
	}
	contact.SetCreated()
	if err := db.GetCollection(pkgmodels.UserCollection).Insert(&contact); err != nil {
		return nil, err
	}
	return &contact, nil
}

func findOrCreateThread(tenantID, accountID bson.ObjectId, providerThreadID, subject string, participants ...string) (*pkgmodels.EmailThread, error) {
	var thread pkgmodels.EmailThread
	if providerThreadID != "" {
		err := db.GetCollection(pkgmodels.EmailThreadCollection).Find(bson.M{"tenant_id": tenantID, "inbox_account_id": accountID, "provider_thread_id": providerThreadID}).One(&thread)
		if err == nil {
			return &thread, nil
		}
	}
	thread = *pkgmodels.NewEmailThread(tenantID, accountID, subject)
	thread.ProviderThreadID = providerThreadID
	thread.ParticipantsJSON = participants
	now := time.Now()
	thread.LastMessageAt = &now
	thread.SetCreated()
	if err := db.GetCollection(pkgmodels.EmailThreadCollection).Insert(&thread); err != nil {
		return nil, err
	}
	return &thread, nil
}

func classifyInboundEmail(text string) inboxClassification {
	// Try LLM first; fall back to keyword heuristics.
	if provider, err := ai.GetConfiguredProvider(); err == nil && provider != nil {
		if clf, err := classifyWithAI(provider, text); err == nil {
			return clf
		}
	}
	return classifyKeyword(text)
}

func classifyWithAI(provider ai.SiteAIProvider, text string) (inboxClassification, error) {
	prompt := `Classify this inbound email. Return ONLY valid JSON matching this structure exactly:
{"primary_category":"","secondary_categories":[],"intent":"","urgency":"","emotional_tone":"","buyer_readiness":"","risk_level":"","confidence_score":0.0,"recommended_action":""}

primary_category options: sales_inquiry, objection, pricing_question, support_request, login_access_issue, refund_request, cancellation_request, complaint, testimonial, partnership_inquiry, general_inquiry, spam
risk_level options: low, medium, high
urgency options: low, medium, high
buyer_readiness options: unknown, cold, warm, hot
emotional_tone options: neutral, excited, anxious, frustrated, angry, sad
recommended_action options: auto_send, save_draft, draft_only, escalate, ignore

HIGH RISK required for: refund_request, cancellation_request, complaint, angry tone, legal threats.
MEDIUM RISK for: objections, dissatisfaction, complexity.
LOW RISK for: simple questions, sales inquiries, access issues.

Email:
` + truncate(text, 1200)

	raw, err := provider.GenerateText(ai.GenerateTextRequest{Prompt: prompt, MaxTokens: 200})
	if err != nil {
		return inboxClassification{}, err
	}
	// Extract JSON from response (model may wrap in markdown)
	raw = strings.TrimSpace(raw)
	if i := strings.Index(raw, "{"); i >= 0 {
		raw = raw[i:]
	}
	if i := strings.LastIndex(raw, "}"); i >= 0 {
		raw = raw[:i+1]
	}
	var clf inboxClassification
	if err := json.Unmarshal([]byte(raw), &clf); err != nil {
		return inboxClassification{}, err
	}
	if clf.SecondaryCategories == nil {
		clf.SecondaryCategories = []string{}
	}
	if clf.ConfidenceScore == 0 {
		clf.ConfidenceScore = 0.85
	}
	return clf, nil
}

func classifyKeyword(text string) inboxClassification {
	lower := strings.ToLower(text)
	out := inboxClassification{
		PrimaryCategory:     "general_inquiry",
		SecondaryCategories: []string{},
		Intent:              "asks_for_information",
		Urgency:             "medium",
		EmotionalTone:       "neutral",
		BuyerReadiness:      "unknown",
		RiskLevel:           pkgmodels.InboxRiskLow,
		ConfidenceScore:     0.75,
		RecommendedAction:   "save_draft",
	}
	containsAny := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(lower, w) {
				return true
			}
		}
		return false
	}
	if containsAny("refund", "money back", "chargeback", "cancel", "cancellation", "lawyer", "legal", "fraud") {
		out.PrimaryCategory = "refund_or_cancellation"
		out.RiskLevel = pkgmodels.InboxRiskHigh
		out.RecommendedAction = "draft_only"
		return out
	}
	if containsAny("angry", "upset", "complaint", "scam", "did not work", "doesn't work", "terrible") {
		out.PrimaryCategory = "complaint"
		out.EmotionalTone = "frustrated"
		out.RiskLevel = pkgmodels.InboxRiskHigh
		return out
	}
	if containsAny("price", "pricing", "cost", "how much", "$") {
		out.PrimaryCategory = "pricing_question"
		out.BuyerReadiness = "warm"
		out.ConfidenceScore = 0.82
	}
	if containsAny("buy", "purchase", "checkout", "sign up", "interested", "book", "call") {
		out.PrimaryCategory = "sales_inquiry"
		out.BuyerReadiness = "warm"
		out.ConfidenceScore = 0.83
	}
	if containsAny("login", "access", "password", "where do i find", "cannot access") {
		out.PrimaryCategory = "login_access_issue"
		out.ConfidenceScore = 0.84
	}
	if containsAny("urgent", "asap", "today", "right now") {
		out.Urgency = "high"
	}
	if containsAny("worried", "anxious", "scared", "stressed") {
		out.EmotionalTone = "anxious"
		out.RiskLevel = pkgmodels.InboxRiskMedium
	}
	return out
}

func retrieveInboxContext(tenantID bson.ObjectId, query string) []string {
	return retrieveInboxContextForAgent(tenantID, "", query)
}

func retrieveInboxContextForAgent(tenantID, agentID bson.ObjectId, query string) []string {
	terms := significantTerms(query)
	type scored struct {
		text  string
		score int
	}
	var all []scored

	// Business Brain chunks
	var brainChunks []pkgmodels.BusinessBrainChunk
	brain, err := latestBusinessBrain(tenantID)
	if err != nil {
		brain, _, err = regenerateBusinessBrain(tenantID)
	}
	if err == nil {
		_ = db.GetCollection(pkgmodels.BusinessBrainChunkCollection).Find(bson.M{"tenant_id": tenantID, "business_brain_id": brain.Id}).All(&brainChunks)
	}
	for _, chunk := range brainChunks {
		score := termScore(terms, chunk.ChunkContent)
		if score > 0 || len(all) < 4 {
			all = append(all, scored{text: chunk.ChunkContent, score: score})
		}
	}

	// Active context pack chunks for this agent
	if agentID != "" {
		var settings pkgmodels.InboxAgentSettings
		_ = db.GetCollection(pkgmodels.InboxAgentSettingsCollection).Find(bson.M{"tenant_id": tenantID, "inbox_agent_id": agentID}).One(&settings)
		if len(settings.ActiveContextPackIDs) > 0 {
			var packs []pkgmodels.ContextPack
			_ = db.GetCollection(pkgmodels.ContextPackCollection).Find(bson.M{"tenant_id": tenantID, "_id": bson.M{"$in": settings.ActiveContextPackIDs}, "timestamps.deleted_at": nil}).All(&packs)
			for _, pack := range packs {
				for _, chunk := range pack.Chunks {
					score := termScore(terms, chunk.Text)
					if score > 0 || len(all) < 6 {
						all = append(all, scored{text: chunk.Text, score: score})
					}
				}
			}
		}
	}

	sort.SliceStable(all, func(i, j int) bool { return all[i].score > all[j].score })
	limit := 8
	if len(all) < limit {
		limit = len(all)
	}
	out := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, all[i].text)
	}
	return out
}

func termScore(terms []string, text string) int {
	lower := strings.ToLower(text)
	score := 0
	for _, t := range terms {
		if strings.Contains(lower, t) {
			score++
		}
	}
	return score
}

func significantTerms(text string) []string {
	raw := strings.Fields(strings.ToLower(text))
	stop := map[string]bool{"the": true, "and": true, "for": true, "you": true, "with": true, "that": true, "this": true, "can": true, "how": true, "what": true, "when": true, "where": true, "does": true, "have": true}
	seen := map[string]bool{}
	var terms []string
	for _, w := range raw {
		w = strings.Trim(w, ".,!?;:'\"()[]{}")
		if len(w) < 4 || stop[w] || seen[w] {
			continue
		}
		seen[w] = true
		terms = append(terms, w)
	}
	return terms
}

func generateInboxDraftReply(tenantID bson.ObjectId, agent *pkgmodels.InboxAgent, contact *pkgmodels.User, classification inboxClassification, emailBody string, contextChunks []string) string {
	brand := resolveBrandProfileForInbox(tenantID)
	var memory pkgmodels.ContactMemory
	_ = db.GetCollection(pkgmodels.ContactMemoryCollection).Find(bson.M{"tenant_id": tenantID, "contact_id": contact.Id}).One(&memory)
	var settings pkgmodels.InboxAgentSettings
	_ = db.GetCollection(pkgmodels.InboxAgentSettingsCollection).Find(bson.M{"tenant_id": tenantID, "inbox_agent_id": agent.Id}).One(&settings)
	var playbook pkgmodels.SalesPlaybook
	if agent.ActivePlaybookID != "" {
		_ = db.GetCollection(pkgmodels.SalesPlaybookCollection).FindId(agent.ActivePlaybookID).One(&playbook)
	}
	prompt := buildInboxReplyPrompt(agent, contact, classification, emailBody, contextChunks, brand, memory, settings, playbook)
	if provider, err := ai.GetConfiguredProvider(); err == nil && provider != nil {
		if text, err := provider.GenerateText(ai.GenerateTextRequest{
			Prompt:        prompt,
			ReferenceText: strings.Join(contextChunks, "\n\n---\n\n"),
			BrandProfile:  brand,
			MaxTokens:     220,
		}); err == nil && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return heuristicInboxReply(classification)
}

func buildInboxReplyPrompt(agent *pkgmodels.InboxAgent, contact *pkgmodels.User, classification inboxClassification, emailBody string, contextChunks []string, brand string, memory pkgmodels.ContactMemory, settings pkgmodels.InboxAgentSettings, playbook pkgmodels.SalesPlaybook) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Write a human email reply on behalf of %s.\n", agent.ReplyIdentity)
	b.WriteString("Rules: do not mention AI, do not sound robotic, do not over-explain, do not invent product facts or policies.\n")

	// Length doctrine
	lengthMode := settings.ReplyLengthMode
	if lengthMode == "" {
		lengthMode = "short"
	}
	fmt.Fprintf(&b, "Reply length mode: %s. Directness: %d/10. Formality: %d/10.\n", lengthMode, settings.DirectnessLevel, settings.FormalityLevel)
	if settings.EmojiUsage == "none" || settings.EmojiUsage == "" {
		b.WriteString("Do not use emojis.\n")
	}
	if len(settings.ForbiddenPhrasesJSON) > 0 {
		fmt.Fprintf(&b, "Never use these phrases: %s.\n", strings.Join(settings.ForbiddenPhrasesJSON, ", "))
	}

	// Playbook
	if playbook.Name != "" {
		fmt.Fprintf(&b, "Sales playbook: %s. %s\n", playbook.Name, playbook.Instructions)
		if len(playbook.LengthRulesJSON) > 0 {
			fmt.Fprintf(&b, "Length rules: %s\n", strings.Join(playbook.LengthRulesJSON, ". "))
		}
	}

	// Classification context
	fmt.Fprintf(&b, "Email category: %s. Risk: %s. Buyer readiness: %s. Emotional tone: %s.\n",
		classification.PrimaryCategory, classification.RiskLevel, classification.BuyerReadiness, classification.EmotionalTone)

	// Contact context
	if contact.Name.First != "" {
		fmt.Fprintf(&b, "Sender: %s.\n", contact.Name.First)
	}
	if memory.Summary != "" {
		fmt.Fprintf(&b, "Contact history: %s\n", memory.Summary)
	}
	if len(memory.InterestsJSON) > 0 {
		fmt.Fprintf(&b, "Known interests: %s.\n", strings.Join(memory.InterestsJSON, ", "))
	}
	if len(memory.ObjectionsJSON) > 0 {
		fmt.Fprintf(&b, "Past objections: %s.\n", strings.Join(memory.ObjectionsJSON, ", "))
	}

	// Brand/voice
	if brand != "" {
		fmt.Fprintf(&b, "Tenant voice profile:\n%s\n", brand)
	}
	if settings.CustomInstructions != "" {
		fmt.Fprintf(&b, "Additional instructions: %s\n", settings.CustomInstructions)
	}
	if len(contextChunks) > 0 {
		b.WriteString("Business context is provided as reference. Use only what is relevant and accurate.\n")
	}

	b.WriteString("\nInbound email:\n")
	b.WriteString(emailBody)
	b.WriteString("\n\nReturn only the reply body text. No subject line. No markdown.")
	return b.String()
}

// doubleCheckDraft runs a second LLM pass to audit the generated reply.
// Returns the audit result and optionally a revised draft body.
func doubleCheckDraft(draftBody, emailBody string, classification inboxClassification) *pkgmodels.AIReplyAudit {
	provider, err := ai.GetConfiguredProvider()
	if err != nil || provider == nil {
		return nil
	}
	prompt := fmt.Sprintf(`Audit this AI-generated email reply before it is sent.

Original inbound email:
%s

Generated reply:
%s

Category: %s. Risk: %s. Confidence: %.2f.

Check and return ONLY valid JSON:
{"approved_for_send":true,"issues":[],"suggested_revision":null,"final_risk_level":"%s","final_confidence_score":0.0}

Issues to check: factual accuracy, correct tone, not too long, not robotic, no hallucinated policies, no invented product details, answered the question, appropriate risk level.
Set approved_for_send to false if risk is high or any serious issue found.
suggested_revision should be null or a corrected reply string.`,
		truncate(emailBody, 600),
		truncate(draftBody, 600),
		classification.PrimaryCategory,
		classification.RiskLevel,
		classification.ConfidenceScore,
		classification.RiskLevel,
	)

	raw, err := provider.GenerateText(ai.GenerateTextRequest{Prompt: prompt, MaxTokens: 300})
	if err != nil {
		return nil
	}
	raw = strings.TrimSpace(raw)
	if i := strings.Index(raw, "{"); i >= 0 {
		raw = raw[i:]
	}
	if i := strings.LastIndex(raw, "}"); i >= 0 {
		raw = raw[:i+1]
	}
	var result struct {
		ApprovedForSend    bool     `json:"approved_for_send"`
		Issues             []string `json:"issues"`
		SuggestedRevision  *string  `json:"suggested_revision"`
		FinalRiskLevel     string   `json:"final_risk_level"`
		FinalConfidenceScore float64 `json:"final_confidence_score"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil
	}
	audit := &pkgmodels.AIReplyAudit{
		Id:                   bson.NewObjectId(),
		PublicId:             utils.GeneratePublicId(),
		ApprovedForSend:      result.ApprovedForSend,
		IssuesJSON:           result.Issues,
		FinalRiskLevel:       result.FinalRiskLevel,
		FinalConfidenceScore: result.FinalConfidenceScore,
	}
	if result.SuggestedRevision != nil {
		audit.SuggestedRevision = *result.SuggestedRevision
	}
	if audit.FinalRiskLevel == "" {
		audit.FinalRiskLevel = classification.RiskLevel
	}
	return audit
}

func heuristicInboxReply(classification inboxClassification) string {
	switch classification.PrimaryCategory {
	case "refund_or_cancellation":
		return "I hear you. I’m going to look at this properly before giving you the wrong answer here."
	case "complaint":
		return "I understand. Let me look into what happened and come back to you properly."
	case "login_access_issue":
		return "Yes, I can help with that. Try the login page first, and if it still does not show up, reply with the email you used at checkout."
	case "pricing_question":
		return "Yes. The best next step is to use the checkout page for the current price and options."
	case "sales_inquiry":
		return "Yes, this sounds like it could be a fit. The clean next step is to start with the offer page and make sure it matches what you want."
	default:
		return "Yes, I can help with this. Send me the one detail you most want answered and I’ll point you in the right direction."
	}
}

func decideInboxAction(agent *pkgmodels.InboxAgent, c inboxClassification) string {
	if c.RiskLevel == pkgmodels.InboxRiskHigh || c.ConfidenceScore < 0.70 {
		return "escalate"
	}
	if agent.SendMode == pkgmodels.InboxSendModeDraftOnly || c.RiskLevel == pkgmodels.InboxRiskMedium || c.ConfidenceScore < 0.90 {
		return "save_draft"
	}
	if agent.SendMode == pkgmodels.InboxSendModeTimerApproval {
		return "timer_send"
	}
	if (agent.SendMode == pkgmodels.InboxSendModeAutoSend || agent.SendMode == pkgmodels.InboxSendModeSmartSend) && agent.AutoSendEnabled {
		return "auto_send"
	}
	return "save_draft"
}

func createAuditForDraft(tenantID bson.ObjectId, draft *pkgmodels.AIReplyDraft, c inboxClassification) *pkgmodels.AIReplyAudit {
	audit := &pkgmodels.AIReplyAudit{
		Id:                   bson.NewObjectId(),
		PublicId:             utils.GeneratePublicId(),
		TenantID:             tenantID,
		AIReplyDraftID:       draft.Id,
		ApprovedForSend:      c.RiskLevel == pkgmodels.InboxRiskLow && c.ConfidenceScore >= 0.90,
		FinalRiskLevel:       c.RiskLevel,
		FinalConfidenceScore: c.ConfidenceScore,
	}
	if c.RiskLevel == pkgmodels.InboxRiskHigh {
		audit.IssuesJSON = append(audit.IssuesJSON, "High-risk category requires human approval.")
	}
	audit.SetCreated()
	return audit
}

func sendDraftNow(agent *pkgmodels.InboxAgent, account *pkgmodels.InboxAccount, draft pkgmodels.AIReplyDraft) error {
	var thread pkgmodels.EmailThread
	var original pkgmodels.EmailMessage
	if err := db.GetCollection(pkgmodels.EmailThreadCollection).FindId(draft.EmailThreadID).One(&thread); err != nil {
		return fmt.Errorf("thread not found")
	}
	if err := db.GetCollection(pkgmodels.EmailMessageCollection).FindId(draft.EmailMessageID).One(&original); err != nil {
		return fmt.Errorf("original message not found")
	}
	from := agent.ReplyIdentity
	if from == "" {
		from = account.EmailAddress
	}
	if from == "" {
		return fmt.Errorf("agent has no reply identity")
	}
	subject := thread.Subject
	if subject != "" && !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}
	htmlBody := "<p>" + strings.ReplaceAll(html.EscapeString(draft.DraftBody), "\n", "<br>") + "</p>"
	if err := sendViaAccount(account, from, original.FromEmail, subject, htmlBody); err != nil {
		return fmt.Errorf("send failed: %w", err)
	}
	now := time.Now()
	outbound := pkgmodels.NewEmailMessage(draft.TenantID, draft.EmailThreadID, "outbound")
	outbound.FromEmail = from
	outbound.ToJSON = []string{original.FromEmail}
	outbound.Subject = subject
	outbound.BodyText = draft.DraftBody
	outbound.BodyHTML = htmlBody
	outbound.SentAt = &now
	outbound.SetCreated()
	_ = db.GetCollection(pkgmodels.EmailMessageCollection).Insert(outbound)
	return db.GetCollection(pkgmodels.AIReplyDraftCollection).UpdateId(draft.Id, bson.M{"$set": bson.M{"status": pkgmodels.AIReplyDraftStatusSent, "sent_at": now, "timestamps.updated_at": now}})
}

func logInboxActivity(tenantID, agentID bson.ObjectId, eventType string, threadID, messageID, contactID bson.ObjectId, meta bson.M) {
	entry := pkgmodels.NewAIActivityLog(tenantID, agentID, eventType)
	entry.EmailThreadID = threadID
	entry.EmailMessageID = messageID
	entry.ContactID = contactID
	entry.MetadataJSON = meta
	_ = db.GetCollection(pkgmodels.AIActivityLogCollection).Insert(entry)
}

func updateContactMemoryFromDraft(tenantID, contactID, messageID bson.ObjectId, c inboxClassification, body string) {
	summary := fmt.Sprintf("Latest inbound: %s; category=%s; tone=%s.", truncate(body, 180), c.PrimaryCategory, c.EmotionalTone)
	var existing pkgmodels.ContactMemory
	err := db.GetCollection(pkgmodels.ContactMemoryCollection).Find(bson.M{"tenant_id": tenantID, "contact_id": contactID}).One(&existing)
	now := time.Now()
	if err != nil {
		mem := pkgmodels.NewContactMemory(tenantID, contactID)
		mem.Summary = summary
		mem.LastUpdatedFromMessageID = messageID
		mem.SetCreated()
		_ = db.GetCollection(pkgmodels.ContactMemoryCollection).Insert(mem)
		return
	}
	_ = db.GetCollection(pkgmodels.ContactMemoryCollection).UpdateId(existing.Id, bson.M{"$set": bson.M{"summary": summary, "last_updated_from_message_id": messageID, "timestamps.updated_at": now}})
}

func regenerateBusinessBrain(tenantID bson.ObjectId) (*pkgmodels.BusinessBrain, []pkgmodels.BusinessBrainChunk, error) {
	markdown := buildBusinessBrainMarkdown(tenantID)
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(markdown)))
	prev, _ := latestBusinessBrain(tenantID)
	version := 1
	if prev != nil {
		version = prev.Version + 1
	}
	now := time.Now()
	brain := pkgmodels.NewBusinessBrain(tenantID)
	brain.MarkdownContent = markdown
	brain.SourceHash = hash
	brain.Version = version
	brain.GeneratedAt = &now
	brain.SetCreated()
	if err := db.GetCollection(pkgmodels.BusinessBrainCollection).Insert(brain); err != nil {
		return nil, nil, err
	}
	chunks := chunkBusinessBrain(tenantID, brain.Id, markdown)
	for i := range chunks {
		chunks[i].SetCreated()
		if err := db.GetCollection(pkgmodels.BusinessBrainChunkCollection).Insert(&chunks[i]); err != nil {
			return nil, nil, err
		}
	}
	return brain, chunks, nil
}

func buildBusinessBrainMarkdown(tenantID bson.ObjectId) string {
	var b strings.Builder
	b.WriteString("# Business Brain\n\n")
	var profile pkgmodels.BrandProfile
	if err := db.GetCollection(pkgmodels.BrandProfileCollection).Find(bson.M{"tenant_id": tenantID}).One(&profile); err == nil {
		b.WriteString("## Tenant Overview\n")
		fmt.Fprintf(&b, "- Voice: %s\n- Positioning: %s\n- Audience: %s\n- CTA style: %s\n\n", profile.VoiceTone, profile.Positioning, profile.AvatarDescription, profile.CTAStyle)
	}
	var products []pkgmodels.Product
	_ = db.GetCollection(pkgmodels.ProductCollection).Find(bson.M{"tenant_id": tenantID, "timestamps.deleted_at": nil}).All(&products)
	b.WriteString("## Products\n")
	for _, p := range products {
		fmt.Fprintf(&b, "### %s\n- ID: %s\n- Type: %s\n- Status: %s\n- Price: %.2f %s\n- Description: %s\n", p.Name, p.Id.Hex(), p.ProductType, p.Status, p.Price, p.Currency, p.Description)
		if p.Coaching != nil {
			fmt.Fprintf(&b, "- Coaching scheduling provider: %s\n- Booking URL: %s\n", p.Coaching.Scheduling.Provider, p.Coaching.Scheduling.CustomURL)
		}
		b.WriteString("\n")
	}
	var offers []pkgmodels.Offer
	_ = db.GetCollection(pkgmodels.OfferCollection).Find(bson.M{"tenant_id": tenantID, "timestamps.deleted_at": nil}).All(&offers)
	b.WriteString("## Offers\n")
	for _, o := range offers {
		fmt.Fprintf(&b, "### %s\n- ID: %s\n- Pricing model: %s\n- Amount: %d %s\n- Stripe price: %s\n- Included products: %v\n\n", o.Title, o.Id.Hex(), o.PricingModel, o.Amount, o.Currency, o.StripePriceID, o.IncludedProducts)
	}
	var packs []pkgmodels.ContextPack
	_ = db.GetCollection(pkgmodels.ContextPackCollection).Find(bson.M{"tenant_id": tenantID, "timestamps.deleted_at": nil}).All(&packs)
	b.WriteString("## Uploaded Context Documents\n")
	for _, p := range packs {
		fmt.Fprintf(&b, "### %s\n- File: %s\n- Type: %s\n- Notes: %s\n", p.Name, p.FileName, p.FileType, p.Notes)
		for _, ch := range p.Chunks {
			fmt.Fprintf(&b, "%s\n", truncate(ch.Text, 700))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func chunkBusinessBrain(tenantID, brainID bson.ObjectId, markdown string) []pkgmodels.BusinessBrainChunk {
	sections := strings.Split(markdown, "\n### ")
	var chunks []pkgmodels.BusinessBrainChunk
	for i, s := range sections {
		title := "Overview"
		content := s
		if i > 0 {
			lines := strings.SplitN(s, "\n", 2)
			title = strings.TrimSpace(lines[0])
			if len(lines) > 1 {
				content = "### " + s
			}
		}
		if strings.TrimSpace(content) == "" {
			continue
		}
		chunks = append(chunks, pkgmodels.BusinessBrainChunk{
			Id:              bson.NewObjectId(),
			PublicId:        utils.GeneratePublicId(),
			TenantID:        tenantID,
			BusinessBrainID: brainID,
			SourceType:      "business_brain",
			ChunkTitle:      title,
			ChunkContent:    content,
		})
	}
	return chunks
}

func latestBusinessBrain(tenantID bson.ObjectId) (*pkgmodels.BusinessBrain, error) {
	var brain pkgmodels.BusinessBrain
	err := db.GetCollection(pkgmodels.BusinessBrainCollection).Find(bson.M{"tenant_id": tenantID, "timestamps.deleted_at": nil}).Sort("-version", "-created_at").One(&brain)
	if err != nil {
		return nil, err
	}
	return &brain, nil
}

func resolveBrandProfileForInbox(tenantID bson.ObjectId) string {
	var profile pkgmodels.BrandProfile
	if err := db.GetCollection(pkgmodels.BrandProfileCollection).Find(bson.M{"tenant_id": tenantID}).One(&profile); err != nil {
		return ""
	}
	var parts []string
	if profile.VoiceTone != "" {
		parts = append(parts, "Voice/Tone: "+profile.VoiceTone)
	}
	if profile.Positioning != "" {
		parts = append(parts, "Positioning: "+profile.Positioning)
	}
	if profile.AvatarDescription != "" {
		parts = append(parts, "Ideal customer: "+profile.AvatarDescription)
	}
	if profile.DefaultGenPrefs != "" {
		parts = append(parts, "Preferences: "+profile.DefaultGenPrefs)
	}
	return strings.Join(parts, "\n")
}

func builtinPlaybooks() []pkgmodels.SalesPlaybook {
	names := []struct {
		id, name, desc, instructions string
	}{
		{"builtin-direct-response-authority", "Direct Response Authority", "Direct, certain, offer-focused.", "Be brief, confident, and move toward a clear next step."},
		{"builtin-casual-trust-builder", "Casual Trust Builder", "Warm, conversational, low-pressure.", "Answer simply, reduce friction, and ask one useful question when needed."},
		{"builtin-support-first", "Support-First Mode", "Solve first, sell only when useful.", "Prioritize resolution and avoid aggressive selling."},
		{"builtin-refund-rescue", "Refund Rescue Mode", "Escalation-first refund handling.", "Draft only. Acknowledge calmly and avoid policy invention."},
		{"builtin-busy-expert", "Busy Expert Mode", "Short, direct, authoritative.", "Use the fewest natural words that answer the sender."},
	}
	out := make([]pkgmodels.SalesPlaybook, 0, len(names))
	for _, n := range names {
		out = append(out, pkgmodels.SalesPlaybook{
			PublicId:        n.id,
			Name:            n.name,
			Type:            "builtin",
			Description:     n.desc,
			Instructions:    n.instructions,
			LengthRulesJSON: []string{"Prefer short natural replies.", "Do not sound like a generic assistant."},
		})
	}
	return out
}

func findCustomPlaybook(tenantID bson.ObjectId, raw string, out *pkgmodels.SalesPlaybook) error {
	q := bson.M{"tenant_id_nullable": tenantID, "timestamps.deleted_at": nil}
	if bson.IsObjectIdHex(raw) {
		q["_id"] = bson.ObjectIdHex(raw)
	} else {
		q["public_id"] = raw
	}
	return db.GetCollection(pkgmodels.SalesPlaybookCollection).Find(q).One(out)
}

func objectIDIfHex(raw string) bson.ObjectId {
	if bson.IsObjectIdHex(raw) {
		return bson.ObjectIdHex(raw)
	}
	return ""
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// sendViaAccount dispatches the reply through the account's own SMTP credentials
// if configured, otherwise falls back to the global smtpProvider (dev/MailHog).
func sendViaAccount(account *pkgmodels.InboxAccount, from, to, subject, htmlBody string) error {
	if account != nil && account.SMTPHost != "" && account.CredentialsEncrypted != "" {
		raw, err := utils.Decrypt(account.CredentialsEncrypted)
		if err != nil {
			return fmt.Errorf("decrypt credentials: %w", err)
		}
		var creds struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.Unmarshal([]byte(raw), &creds); err != nil {
			return err
		}
		return imapsync.SendReply(account.SMTPHost, account.SMTPPort, creds.Username, creds.Password, from, to, subject, htmlBody)
	}
	if smtpProvider != nil {
		return smtpProvider.SendEmail(from, to, subject, htmlBody, from)
	}
	log.Printf("inbox: no send provider configured — draft not sent to %s", to)
	return nil
}

// RegisterIMAPHandler wires the IMAP sync loop into the inbound processing pipeline.
// Called from main after routes are set up.
func RegisterIMAPHandler() {
	imapsync.RegisterHandler(func(tenantID, accountID bson.ObjectId, msg imapsync.Message) {
		agent, account, err := resolveInboxAgentForInbound(tenantID, "", accountID.Hex(), msg.ToList)
		if err != nil {
			log.Printf("imap: no agent for account %s: %v", accountID.Hex(), err)
			return
		}
		contact, err := findOrCreateInboxContact(tenantID, msg.FromEmail, msg.FromName)
		if err != nil {
			log.Printf("imap: contact error for %s: %v", msg.FromEmail, err)
			return
		}
		thread, err := findOrCreateThread(tenantID, account.Id, msg.InReplyTo, msg.Subject, msg.FromEmail, account.EmailAddress)
		if err != nil {
			log.Printf("imap: thread error: %v", err)
			return
		}
		now := msg.Date
		if now.IsZero() {
			now = time.Now()
		}
		emailMsg := pkgmodels.NewEmailMessage(tenantID, thread.Id, "inbound")
		emailMsg.ProviderMessageID = msg.MessageID
		emailMsg.FromEmail = msg.FromEmail
		emailMsg.FromName = msg.FromName
		emailMsg.ToJSON = msg.ToList
		emailMsg.Subject = msg.Subject
		emailMsg.BodyText = msg.BodyText
		emailMsg.ReceivedAt = &now
		emailMsg.SetCreated()
		if err := db.GetCollection(pkgmodels.EmailMessageCollection).Insert(emailMsg); err != nil {
			log.Printf("imap: failed to store message from %s: %v", msg.FromEmail, err)
			return
		}
		_ = db.GetCollection(pkgmodels.EmailThreadCollection).UpdateId(thread.Id, bson.M{"$set": bson.M{"last_message_at": now}})

		classification := classifyInboundEmail(msg.Subject + "\n" + msg.BodyText)
		retrieved := retrieveInboxContext(tenantID, msg.BodyText)
		reply := generateInboxDraftReply(tenantID, agent, contact, classification, msg.BodyText, retrieved)
		draft := pkgmodels.NewAIReplyDraft(tenantID, agent.Id, thread.Id, emailMsg.Id, contact.Id)
		draft.DraftBody = reply
		draft.Category = classification.PrimaryCategory
		draft.RiskLevel = classification.RiskLevel
		draft.ConfidenceScore = classification.ConfidenceScore
		draft.RecommendedAction = decideInboxAction(agent, classification)
		draft.ReasoningSummary = "Draft generated from IMAP-synced message."
		if draft.RecommendedAction == "escalate" {
			draft.Status = pkgmodels.AIReplyDraftStatusEscalated
		}
		if draft.RecommendedAction == "timer_send" {
			t := time.Now().Add(time.Duration(agent.TimerMinutes) * time.Minute)
			draft.SendAfterAt = &t
			draft.Status = pkgmodels.AIReplyDraftStatusTimer
		}
		draft.SetCreated()
		if err := db.GetCollection(pkgmodels.AIReplyDraftCollection).Insert(draft); err != nil {
			log.Printf("imap: failed to save draft: %v", err)
			return
		}
		audit := createAuditForDraft(tenantID, draft, classification)
		_ = db.GetCollection(pkgmodels.AIReplyAuditCollection).Insert(audit)
		logInboxActivity(tenantID, agent.Id, "email_received", thread.Id, emailMsg.Id, contact.Id, bson.M{"source": "imap", "category": classification.PrimaryCategory})
		updateContactMemoryFromDraft(tenantID, contact.Id, emailMsg.Id, classification, msg.BodyText)

		if draft.RecommendedAction == "auto_send" {
			_ = sendDraftNow(agent, account, *draft)
		}
	})
}

// ─── Voice Profile ────────────────────────────────────────────────────────────

func handleGenerateVoiceProfile(c *gin.Context) {
	tenantID, ok := tenantIDFromContext(c)
	if !ok {
		return
	}
	var agent pkgmodels.InboxAgent
	if err := findByIDOrPublic(pkgmodels.InboxAgentCollection, tenantID, c.Param("id"), &agent); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "agent not found"})
		return
	}

	// Gather approved/sent drafts as voice samples
	var drafts []pkgmodels.AIReplyDraft
	_ = db.GetCollection(pkgmodels.AIReplyDraftCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"inbox_agent_id":        agent.Id,
		"status":                bson.M{"$in": []string{pkgmodels.AIReplyDraftStatusApproved, pkgmodels.AIReplyDraftStatusSent}},
		"timestamps.deleted_at": nil,
	}).Sort("-created_at").Limit(20).All(&drafts)

	brand := resolveBrandProfileForInbox(tenantID)

	var sampleBodies []string
	for _, d := range drafts {
		if d.DraftBody != "" {
			sampleBodies = append(sampleBodies, d.DraftBody)
		}
	}

	provider, err := ai.GetConfiguredProvider()
	if err != nil || provider == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI provider not configured"})
		return
	}

	samplesText := strings.Join(sampleBodies, "\n---\n")
	prompt := fmt.Sprintf(`Analyze these email replies written by or approved by a business owner.
Produce a voice profile as JSON with exactly these fields:
{"name":"Inferred Voice","summary":"","average_reply_length":0,"directness_score":0,"formality_score":0,"sales_tone_score":0,"common_phrases":[],"example_replies":[]}

Scores are 1-10. average_reply_length is word count. common_phrases are up to 5 phrases.
example_replies are up to 3 short examples from the samples.

Brand context: %s

Email reply samples:
%s`, brand, truncate(samplesText, 2000))

	raw, err := provider.GenerateText(ai.GenerateTextRequest{Prompt: prompt, MaxTokens: 400})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "AI generation failed"})
		return
	}
	raw = strings.TrimSpace(raw)
	if i := strings.Index(raw, "{"); i >= 0 {
		raw = raw[i:]
	}
	if i := strings.LastIndex(raw, "}"); i >= 0 {
		raw = raw[:i+1]
	}
	var vp struct {
		Name               string   `json:"name"`
		Summary            string   `json:"summary"`
		AverageReplyLength int      `json:"average_reply_length"`
		DirectnessScore    int      `json:"directness_score"`
		FormalityScore     int      `json:"formality_score"`
		SalesToneScore     int      `json:"sales_tone_score"`
		CommonPhrases      []string `json:"common_phrases"`
		ExampleReplies     []string `json:"example_replies"`
	}
	if err := json.Unmarshal([]byte(raw), &vp); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to parse voice profile"})
		return
	}

	profile := pkgmodels.VoiceProfile{
		Id:                 bson.NewObjectId(),
		PublicId:           utils.GeneratePublicId(),
		TenantID:           tenantID,
		Name:               vp.Name,
		Source:             "approved_drafts",
		Summary:            vp.Summary,
		AverageReplyLength: vp.AverageReplyLength,
		DirectnessScore:    vp.DirectnessScore,
		FormalityScore:     vp.FormalityScore,
		SalesToneScore:     vp.SalesToneScore,
		CommonPhrasesJSON:  vp.CommonPhrases,
		ExampleRepliesJSON: vp.ExampleReplies,
	}
	profile.SetCreated()
	_ = db.GetCollection(pkgmodels.VoiceProfileCollection).Insert(&profile)

	now := time.Now()
	_ = db.GetCollection(pkgmodels.InboxAgentCollection).UpdateId(agent.Id, bson.M{"$set": bson.M{
		"voice_profile_id":      profile.Id,
		"timestamps.updated_at": now,
	}})

	c.JSON(http.StatusCreated, gin.H{"voice_profile": profile})
}

// ─── Context Packs for Agent ──────────────────────────────────────────────────

func handleInboxAgentListContextPacks(c *gin.Context) {
	tenantID, ok := tenantIDFromContext(c)
	if !ok {
		return
	}
	var agent pkgmodels.InboxAgent
	if err := findByIDOrPublic(pkgmodels.InboxAgentCollection, tenantID, c.Param("id"), &agent); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "agent not found"})
		return
	}
	var settings pkgmodels.InboxAgentSettings
	_ = db.GetCollection(pkgmodels.InboxAgentSettingsCollection).Find(bson.M{"tenant_id": tenantID, "inbox_agent_id": agent.Id}).One(&settings)

	var all []pkgmodels.ContextPack
	_ = db.GetCollection(pkgmodels.ContextPackCollection).Find(bson.M{"tenant_id": tenantID, "timestamps.deleted_at": nil}).All(&all)

	activeSet := map[string]bool{}
	for _, id := range settings.ActiveContextPackIDs {
		activeSet[id.Hex()] = true
	}
	type packWithActive struct {
		pkgmodels.ContextPack `bson:",inline"`
		Active                bool `json:"active"`
	}
	out := make([]packWithActive, 0, len(all))
	for _, p := range all {
		out = append(out, packWithActive{ContextPack: p, Active: activeSet[p.Id.Hex()]})
	}
	c.JSON(http.StatusOK, gin.H{"context_packs": out})
}

func handleInboxAgentSetContextPacks(c *gin.Context) {
	tenantID, ok := tenantIDFromContext(c)
	if !ok {
		return
	}
	var agent pkgmodels.InboxAgent
	if err := findByIDOrPublic(pkgmodels.InboxAgentCollection, tenantID, c.Param("id"), &agent); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "agent not found"})
		return
	}
	var req struct {
		ContextPackIDs []string `json:"context_pack_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "context_pack_ids required"})
		return
	}
	ids := make([]bson.ObjectId, 0, len(req.ContextPackIDs))
	for _, raw := range req.ContextPackIDs {
		if bson.IsObjectIdHex(raw) {
			ids = append(ids, bson.ObjectIdHex(raw))
		}
	}
	_ = db.GetCollection(pkgmodels.InboxAgentSettingsCollection).Update(
		bson.M{"tenant_id": tenantID, "inbox_agent_id": agent.Id},
		bson.M{"$set": bson.M{"active_context_pack_ids": ids, "timestamps.updated_at": time.Now()}},
	)
	c.JSON(http.StatusOK, gin.H{"active_count": len(ids)})
}

// ─── Timer Approval Job ───────────────────────────────────────────────────────

// StartTimerApprovalLoop runs every minute and auto-sends timer_pending drafts
// whose send_after_at has passed and have not been rejected or edited.
func StartTimerApprovalLoop() {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			runTimerApprovals()
		}
	}()
	log.Printf("inbox: timer approval loop started")
}

func runTimerApprovals() {
	now := time.Now()
	var drafts []pkgmodels.AIReplyDraft
	err := db.GetCollection(pkgmodels.AIReplyDraftCollection).Find(bson.M{
		"status":                pkgmodels.AIReplyDraftStatusTimer,
		"send_after_at":         bson.M{"$lte": now},
		"timestamps.deleted_at": nil,
	}).All(&drafts)
	if err != nil || len(drafts) == 0 {
		return
	}
	for _, draft := range drafts {
		var agent pkgmodels.InboxAgent
		var account pkgmodels.InboxAccount
		if err := db.GetCollection(pkgmodels.InboxAgentCollection).FindId(draft.InboxAgentID).One(&agent); err != nil {
			continue
		}
		_ = db.GetCollection(pkgmodels.InboxAccountCollection).FindId(agent.InboxAccountID).One(&account)
		if err := sendDraftNow(&agent, &account, draft); err != nil {
			log.Printf("inbox: timer send failed for draft %s: %v", draft.PublicId, err)
			continue
		}
		logInboxActivity(draft.TenantID, agent.Id, "reply_timer_sent", draft.EmailThreadID, draft.EmailMessageID, draft.ContactID, bson.M{"draft_id": draft.Id.Hex()})
	}
}
