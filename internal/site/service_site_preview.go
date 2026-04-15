package site

import (
	"fmt"
	"html"
	"strings"

	"gopkg.in/mgo.v2/bson"

	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// ServicePreviewPage generates preview HTML from the current draft document.
func ServicePreviewPage(pageID, tenantID bson.ObjectId) (string, error) {
	page, err := GetSitePageByID(pageID, tenantID)
	if err != nil {
		return "", fmt.Errorf("page not found: %w", err)
	}
	if page.DraftDocument == nil {
		return "", fmt.Errorf("no draft document to preview")
	}

	// Fetch the parent site for navigation/SEO context.
	site, _ := GetSiteByID(page.SiteID, tenantID)

	html := RenderPuckDocumentToHTML(page.DraftDocument, page.SEO, site)

	// Save the preview HTML on the page.
	_ = UpdateSitePage(pageID, tenantID, bson.M{
		"last_preview_html": html,
	})

	// Create a preview version snapshot.
	latestVer, _ := GetLatestVersionNumber(pageID, tenantID)
	version := NewSitePageVersion(page.SiteID, pageID, tenantID, VersionTypePreview, latestVer+1)
	version.PuckRoot = page.DraftDocument
	version.RenderedHTML = html
	version.SEO = page.SEO
	version.Metadata = &SiteVersionMetadata{GeneratedBy: "preview"}
	_ = CreateSitePageVersion(version)

	return html, nil
}

// RenderPuckDocumentToHTML converts a Puck document tree into static HTML.
// This is Sentanyl's server-side renderer — Puck is NOT used at runtime.
func RenderPuckDocumentToHTML(doc map[string]any, seo *pkgmodels.SEOConfig, site *pkgmodels.Site) string {
	var sb strings.Builder

	title := "Sentanyl Website"
	metaDesc := ""
	if seo != nil {
		if seo.MetaTitle != "" {
			title = seo.MetaTitle
		}
		if seo.MetaDescription != "" {
			metaDesc = seo.MetaDescription
		}
	}

	sb.WriteString("<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n")
	sb.WriteString("<meta charset=\"UTF-8\">\n")
	sb.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1.0\">\n")
	sb.WriteString(fmt.Sprintf("<title>%s</title>\n", html.EscapeString(title)))
	if metaDesc != "" {
		sb.WriteString(fmt.Sprintf("<meta name=\"description\" content=\"%s\">\n", html.EscapeString(metaDesc)))
	}
	if seo != nil {
		if seo.CanonicalURL != "" {
			sb.WriteString(fmt.Sprintf("<link rel=\"canonical\" href=\"%s\">\n", html.EscapeString(seo.CanonicalURL)))
		}
		if seo.OpenGraphTitle != "" {
			sb.WriteString(fmt.Sprintf("<meta property=\"og:title\" content=\"%s\">\n", html.EscapeString(seo.OpenGraphTitle)))
		}
		if seo.OpenGraphDesc != "" {
			sb.WriteString(fmt.Sprintf("<meta property=\"og:description\" content=\"%s\">\n", html.EscapeString(seo.OpenGraphDesc)))
		}
		if seo.OpenGraphImageURL != "" {
			sb.WriteString(fmt.Sprintf("<meta property=\"og:image\" content=\"%s\">\n", html.EscapeString(seo.OpenGraphImageURL)))
		}
		if seo.NoIndex {
			sb.WriteString("<meta name=\"robots\" content=\"noindex, nofollow\">\n")
		}
	}
	sb.WriteString("<style>\n")
	sb.WriteString("* { margin: 0; padding: 0; box-sizing: border-box; }\n")
	sb.WriteString("body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; line-height: 1.6; color: #1a1a1a; }\n")
	sb.WriteString("img { max-width: 100%; height: auto; }\n")
	sb.WriteString(".section { padding: 60px 20px; max-width: 1200px; margin: 0 auto; }\n")
	sb.WriteString(".hero { text-align: center; padding: 80px 20px; background: linear-gradient(135deg, #667eea 0%, #764ba2 100%); color: white; }\n")
	sb.WriteString(".hero h1 { font-size: 2.5rem; margin-bottom: 1rem; }\n")
	sb.WriteString(".hero p { font-size: 1.2rem; opacity: 0.9; }\n")
	sb.WriteString(".cta-button { display: inline-block; padding: 12px 32px; background: #4f46e5; color: white; text-decoration: none; border-radius: 8px; font-weight: 600; margin-top: 1rem; }\n")
	sb.WriteString(".nav { background: #fff; border-bottom: 1px solid #e5e7eb; padding: 16px 20px; display: flex; justify-content: space-between; align-items: center; }\n")
	sb.WriteString(".nav-links { display: flex; gap: 24px; list-style: none; }\n")
	sb.WriteString(".nav-links a { color: #374151; text-decoration: none; font-weight: 500; }\n")
	sb.WriteString(".footer { background: #1f2937; color: #d1d5db; padding: 40px 20px; text-align: center; }\n")
	sb.WriteString(".columns { display: grid; grid-template-columns: repeat(auto-fit, minmax(300px, 1fr)); gap: 24px; }\n")
	sb.WriteString(".card { border: 1px solid #e5e7eb; border-radius: 12px; padding: 24px; }\n")
	sb.WriteString(".faq-item { border-bottom: 1px solid #e5e7eb; padding: 16px 0; }\n")
	sb.WriteString(".faq-item h3 { margin-bottom: 8px; }\n")
	sb.WriteString(".testimonial { background: #f9fafb; padding: 24px; border-radius: 12px; margin-bottom: 16px; }\n")
	sb.WriteString("</style>\n")
	sb.WriteString("</head>\n<body>\n")

	// Render navigation if site has one.
	if site != nil && site.Navigation != nil && len(site.Navigation.HeaderNavLinks) > 0 {
		sb.WriteString("<nav class=\"nav\"><div><strong>")
		sb.WriteString(html.EscapeString(site.Name))
		sb.WriteString("</strong></div><ul class=\"nav-links\">")
		for _, link := range site.Navigation.HeaderNavLinks {
			sb.WriteString(fmt.Sprintf("<li><a href=\"%s\">%s</a></li>", html.EscapeString(link.URL), html.EscapeString(link.Label)))
		}
		sb.WriteString("</ul></nav>\n")
	}

	// Render Puck content array.
	if content, ok := doc["content"].([]any); ok {
		for _, item := range content {
			comp, ok := item.(map[string]any)
			if !ok {
				continue
			}
			renderComponent(&sb, comp)
		}
	}

	// Render footer if site has footer links.
	if site != nil && site.Navigation != nil && len(site.Navigation.FooterNavLinks) > 0 {
		sb.WriteString("<footer class=\"footer\"><p>")
		for i, link := range site.Navigation.FooterNavLinks {
			if i > 0 {
				sb.WriteString(" | ")
			}
			sb.WriteString(fmt.Sprintf("<a href=\"%s\" style=\"color:#9ca3af\">%s</a>", html.EscapeString(link.URL), html.EscapeString(link.Label)))
		}
		sb.WriteString("</p></footer>\n")
	}

	sb.WriteString("</body>\n</html>")
	return sb.String()
}

