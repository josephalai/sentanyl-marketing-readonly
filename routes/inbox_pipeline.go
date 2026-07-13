package routes

import (
	"strings"
	"time"

	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/utils"
)

// inboundEmail is a provider-agnostic inbound message. Every ingestion path
// (IMAP sync, the dev simulate endpoint, the platform inbound relay) reduces
// its input to this shape and hands it to processInboundEmail.
type inboundEmail struct {
	FromEmail         string
	FromName          string
	Subject           string
	BodyText          string
	BodyHTML          string
	ProviderMessageID string
	ProviderThreadID  string
	ToList            []string
	Date              time.Time
	// Source labels the ingestion path: "imap" | "dev" | "platform_inbound".
	Source string
	// EmailSendPublicID correlates the reply to the outbound marketing send
	// (VERP token), when the platform relay resolved one.
	EmailSendPublicID string
}

// inboundResult carries everything the caller may want to report back.
type inboundResult struct {
	Classification inboxClassification
	Thread         *pkgmodels.EmailThread
	Message        *pkgmodels.EmailMessage
	Contact        *pkgmodels.User
	Draft          *pkgmodels.AIReplyDraft
	Audit          *pkgmodels.AIReplyAudit
	Retrieved      []string
}

// processInboundEmail is the single inbound pipeline: contact → thread →
// message → classify → guardrails → grounded draft → optional double-check →
// autonomy decision → persist → action pass → auto/timer send.
func processInboundEmail(tenantID bson.ObjectId, agent *pkgmodels.InboxAgent, account *pkgmodels.InboxAccount, in inboundEmail) (*inboundResult, error) {
	contact, err := findOrCreateInboxContact(tenantID, in.FromEmail, in.FromName)
	if err != nil {
		return nil, err
	}
	thread, err := findOrCreateThread(tenantID, account.Id, in.ProviderThreadID, in.Subject, in.FromEmail, account.EmailAddress)
	if err != nil {
		return nil, err
	}
	now := in.Date
	if now.IsZero() {
		now = time.Now()
	}
	msg := pkgmodels.NewEmailMessage(tenantID, thread.Id, "inbound")
	msg.ProviderMessageID = in.ProviderMessageID
	msg.FromEmail = strings.ToLower(strings.TrimSpace(in.FromEmail))
	msg.FromName = in.FromName
	msg.ToJSON = in.ToList
	msg.Subject = in.Subject
	msg.BodyText = in.BodyText
	msg.BodyHTML = in.BodyHTML
	msg.ReceivedAt = &now
	msg.SetCreated()
	if err := db.GetCollection(pkgmodels.EmailMessageCollection).Insert(msg); err != nil {
		return nil, err
	}
	_ = db.GetCollection(pkgmodels.EmailThreadCollection).UpdateId(thread.Id, bson.M{"$set": bson.M{"last_message_at": now}})

	classification := classifyInboundEmail(in.Subject + "\n" + in.BodyText)
	retrieved := retrieveInboxContextForAgent(tenantID, agent.Id, in.BodyText)
	reply := generateInboxDraftReply(tenantID, agent, contact, classification, in.BodyText, retrieved)

	var audit *pkgmodels.AIReplyAudit
	if agent.DoubleCheckEnabled {
		if dc := doubleCheckDraft(reply, in.BodyText, classification); dc != nil {
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

	tenant := loadTenantForInbox(tenantID)
	verdict := evaluateSendGuardrails(tenant, agent, contact, classification, time.Now())

	draft := pkgmodels.NewAIReplyDraft(tenantID, agent.Id, thread.Id, msg.Id, contact.Id)
	draft.DraftBody = reply
	draft.Category = classification.PrimaryCategory
	draft.RiskLevel = classification.RiskLevel
	draft.ConfidenceScore = classification.ConfidenceScore
	draft.RecommendedAction = verdict.Action
	draft.ReasoningSummary = "Draft generated from inbound email, customer context, active agent settings, and available Business Brain/context chunks."
	if verdict.Reason != "" {
		draft.ReasoningSummary += " Guardrail: " + verdict.Reason + "."
	}
	switch verdict.Action {
	case "escalate":
		draft.Status = pkgmodels.AIReplyDraftStatusEscalated
	case "timer_send":
		t := verdict.SendAfterAt
		if t == nil {
			after := time.Now().Add(time.Duration(agent.TimerMinutes) * time.Minute)
			t = &after
		}
		draft.SendAfterAt = t
		draft.Status = pkgmodels.AIReplyDraftStatusTimer
	}
	draft.SetCreated()
	if err := db.GetCollection(pkgmodels.AIReplyDraftCollection).Insert(draft); err != nil {
		return nil, err
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
	logInboxActivity(tenantID, agent.Id, "email_received", thread.Id, msg.Id, contact.Id, bson.M{"source": in.Source, "category": classification.PrimaryCategory})
	logInboxActivity(tenantID, agent.Id, "reply_generated", thread.Id, msg.Id, contact.Id, bson.M{"draft_id": draft.Id.Hex(), "action": draft.RecommendedAction, "guardrail": verdict.Reason})
	updateContactMemoryFromDraft(tenantID, contact.Id, msg.Id, classification, in.BodyText)

	if verdict.Reason != "contact_unsubscribed" {
		runInboxActionPass(tenant, agent, contact, thread, msg, draft, classification)
	}

	if verdict.Action == "auto_send" {
		_ = sendDraftNow(agent, account, *draft)
	}

	return &inboundResult{
		Classification: classification,
		Thread:         thread,
		Message:        msg,
		Contact:        contact,
		Draft:          draft,
		Audit:          audit,
		Retrieved:      retrieved,
	}, nil
}
