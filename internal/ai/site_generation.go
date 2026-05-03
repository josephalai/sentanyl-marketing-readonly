package ai

import (
	"encoding/json"
	"fmt"
	"strings"
)

// parsePageSuggestions accepts the raw LLM response for SuggestPages and
// returns a normalized []PageSuggestion. Tolerates four common LLM regressions:
//  1. proper [{name, slug, ...}] objects (the spec)
//  2. [{Name, Slug, ...}] with capitalized keys
//  3. ["Home", "About", ...] bare strings (we synthesize slug + page_type)
//  4. trailing prose around the JSON array
//
// Returning a partial-but-useful result is much better than 500ing — the
// frontend just shows whichever names came back.
func parsePageSuggestions(raw string) ([]PageSuggestion, error) {
	trimmed := strings.TrimSpace(raw)
	// Strip ```json fences if present.
	trimmed = strings.TrimPrefix(trimmed, "```json")
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSuffix(trimmed, "```")
	trimmed = strings.TrimSpace(trimmed)
	// Carve out the JSON array if it's surrounded by other text.
	if idx := strings.Index(trimmed, "["); idx >= 0 {
		if end := strings.LastIndex(trimmed, "]"); end > idx {
			trimmed = trimmed[idx : end+1]
		}
	}
	if trimmed == "" {
		return nil, fmt.Errorf("empty page suggestions response")
	}

	// Try the canonical []PageSuggestion shape first.
	var canonical []PageSuggestion
	if err := json.Unmarshal([]byte(trimmed), &canonical); err == nil && len(canonical) > 0 {
		return canonical, nil
	}

	// Fallback 1: bare []string of page names.
	var names []string
	if err := json.Unmarshal([]byte(trimmed), &names); err == nil && len(names) > 0 {
		out := make([]PageSuggestion, 0, len(names))
		for _, n := range names {
			out = append(out, suggestionFromName(n))
		}
		return out, nil
	}

	// Fallback 2: array of arbitrary objects with possibly-capitalized keys.
	var loose []map[string]any
	if err := json.Unmarshal([]byte(trimmed), &loose); err == nil && len(loose) > 0 {
		out := make([]PageSuggestion, 0, len(loose))
		for _, m := range loose {
			out = append(out, suggestionFromMap(m))
		}
		return out, nil
	}

	return nil, fmt.Errorf("failed to parse page suggestions: not a recognized shape: %.200s", trimmed)
}

// suggestionFromName turns a bare page name into a PageSuggestion with an
// inferred slug + page_type. The pageType heuristic is keyword-based — good
// enough to keep the UI useful when the LLM regresses to flat strings.
func suggestionFromName(name string) PageSuggestion {
	clean := strings.TrimSpace(name)
	slug := slugFromName(clean)
	return PageSuggestion{
		Name:     clean,
		Slug:     slug,
		PageType: inferPageType(clean),
		Reason:   "",
	}
}

// suggestionFromMap pulls fields from an arbitrary LLM object, tolerating
// "Name"/"name", "Slug"/"slug", etc., and any unknown keys.
func suggestionFromMap(m map[string]any) PageSuggestion {
	pick := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := m[k]; ok {
				if s, ok := v.(string); ok && s != "" {
					return s
				}
			}
		}
		return ""
	}
	name := pick("name", "Name", "title", "Title", "page", "Page")
	slug := pick("slug", "Slug", "path", "Path", "url", "URL")
	if slug == "" && name != "" {
		slug = slugFromName(name)
	}
	pageType := pick("page_type", "pageType", "PageType", "type", "Type")
	if pageType == "" {
		pageType = inferPageType(name)
	}
	reason := pick("reason", "Reason", "description", "Description", "why", "Why")

	var blocks []string
	switch v := m["blocks"].(type) {
	case []any:
		for _, b := range v {
			if s, ok := b.(string); ok {
				blocks = append(blocks, s)
			}
		}
	case []string:
		blocks = v
	}

	return PageSuggestion{
		Name:              name,
		Slug:              slug,
		PageType:          pageType,
		Reason:            reason,
		RecommendedBlocks: blocks,
	}
}

// slugFromName lowercases a name, swaps non-alphanumerics for hyphens, and
// returns "/<slug>" — matching the URL convention in the rest of the system.
// Empty / "home" / "/" all collapse to "/".
func slugFromName(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" || lower == "home" || lower == "/" {
		return "/"
	}
	var b strings.Builder
	prevDash := false
	for _, r := range lower {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		case r == ' ' || r == '-' || r == '_' || r == '/':
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if out == "" {
		return "/"
	}
	return "/" + out
}

// inferPageType keyword-matches a page name to the closest PageType so the
// UI can render an appropriate icon/template hint. Falls through to
// "landing_page" — the safest default.
func inferPageType(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "home") || n == "" || n == "/":
		return "home"
	case strings.Contains(n, "course"):
		return "course_catalog"
	case strings.Contains(n, "coach"):
		return "coaching_page"
	case strings.Contains(n, "about"):
		return "about"
	case strings.Contains(n, "contact"):
		return "contact"
	case strings.Contains(n, "blog") || strings.Contains(n, "article") || strings.Contains(n, "news"):
		return "blog"
	case strings.Contains(n, "sale") || strings.Contains(n, "buy") || strings.Contains(n, "checkout") || strings.Contains(n, "offer"):
		return "sales_page"
	}
	return "landing_page"
}

