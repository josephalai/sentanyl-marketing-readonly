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
	return sb.String()
}
