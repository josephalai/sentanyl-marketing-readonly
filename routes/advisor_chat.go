package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/aigov"
	pkgauth "github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/llm"
	"github.com/josephalai/sentanyl/pkg/mcptools"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// The universal Advisor: a multi-turn Claude tool-use loop that drives the
// shared pkg/mcptools tenant registry. It reuses the inbox action pattern
// (mint machine JWT → mcptools.Invoke → aigov.Record) but with the NATIVE
// tool-use API instead of prompt-coerced JSON, and it inherits the existing
// approval gate: publish/send/destroy tools pause for a human OK before running.
//
// The endpoint is stateless — the client round-trips the full Anthropic
// `messages` history each turn (including tool_use/tool_result blocks).

// maxAdvisorTurns caps the model↔tool round-trips inside one HTTP call, so a
// looping or injected model cannot run unbounded tool chains.
const maxAdvisorTurns = 8

const advisorSystemPrompt = `You are the Sentanyl Advisor, an expert operator embedded in the tenant's marketing platform. You help the user manage their business — coupons, products, offers, forms, funnels, websites, courses, newsletters, coaching, contacts, and email sequences — by calling the available tools.

Guidelines:
- Prefer taking action with tools over describing how the user could do it themselves.
- Read first when unsure (list/get tools) before creating or mutating.
- Consequential actions (publishing, sending, deleting) require the user's approval; propose them and they will be confirmed before they run. Never claim such an action is done until it is confirmed.
- Be concise. Confirm what you did, referencing the concrete result (ids, names).
- If a tool returns an error, explain it plainly and suggest a fix rather than retrying blindly.`

// RegisterAdvisorRoutes wires the universal Advisor chat endpoint under the
// tenant API group (Caddy routes /api/tenant/advisor* to marketing-service).
func RegisterAdvisorRoutes(tenantAPI *gin.RouterGroup) {
	tenantAPI.POST("/advisor/chat", handleAdvisorChat)
}

type advisorChatRequest struct {
	Messages []llm.Message     `json:"messages"`
	Resolve  map[string]string `json:"resolve"` // tool_use_id → "approve" | "reject"
}

type advisorToolCall struct {
	ToolUseID string `json:"tool_use_id"`
	Tool      string `json:"tool"`
	Ok        bool   `json:"ok"`
	Summary   string `json:"summary"`
}

type advisorPending struct {
	ToolUseID  string                 `json:"tool_use_id"`
	Tool       string                 `json:"tool"`
	SideEffect string                 `json:"side_effect"`
	Args       map[string]interface{} `json:"args"`
}

type advisorChatResponse struct {
	Messages         []llm.Message     `json:"messages"`
	AssistantText    string            `json:"assistant_text"`
	ToolCalls        []advisorToolCall `json:"tool_calls"`
	PendingApprovals []advisorPending  `json:"pending_approvals"`
	Done             bool              `json:"done"`
}