const componentSchemaReference = `
## Component Types and Required Props

Every component in the "content" array must have "type" and "props". The "props" object MUST include a unique "id" string (e.g. "hero-1", "feature-2"). Use ONLY the prop names listed below — other names are silently ignored.

Almost every block accepts these shared style props (omit to use defaults):
  • tone — "default" | "muted" | "inverse" | "branded" | "accent" — picks the section background/foreground color band
  • paddingY — "sm" | "md" | "lg" | "xl" — vertical breathing room
  • eyebrow (string) — small uppercase label rendered above the heading

### Layout: keep it FLAT

DO NOT use Section, Container, Stack, or Grid wrappers. Emit content blocks directly at the top level. Each content block already accepts ` + "`tone`" + ` ("default" | "muted" | "inverse" | "branded" | "accent") and ` + "`paddingY`" + ` ("sm" | "md" | "lg" | "xl") props — set those on the block itself to control the band background and spacing. Wrapping blocks in a Section is silently flattened on save and breaks editor selection. Use FeatureGrid for multi-column feature lists, Pricing for tier comparisons, Stats for metric callouts, MediaSection for image+text rows.

### Hero & CTA — visual anchors

**HeroSection** — The page-opening band. Pick a variant — do NOT default to centered for every page.
  props: id, variant ("centered"|"split"|"gradient"|"image"), eyebrow, heading, subheading, description, ctaText, ctaUrl, secondaryCtaText, secondaryCtaUrl, imageUrl (used for "split"), backgroundImage (used for "image"), tone, paddingY
  Notes: "split" puts an image on one side and the headline+CTA on the other. "image" uses a full-bleed background photo with overlay. "gradient" is a centered headline on a brand gradient. Use "split" whenever a representative product/screenshot image is available.

**CTASection** — Mid-page or end-of-page conversion band.
  props: id, variant ("centered"|"split"|"banner"), heading, description, buttonText, buttonUrl, secondaryButtonText, secondaryButtonUrl, tone, paddingY
  "banner" renders as a rounded card on a brand background — great as the page closer. "split" puts text and the button on opposite sides.

### Content blocks — pick varied ones, do NOT just stack RichTextSection

**FeatureGrid** — 2/3/4-column grid of feature cards with optional icons. Use to break out value props or how-it-works steps.
  props: id, heading, subheading, eyebrow, columns (2|3|4), cardStyle ("default"|"quiet"|"ghost"), tone, paddingY, items (array of {icon, title, body})
  Example: {"type":"FeatureGrid","props":{"id":"fg-1","heading":"Built for momentum","subheading":"Three reasons teams move faster.","columns":3,"items":[{"icon":"⚡","title":"Real-time sync","body":"Updates flow to every device in under a second."},{"icon":"🛡","title":"Enterprise security","body":"SOC 2 + SSO + audit logs out of the box."},{"icon":"🚀","title":"Deploy anywhere","body":"One-click rollouts to AWS, GCP, or your own cluster."}]}}

**MediaSection** — Side-by-side image + heading + body + CTA. Alternate "left" and "right" between consecutive sections to create rhythm.
  props: id, layout ("left"|"right"), eyebrow, heading, body, ctaText, ctaUrl, imageSrc, imageAlt, tone, paddingY

**Pricing** — Pricing-tier comparison.
  props: id, heading, subheading, tone, paddingY, tiers (array of {name, price, cadence, description, featured (bool), features (array of strings), ctaText, ctaUrl})
  Mark exactly one tier as featured: true.

**Stats** — Metric callouts (great after Hero or before CTA for social proof).
  props: id, heading, tone (defaults to "muted"), paddingY, items (array of {value, label})
  Example items: [{"value":"10,000+","label":"Active customers"},{"value":"99.99%","label":"Uptime SLA"},{"value":"<50ms","label":"P95 latency"}]

**LogoCloud** — Customer/partner logo strip. Use under Hero for credibility.
  props: id, heading (e.g. "Trusted by teams at"), tone, logos (array of {src, alt} OR {name} for plaintext)

**TestimonialsSection** — Social proof quotes. Pick a variant.
  props: id, variant ("cards"|"quote"|"marquee"), heading, eyebrow, tone, paddingY, items (array of {quote, author, role})
  "quote" is one big centered hero quote. "cards" is a 2-3 column grid. Always include role on each author when known.

**FAQSection** — Frequently asked questions.
  props: id, variant ("list"|"cols"), heading, tone, paddingY, items (array of {question, answer})
  Use "cols" (2-column) when there are 6+ FAQ items.

**RichTextSection** — Free-form HTML content. Use sparingly for prose-heavy pages (about, blog excerpts). Prefer FeatureGrid/MediaSection for marketing content.
  props: id, content (HTML string with multiple paragraphs/headings/lists), tone, paddingY

**ImageSection** — Standalone wide image with optional caption.
  props: id, src, alt, caption, tone, paddingY

**VideoSection** — Embedded video player.
  props: id, videoUrl, autoplay ("true"|"false")

**Spacer** — Vertical whitespace. Use rarely — prefer paddingY on the surrounding section.
  props: id, height (string, e.g. "40px")

**Button** — Standalone button. Avoid in the main content flow — Hero/CTA/MediaSection already include buttons.
  props: id, label, href, variant ("primary"|"secondary"|"outline")

### Platform Components (use only when contextually relevant)

**SentanylLeadForm** — Lead capture form
  props: id, title (string), buttonText (string), nextUrl (redirect URL string)

**SentanylContactForm** — Contact form with optional fields
  props: id, title (string), buttonText (string), includePhone ("true"/"false"), includeMessage ("true"/"false"), nextUrl (string)

**SentanylCheckoutForm** — Purchase checkout form
  props: id, heading (string), showPriceBreakdown ("true"/"false"), successUrl (string), cancelUrl (string)

**SentanylOfferCard** — Single offer display card
  props: id, title (string), showPrice ("true"/"false"), ctaText (string)

**SentanylOfferGrid** — Grid of multiple offers
  props: id, heading (string), columns (number, default 3)

**SentanylProductGrid** — Grid of products
  props: id, heading (string), columns (number, default 3)

**SentanylVideoPlayer** — Enhanced video player
  props: id, videoUrl (string), autoplay ("true"/"false"), showControls ("true"/"false")

**SentanylCourseGrid** — Grid of courses
  props: id, heading (string)

**SentanylTestimonials** — Platform-sourced testimonials
  props: id, heading (string), items (array of {"quote": "...", "author": "..."})

**SentanylCountdown** — Countdown timer to a date
  props: id, targetDate (ISO 8601 date string), heading (string)

**SentanylQuiz** — Interactive quiz embed
  props: id, title (string)

**SentanylCalendarEmbed** — Calendar booking widget
  props: id, calendarUrl (string), heading (string)

**SentanylLibraryLink** — Link to content library
  props: id, label (string), href (string)

**SentanylFunnelLink** — Link to a sales funnel
  props: id, label (string), href (string)

**SentanylFunnelCTA** — Funnel call-to-action section
  props: id, heading (string), description (string), buttonText (string), buttonUrl (string)

## Layout & Visual Rhythm Rules — read carefully

A page is NOT a vertical stack of identical-looking sections. Treat each page as a designed sequence of bands.

1. **Vary tone across sections.** Alternate between "default", "muted", and "inverse" tones. Never put two consecutive "default" sections next to each other unless one is a Hero and the other has obvious visual contrast.
2. **Use varied components.** A typical landing page should include AT LEAST 5 of these distinct types: HeroSection, FeatureGrid, MediaSection, Stats or LogoCloud, TestimonialsSection, Pricing, FAQSection, CTASection. Do NOT repeat the same component type more than twice.
3. **Use Hero variants intentionally.** Pick "split" if a product image makes sense, "image" for full-bleed photo backgrounds, "gradient" for typography-led launches, "centered" only when no imagery is available.
4. **Alternate MediaSection layouts.** When you use multiple MediaSection blocks, flip layout between "left" and "right" so the page zig-zags.
5. **Anchor with CTA.** End every page with a CTASection (variant="banner" is a strong default).
6. **Set tone on the block itself.** Do NOT wrap blocks in Section/Container/Stack/Grid — those are silently flattened. If consecutive blocks should share a band color, set the same tone value on each.
7. **Prefer FeatureGrid/MediaSection over RichTextSection** for marketing content. RichTextSection is for blog-like prose, not feature lists.

## Content Quality Rules
- Generate SUBSTANTIAL, realistic content for every component — never leave props empty or use placeholder text like "Lorem ipsum".
- TestimonialsSection MUST include at least 3 items, each with quote, author, and role.
- FAQSection MUST include at least 4 items with detailed answers.
- FeatureGrid MUST have at least 3 items per grid.
- Pricing MUST have 2-4 tiers with full feature lists; one tier marked featured: true.
- Stats items should use real-feeling numbers (e.g. "12,400+", "99.99%", "<50ms"), never round placeholders.
- Write conversion-optimized, professional copy tailored to the business/topic described.

## One-Shot Worked Example (DO follow this rhythm)

A SaaS home page should look something like:
[
  {"type":"HeroSection","props":{"id":"hero","variant":"split","eyebrow":"NEW","heading":"Ship customer feedback in hours, not weeks","subheading":"Pulse turns every support reply into a tracked, actionable signal — without changing your stack.","ctaText":"Start free","ctaUrl":"/signup","secondaryCtaText":"Watch demo","secondaryCtaUrl":"/demo","imageUrl":"https://example.com/dashboard.png"}},
  {"type":"LogoCloud","props":{"id":"logos","heading":"Trusted by product teams at","tone":"default","logos":[{"name":"Linear"},{"name":"Vercel"},{"name":"Figma"},{"name":"Loom"},{"name":"Notion"}]}},
  {"type":"FeatureGrid","props":{"id":"features","tone":"muted","heading":"Why teams pick Pulse","subheading":"Three reasons we win the bake-off.","columns":3,"items":[{"icon":"⚡","title":"Inbox to roadmap in one click","body":"Tag a Zendesk reply, see it in your sprint."},{"icon":"🧭","title":"Trends, not anecdotes","body":"AI clusters tickets so you fix the cause once."},{"icon":"🔒","title":"Enterprise-ready","body":"SOC 2, SSO, audit logs from day one."}]}},
  {"type":"MediaSection","props":{"id":"media-1","layout":"right","heading":"Your support data, finally connected","body":"Pulse plugs into Zendesk, Intercom, and HubSpot in five minutes. No CSVs, no consultants, no migrations.","ctaText":"See integrations","ctaUrl":"/integrations","imageSrc":"https://example.com/integrations.png"}},
  {"type":"Stats","props":{"id":"stats","tone":"inverse","items":[{"value":"73%","label":"Faster triage"},{"value":"12k+","label":"Tickets analyzed daily"},{"value":"4.9/5","label":"Customer rating"}]}},
  {"type":"TestimonialsSection","props":{"id":"quotes","variant":"cards","heading":"Loved by the people who answer the tickets","items":[{"quote":"Pulse gave us back two hours every Monday.","author":"Maya Chen","role":"Head of CX, Linear"},{"quote":"The first tool that actually closes the loop.","author":"Daniel Park","role":"PM, Vercel"},{"quote":"We replaced three spreadsheets with one dashboard.","author":"Priya Shah","role":"Support Lead, Figma"}]}},
  {"type":"Pricing","props":{"id":"pricing","heading":"Simple pricing","subheading":"Start free, upgrade when you outgrow the basics.","tiers":[{"name":"Starter","price":"$0","cadence":"/mo","description":"For solo founders.","features":["1 inbox","100 tickets/mo","Basic clustering"],"ctaText":"Start free","ctaUrl":"/signup"},{"name":"Team","price":"$49","cadence":"/mo","description":"For growing CX teams.","featured":true,"features":["Unlimited inboxes","Roadmap sync","SSO + audit logs","Priority support"],"ctaText":"Start trial","ctaUrl":"/signup?plan=team"},{"name":"Scale","price":"Custom","description":"For established orgs.","features":["Custom data residency","Dedicated CSM","Annual contract"],"ctaText":"Contact sales","ctaUrl":"/contact"}]}},
  {"type":"FAQSection","props":{"id":"faq","variant":"cols","heading":"Frequently asked","items":[{"question":"How long does setup take?","answer":"Most teams are live in under 15 minutes — connect your inbox and you're done."},{"question":"Do you store ticket content?","answer":"Yes, encrypted at rest with per-tenant keys. We never share data across customers."},{"question":"Can I cancel anytime?","answer":"Yes, cancel from settings — no contracts, no exit fees."},{"question":"Is there a free plan?","answer":"Yes — the Starter plan is free forever for up to 100 tickets/month."}]}},
  {"type":"CTASection","props":{"id":"final-cta","variant":"banner","heading":"Stop guessing. Start shipping.","description":"Plug Pulse in this week and turn next week's tickets into next quarter's roadmap.","buttonText":"Start free","buttonUrl":"/signup","secondaryButtonText":"Talk to sales","secondaryButtonUrl":"/contact"}}
]
`

