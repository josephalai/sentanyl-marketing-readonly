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
  props: id, variant ("centered"|"split"|"gradient"|"image"), eyebrow, heading, subheading, description, ctaText, ctaUrl, secondaryCtaText, secondaryCtaUrl, imageUrl (used for "split"), imagePosition ("left"|"right"; mirror the source's hero layout — image-LEFT is the default modern pattern), imageAspect ("square"|"landscape"|"wide"|"tall"|"auto"; pass through the [aspect=…] hint from source), backgroundImage (used for "image"), tone, paddingY
  Notes: "split" puts an image on one side and the headline+CTA on the other. "image" uses a full-bleed background photo with overlay. "gradient" is a centered headline on a brand gradient. Use "split" whenever a representative product/screenshot image is available.

**CTASection** — Mid-page or end-of-page conversion band.
  props: id, variant ("centered"|"split"|"banner"), heading, description, buttonText, buttonUrl, secondaryButtonText, secondaryButtonUrl, tone, paddingY
  "banner" renders as a rounded card on a brand background — great as the page closer. "split" puts text and the button on opposite sides.

### Content blocks — pick varied ones, do NOT just stack RichTextSection

**FeatureGrid** — 2/3/4-column grid of feature cards with optional icons. Use to break out value props or how-it-works steps.
  props: id, heading, subheading, eyebrow, columns (2|3|4), cardStyle ("default"|"quiet"|"ghost"), tone, paddingY, items (array of {icon, title, body})
  Example: {"type":"FeatureGrid","props":{"id":"fg-1","heading":"Built for momentum","subheading":"Three reasons teams move faster.","columns":3,"items":[{"icon":"⚡","title":"Real-time sync","body":"Updates flow to every device in under a second."},{"icon":"🛡","title":"Enterprise security","body":"SOC 2 + SSO + audit logs out of the box."},{"icon":"🚀","title":"Deploy anywhere","body":"One-click rollouts to AWS, GCP, or your own cluster."}]}}

**MediaSection** — Side-by-side image + heading + body + CTA. Alternate "left" and "right" between consecutive sections to create rhythm.
  props: id, layout ("left"|"right"), eyebrow, heading, body, ctaText, ctaUrl, imageSrc, imageAlt, imageAspect ("square"|"landscape"|"wide"|"tall"|"auto"; pass through [aspect=…] from the source — when wide/tall, the renderer letterboxes instead of cover-cropping, which prevents stretched logos/banners), tone, paddingY

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

**RichTextSection** — Free-form HTML content. STRICT: ONLY use when there's substantial prose (>100 words of body text from the source). Never use for short taglines, single phrases, or "subheading-shaped" content — those belong as the description/subheading prop of an adjacent block. If you find yourself emitting a RichTextSection with one short paragraph, you should be using a HeroSection (centered) or merging into the next block. Defaults: tone="default", paddingY="lg".
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

**SentanylLeadForm** — Lead capture form (newsletter / email signup)
  props: id, title (string), subtitle (string; social proof or one-line context — e.g. "Join 125,000+ subscribers"), buttonText (string), tone ("default"|"muted"|"inverse"|"branded"), nextUrl (redirect URL string)

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
0. **Newsletter heading + hero image → keep BOTH the visual hero AND the form.** If the FIRST extracted section's heading is newsletter-shaped (contains "newsletter", "subscribe", "join my", "get my email") AND it has an imageUrl, the visual hero is the page's anchor — DO NOT delete it.
   - Block 0 MUST be HeroSection variant="split" with the source's imageUrl, the source heading, the source subheading/tagline, ctaText="Subscribe", ctaUrl="#subscribe", and tone="default" (or "inverse" if the source band is dark).
   - Block 1 MUST be SentanylLeadForm with id="subscribe", title=a short "Get on the list"-style restatement, subtitle=social proof if available (e.g. "Join 125,000+ subscribers"), buttonText="Subscribe", tone="muted" or "default".
   It is FORBIDDEN to emit a HeroSection with ctaText="Log in" / "Sign in" — that login link belongs in site navigation, never as the hero CTA. If a newsletter-headed first section has NO imageUrl, then Block 0 may be SentanylLeadForm directly.
   Worked example for the WITH-IMAGE case:
   WRONG → {"type":"HeroSection","props":{"variant":"split","heading":"Get My Weekly Newsletter","ctaText":"Log in","ctaUrl":"/login/","imageUrl":"…"}}
   ALSO WRONG → emitting only SentanylLeadForm and dropping the hero image entirely — this destroys the page's visual weight.
   RIGHT → [HeroSection variant=split with image + ctaText="Subscribe" + ctaUrl="#subscribe", then SentanylLeadForm id="subscribe"]
1. Use ONLY the content provided (headings, body text, image URLs, CTA text) for the home page. Never invent content for sections that came from the source. EXCEPTION: Stats blocks should have at least 3 items — if the source only surfaced one big metric, fabricate 2 brand-plausible adjacent metrics (e.g. "125k subscribers" → add "25 years" and "Top 1% podcast"). Stats with one item looks broken.
2. Preserve image URLs exactly — pass them through as imageSrc/imageUrl/src on the matching component.
3. **Bands carry tone.** The user prompt groups source sections into bands and gives you the tone for each band. Every block you emit for sections inside a band MUST set props.tone to that band's tone value. This is the single most important rule for visual fidelity.
4. Include ALL nav links in the site navigation.
5. **Every page in the pages array MUST have a fully populated puck_root with 6-10 content blocks. Do NOT emit empty stubs.** This is non-negotiable — there is no fallback HTML. If a page has no source content (because only the home page was crawled), generate plausible content for that page based on its name + the source brand. A "Pricing" page should have a Pricing block with reasonable tier names; an "About" page should have a Hero + MediaSection (founder/team) + a closing CTASection; a "Blog" page should have a Hero + a 3-column FeatureGrid acting as recent-post cards; a "Contact" page should have a Hero + SentanylLeadForm + FAQSection. Always close every page with a CTASection variant=banner.
6. Generate at least 7-10 top-level components for the home page using a varied mix.
7. Do NOT wrap blocks in Section/Container/Stack/Grid — the editor will silently flatten them. Use per-block tone instead.
8. The response must be the same JSON structure as GenerateSite: site_name, theme, navigation, seo, pages array. Every page must include is_home (boolean), name, slug, seo, and puck_root.
9. **EVERY block MUST carry meaningful content.** No empty blocks, no orphan tagline-only blocks, no RichTextSection containing a single half-finished sentence. If you'd emit a block whose only content is a stray phrase ("And let me help you achieve your full potential"), instead attach that phrase to the FOLLOWING block as its description/subheading. Standalone "tagline" blocks are forbidden — every block needs at least: a heading + (body OR cta OR image OR items[]).
10. **When a section has imageAspect=wide (a banner/wordmark/podcast badge) or imageAspect=tall (a phone screenshot/poster), DO NOT use it as a hero or media-section image with cover crop — emit it inside a LogoCloud, FeatureGrid item.image, or set the block's imageAspect prop to "wide" so the renderer letterboxes instead of stretching.**
11. **Hero image position: when the source image's position=left or position=right, set imagePosition on the HeroSection prop accordingly so the renderer mirrors the source layout.** Default for split heroes when unspecified: imagePosition="left" (matches the most common modern landing-page pattern).

Mapping guide for source patterns:
- First band with one big heading + tagline + image side-by-side → HeroSection variant="split", imageUrl set, subheading + ctaText REQUIRED.
- First band with heading + tagline + button on a dark photo → HeroSection variant="image", backgroundImage set, tone="inverse".
- **Newsletter / email signup section** (heading like "Get my newsletter", "Subscribe", "Join 100k+", section containing an email input or "Submit") → ONE SentanylLeadForm with title=heading, buttonText="Subscribe" (or the source's submit text). NEVER a HeroSection with a "Log in" button — the conversion event is the email capture, not a login.
- A heading like "Join 100,000+ subscribers", "Trusted by 5M readers", "Used by 12k teams" with one or two big numbers and minimal body → ONE Stats block (extract the numbers as items, not a Hero).
- A row of customer/partner logos → LogoCloud.
- A band of 3-4 parallel icon+title+body sections (often under a parent heading like "Here's how I can help" or "What you get") → ONE FeatureGrid block with the parent heading and N items.
- A band with a single image+text section → ONE MediaSection (alternate layout left/right between consecutive MediaSections).
- A band of 3-4 metric/number callouts → ONE Stats block.
- A band of pricing tiers → ONE Pricing block (mark the most popular tier featured: true).
- A band of customer quotes → ONE TestimonialsSection variant="cards" (or "quote" for a single big featured quote).
- A band of FAQ items → ONE FAQSection (variant="cols" if 6+ items).
- A row of podcast / app / store badges (Apple Podcasts, Spotify, App Store) → LogoCloud with the brand names.
- Closing band with a heading + button → ONE CTASection variant="banner".

DO NOT emit:
- A trailing section that is just a copyright string ("© Brand 2026 | Privacy | Terms") — the site footer is rendered by the navigation config; do not include it as a content block.
- A standalone HeroSection for an intra-page section heading. If you see a heading like "Here's how I can help" or "What's included" or "Learn how to make money" without its own image and primary CTA, attach it as the heading on the FOLLOWING block (FeatureGrid heading, MediaSection heading, Stats heading), do NOT make it its own Hero.
- More than ONE HeroSection per page. Subsequent visual anchors should be MediaSection, FeatureGrid, or Stats.

Required Hero fields: every HeroSection MUST set heading AND (subheading OR description) AND ctaText AND ctaUrl. Do not omit them. If the source's primary CTA is an email signup, swap the HeroSection for a SentanylLeadForm and let the next block carry the brand pitch.

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

// imageAspectCategory bucks an extracted image's pixel dimensions into one
// of the renderer's supported aspect modes. Wide banners (>=2:1) get
// letterboxed; ultra-tall portraits (taller than 1.5:1) likewise. Standard
// 4:3-ish photos get cover-cropped. The returned token flows through the
// prompt and ends up as `imageAspect` on the emitted block, which the
// renderer reads to set object-fit and the data-aspect attribute.
func imageAspectCategory(w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	r := float64(w) / float64(h)
	switch {
	case r >= 1.9:
		return "wide" // banner / podcast badge / wordmark — DON'T cover-crop
	case r <= 0.75:
		return "tall" // poster / phone screenshot — letterbox
	case r >= 1.25 && r < 1.9:
		return "landscape"
	default:
		return "square" // ~1:1 photos
	}
}

// firstHeading returns the heading of the first non-empty source section,
// skipping leading sections whose heading is blank.
func firstHeading(sections []ExtractedSection) string {
	for _, s := range sections {
		if h := strings.TrimSpace(s.Heading); h != "" {
			return h
		}
	}
	return ""
}

// firstHeroImagePosition returns the position label ("left"|"right") of
// the first source section whose image carries a left/right position
// signal. Used to drive the hero block's imagePosition default per-clone
// rather than via a renderer-side opinion.
func firstHeroImagePosition(sections []ExtractedSection) string {
	for _, s := range sections {
		if s.ImageURL == "" {
			continue
		}
		switch s.ImagePosition {
		case "left", "right":
			return s.ImagePosition
		}
	}
	return ""
}

// firstSectionWithHeading returns the first section that actually carries a
// heading. Useful for inspecting properties (image, body) of the page's
// real first content block, since the crawler sometimes leads with empty
// chrome sections.
func firstSectionWithHeading(sections []ExtractedSection) *ExtractedSection {
	for i := range sections {
		if strings.TrimSpace(sections[i].Heading) != "" {
			return &sections[i]
		}
	}
	return nil
}

// jsonString returns a JSON-quoted version of a string for safe embedding
// in prompt text (handles internal quotes and special chars).
func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

// isNewsletterHeading detects the email-capture phrasings that the LLM
// keeps mis-routing into HeroSection/Log-in CTAs.
func isNewsletterHeading(heading string) bool {
	h := strings.ToLower(heading)
	if h == "" {
		return false
	}
	for _, kw := range []string{
		"newsletter", "subscribe", "join my", "join the",
		"get my email", "get the email", "free email",
		"sign up", "signup", "sign-up", "weekly email",
		"weekly newsletter", "daily newsletter",
	} {
		if strings.Contains(h, kw) {
			return true
		}
	}
	return false
}

// isFooterJunk returns true for sections that are obviously page footers
// — empty heading combined with a body that's mostly copyright/legal text,
// or only a "Privacy Policy" / "Terms" / "Cookie Policy" CTA. The crawler's
// section walker doesn't always strip footers cleanly (especially when a
// brand logo image lives in the footer) and these leak into the LLM
// prompt as trailing junk blocks.
func isFooterJunk(s ExtractedSection) bool {
	heading := strings.ToLower(strings.TrimSpace(s.Heading))
	body := strings.ToLower(strings.TrimSpace(s.Body))
	cta := strings.ToLower(strings.TrimSpace(s.CTAText))

	junkCTAs := []string{"privacy policy", "terms of use", "terms of service", "cookie", "copyright", "all rights", "earnings disclaimer"}
	legalPhrases := []string{"©", "all rights reserved", "privacy policy", "terms of use", "terms of service", "earnings disclaimer", "cookie policy"}

	// Heading-less sections whose body is dominated by legal text — even
	// if there's a brand logo image attached. Footers commonly carry the
	// site logo; that doesn't make them content.
	if heading == "" && body != "" && len(body) < 600 {
		hits := 0
		for _, l := range legalPhrases {
			if strings.Contains(body, l) {
				hits++
			}
		}
		if hits >= 2 {
			return true
		}
	}

	// Heading-less sections whose only CTA is legal navigation.
	if heading == "" && cta != "" {
		for _, j := range junkCTAs {
			if strings.Contains(cta, j) {
				return true
			}
		}
	}

	return false
}

// mergeSimilarConsecutiveSections finds runs of 3+ consecutive sections
// that share structural shape (same heading level, similar body length,
// no CTA divergence, all have-image or all not-have-image, similar tone)
// and merges them into a synthetic single section carrying GridItems.
// This catches the Elementor pattern where each "card" is its own
// .elementor-section — the LLM otherwise emits 3 stacked MediaSections
// instead of one FeatureGrid.
//
// Universal: triggers whenever the structural similarity is present,
// regardless of source platform.
func mergeSimilarConsecutiveSections(sections []ExtractedSection) []ExtractedSection {
	if len(sections) < 3 {
		return sections
	}
	out := make([]ExtractedSection, 0, len(sections))
	i := 0
	for i < len(sections) {
		// Find length of a run starting at i where each item shares shape
		// with sections[i].
		j := i + 1
		for j < len(sections) && sectionShapeMatches(sections[i], sections[j]) {
			j++
		}
		runLen := j - i
		if runLen >= 3 && sections[i].Heading != "" {
			// Merge: synth a parent section whose GridItems are the run.
			merged := ExtractedSection{
				Heading:      "", // intentionally blank — caller's prompt
				HeadingLevel: 2,
				IsDark:       sections[i].IsDark,
				BgColor:      sections[i].BgColor,
			}
			// Promote the FIRST card's heading as the grid heading only if
			// the run has 4+ items and they all start with verbs/labels —
			// otherwise leave heading blank and let the band-grouping band
			// header carry it.
			items := make([]ExtractedGridItem, 0, runLen)
			for k := i; k < j; k++ {
				items = append(items, ExtractedGridItem{
					Title:    sections[k].Heading,
					Body:     truncateForGrid(sections[k].Body),
					ImageURL: sections[k].ImageURL,
				})
			}
			merged.GridItems = items
			out = append(out, merged)
			i = j
			continue
		}
		out = append(out, sections[i])
		i++
	}
	return out
}

// sectionShapeMatches returns true if two sections look like cards in the
// same grid. Conservative criteria: card-shaped means short heading
// (≤45 chars), short body (≤220 chars or empty), same heading level,
// consistent has-image, no CTA divergence, same tone band. Heroes,
// image+text editorial blocks, and feature spotlights all fail one or
// more of these checks and survive as standalone blocks.
func sectionShapeMatches(a, b ExtractedSection) bool {
	if a.HeadingLevel != b.HeadingLevel {
		return false
	}
	if a.Heading == "" || b.Heading == "" {
		return false
	}
	// Card pattern: short heading text. Editorial / feature sections
	// typically have longer titles. 45 chars accommodates "Demystified
	// Techniques" / "Comprehensive Learning" but rules out "THE MOST
	// DEPENDABLE AND CONSISTENT WAY TO MANIFEST".
	if len(a.Heading) > 45 || len(b.Heading) > 45 {
		return false
	}
	// Both must EITHER have image or both not.
	if (a.ImageURL != "") != (b.ImageURL != "") {
		return false
	}
	// Card pattern: bodies are short (or empty). Long-body sections are
	// editorial spotlights — never merge those.
	la, lb := len(a.Body), len(b.Body)
	if la > 220 || lb > 220 {
		return false
	}
	if la > 0 && lb > 0 {
		shorter, longer := la, lb
		if shorter > longer {
			shorter, longer = longer, shorter
		}
		if float64(shorter)/float64(longer) < 0.4 {
			return false
		}
	}
	// CTA presence consistent.
	if (a.CTAText != "") != (b.CTAText != "") {
		return false
	}
	// Don't merge dark+light bands together.
	if a.IsDark != b.IsDark {
		return false
	}
	return true
}

func truncateForGrid(s string) string {
	if len(s) > 240 {
		return s[:240] + "…"
	}
	return s
}

// trimFooterSections drops trailing sections that are clearly page-chrome
// junk (footer copyright/privacy/terms). Only trims from the end — a
// section with no heading mid-page might be valid (e.g. an image-only
// callout) and we don't want to nuke it.
func trimFooterSections(sections []ExtractedSection) []ExtractedSection {
	for len(sections) > 0 && isFooterJunk(sections[len(sections)-1]) {
		sections = sections[:len(sections)-1]
	}
	return sections
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

	cleanSections := trimFooterSections(req.Sections)
	cleanSections = mergeSimilarConsecutiveSections(cleanSections)

	// If the very first source section is newsletter-headed, the LLM
	// keeps mistaking it for a Hero with a "Log in" CTA. Inject an
	// explicit per-clone override above the bands so the rule is
	// impossible to miss.
	// If the very first source section has NO heading but DOES have an
	// image (Kajabi/Squarespace pattern: hero is a CSS background or a
	// graphic-text image that the crawler can't read), the LLM tends to
	// drop it because rule 9 forbids empty blocks. Inject an explicit
	// override telling the LLM to emit it as a HeroSection using the
	// site title as the heading.
	if len(cleanSections) > 0 && strings.TrimSpace(cleanSections[0].Heading) == "" && cleanSections[0].ImageURL != "" {
		sb.WriteString("\n## CRITICAL OVERRIDE — FIRST SECTION HAS IMAGE BUT NO TEXT\n")
		sb.WriteString(fmt.Sprintf("Source section 0 is the page hero — it has an image (%s) but the crawler couldn't extract its heading text (likely rendered as graphic, in a CSS background, or as a custom font in a deeply-nested element). DO NOT drop this section. Emit Block 0 as HeroSection variant=\"split\" (or variant=\"image\" if imageAspect=wide and imagePosition=background) using:\n  heading = the site name from the page title (or %q),\n  subheading = the meta description if available,\n  imageUrl = %s,\n  imageAspect = pass through whatever [aspect=…] hint appears in the source listing,\n  imagePosition = pass through [position=…] when shown.\nIf the image is a logo/wordmark (small width, ends in .svg or .png with brand-name in filename), use variant=\"centered\" instead and put the logo in the eyebrow with a short tagline as heading.\n", cleanSections[0].ImageURL, req.SiteName, cleanSections[0].ImageURL))
	}
	// Page-level hero-image position: the LLM consistently fails to
	// transcribe imagePosition from the screenshot, so we surface the
	// detected position from the first section that actually carries
	// position info. This is a per-clone signal, not a global opinion.
	if pos := firstHeroImagePosition(cleanSections); pos != "" {
		sb.WriteString(fmt.Sprintf("\n## HERO IMAGE POSITION (sandbox-detected)\nThe source's first hero image is on the %q side of its heading. Set imagePosition=%q on the Home page's HeroSection so the cloned layout mirrors the source. Apply the same imagePosition signal to any subsequent MediaSections with imagery.\n", pos, pos))
	}

	if isNewsletterHeading(firstHeading(cleanSections)) {
		sb.WriteString("\n## CRITICAL OVERRIDE FOR THIS CLONE\n")
		first := firstSectionWithHeading(cleanSections)
		if first != nil && first.ImageURL != "" {
			sb.WriteString("The FIRST source section is newsletter-shaped AND has a hero image. Emit:\n")
			sb.WriteString("  Block 0 → HeroSection variant=\"split\" with imageUrl=" + first.ImageURL + ", heading=" + jsonString(first.Heading) + ", ctaText=\"Subscribe\", ctaUrl=\"#subscribe\". KEEP THE IMAGE — it is the page's primary visual anchor.\n")
			sb.WriteString("  Block 1 → SentanylLeadForm with id=\"subscribe\", title=a short call-to-subscribe, buttonText=\"Subscribe\".\n")
			sb.WriteString("DO NOT replace the hero with a standalone SentanylLeadForm — that strips the page's visual weight. DO NOT use ctaText=\"Log in\" or ctaUrl=\"/login/\".\n")
		} else {
			sb.WriteString("The FIRST source section is newsletter-shaped and has NO hero image. Block 0 of Home MUST be SentanylLeadForm (title=heading, subtitle=tagline, buttonText=\"Subscribe\"). Do NOT emit a HeroSection with ctaText=\"Log in\".\n")
		}
	}

	bands := groupSectionsByBand(cleanSections)

	// Tally how dark the source actually is. The LLM under-uses inverse
	// tone on its own — sites like mikedillard.com are 60-70% dark and
	// clones come back almost entirely default-toned. Inject a hard
	// minimum so the visual rhythm survives.
	inverseBands := 0
	for _, b := range bands {
		if b.Tone == "inverse" {
			inverseBands++
		}
	}
	if inverseBands >= 1 {
		sb.WriteString("\n## TONE BUDGET (mandatory)\n")
		sb.WriteString(fmt.Sprintf("The source has %d dark band(s) out of %d. The Home page MUST emit AT LEAST %d block(s) with tone=\"inverse\". Spread them through the page (don't pile them all at the bottom). Setting tone=\"inverse\" on a CTASection variant=\"banner\" alone is NOT enough — at least one inverse block must be a content block (FeatureGrid, MediaSection, or HeroSection). If the source is mostly dark, default to inverse and use default/muted as the contrast accent.\n", inverseBands, len(bands), inverseBands))
	}

	// Image preservation: every source section with an imageURL contributes
	// a usable photo. Tell the LLM to carry those URLs forward — the
	// most common mistake is emitting a MediaSection with no imageUrl
	// when the source had one.
	withImages := 0
	for _, s := range cleanSections {
		if s.ImageURL != "" {
			withImages++
		}
	}
	if withImages >= 2 {
		sb.WriteString("\n## IMAGE PRESERVATION\n")
		sb.WriteString(fmt.Sprintf("The source has %d sections with imageURLs. Every block you emit from a source section that had an imageURL MUST include the imageUrl prop. Empty MediaSections with no image are forbidden — if the source had an image at that position, use it. If you collapse multiple image sections into one FeatureGrid, each grid item gets the corresponding source image.\n", withImages))
	}
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
				aspect := imageAspectCategory(section.ImageWidth, section.ImageHeight)
				pos := section.ImagePosition
				extra := ""
				if aspect != "" {
					extra = fmt.Sprintf("  [aspect=%s, %dx%d]", aspect, section.ImageWidth, section.ImageHeight)
				}
				if pos != "" {
					extra += fmt.Sprintf("  [position=%s]", pos)
				}
				sb.WriteString(fmt.Sprintf("    Image: %s (alt: %s)%s\n", section.ImageURL, section.ImageAlt, extra))
			}
			if section.CTAText != "" {
				sb.WriteString(fmt.Sprintf("    CTA: %q → %s\n", section.CTAText, section.CTAUrl))
			}
			if section.FormType != "" {
				btn := section.FormButtonText
				if btn == "" {
					btn = "Subscribe"
				}
				switch section.FormType {
				case "newsletter":
					sb.WriteString(fmt.Sprintf("    FORM (newsletter signup, button=%q) → emit ONE SentanylLeadForm block (title=section heading, buttonText=%q, includeName=%v). Do NOT emit a HeroSection here even if heading looks hero-ish.\n",
						btn, btn, section.FormHasName))
				case "contact":
					sb.WriteString(fmt.Sprintf("    FORM (contact form with message field, button=%q) → emit ONE SentanylContactForm block (title=section heading, buttonText=%q, includeMessage=true).\n",
						btn, btn))
				}
			}
			if len(section.GridItems) >= 3 {
				sb.WriteString(fmt.Sprintf("    GRID DETECTED (%d repeating cards) → emit ONE FeatureGrid block (heading=section heading, columns=3 if items<=3 else 4) with these EXACT items:\n", len(section.GridItems)))
				for i, gi := range section.GridItems {
					if i >= 8 {
						break
					}
					title := gi.Title
					if len(title) > 80 {
						title = title[:80]
					}
					body := gi.Body
					if len(body) > 200 {
						body = body[:200] + "..."
					}
					sb.WriteString(fmt.Sprintf("      • title=%q body=%q", title, body))
					if gi.ImageURL != "" {
						sb.WriteString(fmt.Sprintf(" image=%s", gi.ImageURL))
					}
					sb.WriteString("\n")
				}
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

// navLinkToSlug derives a URL-safe slug from a nav link's URL and/or label.
// We ALWAYS create a local page for nav items — the source's nav reflects
// the user's expected information architecture, and a clean slug derived
// from the label gives the cloned site the same shape (the user can
// delete pages they don't want). For external links to social/CDN hosts
// (youtube, twitter, instagram) we still synthesize a local page using
// the label so the cloned site has e.g. "/subscribe" matching the source's
// "Subscribe" nav button.
func navLinkToSlug(rawURL, label string) string {
	if trimmed := strings.TrimSpace(label); trimmed != "" {
		if s := slugFromName(trimmed); s != "" {
			return s
		}
	}
	// Empty label — fall back to the URL path.
	u := strings.TrimSpace(rawURL)
	if u == "" || u == "#" {
		return ""
	}
	// Strip protocol+host to get a path-ish thing.
	if i := strings.Index(u, "://"); i >= 0 {
		rest := u[i+3:]
		if j := strings.Index(rest, "/"); j >= 0 {
			u = rest[j:]
		} else {
			u = ""
		}
	}
	if u == "" {
		return ""
	}
	clean := strings.SplitN(u, "?", 2)[0]
	clean = strings.SplitN(clean, "#", 2)[0]
	clean = strings.TrimRight(clean, "/")
	if clean == "" || clean == "/" {
		return ""
	}
	if !strings.HasPrefix(clean, "/") {
		clean = "/" + clean
	}
	return clean
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
	case strings.Contains(n, "subscribe") || strings.Contains(n, "newsletter") || strings.Contains(n, "email"):
		return "Newsletter signup page: SentanylLeadForm (title=heading from source like \"Get My Weekly Newsletter\", buttonText=\"Subscribe\") FIRST as the primary block + MediaSection explaining what subscribers get + Stats (subscriber count, open rate, years running — keep authentic to brand) + TestimonialsSection variant=quote + closing CTASection variant=banner. Do NOT lead with HeroSection — the conversion event IS the email capture."
	case strings.Contains(n, "show") || strings.Contains(n, "podcast") || strings.Contains(n, "episode"):
		return "Podcast / show page: HeroSection (split if image, eyebrow=\"The Show\") + LogoCloud (Apple Podcasts / Spotify / YouTube / Stitcher — list whichever platforms make sense) + FeatureGrid columns=3 with 3-6 recent episode-shaped items + closing CTASection (\"Subscribe wherever you listen\")."
	case strings.Contains(n, "review") || strings.Contains(n, "testimonial") || strings.Contains(n, "press"):
		return "Reviews / press page: HeroSection (centered, heading=\"What people say\") + TestimonialsSection variant=cards (6 testimonials) + Stats (3 social-proof metrics) + LogoCloud (publications / clients) + closing CTASection."
	case strings.Contains(n, "venture") || strings.Contains(n, "portfolio") || strings.Contains(n, "investment") || strings.Contains(n, "deal"):
		return "Ventures / portfolio page: HeroSection (centered, eyebrow=\"Ventures\") + FeatureGrid columns=3 (4-6 portfolio companies / deals as items with title + 1-line description + image if available) + MediaSection (investment thesis or criteria) + closing CTASection (\"Submit a deal\" or \"Get in touch\")."
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
