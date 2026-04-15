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

const pageEditSystemPrompt = `You are a website page editor AI. Apply the requested edits to the given Puck document and return the result.

The response must be valid JSON with this structure:
{
  "updated_document": {
    "content": [...],
    "root": {"props": {}}
  },
  "summary": "Brief description of what was changed"
}

Apply the edit instruction precisely. Preserve existing components that are not being modified. Only change what the instruction requests.`

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