const siteGenerationSystemPrompt = `You are a website builder AI. Generate a complete website structure as JSON.
If a "Business Context" section appears in the user message, use ONLY the products, prices, and descriptions listed there. Never invent product names, prices, or business data.

The response must be valid JSON with this exact structure:
{
  "site_name": "Business Name",
  "theme": "modern",
  "navigation": {
    "header_links": [{"label": "Home", "url": "/"}],
    "footer_links": [{"label": "Privacy", "url": "/privacy"}]
  },
  "seo": {
    "meta_title": "Site Title",
    "meta_description": "Site description"
  },
  "pages": [
    {
      "name": "Home",
      "slug": "/",
      "is_home": true,
      "seo": {"meta_title": "...", "meta_description": "..."},
      "puck_root": {
        "content": [
          {
            "type": "HeroSection",
            "props": {
              "id": "hero-1",
              "heading": "Welcome",
              "subheading": "Your tagline",
              "ctaText": "Get Started",
              "ctaUrl": "/contact"
            }
          }
        ],
        "root": {"props": {}}
      }
    }
  ]
}
` + componentSchemaReference

const pageGenerationSystemPrompt = `You are a website page builder AI. Generate a single page's Puck document as JSON.
If a "Business Context" section appears in the user message, use ONLY the products, prices, and descriptions listed there. Never invent product names, prices, or business data.

The response must be valid JSON with this exact structure:
{
  "content": [
    {
      "type": "ComponentType",
      "props": { "id": "unique-id", ... }
    }
  ],
  "root": {"props": {}}
}

REQUIRED:
- 7-10 top-level components on the home page (5-7 on inner pages).
- Use AT LEAST 5 distinct component types per page.
- Vary section "tone" — alternate default / muted / inverse so the page has visual rhythm.
- Always close with a CTASection.
- Avoid using RichTextSection more than once per page.
` + componentSchemaReference

