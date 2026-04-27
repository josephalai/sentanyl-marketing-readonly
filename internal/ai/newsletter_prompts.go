package ai

import (
	"encoding/json"
	"fmt"
	"strings"
)

// aiTextSystemPrompt drives single-handlebar resolutions. Constraints are
// intentionally tight: any drift toward HTML, markdown, or runaway length
// breaks the inline substitution the resolver performs in pkg/render.
const aiTextSystemPrompt = `You resolve a single inline newsletter handlebar into a short stretch of plain text.

Output rules — non-negotiable:
- Plain text only. No HTML, no markdown, no surrounding quotes.
- One paragraph, ≤80 words, ≤500 characters total.
- If reference text is provided, draw any factual claims, attributions, or quoted material exclusively from it. Never invent sources.
- If brand profile context is provided, match its voice and tone.
- Do not preface with "Here is", "Sure!", or any other meta-commentary. Output the text directly.
- Do not wrap the output in JSON or any structured shape — return it as bare text.`

// seriesOutlineSystemPrompt drives the series-planning call. Returns JSON
// with issue titles, briefs, and key_points so the per-issue content
// generator has a real plan to follow rather than re-deriving structure.
const seriesOutlineSystemPrompt = `You plan a newsletter series. The user gives you a topic, an audience, and an intended outcome; you return a structured plan as JSON.

JSON schema:
{
  "series_title": "string — 3-7 words, no clickbait",
  "description": "string — 1 sentence, ≤140 chars",
  "issues": [
    {
      "order": 1,
      "title": "string — 4-12 words, builds curiosity but is concrete",
      "brief": "string — 2-3 sentences, what this issue covers and why this slot in the arc",
      "key_points": ["string", "string", "string"]
    }
  ]
}

Rules:
- The issues array MUST have exactly the count the user requests, ordered 1..N.
- The arc must progress: early issues set context; middle issues develop the core idea; later issues apply or extend it. No two issues should be interchangeable.
- If reference text is provided, every issue's brief and key_points must be anchored in it — quote it, name its examples, do not invent parallel sources.
- If brand profile is provided, match its voice in titles and briefs.
- Output ONLY the JSON object. No commentary before or after.`

// postFromBriefSystemPrompt drives the per-issue content call. Output is a
// Puck document tree using the same component types Sentanyl's site
// renderer already understands (HeroSection, RichTextSection, CTASection),
// plus the two break blocks the gate splitter uses
// (NewsletterSubscriberBreak, NewsletterPaywallBreak).
const postFromBriefSystemPrompt = `You author one issue of a newsletter series as a Puck document tree.

Output a single JSON object matching this Puck root shape:
{
  "root": { "props": {} },
  "content": [
    {"type": "HeroSection", "props": {"heading": "...", "subheading": "..."}},
    {"type": "RichTextSection", "props": {"content": "<p>...</p>"}},
    {"type": "NewsletterSubscriberBreak", "props": {}},
    {"type": "RichTextSection", "props": {"content": "<p>...</p>"}}
  ]
}

Rules:
- The first content block MUST be HeroSection with the issue title as heading and a 1-line hook as subheading.
- Use 3-5 RichTextSection blocks. Each contains semantic HTML — <p>, <h2>, <ul>, <li>, <em>, <strong>, <blockquote>, <a href="...">. Do NOT wrap content in <html> or <body>.
- Place exactly one NewsletterSubscriberBreak after the public preface (typically after 1-2 RichTextSection blocks). Content above is publicly readable; content below is for subscribers.
- Optionally place one NewsletterPaywallBreak before a deeper-dive section. Use {"props": {"tier": ""}} for any paid tier.
- If reference text is provided, anchor every claim, quote, and example in it — name the source explicitly. Never invent sources.
- Match the requested tone and brand voice.
- Output ONLY the JSON object. No commentary before or after.`

// buildAITextPrompt composes the user-facing message body for an inline
// handlebar resolution. Reference text and brand profile are optional; when
// present they are appended as labeled sections so the model can ignore them
// gracefully if they aren't relevant to the prompt.
func buildAITextPrompt(req GenerateTextRequest) string {
	var b strings.Builder
	b.WriteString("Prompt: ")
	b.WriteString(req.Prompt)
	if strings.TrimSpace(req.BrandProfile) != "" {
		b.WriteString("\n\nBrand voice and positioning:\n")
		b.WriteString(req.BrandProfile)
	}
	if strings.TrimSpace(req.ReferenceText) != "" {
		b.WriteString("\n\nReference text (draw factual claims and quotes from this exclusively):\n")
		b.WriteString(req.ReferenceText)
	}
	return b.String()
}

// buildSeriesOutlinePrompt composes the user message for the series-outline
// call. Supplies all of the structured fields plus the optional grounding.
func buildSeriesOutlinePrompt(req SeriesOutlineRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Topic: %s\n", req.Topic)
	if req.Audience != "" {
		fmt.Fprintf(&b, "Audience: %s\n", req.Audience)
	}
	if req.Outcome != "" {
		fmt.Fprintf(&b, "Outcome: %s\n", req.Outcome)
	}
	if req.Tone != "" {
		fmt.Fprintf(&b, "Tone: %s\n", req.Tone)
	}
	count := req.IssueCount
	if count <= 0 {
		count = 4
	}
	fmt.Fprintf(&b, "Issue count: %d (return exactly this many)\n", count)
	if req.BrandProfile != "" {
		b.WriteString("\nBrand voice and positioning:\n")
		b.WriteString(req.BrandProfile)
		b.WriteString("\n")
	}
	if req.ReferenceText != "" {
		b.WriteString("\nReference text (every issue must anchor in this):\n")
		b.WriteString(req.ReferenceText)
	}
	return b.String()
}

// buildPostFromBriefPrompt composes the user message for one per-issue
// content generation call. The brief and key points narrow the model's
// search space so all N posts in a series add up to a coherent arc.
func buildPostFromBriefPrompt(req PostFromBriefRequest) string {
	var b strings.Builder
	if req.SeriesTitle != "" {
		fmt.Fprintf(&b, "Series: %s\n", req.SeriesTitle)
	}
	fmt.Fprintf(&b, "Issue title: %s\n", req.IssueTitle)
	if req.IssueBrief != "" {
		fmt.Fprintf(&b, "Issue brief: %s\n", req.IssueBrief)
	}
	if len(req.KeyPoints) > 0 {
		jsonKP, _ := json.Marshal(req.KeyPoints)
		fmt.Fprintf(&b, "Key points to cover (in order): %s\n", string(jsonKP))
	}
	if req.Tone != "" {
		fmt.Fprintf(&b, "Tone: %s\n", req.Tone)
	}
	if req.Audience != "" {
		fmt.Fprintf(&b, "Audience: %s\n", req.Audience)
	}
	if req.BrandProfile != "" {
		b.WriteString("\nBrand voice and positioning:\n")
		b.WriteString(req.BrandProfile)
		b.WriteString("\n")
	}
	if req.ReferenceText != "" {
		b.WriteString("\nReference text (anchor every claim, example, and quote in this):\n")
		b.WriteString(req.ReferenceText)
	}
	return b.String()
}
