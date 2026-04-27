package ai

import (
	"fmt"
	"strings"
)

const componentSchemaReference = `
## Component Types and Required Props

Every component in the "content" array must have "type" and "props". The "props" object MUST include a unique "id" string (e.g. "hero-1", "text-2"). Use ONLY the prop names listed below — other names will be silently ignored.

### Layout / Content Components

**HeroSection** — Full-width hero banner with gradient background
  props: id, heading (string), subheading (string), ctaText (string), ctaUrl (string)

**RichTextSection** — Free-form HTML content block. IMPORTANT: the "content" prop must be an HTML string, NOT plain text.
  props: id, content (HTML string, e.g. "<h2>About Us</h2><p>We help businesses grow...</p><ul><li>Benefit one</li></ul>")

**ImageSection** — Image with optional caption
  props: id, src (image URL string), alt (string), caption (string)

**VideoSection** — Embedded video player
  props: id, videoUrl (string), autoplay ("true" or "false")

**CTASection** — Call-to-action banner with button
  props: id, heading (string), description (string), buttonText (string), buttonUrl (string)

**TestimonialsSection** — Customer testimonial quotes. IMPORTANT: the "items" prop must be an array of objects, each with "quote" and "author" strings. Without items, the section renders empty.
  props: id, heading (string), items (array of {"quote": "...", "author": "..."})
  Example: {"type": "TestimonialsSection", "props": {"id": "testimonials-1", "heading": "What People Say", "items": [{"quote": "This changed my life!", "author": "Jane D."}, {"quote": "Incredible results.", "author": "Mike S."}]}}

**FAQSection** — Frequently asked questions. IMPORTANT: the "items" prop must be an array of objects, each with "question" and "answer" strings. Without items, the section renders empty.
  props: id, heading (string), items (array of {"question": "...", "answer": "..."})
  Example: {"type": "FAQSection", "props": {"id": "faq-1", "heading": "FAQ", "items": [{"question": "How does it work?", "answer": "Simply sign up and..."}]}}

**Spacer** — Vertical whitespace
  props: id, height (string, e.g. "40px", "80px")

**Button** — Standalone button link
  props: id, label (string), href (string), variant ("primary" or "secondary" or "outline")

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

## Content Quality Rules
- Generate SUBSTANTIAL, realistic content for every component — never leave props empty or with generic placeholder text like "Lorem ipsum".
- RichTextSection content should have multiple paragraphs, headings, and/or lists with real, relevant copy.
- TestimonialsSection and FAQSection MUST include at least 3 items each with detailed, realistic content.
- Write conversion-optimized, professional copy tailored to the business/topic described.
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

Generate a page with at least 4-6 components. Every component must have fully populated props with substantial content.
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

const siteDuplicateSystemPrompt = `You are an expert website-to-Puck converter. Your job is to take extracted content and structure from a real website and reproduce it as faithfully as possible using Puck editor components.

IMPORTANT JSON RULES:
- "theme" MUST be a plain string: one of "modern", "minimal", "dark", "light" (never an object)
- All string fields must be plain strings, not objects
- Return ONLY valid JSON, no markdown fences, no explanation text before or after

CRITICAL RULES:
1. Use ONLY the content provided (headings, body text, image URLs, CTA text). Never invent content.
2. Preserve image URLs exactly as provided — use them in ImageSection props.
3. Match the visual structure: dark sections become dark-background CTASection or HeroSection; light sections become RichTextSection or CTASection.
4. Include ALL nav links in the site navigation.
5. Generate at least 5-8 components for the home page — do not make it sparse.
6. The response must be the same JSON structure as GenerateSite: site_name, theme, navigation, seo, pages array.

For dark-background sections: use CTASection with the prop "theme": "dark". This makes the section render with a dark background and white text.
For image+text sections: use ImageSection (with the actual image URL in "src") followed by RichTextSection.
For newsletter/email capture forms: use SentanylLeadForm.
For testimonials/reviews: use TestimonialsSection.
For FAQ: use FAQSection.
For the hero/headline section: use HeroSection with the "heading", "subheading", and "ctaText" props.
For a dark hero: use HeroSection (it renders with dark background by default).
Always include the "description" prop in CTASection with the body text from that section.

` + componentSchemaReference

// BuildSiteDuplicatePrompt constructs the AI prompt for site duplication.
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
		sb.WriteString(fmt.Sprintf("\n## Design Tokens:\n"))
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

	sb.WriteString("\n## Page Sections (reproduce all of these in order):\n")
	for i, section := range req.Sections {
		sb.WriteString(fmt.Sprintf("\n### Section %d", i+1))
		if section.IsDark {
			sb.WriteString(` [DARK BACKGROUND — use CTASection with "theme": "dark" prop]`)
		}
		sb.WriteString("\n")
		if section.Heading != "" {
			sb.WriteString(fmt.Sprintf("Heading (H%d): %s\n", section.HeadingLevel, section.Heading))
		}
		if section.Body != "" {
			body := section.Body
			if len(body) > 400 {
				body = body[:400] + "..."
			}
			sb.WriteString(fmt.Sprintf("Body text: %s\n", body))
		}
		if section.ImageURL != "" {
			sb.WriteString(fmt.Sprintf("Image: %s (alt: %s)\n", section.ImageURL, section.ImageAlt))
		}
		if section.CTAText != "" {
			sb.WriteString(fmt.Sprintf("CTA button: \"%s\" → %s\n", section.CTAText, section.CTAUrl))
		}
	}

	sb.WriteString("\n\nGenerate the complete Puck site JSON. Include the home page and stub pages for each navigation item.")
	return sb.String()
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