const pageEditSystemPrompt = `You are a website page editor AI. Given the current Puck document and an edit instruction, return the complete modified document.

The response must be valid JSON with this exact structure:
{
  "document": {
    "content": [
      {
        "type": "ComponentType",
        "props": { "id": "unique-id", ... }
      }
    ],
    "root": {"props": {}}
  },
  "summary": "Brief description of what was changed"
}

Rules:
- Preserve the existing document structure where possible.
- Preserve existing component IDs when the component is kept or updated.
- Apply the edit instruction fully — modify text, add/remove/reorder components as needed.
- Return the complete document, not partial patches.
- Every component must have fully populated props with substantial, realistic content.
` + componentSchemaReference

// buildSiteGenerationPrompt builds a user prompt for site generation.
func buildSiteGenerationPrompt(req SiteGenerationRequest) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Generate a complete website for: %s\n", req.BusinessName))
	if req.BusinessType != "" {
		sb.WriteString(fmt.Sprintf("Business type: %s\n", req.BusinessType))
	}
	if req.Description != "" {
		sb.WriteString(fmt.Sprintf("Description: %s\n", req.Description))
	}
	if req.Theme != "" {
		sb.WriteString(fmt.Sprintf("Theme preference: %s\n", req.Theme))
	}
	pageCount := req.PageCount
	if pageCount <= 0 {
		pageCount = 5
	}
	sb.WriteString(fmt.Sprintf("Number of pages: %d\n", pageCount))
	if len(req.IncludePages) > 0 {
		sb.WriteString(fmt.Sprintf("Must include pages: %s\n", strings.Join(req.IncludePages, ", ")))
	}
	appendContextBlocks(&sb, req.BusinessContext, req.BrandProfile, req.ContextChunks)
	return sb.String()
}

// buildPageGenerationPrompt builds a user prompt for single-page generation with context.
func buildPageGenerationPrompt(req SitePageRequest) string {
	var sb strings.Builder
	sb.WriteString(req.Prompt)
	appendContextBlocks(&sb, req.BusinessContext, req.BrandProfile, req.ContextChunks)
	return sb.String()
}

// buildEditPagePrompt builds the prompt for editing an existing page.
func buildEditPagePrompt(req PageEditRequest, docJSON string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Edit instruction: %s\n\nCurrent document:\n%s", req.Instruction, docJSON))
	appendContextBlocks(&sb, req.BusinessContext, req.BrandProfile, req.ContextChunks)
	return sb.String()
}