func handleAdvisorChat(c *gin.Context) {
	tenantID := pkgauth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req advisorChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(req.Messages) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "messages required"})
		return
	}

	client := llm.NewFromEnv()
	if client == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Advisor LLM not configured (ANTHROPIC_API_KEY)"})
		return
	}

	// Machine identity: the Advisor runs as the tenant's "advisor" principal with
	// the standard machine scopes. `allowed` is the explicit full tool-name list
	// so every tool passes IsAllowed while the approval gate still fires on
	// publish/send/destroy (AllToolNames carries no approval authority).
	jwt, err := pkgauth.MintMachineJWT(tenantID, pkgmodels.ServicePrincipalAdvisor, pkgauth.MachineDefaultScopes)
	if err != nil {
		log.Printf("advisor: machine jwt for tenant %s: %v", tenantID.Hex(), err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to authenticate advisor session"})
		return
	}
	allowed := mcptools.AllToolNames()
	tools := advisorTools(allowed)

	ctx, cancel := context.WithTimeout(c.Request.Context(), 110*time.Second)
	defer cancel()

	messages := req.Messages
	var toolCalls []advisorToolCall

	// Resume path: the previous turn paused for approval. Answer every tool_use
	// block in the last assistant turn — approval-gated ones per the user's
	// decision, the rest auto-executed — then fall through into the loop.
	if len(req.Resolve) > 0 {
		results, calls := resolvePendingTurn(jwt, allowed, messages, req.Resolve)
		if results != nil {
			messages = append(messages, llm.Message{Role: "user", Content: results})
			toolCalls = append(toolCalls, calls...)
		}
	}

	for turn := 0; turn < maxAdvisorTurns; turn++ {
		resp, err := client.Messages(ctx, llm.MessagesRequest{
			System:    advisorSystemPrompt,
			Messages:  messages,
			Tools:     tools,
			MaxTokens: 4096,
		})
		if err != nil {
			log.Printf("advisor: model call: %v", err)
			recordAdvisor(tenantID, client.Model(), toolCalls, pkgmodels.AIOutcomeError)
			c.JSON(http.StatusBadGateway, gin.H{"error": "advisor model call failed"})
			return
		}
		messages = append(messages, llm.Message{Role: "assistant", Content: resp.Content})

		uses := resp.ToolUses()
		if len(uses) == 0 {
			recordAdvisor(tenantID, client.Model(), toolCalls, advisorOutcome(toolCalls))
			c.JSON(http.StatusOK, advisorChatResponse{
				Messages:      messages,
				AssistantText: resp.TextContent(),
				ToolCalls:     toolCalls,
				Done:          true,
			})
			return
		}

		// Split into auto-executable and approval-gated tool_use blocks.
		var pending []advisorPending
		for _, b := range uses {
			if t := mcptools.Find(b.Name); t != nil && t.RequiresApproval() {
				pending = append(pending, advisorPending{
					ToolUseID:  b.ID,
					Tool:       b.Name,
					SideEffect: t.EffectiveSideEffect(),
					Args:       parseToolInput(b.Input),
				})
			}
		}
		if len(pending) > 0 {
			// Pause the whole turn for a human OK. The assistant turn is already
			// appended; the client resolves and re-posts to continue.
			recordAdvisor(tenantID, client.Model(), toolCalls, pkgmodels.AIOutcomeDraft)
			c.JSON(http.StatusOK, advisorChatResponse{
				Messages:         messages,
				AssistantText:    resp.TextContent(),
				ToolCalls:        toolCalls,
				PendingApprovals: pending,
				Done:             false,
			})
			return
		}

		// All auto-executable — run and feed results back, then loop.
		var results []llm.Block
		for _, b := range uses {
			block, call := invokeToolUse(jwt, allowed, b, false)
			results = append(results, block)
			toolCalls = append(toolCalls, call)
		}
		messages = append(messages, llm.Message{Role: "user", Content: results})
	}

	// Hit the turn cap without a natural end.
	recordAdvisor(tenantID, client.Model(), toolCalls, advisorOutcome(toolCalls))
	c.JSON(http.StatusOK, advisorChatResponse{
		Messages:      messages,
		AssistantText: "I've reached the step limit for one message. Tell me how you'd like to continue.",
		ToolCalls:     toolCalls,
		Done:          true,
	})
}

// resolvePendingTurn answers every tool_use block in the last assistant message:
// approval-gated tools per the user's approve/reject decision, others executed
// normally. Returns the tool_result blocks and a call log, or nil if the last
// message isn't an assistant tool-use turn.
func resolvePendingTurn(jwt string, allowed []string, messages []llm.Message, resolve map[string]string) ([]llm.Block, []advisorToolCall) {
	if len(messages) == 0 {
		return nil, nil
	}
	last := messages[len(messages)-1]
	if last.Role != "assistant" {
		return nil, nil
	}
	var results []llm.Block
	var calls []advisorToolCall
	for _, b := range last.Content {
		if b.Type != "tool_use" {
			continue
		}
		t := mcptools.Find(b.Name)
		if t != nil && t.RequiresApproval() {
			if resolve[b.ID] != "approve" {
				results = append(results, llm.Block{
					Type: "tool_result", ToolUseID: b.ID,
					Content: "The user declined to run this action.",
				})
				calls = append(calls, advisorToolCall{ToolUseID: b.ID, Tool: b.Name, Ok: false, Summary: "declined by user"})
				continue
			}
			block, call := invokeToolUse(jwt, allowed, b, true) // approved
			results = append(results, block)
			calls = append(calls, call)
			continue
		}
		block, call := invokeToolUse(jwt, allowed, b, false)
		results = append(results, block)
		calls = append(calls, call)
	}
	if len(results) == 0 {
		return nil, nil
	}
	return results, calls
}

