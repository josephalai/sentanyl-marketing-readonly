package routes

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"gopkg.in/mgo.v2/bson"

	pkgauth "github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/mcptools"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"

	"github.com/josephalai/sentanyl/marketing-service/internal/ai"
)

// maxInboxActionsPerEmail caps how many tool calls one inbound email may
// trigger — containment for a runaway or prompt-injected model.
const maxInboxActionsPerEmail = 3

// runInboxActionPass lets the agent take in-app actions for an inbound email,
// strictly limited to its ToolWhitelist. The LLM proposes actions as JSON;
// each is executed through the shared MCP tool registry as the tenant's
// "inbox-agent" machine principal (MCP-001, content scope only) and logged to
// the activity log. Failures are non-fatal — the reply pipeline never blocks
// on actions. Never called for suppressed contacts.
func runInboxActionPass(tenant *pkgmodels.Tenant, agent *pkgmodels.InboxAgent, contact *pkgmodels.User, thread *pkgmodels.EmailThread, msg *pkgmodels.EmailMessage, draft *pkgmodels.AIReplyDraft, c inboxClassification) {
	if tenant == nil || agent == nil || len(agent.ToolWhitelist) == 0 {
		return
	}
	actions := proposeInboxActions(agent, contact, thread, msg, draft, c)
	if len(actions) > maxInboxActionsPerEmail {
		actions = actions[:maxInboxActionsPerEmail]
	}
	if len(actions) == 0 {
		return
	}
	jwt, err := pkgauth.MintMachineJWT(tenant.Id, pkgmodels.ServicePrincipalInboxAgent, []pkgauth.Permission{pkgauth.PermContentManage})
	if err != nil {
		log.Printf("inbox actions: machine jwt for tenant %s: %v", tenant.Id.Hex(), err)
		return
	}
	for _, a := range actions {
		result, err := mcptools.Invoke(jwt, a.Tool, a.Args, agent.ToolWhitelist)
		meta := bson.M{"tool": a.Tool, "draft_id": draft.Id.Hex(), "principal": pkgmodels.ServicePrincipalInboxAgent}
		if err != nil {
			meta["error"] = err.Error()
		} else if isErr, _ := result["isError"].(bool); isErr {
			meta["error"] = "tool returned error"
		} else {
			meta["ok"] = true
		}
		logInboxActivity(tenant.Id, agent.Id, "tool_invoked", thread.Id, msg.Id, contact.Id, meta)
	}
}

type inboxAction struct {
	Tool string                 `json:"tool"`
	Args map[string]interface{} `json:"args"`
}

// proposeInboxActions asks the LLM which whitelisted tools (if any) to run.
// Without a configured provider it falls back to one deterministic rule: an
// escalated draft with todos_create whitelisted becomes a follow-up to-do —
// this keeps the behavior testable in the e2e stack.
func proposeInboxActions(agent *pkgmodels.InboxAgent, contact *pkgmodels.User, thread *pkgmodels.EmailThread, msg *pkgmodels.EmailMessage, draft *pkgmodels.AIReplyDraft, c inboxClassification) []inboxAction {
	provider, err := ai.GetConfiguredProvider()
	if err != nil || provider == nil {
		return fallbackInboxActions(agent, contact, thread, msg, draft)
	}

	var tools strings.Builder
	for _, name := range agent.ToolWhitelist {
		if t := mcptools.Find(name); t != nil {
			fmt.Fprintf(&tools, "- %s: %s", t.Name, t.Description)
			if t.BodyDoc != "" {
				fmt.Fprintf(&tools, " Body: %s", t.BodyDoc)
			}
			tools.WriteString("\n")
		}
	}
	if tools.Len() == 0 {
		return nil
	}

	prompt := fmt.Sprintf(`You are an email assistant deciding which in-app actions to take after an inbound email. You may ONLY use these tools:
%s
Return ONLY valid JSON: {"actions":[{"tool":"<name>","args":{...}}]} with at most %d actions. Use an empty actions array when nothing is warranted. For POST/PUT tools put the request payload under args.body. When creating a to-do, set body.created_by to "ai", body.thread_id to %q and body.contact_id to %q.

Inbound email from %s <%s>:
Subject: %s
%s

Classification: category=%s risk=%s. The drafted reply disposition is %q.`,
		tools.String(), maxInboxActionsPerEmail, thread.Id.Hex(), contact.Id.Hex(),
		msg.FromName, msg.FromEmail, msg.Subject, truncate(msg.BodyText, 800),
		c.PrimaryCategory, c.RiskLevel, draft.RecommendedAction)

	raw, err := provider.GenerateText(ai.GenerateTextRequest{Prompt: prompt, MaxTokens: 300})
	if err != nil {
		return fallbackInboxActions(agent, contact, thread, msg, draft)
	}
	raw = strings.TrimSpace(raw)
	if i := strings.Index(raw, "{"); i >= 0 {
		raw = raw[i:]
	}
	if i := strings.LastIndex(raw, "}"); i >= 0 {
		raw = raw[:i+1]
	}
	var out struct {
		Actions []inboxAction `json:"actions"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out.Actions
}

func fallbackInboxActions(agent *pkgmodels.InboxAgent, contact *pkgmodels.User, thread *pkgmodels.EmailThread, msg *pkgmodels.EmailMessage, draft *pkgmodels.AIReplyDraft) []inboxAction {
	if draft.RecommendedAction != "escalate" || !mcptools.IsAllowed("todos_create", agent.ToolWhitelist) {
		return nil
	}
	subject := msg.Subject
	if subject == "" {
		subject = "inbound email"
	}
	return []inboxAction{{
		Tool: "todos_create",
		Args: map[string]interface{}{
			"body": map[string]interface{}{
				"title":      "Follow up: " + subject,
				"note":       "Escalated by the AI auto-responder. From " + msg.FromEmail + ".",
				"created_by": pkgmodels.TodoCreatedByAI,
				"contact_id": contact.Id.Hex(),
				"thread_id":  thread.Id.Hex(),
			},
		},
	}}
}