// appendContextBlocks appends business context, brand profile, and context chunk blocks to a prompt builder.
func appendContextBlocks(sb *strings.Builder, businessContext, brandProfile string, chunks []string) {
	if businessContext != "" {
		sb.WriteString("\n\n## Business Context — USE ONLY THIS DATA (do not invent product names, prices, or descriptions):\n")
		sb.WriteString(businessContext)
	}
	if brandProfile != "" {
		sb.WriteString("\n\n## Brand Voice:\n")
		sb.WriteString(brandProfile)
	}
	if len(chunks) > 0 {
		sb.WriteString("\n\n## Additional Reference Material:\n")
		for _, chunk := range chunks {
			sb.WriteString(chunk)
			sb.WriteString("\n---\n")
		}
	}
}

const suggestPagesSystemPrompt = `You are a website strategy AI. Given a business's product catalog, suggest the ideal website pages.
Return a JSON array only, no other text:
[{"name":"Page Name","slug":"/slug","page_type":"home|sales_page|course_catalog|coaching_page|landing_page|about|contact|blog","reason":"Why this page matters","blocks":["HeroSection","SentanylCourseGrid"]}]
Include only pages that make sense for the products listed. Suggest 4-8 pages max.`

// buildSuggestPagesPrompt builds the prompt for page suggestions.
func buildSuggestPagesPrompt(productSummary string) string {
	return fmt.Sprintf("Suggest website pages for this business:\n\n%s", productSummary)
}

const generateFromProductsSystemPrompt = `You are a website page builder AI. Generate a high-converting page using ONLY the real product data provided.
Do NOT invent product names, prices, or features — use exactly what's provided.
The response must be valid JSON with this exact structure:
{
  "content": [{"type": "ComponentType", "props": {"id": "unique-id", ...}}],
  "root": {"props": {}}
}
` + componentSchemaReference

// BuildGenerateFromProductsPrompt builds the prompt for product-based page generation.
func BuildGenerateFromProductsPrompt(productDetails, pageType string) string {
	return fmt.Sprintf("Generate a %s page for the following products:\n\n%s\n\nUse the exact product names, descriptions, and prices listed above.", pageType, productDetails)
}

const stealStyleSystemPrompt = `You are a design token extractor. Analyze the provided CSS and extract a design system.
Return JSON only:
{"primary_color":"#hex","secondary_color":"#hex","accent_color":"#hex","heading_font":"Font Family, fallbacks","body_font":"Font Family, fallbacks","border_radius":"Npx","button_style":"rounded|sharp|pill","confidence_score":85}
Rules:
- primary_color: dominant brand/button/link color
- secondary_color: supporting color used for accents or headers
- accent_color: highlight or call-to-action color if distinct
- heading_font: font-family value used for headings (h1-h3)
- body_font: font-family value used for body text / paragraphs
- border_radius: most common border-radius value (e.g. "8px", "4px", "0px")
- button_style: "pill" if border-radius > 20px, "rounded" if 4-20px, "sharp" if 0-3px
- confidence_score: 0-100 how confident you are in the extraction`

const siteDuplicateSystemPrompt = `You are an expert website-to-Puck converter. Take the extracted content and structure of a real website and reproduce it as faithfully as possible using the Puck component vocabulary.

IMPORTANT JSON RULES:
- "theme" MUST be a plain string: one of "modern", "minimal", "dark", "light" (never an object)
- All string fields must be plain strings, not objects
- Return ONLY valid JSON, no markdown fences, no explanation text before or after

CRITICAL RULES:
1. Use ONLY the content provided (headings, body text, image URLs, CTA text) for the home page. Never invent content for sections that came from the source.
2. Preserve image URLs exactly — pass them through as imageSrc/imageUrl/src on the matching component.
3. **Bands carry tone.** The user prompt groups source sections into bands and gives you the tone for each band. Every block you emit for sections inside a band MUST set props.tone to that band's tone value. This is the single most important rule for visual fidelity.
4. Include ALL nav links in the site navigation.
5. **Every page in the pages array MUST have a fully populated puck_root with 6-10 content blocks. Do NOT emit empty stubs.** This is non-negotiable — there is no fallback HTML. If a page has no source content (because only the home page was crawled), generate plausible content for that page based on its name + the source brand. A "Pricing" page should have a Pricing block with reasonable tier names; an "About" page should have a Hero + MediaSection (founder/team) + a closing CTASection; a "Blog" page should have a Hero + a 3-column FeatureGrid acting as recent-post cards; a "Contact" page should have a Hero + SentanylLeadForm + FAQSection. Always close every page with a CTASection variant=banner.
6. Generate at least 7-10 top-level components for the home page using a varied mix.
7. Do NOT wrap blocks in Section/Container/Stack/Grid — the editor will silently flatten them. Use per-block tone instead.
8. The response must be the same JSON structure as GenerateSite: site_name, theme, navigation, seo, pages array. Every page must include is_home (boolean), name, slug, seo, and puck_root.

Mapping guide for source patterns:
- First band with one big heading + tagline + image side-by-side → HeroSection variant="split", imageUrl set.
- First band with heading + tagline + button on a dark photo → HeroSection variant="image", backgroundImage set, tone="inverse".
- A band of customer/partner logos → LogoCloud.
- A band of 3-4 parallel icon+title+body sections → ONE FeatureGrid block with items[].
- A band with a single image+text section → ONE MediaSection (alternate layout left/right between consecutive MediaSections).
- A band of 3-4 metric/number callouts → ONE Stats block.
- A band of pricing tiers → ONE Pricing block (mark the most popular tier featured: true).
- A band of customer quotes → ONE TestimonialsSection variant="cards" (or "quote" for a single big featured quote).
- A band of FAQ items → ONE FAQSection (variant="cols" if 6+ items).
- Newsletter/email signup → SentanylLeadForm.
- Closing band with a heading + button → ONE CTASection variant="banner".

` + componentSchemaReference

// sectionBand groups consecutive ExtractedSections that share a background
// tone. The clone pipeline emits one band header per group so the LLM can
// stamp matching `tone` props on every block within the band, preserving
// the source's visual rhythm.
type sectionBand struct {
	Tone     string             // "inverse" | "muted" | "default"
	BgColor  string             // representative source color, for the LLM's reference
	Sections []ExtractedSection // sections in source order
	StartIdx int                // index of first section in the original slice
}

