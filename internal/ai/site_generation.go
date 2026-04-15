package ai

import (
	"fmt"
	"strings"
)

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

Available component types: HeroSection, RichTextSection, ImageSection, VideoSection, TestimonialsSection, FAQSection, CTASection, NavigationBar, Footer, Spacer, Columns, Button, SentanylLeadForm, SentanylCheckoutForm, SentanylOfferCard, SentanylOfferGrid, SentanylProductGrid, SentanylVideoPlayer, SentanylCourseGrid, SentanylTestimonials, SentanylCountdown, SentanylQuiz, SentanylCalendarEmbed, SentanylLibraryLink, SentanylFunnelLink, SentanylFunnelCTA, SentanylContactForm.

Generate professional, conversion-optimized content. Use realistic placeholder text.`

const pageGenerationSystemPrompt = `You are a website page builder AI. Generate a single page's Puck document as JSON.

The response must be valid JSON with this structure:
{
  "content": [
    {
      "type": "ComponentType",
      "props": { ... }
    }
  ],
  "root": {"props": {}}
}

Available component types: HeroSection, RichTextSection, ImageSection, VideoSection, TestimonialsSection, FAQSection, CTASection, Spacer, Columns, Button, SentanylLeadForm, SentanylCheckoutForm, SentanylOfferCard, SentanylOfferGrid, SentanylProductGrid, SentanylVideoPlayer, SentanylCourseGrid, SentanylTestimonials, SentanylCountdown, SentanylQuiz, SentanylCalendarEmbed, SentanylLibraryLink, SentanylFunnelLink, SentanylFunnelCTA, SentanylContactForm.

Generate professional, conversion-optimized content.`

const pageEditSystemPrompt = `You are a website page editor AI. Given the current Puck document and an edit instruction, return a set of patch operations.

The response must be valid JSON with this exact structure:
{
  "operations": [
    {
      "op": "replaceProps",
      "nodeId": "component-id-from-props.id",
      "props": {"heading": "New Heading"}
    }
  ],
  "summary": "Brief description of what was changed"
}

Allowed operations:
- "replaceProps": Update props on a component. Requires "nodeId" and "props".
- "insertAfter": Insert a new component after a target. Requires "nodeId" and "node" (the new component object with "type" and "props").
- "insertBefore": Insert a new component before a target. Requires "nodeId" and "node".
- "remove": Remove a component. Requires "nodeId".
- "insertAt": Insert a component at a specific index. Requires "node" and optionally "index" (defaults to end).
- "moveAfter": Move a component after another. Requires "nodeId" (source) and "path" (target nodeId).

Rules:
- Use the "id" field inside each component's "props" object as the "nodeId".
- Only modify what the instruction requests.
- Return the minimum set of operations needed.
- New nodes must have a "type" and "props" object with an "id" field.
- Do not return the full updated document — only the operations array.`

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