// invokeToolUse runs one tool_use block through mcptools and returns the
// tool_result block to feed back plus a display-friendly call record.
func invokeToolUse(jwt string, allowed []string, b llm.Block, approved bool) (llm.Block, advisorToolCall) {
	args := parseToolInput(b.Input)
	if approved {
		args["approved"] = true
	}
	call := advisorToolCall{ToolUseID: b.ID, Tool: b.Name}
	result, err := mcptools.Invoke(jwt, b.Name, args, allowed)
	if err != nil {
		call.Summary = err.Error()
		return llm.Block{Type: "tool_result", ToolUseID: b.ID, Content: "Error: " + err.Error(), IsError: true}, call
	}
	text := mcpResultText(result)
	if isErr, _ := result["isError"].(bool); isErr {
		call.Ok = false
		call.Summary = truncate(text, 160)
		return llm.Block{Type: "tool_result", ToolUseID: b.ID, Content: text, IsError: true}, call
	}
	call.Ok = true
	call.Summary = truncate(text, 160)
	return llm.Block{Type: "tool_result", ToolUseID: b.ID, Content: text}, call
}

// advisorTools maps the mcptools descriptors into Anthropic tool definitions —
// the only transform is inputSchema → input_schema.
func advisorTools(allowed []string) []llm.Tool {
	descs := mcptools.Descriptors(allowed)
	out := make([]llm.Tool, 0, len(descs))
	for _, d := range descs {
		schema, _ := d["inputSchema"].(map[string]interface{})
		out = append(out, llm.Tool{
			Name:        fmt.Sprint(d["name"]),
			Description: fmt.Sprint(d["description"]),
			InputSchema: schema,
		})
	}
	return out
}

func parseToolInput(raw json.RawMessage) map[string]interface{} {
	m := map[string]interface{}{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &m)
	}
	return m
}

// mcpResultText extracts the text payload from an MCP tool result map.
func mcpResultText(result map[string]interface{}) string {
	content, _ := result["content"].([]interface{})
	var sb strings.Builder
	for _, c := range content {
		if m, ok := c.(map[string]interface{}); ok {
			if t, _ := m["type"].(string); t == "text" {
				sb.WriteString(fmt.Sprint(m["text"]))
			}
		}
	}
	if sb.Len() == 0 {
		if b, err := json.Marshal(result); err == nil {
			return string(b)
		}
	}
	return sb.String()
}

func advisorOutcome(calls []advisorToolCall) string {
	for _, c := range calls {
		if c.Ok {
			return pkgmodels.AIOutcomeMutated
		}
	}
	if len(calls) > 0 {
		return pkgmodels.AIOutcomeError
	}
	return pkgmodels.AIOutcomeDraft
}

// recordAdvisor ledgers one Advisor turn under the tenant's advisor principal.
func recordAdvisor(tenantID bson.ObjectId, model string, calls []advisorToolCall, outcome string) {
	proposals := make([]bson.M, 0, len(calls))
	for _, c := range calls {
		proposals = append(proposals, bson.M{"tool": c.Tool, "ok": c.Ok})
	}
	aigov.Record(&pkgmodels.AIExecution{
		TenantID:      tenantID,
		PrincipalKind: pkgmodels.AuthSessionKindMachine,
		PrincipalID:   pkgauth.EnsureServicePrincipalID(tenantID, pkgmodels.ServicePrincipalAdvisor),
		Surface:       pkgmodels.AISurfaceAdvisor,
		Provider:      "anthropic",
		Model:         model,
		Proposals:     proposals,
		Outcome:       outcome,
	})
}