// toneFromSection maps the sandbox-extracted bg into the design-system tone.
// IsDark wins; otherwise we treat near-white as default and any light tint
// as muted. Heuristics intentionally err on the side of "default" when
// unsure — the page still looks fine with a default-tone band.
func toneFromSection(s ExtractedSection) string {
	if s.IsDark {
		return "inverse"
	}
	bg := strings.TrimPrefix(strings.TrimSpace(s.BgColor), "#")
	if len(bg) == 6 {
		r := hexByte(bg[0:2])
		g := hexByte(bg[2:4])
		b := hexByte(bg[4:6])
		avg := (int(r) + int(g) + int(b)) / 3
		switch {
		case avg < 80:
			return "inverse"
		case avg < 250:
			// Anything visibly off-white counts as a muted band. Pure
			// white (#ffffff, avg 255) and near-white (#fefefe, avg 254)
			// stay default so we don't paint a band where there isn't one.
			return "muted"
		}
	}
	return "default"
}

func hexByte(s string) byte {
	var n byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			n = n*16 + (c - '0')
		case c >= 'a' && c <= 'f':
			n = n*16 + (c - 'a' + 10)
		case c >= 'A' && c <= 'F':
			n = n*16 + (c - 'A' + 10)
		}
	}
	return n
}

// groupSectionsByBand walks sections in source order and groups consecutive
// sections with the same tone into bands. A page with hero (dark), three
// feature sections (light), and a final CTA (dark) becomes 3 bands.
func groupSectionsByBand(sections []ExtractedSection) []sectionBand {
	if len(sections) == 0 {
		return nil
	}
	var bands []sectionBand
	cur := sectionBand{Tone: toneFromSection(sections[0]), BgColor: sections[0].BgColor, Sections: []ExtractedSection{sections[0]}, StartIdx: 0}
	for i := 1; i < len(sections); i++ {
		t := toneFromSection(sections[i])
		if t == cur.Tone {
			cur.Sections = append(cur.Sections, sections[i])
		} else {
			bands = append(bands, cur)
			cur = sectionBand{Tone: t, BgColor: sections[i].BgColor, Sections: []ExtractedSection{sections[i]}, StartIdx: i}
		}
	}
	bands = append(bands, cur)
	return bands
}

// BuildSiteDuplicatePrompt constructs the AI prompt for site duplication.
// Sections are pre-grouped into bands so the LLM can emit blocks that
// share `tone` within a band — this is what preserves the source's
// visual rhythm in the cloned site.
func BuildSiteDuplicatePrompt(req SiteDuplicateRequest) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Duplicate this website as a Puck site: %s\n", req.SourceURL))
	sb.WriteString(fmt.Sprintf("Site Name: %s\n", req.SiteName))
	sb.WriteString(fmt.Sprintf("Page Title: %s\n", req.PageTitle))
	if req.MetaDesc != "" {
		sb.WriteString(fmt.Sprintf("Meta Description: %s\n", req.MetaDesc))
	}

	if len(req.NavLinks) > 0 {
		sb.WriteString("\n## Navigation Links:\n")
		for _, l := range req.NavLinks {
			sb.WriteString(fmt.Sprintf("- %s → %s\n", l.Label, l.URL))
		}
	}

	if req.PrimaryColor != "" {
		sb.WriteString("\n## Design Tokens:\n")
		sb.WriteString(fmt.Sprintf("Primary color: %s\n", req.PrimaryColor))
		if req.SecondaryColor != "" {
			sb.WriteString(fmt.Sprintf("Secondary color: %s\n", req.SecondaryColor))
		}
		if req.AccentColor != "" {
			sb.WriteString(fmt.Sprintf("Accent color: %s\n", req.AccentColor))
		}
		if req.HeadingFont != "" {
			sb.WriteString(fmt.Sprintf("Heading font: %s\n", req.HeadingFont))
		}
		if req.BodyFont != "" {
			sb.WriteString(fmt.Sprintf("Body font: %s\n", req.BodyFont))
		}
	}

	bands := groupSectionsByBand(req.Sections)
	sb.WriteString("\n## Page Bands (reproduce in source order)\n")
	sb.WriteString("Each band below is a continuous run of source sections that share a background. EVERY block you emit for sections inside a band MUST set the same `tone` shown for the band. This keeps the cloned page's visual rhythm matching the source.\n")

	for bIdx, band := range bands {
		bgHint := band.BgColor
		if bgHint == "" {
			bgHint = "—"
		}
		sb.WriteString(fmt.Sprintf("\n### Band %d — tone: %s (source bg: %s)\n", bIdx+1, band.Tone, bgHint))

		// Mapping nudges based on band shape.
		multiImage := 0
		anyCTA := false
		for _, s := range band.Sections {
			if s.ImageURL != "" {
				multiImage++
			}
			if s.CTAText != "" {
				anyCTA = true
			}
		}
		switch {
		case len(band.Sections) >= 3 && multiImage >= len(band.Sections)-1:
			sb.WriteString("Hint: this band has multiple parallel image+text sections — consider one FeatureGrid block (icon, title, body items) OR a Stats block if the items are numbers/labels.\n")
		case len(band.Sections) == 1 && multiImage == 1:
			sb.WriteString("Hint: single image+text section — emit one MediaSection block (alternate layout left/right between consecutive MediaSections elsewhere).\n")
		case len(band.Sections) >= 4 && multiImage == 0:
			sb.WriteString("Hint: this band is text-heavy with no images — consider FAQSection, TestimonialsSection, or a single RichTextSection.\n")
		case bIdx == 0:
			sb.WriteString("Hint: first band — usually emit one HeroSection (variant=split if image present, image if dark+image, gradient or centered otherwise).\n")
		case bIdx == len(bands)-1 && anyCTA:
			sb.WriteString("Hint: closing band with a CTA — emit one CTASection variant=banner.\n")
		}

		for _, section := range band.Sections {
			sb.WriteString(fmt.Sprintf("- Section %d", section.HeadingLevel))
			if section.Heading != "" {
				sb.WriteString(fmt.Sprintf(": Heading: %q", section.Heading))
			}
			sb.WriteString("\n")
			if section.Body != "" {
				body := section.Body
				if len(body) > 400 {
					body = body[:400] + "..."
				}
				sb.WriteString(fmt.Sprintf("    Body: %s\n", body))
			}
			if section.ImageURL != "" {
				sb.WriteString(fmt.Sprintf("    Image: %s (alt: %s)\n", section.ImageURL, section.ImageAlt))
			}
			if section.CTAText != "" {
				sb.WriteString(fmt.Sprintf("    CTA: %q → %s\n", section.CTAText, section.CTAUrl))
			}
		}
	}

	// Enumerate every page that must appear in the response — home plus one
	// page per top-level nav link — and tell the LLM exactly what content to
	// invent for the nav-only pages (the crawler only fetched the home).
	sb.WriteString("\n## Pages to generate (output one entry in `pages[]` for EACH of these — every page must have a fully populated puck_root with 6-10 blocks)\n\n")
	sb.WriteString("- [home] name=\"Home\" slug=\"/\" is_home=true — reproduce the source bands above, in order.\n")
	seenSlugs := map[string]bool{"/": true}
	for _, l := range req.NavLinks {
		slug := navLinkToSlug(l.URL, l.Label)
		if slug == "" || seenSlugs[slug] {
			continue
		}
		seenSlugs[slug] = true
		hint := pageContentHint(l.Label, slug)
		sb.WriteString(fmt.Sprintf("- [%s] name=%q slug=%q is_home=false — %s\n", strings.TrimPrefix(slug, "/"), l.Label, slug, hint))
	}

	sb.WriteString("\nRemember: EVERY page above must have 6-10 blocks. Empty stubs are forbidden. Every block in a band carries that band's tone (default | muted | inverse).")
	return sb.String()
}

