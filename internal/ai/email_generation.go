package ai

import "strings"

const emailGenerationSystemPrompt = `You are an expert email copywriter. You generate high-converting HTML emails.

Always respond with a JSON object in this exact format:
{
  "subject": "The email subject line",
  "body": "<html>...full HTML email body...</html>",
  "summary": "Brief description of what was generated or changed"
}

HTML email rules:
- Use table-based layouts for email client compatibility
- Inline CSS only — no <style> tags or external stylesheets
- Max width 600px, centered
- Mobile-friendly (single column preferred)
- Use {{firstName}}, {{lastName}}, {{email}} for personalization tokens
- Include an unsubscribe link in the footer
- Color palette: use professional, brand-appropriate colors`

// campaignEmailSystemPrompt extends the email copywriter persona with a
// campaign-specific contract: the model also proposes a badge audience.
const campaignEmailSystemPrompt = `You are an expert email copywriter generating a one-off CAMPAIGN email.

Always respond with a JSON object in this exact format:
{
  "subject": "The email subject line",
  "body": "<html>...full HTML email body...</html>",
  "summary": "One-sentence description of the angle and goal",
  "audience_suggestion": {
    "must_have": ["badge_id_or_name", "..."],
    "must_not_have": ["badge_id_or_name", "..."],
    "reason": "One sentence explaining the audience pick"
  }
}

When the user provides a BADGE CATALOG, choose audience entries ONLY from that
catalog. Use exact strings from the catalog — do not invent badge names. If no
catalog is provided, return empty arrays for must_have / must_not_have but still
include the audience_suggestion object with a reason.

HTML email rules:
- Use table-based layouts for email client compatibility
- Inline CSS only — no <style> tags or external stylesheets
- Max width 600px, centered
- Mobile-friendly (single column preferred)
- Use {{firstName}}, {{lastName}}, {{email}} for personalization tokens
- Include an unsubscribe link in the footer
- Color palette: professional, brand-appropriate`

// buildCampaignGenerationPrompt wraps the standard email prompt with a
// campaign-specific badge catalog block so the LLM can produce an
// audience_suggestion grounded in real tenant badges.
func buildCampaignGenerationPrompt(req EmailGenerationRequest) string {
	base := buildEmailGenerationPrompt(req.Instruction, req.ContextChunks, req.BrandProfile)
	if len(req.BadgeCatalog) == 0 {
		return base
	}
	var sb strings.Builder
	sb.WriteString("AVAILABLE BADGES (use ONLY these strings in audience_suggestion):\n")
	for _, b := range req.BadgeCatalog {
		sb.WriteString("- ")
		sb.WriteString(b)
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
	sb.WriteString(base)
	return sb.String()
}

func buildEmailGenerationPrompt(instruction string, contextChunks []string, brandProfile string) string {
	var sb strings.Builder
	if brandProfile != "" {
		sb.WriteString("BRAND PROFILE:\n")
		sb.WriteString(brandProfile)
		sb.WriteString("\n\n")
	}
	if len(contextChunks) > 0 {
		sb.WriteString("CONTEXT / REFERENCE MATERIAL:\n")
		for i, chunk := range contextChunks {
			if i > 0 {
				sb.WriteString("\n---\n")
			}
			sb.WriteString(chunk)
		}
		sb.WriteString("\n\n")
	}
	sb.WriteString("TASK:\n")
	sb.WriteString(instruction)
	return sb.String()
}

func buildEmailEditPrompt(req EmailEditRequest) string {
	var sb strings.Builder
	if req.BrandProfile != "" {
		sb.WriteString("BRAND PROFILE:\n")
		sb.WriteString(req.BrandProfile)
		sb.WriteString("\n\n")
	}
	if len(req.ContextChunks) > 0 {
		sb.WriteString("CONTEXT / REFERENCE MATERIAL:\n")
		for i, chunk := range req.ContextChunks {
			if i > 0 {
				sb.WriteString("\n---\n")
			}
			sb.WriteString(chunk)
		}
		sb.WriteString("\n\n")
	}
	sb.WriteString("CURRENT SUBJECT:\n")
	sb.WriteString(req.CurrentSubject)
	sb.WriteString("\n\nCURRENT BODY:\n")
	sb.WriteString(req.CurrentBody)
	sb.WriteString("\n\nEDIT INSTRUCTION:\n")
	sb.WriteString(req.Instruction)
	sb.WriteString("\n\nReturn the full updated subject and body — not just the changed parts.")
	return sb.String()
}