// renderComponent renders a single Puck component to HTML.
func renderComponent(sb *strings.Builder, comp map[string]any) {
	compType, _ := comp["type"].(string)
	props, _ := comp["props"].(map[string]any)
	if props == nil {
		props = map[string]any{}
	}

	// escAttr escapes a string for safe use in HTML attributes.
	esc := func(s string) string { return html.EscapeString(s) }

	switch compType {
	case "HeroSection":
		heading, _ := props["heading"].(string)
		subheading, _ := props["subheading"].(string)
		ctaText, _ := props["ctaText"].(string)
		ctaURL, _ := props["ctaUrl"].(string)
		sb.WriteString("<section class=\"hero\">\n")
		if heading != "" {
			sb.WriteString(fmt.Sprintf("<h1>%s</h1>\n", esc(heading)))
		}
		if subheading != "" {
			sb.WriteString(fmt.Sprintf("<p>%s</p>\n", esc(subheading)))
		}
		if ctaText != "" {
			if ctaURL == "" {
				ctaURL = "#"
			}
			sb.WriteString(fmt.Sprintf("<a class=\"cta-button\" href=\"%s\">%s</a>\n", esc(ctaURL), esc(ctaText)))
		}
		sb.WriteString("</section>\n")

	case "RichTextSection":
		content, _ := props["content"].(string)
		sb.WriteString("<section class=\"section\">\n")
		sb.WriteString(content) // Rich text is intentionally unescaped HTML
		sb.WriteString("\n</section>\n")

	case "ImageSection":
		src, _ := props["src"].(string)
		alt, _ := props["alt"].(string)
		sb.WriteString("<section class=\"section\" style=\"text-align:center\">\n")
		sb.WriteString(fmt.Sprintf("<img src=\"%s\" alt=\"%s\">\n", esc(src), esc(alt)))
		sb.WriteString("</section>\n")

	case "VideoSection", "SentanylVideoPlayer":
		videoURL, _ := props["videoUrl"].(string)
		sb.WriteString("<section class=\"section\" style=\"text-align:center\">\n")
		sb.WriteString(fmt.Sprintf("<video controls style=\"max-width:100%%\"><source src=\"%s\"></video>\n", esc(videoURL)))
		sb.WriteString("</section>\n")

	case "CTASection":
		heading, _ := props["heading"].(string)
		buttonText, _ := props["buttonText"].(string)
		buttonURL, _ := props["buttonUrl"].(string)
		sb.WriteString("<section class=\"section\" style=\"text-align:center;background:#f3f4f6;padding:60px 20px\">\n")
		if heading != "" {
			sb.WriteString(fmt.Sprintf("<h2>%s</h2>\n", esc(heading)))
		}
		if buttonText != "" {
			if buttonURL == "" {
				buttonURL = "#"
			}
			sb.WriteString(fmt.Sprintf("<a class=\"cta-button\" href=\"%s\">%s</a>\n", esc(buttonURL), esc(buttonText)))
		}
		sb.WriteString("</section>\n")

	case "TestimonialsSection", "SentanylTestimonials":
		sb.WriteString("<section class=\"section\">\n<h2>Testimonials</h2>\n")
		if items, ok := props["items"].([]any); ok {
			for _, item := range items {
				if t, ok := item.(map[string]any); ok {
					quote, _ := t["quote"].(string)
					author, _ := t["author"].(string)
					sb.WriteString("<div class=\"testimonial\">\n")
					sb.WriteString(fmt.Sprintf("<p>\"%s\"</p>\n", esc(quote)))
					sb.WriteString(fmt.Sprintf("<p><strong>— %s</strong></p>\n", esc(author)))
					sb.WriteString("</div>\n")
				}
			}
		}
		sb.WriteString("</section>\n")

	case "FAQSection":
		sb.WriteString("<section class=\"section\">\n<h2>FAQ</h2>\n")
		if items, ok := props["items"].([]any); ok {
			for _, item := range items {
				if faq, ok := item.(map[string]any); ok {
					q, _ := faq["question"].(string)
					a, _ := faq["answer"].(string)
					sb.WriteString("<div class=\"faq-item\">\n")
					sb.WriteString(fmt.Sprintf("<h3>%s</h3>\n", esc(q)))
					sb.WriteString(fmt.Sprintf("<p>%s</p>\n", esc(a)))
					sb.WriteString("</div>\n")
				}
			}
		}
		sb.WriteString("</section>\n")

	case "Spacer":
		height, _ := props["height"].(string)
		if height == "" {
			height = "40px"
		}
		sb.WriteString(fmt.Sprintf("<div style=\"height:%s\"></div>\n", esc(height)))

	case "Button":
		label, _ := props["label"].(string)
		href, _ := props["href"].(string)
		if href == "" {
			href = "#"
		}
		sb.WriteString(fmt.Sprintf("<div class=\"section\" style=\"text-align:center\"><a class=\"cta-button\" href=\"%s\">%s</a></div>\n", esc(href), esc(label)))

	case "Columns":
		sb.WriteString("<section class=\"section\"><div class=\"columns\">\n")
		if cols, ok := props["columns"].([]any); ok {
			for _, col := range cols {
				if colMap, ok := col.(map[string]any); ok {
					sb.WriteString("<div class=\"card\">\n")
					if children, ok := colMap["children"].([]any); ok {
						for _, child := range children {
							if childComp, ok := child.(map[string]any); ok {
								renderComponent(sb, childComp)
							}
						}
					}
					sb.WriteString("</div>\n")
				}
			}
		}
		sb.WriteString("</div></section>\n")

	case "NavigationBar":
		// Rendered at site level, skip here.

	case "Footer":
		text, _ := props["text"].(string)
		sb.WriteString("<footer class=\"footer\"><p>")
		sb.WriteString(esc(text))
		sb.WriteString("</p></footer>\n")

	case "SentanylLeadForm", "SentanylContactForm":
		formTitle, _ := props["title"].(string)
		if formTitle == "" {
			formTitle = "Get in Touch"
		}
		sb.WriteString("<section class=\"section\">\n")
		sb.WriteString(fmt.Sprintf("<h2>%s</h2>\n", esc(formTitle)))
		sb.WriteString("<form style=\"max-width:500px;margin:0 auto\">\n")
		sb.WriteString("<input type=\"text\" placeholder=\"Name\" style=\"width:100%%;padding:12px;margin:8px 0;border:1px solid #d1d5db;border-radius:8px\">\n")
		sb.WriteString("<input type=\"email\" placeholder=\"Email\" style=\"width:100%%;padding:12px;margin:8px 0;border:1px solid #d1d5db;border-radius:8px\">\n")
		sb.WriteString("<button type=\"submit\" class=\"cta-button\" style=\"width:100%%;border:none;cursor:pointer\">Submit</button>\n")
		sb.WriteString("</form>\n</section>\n")

	case "SentanylCheckoutForm":
		sb.WriteString("<section class=\"section\" style=\"text-align:center\">\n")
		sb.WriteString("<div class=\"card\"><h3>Checkout</h3><p>Secure checkout powered by Sentanyl</p></div>\n")
		sb.WriteString("</section>\n")

	case "SentanylOfferCard":
		title, _ := props["title"].(string)
		sb.WriteString("<div class=\"card\" style=\"text-align:center\">\n")
		sb.WriteString(fmt.Sprintf("<h3>%s</h3>\n", esc(title)))
		sb.WriteString("<a class=\"cta-button\" href=\"#\">Get This Offer</a>\n")
		sb.WriteString("</div>\n")

	case "SentanylOfferGrid", "SentanylProductGrid", "SentanylCourseGrid":
		heading, _ := props["heading"].(string)
		if heading == "" {
			heading = compType
		}
		sb.WriteString("<section class=\"section\">\n")
		sb.WriteString(fmt.Sprintf("<h2>%s</h2>\n", esc(heading)))
		sb.WriteString("<div class=\"columns\"><div class=\"card\"><p>Items loaded dynamically</p></div></div>\n")
		sb.WriteString("</section>\n")

	case "SentanylCountdown":
		sb.WriteString("<section class=\"section\" style=\"text-align:center\">\n")
		sb.WriteString("<div style=\"font-size:2rem;font-weight:bold\">⏰ Countdown Timer</div>\n")
		sb.WriteString("</section>\n")

	case "SentanylQuiz":
		sb.WriteString("<section class=\"section\">\n")
		sb.WriteString("<div class=\"card\"><h3>Quiz</h3><p>Interactive quiz powered by Sentanyl</p></div>\n")
		sb.WriteString("</section>\n")

	case "SentanylCalendarEmbed":
		sb.WriteString("<section class=\"section\" style=\"text-align:center\">\n")
		sb.WriteString("<div class=\"card\"><h3>📅 Schedule a Call</h3></div>\n")
		sb.WriteString("</section>\n")

	case "SentanylLibraryLink", "SentanylFunnelLink", "SentanylFunnelCTA":
		label, _ := props["label"].(string)
		href, _ := props["href"].(string)
		if label == "" {
			label = compType
		}
		if href == "" {
			href = "#"
		}
		sb.WriteString(fmt.Sprintf("<div class=\"section\" style=\"text-align:center\"><a class=\"cta-button\" href=\"%s\">%s</a></div>\n", esc(href), esc(label)))

	default:
		// Unknown component — render as a generic section.
		sb.WriteString(fmt.Sprintf("<section class=\"section\"><p>[%s]</p></section>\n", esc(compType)))
	}
}