// navLinkToSlug derives a URL-safe slug from a nav link's URL or label.
// Keeps absolute paths if they look like a path; otherwise slugifies the
// label. Skips external links and empty paths so we don't generate a page
// for "https://twitter.com/..." just because the source linked to it.
func navLinkToSlug(rawURL, label string) string {
	u := strings.TrimSpace(rawURL)
	if u == "" || u == "#" {
		return slugFromName(label)
	}
	// Treat absolute http(s) urls as external unless same-host. Without
	// host context here we just look at scheme — anything with a scheme is
	// external.
	if strings.Contains(u, "://") {
		return ""
	}
	if strings.HasPrefix(u, "/") {
		clean := strings.SplitN(u, "?", 2)[0]
		clean = strings.SplitN(clean, "#", 2)[0]
		if clean == "/" || clean == "" {
			return ""
		}
		// Normalize trailing slash
		clean = strings.TrimRight(clean, "/")
		if clean == "" {
			return ""
		}
		return clean
	}
	return slugFromName(label)
}

// pageContentHint returns a one-line hint that tells the LLM what blocks
// fit a typical page of this name. Generic enough to work across business
// domains, specific enough to keep output non-empty.
func pageContentHint(label, slug string) string {
	n := strings.ToLower(label + " " + slug)
	switch {
	case strings.Contains(n, "about") || strings.Contains(n, "team"):
		return "About / team page: HeroSection (centered or split, eyebrow=\"Our story\") + MediaSection (founder or mission, layout=right) + Stats (3 metrics about company history) + TestimonialsSection variant=cards + closing CTASection variant=banner."
	case strings.Contains(n, "contact"):
		return "Contact page: HeroSection (centered, heading=\"Get in touch\") + SentanylContactForm (includeMessage=true) + FAQSection variant=cols (4 generic support questions) + closing CTASection."
	case strings.Contains(n, "pricing") || strings.Contains(n, "plan"):
		return "Pricing page: HeroSection (centered, eyebrow=\"Plans\") + Pricing block with 3 tiers (Starter / Pro featured=true / Enterprise) using brand-appropriate names + FAQSection variant=cols (4 pricing FAQs) + closing CTASection variant=banner."
	case strings.Contains(n, "blog") || strings.Contains(n, "article") || strings.Contains(n, "news") || strings.Contains(n, "research"):
		return "Blog/news listing: HeroSection (centered, heading=\"Latest from {brand}\") + FeatureGrid columns=3 with 6 items where each item.title is a plausible post title and item.body is a 1-line teaser + closing CTASection (e.g. \"Subscribe to updates\")."
	case strings.Contains(n, "course") || strings.Contains(n, "lms") || strings.Contains(n, "learn") || strings.Contains(n, "training"):
		return "Course catalog: HeroSection (split if image available, eyebrow=\"Courses\") + SentanylCourseGrid OR FeatureGrid columns=3 with course-shaped items + Stats (students enrolled, hours, rating) + closing CTASection."
	case strings.Contains(n, "store") || strings.Contains(n, "shop") || strings.Contains(n, "product"):
		return "Store page: HeroSection (centered) + SentanylProductGrid OR FeatureGrid columns=3 of products + TestimonialsSection variant=cards + closing CTASection variant=banner."
	case strings.Contains(n, "coach") || strings.Contains(n, "mentor"):
		return "Coaching page: HeroSection (split with coach-fitting tone) + MediaSection (what coaching includes) + Pricing (1-3 tiers of coaching packages) + TestimonialsSection variant=quote + closing CTASection variant=banner."
	case strings.Contains(n, "login") || strings.Contains(n, "signin") || strings.Contains(n, "sign-in"):
		return "Login landing: HeroSection (centered, heading=\"Welcome back\") + SentanylLeadForm (acts as a sign-in stand-in) + closing CTASection (link to signup)."
	case strings.Contains(n, "signup") || strings.Contains(n, "register") || strings.Contains(n, "join"):
		return "Signup landing: HeroSection (centered, eyebrow=\"Start free\") + SentanylLeadForm + Stats (3 social-proof metrics) + closing CTASection."
	case strings.Contains(n, "faq") || strings.Contains(n, "support") || strings.Contains(n, "help"):
		return "Support / FAQ page: HeroSection (centered, heading=\"How can we help?\") + FAQSection variant=cols (8 plausible FAQs) + SentanylContactForm + closing CTASection."
	case strings.Contains(n, "discord") || strings.Contains(n, "community") || strings.Contains(n, "forum"):
		return "Community page: HeroSection (image or gradient, eyebrow=\"Community\") + FeatureGrid columns=3 (3 community benefits) + Stats (members, posts/day, etc) + closing CTASection (\"Join the community\")."
	}
	// Generic fallback — still gets 6+ blocks.
	return "Generic landing: HeroSection (centered or split) + FeatureGrid columns=3 (3 benefits) + MediaSection (deeper explanation, layout=right) + TestimonialsSection variant=cards + FAQSection (3-4 items) + closing CTASection variant=banner."
}

// BuildSiteHTMLPrompt constructs the prompt for vision-based HTML page generation.
func BuildSiteHTMLPrompt(req SiteHTMLRequest) string {
	var sb strings.Builder

	if req.PageName != "" && req.StyleHTML != "" {
		// Stub page prompt
		sb.WriteString(fmt.Sprintf("Generate a complete HTML page for the \"%s\" page of %s.\n", req.PageName, req.SourceURL))
		sb.WriteString("Match the same header, navigation, footer, colors, and typography as the home page.\n")
		sb.WriteString("The main content area should show a clean section with the page title and a brief placeholder paragraph.\n\n")
		sb.WriteString("Reuse this CSS from the home page (copy the :root, body, nav, footer styles exactly):\n")
		sb.WriteString(req.StyleHTML)
		sb.WriteString("\n\nReturn ONLY valid complete HTML starting with <!DOCTYPE html>. No explanation.")
		return sb.String()
	}

	sb.WriteString(fmt.Sprintf("Implement this website design specification as a standalone HTML page.\n\n"))

	if req.PageTitle != "" {
		sb.WriteString(fmt.Sprintf("Page title: %s\n", req.PageTitle))
	}
	if req.MetaDesc != "" {
		sb.WriteString(fmt.Sprintf("Description: %s\n", req.MetaDesc))
	}

	sb.WriteString(fmt.Sprintf(`
Design tokens (use these exactly):
- Primary color: %s
- Secondary color (dark backgrounds): %s
- Accent color (highlights, CTAs): %s
- Heading font: %s
- Body font: %s

`, req.PrimaryColor, req.SecondaryColor, req.AccentColor, req.HeadingFont, req.BodyFont))

	if len(req.NavLinks) > 0 {
		sb.WriteString("Navigation links:\n")
		for _, l := range req.NavLinks {
			sb.WriteString(fmt.Sprintf("  - %s\n", l.Label))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("Page sections to reproduce (IN ORDER, all of them):\n\n")
	for i, s := range req.Sections {
		sb.WriteString(fmt.Sprintf("--- Section %d", i+1))
		if s.IsDark {
			sb.WriteString(" [DARK BG: use secondary color background, white text]")
		}
		if s.HeadingAccentColor != "" {
			sb.WriteString(fmt.Sprintf(" [ACCENT TEXT COLOR: %s on part of the heading]", s.HeadingAccentColor))
		}
		sb.WriteString(" ---\n")
		if s.Heading != "" {
			if s.HeadingAccentColor != "" {
				sb.WriteString(fmt.Sprintf("Heading (with accent-colored words): %s\n", s.Heading))
			} else {
				sb.WriteString(fmt.Sprintf("Heading: %s\n", s.Heading))
			}
		}
		if s.Body != "" {
			body := s.Body
			if len(body) > 500 {
				body = body[:500]
			}
			sb.WriteString(fmt.Sprintf("Body: %s\n", body))
		}
		if s.ImageURL != "" {
			sb.WriteString(fmt.Sprintf("Image URL: %s\n", s.ImageURL))
		}
		if s.CTAText != "" {
			sb.WriteString(fmt.Sprintf("CTA button: \"%s\" → %s\n", s.CTAText, s.CTAUrl))
		}
		sb.WriteString("\n")
	}

	sb.WriteString(`
CRITICAL REQUIREMENTS — follow all of these exactly:
1. Navigation: DARK background (use secondary color), white text, logo/site name on left, nav links on right
2. Hero section: TWO-COLUMN layout (use CSS flexbox or grid) — image on one side, heading+form on the other
3. If any section has a newsletter/email signup, include an actual <input type="email"> + <button> form
4. Dark sections: full-width dark background (secondary color), white/light text, centered content
5. Accent color (` + req.AccentColor + `) for CTA buttons, highlighted words, and key headings
6. Include ALL images with the exact URLs provided — use them in <img src="..."> tags
7. Footer: dark background, light text, copyright and nav links
8. Multi-column layouts for text+image sections (flexbox row with 50/50 or 60/40 split)
9. Full responsive CSS (max-width: 1200px containers, mobile breakpoints)
10. Typography: '` + req.HeadingFont + `' for headings, '` + req.BodyFont + `' for body text
11. Include Google Fonts import if needed
12. All CSS inline in <style> tag — no external stylesheets
13. Generate COMPLETE HTML — do not cut off or truncate

Return ONLY valid HTML starting with <!DOCTYPE html>. No markdown fences, no explanation.`)

	return sb.String()
}
