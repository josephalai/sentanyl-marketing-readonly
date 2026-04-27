package site

import (
	"fmt"
	"html"
	"strings"

	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// ServicePreviewPage generates preview HTML from the current draft document.
// If the page has a PublishedHTML (set by site duplication), returns that directly
// for maximum fidelity without going through the Puck renderer.
func ServicePreviewPage(pageID, tenantID bson.ObjectId) (string, error) {
	page, err := GetSitePageByID(pageID, tenantID)
	if err != nil {
		return "", fmt.Errorf("page not found: %w", err)
	}

	// High-fidelity HTML from duplication — serve directly.
	if page.PublishedHTML != "" {
		return page.PublishedHTML, nil
	}

	// Stub or empty page — return a minimal placeholder rather than erroring.
	if page.DraftDocument == nil {
		site, _ := GetSiteByID(page.SiteID, tenantID)
		return renderStubPage(page, site), nil
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

// RenderPuckBodyOnly renders just the content blocks of a Puck document,
// without the surrounding <!doctype>/<head>/<body> shell. Newsletter post
// bodies are nested inside their own page chrome (Title + meta + the gate
// renderer), so they only want the inner block stream — and the gate
// splitter relies on the HTML containing the bare `<!--subscriber-break-->`
// and `<!--paywall-break-->` markers without any html wrapper.
func RenderPuckBodyOnly(doc map[string]any) string {
	var sb strings.Builder
	slice := coerceContentSlice(doc["content"])
	for _, item := range slice {
		comp := coerceMap(item)
		if comp == nil {
			continue
		}
		renderComponent(&sb, comp, "")
	}
	return sb.String()
}

// coerceContentSlice tolerates the various concrete slice types mgo and
// the JSON binder may produce for a Puck "content" array.
func coerceContentSlice(v any) []any {
	switch s := v.(type) {
	case []any:
		return s
	case []map[string]any:
		out := make([]any, len(s))
		for i, m := range s {
			out[i] = m
		}
		return out
	}
	return nil
}

// coerceMap tolerates components decoded as map[string]any or bson.M (a
// named type with the same underlying definition; the type assertion
// machinery treats them as distinct).
func coerceMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	if m, ok := v.(bson.M); ok {
		return map[string]any(m)
	}
	return nil
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
	sb.WriteString(buildGlobalStyleVars(site))
	sb.WriteString("* { margin: 0; padding: 0; box-sizing: border-box; }\n")
	sb.WriteString("body { font-family: var(--font-body, -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif); line-height: 1.6; color: #1a1a1a; }\n")
	sb.WriteString("img { max-width: 100%; height: auto; display: block; }\n")
	sb.WriteString(".section { padding: 60px 20px; max-width: 1200px; margin: 0 auto; }\n")
	// Hero uses primary gradient by default
	sb.WriteString(".hero { text-align: center; padding: 80px 20px; background: var(--color-secondary, #141414); color: white; }\n")
	sb.WriteString(".hero h1 { font-size: 2.8rem; margin-bottom: 1rem; font-family: var(--font-heading, inherit); }\n")
	sb.WriteString(".hero p { font-size: 1.2rem; opacity: 0.9; max-width: 600px; margin: 0 auto 1.5rem; }\n")
	sb.WriteString(".cta-button { display: inline-block; padding: 14px 36px; background: var(--color-accent, #65d46e); color: #000; text-decoration: none; border-radius: var(--border-radius, 4px); font-weight: 700; margin-top: 1rem; }\n")
	// Nav
	sb.WriteString(".nav { background: var(--color-secondary, #141414); color: white; padding: 16px 32px; display: flex; justify-content: space-between; align-items: center; position: sticky; top: 0; z-index: 100; }\n")
	sb.WriteString(".nav-brand { font-weight: 700; font-size: 1.1rem; color: white; text-decoration: none; }\n")
	sb.WriteString(".nav-links { display: flex; gap: 24px; list-style: none; }\n")
	sb.WriteString(".nav-links a { color: rgba(255,255,255,0.85); text-decoration: none; font-size: 0.9rem; font-weight: 500; }\n")
	sb.WriteString(".nav-links a:hover { color: white; }\n")
	// Dark sections: CTA with dark background
	sb.WriteString(".cta-section-dark { background: var(--color-secondary, #141414); color: white; padding: 80px 20px; text-align: center; }\n")
	sb.WriteString(".cta-section-dark h2 { font-size: 2.5rem; margin-bottom: 1rem; font-family: var(--font-heading, inherit); }\n")
	sb.WriteString(".cta-section-dark p { font-size: 1.1rem; opacity: 0.85; max-width: 700px; margin: 0 auto 2rem; }\n")
	sb.WriteString(".cta-section-dark .cta-button { background: var(--color-accent, #65d46e); color: #000; }\n")
	// Light CTA sections
	sb.WriteString(".cta-section { padding: 60px 20px; text-align: center; background: #f4f4f4; }\n")
	sb.WriteString(".cta-section h2 { font-size: 2rem; margin-bottom: 1rem; }\n")
	// Lead form
	sb.WriteString(".lead-form { display: flex; gap: 12px; justify-content: center; margin-top: 1.5rem; flex-wrap: wrap; }\n")
	sb.WriteString(".lead-form input { padding: 12px 20px; border: 1px solid #ccc; border-radius: 4px; font-size: 1rem; min-width: 260px; }\n")
	sb.WriteString(".lead-form button { padding: 12px 32px; background: var(--color-accent, #65d46e); color: #000; border: none; border-radius: 4px; font-weight: 700; cursor: pointer; font-size: 1rem; }\n")
	// Image sections
	sb.WriteString(".img-section { width: 100%; max-height: 600px; overflow: hidden; }\n")
	sb.WriteString(".img-section img { width: 100%; object-fit: cover; }\n")
	// Content sections
	sb.WriteString(".content-section { padding: 60px 20px; max-width: 800px; margin: 0 auto; }\n")
	sb.WriteString(".content-section h2 { font-size: 2rem; margin-bottom: 1rem; font-family: var(--font-heading, inherit); }\n")
	sb.WriteString(".footer { background: var(--color-secondary, #141414); color: #9ca3af; padding: 32px 20px; text-align: center; font-size: 0.9rem; }\n")
	sb.WriteString(".footer a { color: #9ca3af; margin: 0 8px; text-decoration: none; }\n")
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
	var tenantID bson.ObjectId
	if site != nil {
		tenantID = site.TenantID
	}
	if content, ok := doc["content"].([]any); ok {
		for _, item := range content {
			comp, ok := item.(map[string]any)
			if !ok {
				continue
			}
			renderComponent(&sb, comp, tenantID)
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
func renderComponent(sb *strings.Builder, comp map[string]any, tenantID bson.ObjectId) {
	compType, _ := comp["type"].(string)
	props := coerceMap(comp["props"])
	if props == nil {
		props = map[string]any{}
	}

	// escAttr escapes a string for safe use in HTML attributes.
	esc := func(s string) string { return html.EscapeString(s) }

	switch compType {
	case "NewsletterSubscriberBreak":
		// Marker the gate splitter looks for. Comments survive HTML parsing
		// so they remain inert when the post is dropped into the live page.
		sb.WriteString("<!--subscriber-break-->\n")

	case "NewsletterPaywallBreak":
		tier, _ := props["tier"].(string)
		if tier != "" {
			sb.WriteString(fmt.Sprintf("<!--paywall-break tier=\"%s\"-->\n", esc(tier)))
		} else {
			sb.WriteString("<!--paywall-break-->\n")
		}

	case "HeroSection":
		heading, _ := props["heading"].(string)
		subheading, _ := props["subheading"].(string)
		description, _ := props["description"].(string)
		ctaText, _ := props["ctaText"].(string)
		ctaURL, _ := props["ctaUrl"].(string)
		imageURL, _ := props["imageUrl"].(string)
		if imageURL == "" {
			imageURL, _ = props["backgroundImage"].(string)
		}
		sb.WriteString("<section class=\"hero\">\n")
		if imageURL != "" {
			sb.WriteString(fmt.Sprintf("<img src=\"%s\" alt=\"\" style=\"max-height:400px;object-fit:cover;border-radius:8px;margin-bottom:24px\">\n", esc(imageURL)))
		}
		if heading != "" {
			sb.WriteString(fmt.Sprintf("<h1>%s</h1>\n", esc(heading)))
		}
		if subheading != "" {
			sb.WriteString(fmt.Sprintf("<p>%s</p>\n", esc(subheading)))
		}
		if description != "" {
			sb.WriteString(fmt.Sprintf("<p>%s</p>\n", esc(description)))
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
		caption, _ := props["caption"].(string)
		// Support Sentanyl asset pipeline: if assetId is provided, resolve asset URL.
		if assetID, ok := props["assetId"].(string); ok && assetID != "" && bson.IsObjectIdHex(assetID) {
			if assetURL := resolveAssetURL(assetID); assetURL != "" {
				src = assetURL
			}
		}
		if src != "" {
			sb.WriteString("<section class=\"img-section\">\n")
			sb.WriteString(fmt.Sprintf("<img src=\"%s\" alt=\"%s\" style=\"width:100%%;max-height:500px;object-fit:cover\">\n", esc(src), esc(alt)))
			if caption != "" {
				sb.WriteString(fmt.Sprintf("<p style=\"text-align:center;font-size:0.85rem;color:#666;padding:8px 20px\">%s</p>\n", esc(caption)))
			}
			sb.WriteString("</section>\n")
		}

	case "VideoSection", "SentanylVideoPlayer":
		videoURL, _ := props["videoUrl"].(string)
		// Support Sentanyl asset pipeline: if videoId or assetId is provided, resolve asset URL.
		if videoID, ok := props["videoId"].(string); ok && videoID != "" && bson.IsObjectIdHex(videoID) {
			if assetURL := resolveAssetURL(videoID); assetURL != "" {
				videoURL = assetURL
			}
		} else if assetID, ok := props["assetId"].(string); ok && assetID != "" && bson.IsObjectIdHex(assetID) {
			if assetURL := resolveAssetURL(assetID); assetURL != "" {
				videoURL = assetURL
			}
		}
		sb.WriteString("<section class=\"section\" style=\"text-align:center\">\n")
		sb.WriteString(fmt.Sprintf("<video controls style=\"max-width:100%%\"><source src=\"%s\"></video>\n", esc(videoURL)))
		sb.WriteString("</section>\n")

	case "CTASection":
		heading, _ := props["heading"].(string)
		description, _ := props["description"].(string)
		buttonText, _ := props["buttonText"].(string)
		buttonURL, _ := props["buttonUrl"].(string)
		theme, _ := props["theme"].(string)
		isDark := theme == "dark"
		sectionClass := "cta-section"
		if isDark {
			sectionClass = "cta-section-dark"
		}
		sb.WriteString(fmt.Sprintf("<section class=\"%s\">\n<div class=\"section\">\n", sectionClass))
		if heading != "" {
			sb.WriteString(fmt.Sprintf("<h2>%s</h2>\n", esc(heading)))
		}
		if description != "" {
			sb.WriteString(fmt.Sprintf("<p>%s</p>\n", esc(description)))
		}
		if buttonText != "" {
			if buttonURL == "" {
				buttonURL = "#"
			}
			sb.WriteString(fmt.Sprintf("<a class=\"cta-button\" href=\"%s\">%s</a>\n", esc(buttonURL), esc(buttonText)))
		}
		sb.WriteString("</div></section>\n")

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
								renderComponent(sb, childComp, tenantID)
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
		buttonText, _ := props["buttonText"].(string)
		if buttonText == "" {
			buttonText = "Submit"
		}
		nextURL, _ := props["nextUrl"].(string)
		includePhone, _ := props["includePhone"].(bool)
		includeMessage, _ := props["includeMessage"].(bool)
		sb.WriteString("<section class=\"section\">\n")
		sb.WriteString(fmt.Sprintf("<h2>%s</h2>\n", esc(formTitle)))
		sb.WriteString("<form method=\"POST\" action=\"/api/marketing/site/form/submit\" style=\"max-width:500px;margin:0 auto\">\n")
		sb.WriteString("<input type=\"text\" name=\"name\" placeholder=\"Name\" required style=\"width:100%%;padding:12px;margin:8px 0;border:1px solid #d1d5db;border-radius:8px\">\n")
		sb.WriteString("<input type=\"email\" name=\"email\" placeholder=\"Email\" required style=\"width:100%%;padding:12px;margin:8px 0;border:1px solid #d1d5db;border-radius:8px\">\n")
		if includePhone {
			sb.WriteString("<input type=\"tel\" name=\"phone\" placeholder=\"Phone\" style=\"width:100%%;padding:12px;margin:8px 0;border:1px solid #d1d5db;border-radius:8px\">\n")
		}
		if includeMessage {
			sb.WriteString("<textarea name=\"message\" placeholder=\"Message\" rows=\"4\" style=\"width:100%%;padding:12px;margin:8px 0;border:1px solid #d1d5db;border-radius:8px\"></textarea>\n")
		}
		if nextURL != "" {
			sb.WriteString(fmt.Sprintf("<input type=\"hidden\" name=\"next_url\" value=\"%s\">\n", esc(nextURL)))
		}
		sb.WriteString(fmt.Sprintf("<button type=\"submit\" class=\"cta-button\" style=\"width:100%%;border:none;cursor:pointer\">%s</button>\n", esc(buttonText)))
		sb.WriteString("</form>\n</section>\n")

	case "SentanylCheckoutForm":
		heading, _ := props["heading"].(string)
		if heading == "" {
			heading = "Complete Your Purchase"
		}
		offerID, _ := props["offerId"].(string)
		successURL, _ := props["successUrl"].(string)
		cancelURL, _ := props["cancelUrl"].(string)
		sb.WriteString("<section class=\"section\" style=\"text-align:center\">\n")
		sb.WriteString(fmt.Sprintf("<div class=\"card\"><h3>%s</h3>\n", esc(heading)))
		sb.WriteString("<p>Secure checkout powered by Sentanyl</p>\n")
		if offerID != "" {
			sb.WriteString("<script>\n")
			sb.WriteString("function startCheckout() {\n")
			sb.WriteString("  var btn = document.getElementById('checkout-btn');\n")
			sb.WriteString("  btn.disabled = true; btn.textContent = 'Processing...';\n")
			sb.WriteString("  fetch('/api/marketing/site/checkout/start', {\n")
			sb.WriteString("    method: 'POST',\n")
			sb.WriteString("    headers: {'Content-Type': 'application/json'},\n")
			sb.WriteString(fmt.Sprintf("    body: JSON.stringify({offer_id:'%s'", esc(offerID)))
			if successURL != "" {
				sb.WriteString(fmt.Sprintf(",success_url:'%s'", esc(successURL)))
			}
			if cancelURL != "" {
				sb.WriteString(fmt.Sprintf(",cancel_url:'%s'", esc(cancelURL)))
			}
			sb.WriteString("})\n")
			sb.WriteString("  }).then(r=>r.json()).then(d=>{\n")
			sb.WriteString("    if(d.checkout_url){window.location.href=d.checkout_url;return;}\n")
			sb.WriteString("    if(d.error){btn.textContent=d.error;btn.disabled=false;return;}\n")
			sb.WriteString("    btn.textContent='Buy Now';btn.disabled=false;\n")
			sb.WriteString("  }).catch(function(){btn.textContent='Error — try again';btn.disabled=false;});\n")
			sb.WriteString("}\n")
			sb.WriteString("</script>\n")
			sb.WriteString("<button id=\"checkout-btn\" class=\"cta-button\" onclick=\"startCheckout()\" style=\"border:none;cursor:pointer\">Buy Now</button>\n")
		}
		sb.WriteString("</div>\n</section>\n")

	case "SentanylOfferCard":
		title, _ := props["title"].(string)
		ctaText, _ := props["ctaText"].(string)
		offerIDStr, _ := props["offerId"].(string)
		if ctaText == "" {
			ctaText = "Get This Offer"
		}
		// Try to load real offer data if offerId is valid.
		if offerIDStr != "" && bson.IsObjectIdHex(offerIDStr) {
			var offer pkgmodels.Offer
			err := db.GetCollection(pkgmodels.OfferCollection).FindId(bson.ObjectIdHex(offerIDStr)).One(&offer)
			if err == nil {
				if title == "" {
					title = offer.Title
				}
				sb.WriteString("<div class=\"card\" style=\"text-align:center\">\n")
				sb.WriteString(fmt.Sprintf("<h3>%s</h3>\n", esc(title)))
				if offer.Amount > 0 {
					sb.WriteString(fmt.Sprintf("<p style=\"font-size:1.5rem;font-weight:bold;margin:0.5rem 0\">$%.2f %s</p>\n", float64(offer.Amount)/100, esc(strings.ToUpper(offer.Currency))))
				}
				sb.WriteString(fmt.Sprintf("<a class=\"cta-button\" href=\"#\">%s</a>\n", esc(ctaText)))
				sb.WriteString("</div>\n")
				break
			}
		}
		if title == "" {
			title = "Offer"
		}
		sb.WriteString("<div class=\"card\" style=\"text-align:center\">\n")
		sb.WriteString(fmt.Sprintf("<h3>%s</h3>\n", esc(title)))
		sb.WriteString(fmt.Sprintf("<a class=\"cta-button\" href=\"#\">%s</a>\n", esc(ctaText)))
		sb.WriteString("</div>\n")

	case "SentanylOfferGrid":
		heading, _ := props["heading"].(string)
		if heading == "" {
			heading = "Our Offers"
		}
		sb.WriteString("<section class=\"section\">\n")
		sb.WriteString(fmt.Sprintf("<h2>%s</h2>\n", esc(heading)))
		sb.WriteString("<div class=\"columns\">\n")
		if cards := renderOfferGrid(props); cards != "" {
			sb.WriteString(cards)
		} else {
			sb.WriteString("<div class=\"card\"><p>No offers available</p></div>\n")
		}
		sb.WriteString("</div>\n</section>\n")

	case "SentanylProductGrid":
		heading, _ := props["heading"].(string)
		if heading == "" {
			heading = "Our Products"
		}
		sb.WriteString("<section class=\"section\">\n")
		sb.WriteString(fmt.Sprintf("<h2>%s</h2>\n", esc(heading)))
		sb.WriteString("<div class=\"columns\">\n")
		if cards := renderProductGrid(props, "", tenantID); cards != "" {
			sb.WriteString(cards)
		} else {
			sb.WriteString("<div class=\"card\"><p>No products available</p></div>\n")
		}
		sb.WriteString("</div>\n</section>\n")

	case "SentanylCourseGrid":
		heading, _ := props["heading"].(string)
		if heading == "" {
			heading = "Our Courses"
		}
		sb.WriteString("<section class=\"section\">\n")
		sb.WriteString(fmt.Sprintf("<h2>%s</h2>\n", esc(heading)))
		sb.WriteString("<div class=\"columns\">\n")
		if cards := renderProductGrid(props, "course", tenantID); cards != "" {
			sb.WriteString(cards)
		} else {
			sb.WriteString("<div class=\"card\"><p>No courses available</p></div>\n")
		}
		sb.WriteString("</div>\n</section>\n")

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

	case "SentanylLibraryLink":
		label, _ := props["label"].(string)
		href, _ := props["href"].(string)
		libraryID, _ := props["libraryId"].(string)
		if label == "" {
			label = "Access Library"
		}
		if href == "" && libraryID != "" {
			href = "/library/" + html.EscapeString(libraryID)
		}
		if href == "" {
			href = "#"
		}
		sb.WriteString(fmt.Sprintf("<div class=\"section\" style=\"text-align:center\"><a class=\"cta-button\" href=\"%s\">%s</a></div>\n", esc(href), esc(label)))

	case "SentanylFunnelLink":
		label, _ := props["label"].(string)
		href, _ := props["href"].(string)
		funnelID, _ := props["funnelId"].(string)
		if label == "" {
			label = "Enter Funnel"
		}
		if href == "" && funnelID != "" && bson.IsObjectIdHex(funnelID) {
			if funnelURL := resolveFunnelURL(funnelID); funnelURL != "" {
				href = funnelURL
			}
		}
		if href == "" {
			href = "#"
		}
		sb.WriteString(fmt.Sprintf("<div class=\"section\" style=\"text-align:center\"><a class=\"cta-button\" href=\"%s\">%s</a></div>\n", esc(href), esc(label)))

	case "SentanylFunnelCTA":
		heading, _ := props["heading"].(string)
		description, _ := props["description"].(string)
		buttonText, _ := props["buttonText"].(string)
		buttonURL, _ := props["buttonUrl"].(string)
		funnelID, _ := props["funnelId"].(string)
		if buttonText == "" {
			buttonText = "Get Started"
		}
		if buttonURL == "" && funnelID != "" && bson.IsObjectIdHex(funnelID) {
			if fURL := resolveFunnelURL(funnelID); fURL != "" {
				buttonURL = fURL
			}
		}
		if buttonURL == "" {
			buttonURL = "#"
		}
		sb.WriteString("<section class=\"section\" style=\"text-align:center;background:#f3f4f6;padding:60px 20px\">\n")
		if heading != "" {
			sb.WriteString(fmt.Sprintf("<h2>%s</h2>\n", esc(heading)))
		}
		if description != "" {
			sb.WriteString(fmt.Sprintf("<p style=\"margin:1rem 0;color:#4b5563\">%s</p>\n", esc(description)))
		}
		sb.WriteString(fmt.Sprintf("<a class=\"cta-button\" href=\"%s\">%s</a>\n", esc(buttonURL), esc(buttonText)))
		sb.WriteString("</section>\n")

	default:
		// Unknown component — render as a generic section.
		sb.WriteString(fmt.Sprintf("<section class=\"section\"><p>[%s]</p></section>\n", esc(compType)))
	}
}

// resolveAssetURL looks up a Sentanyl asset by ObjectId and returns its
// file_url. Returns empty string if not found or invalid.
func resolveAssetURL(assetID string) string {
	if !bson.IsObjectIdHex(assetID) {
		return ""
	}
	var asset pkgmodels.Asset
	err := db.GetCollection(pkgmodels.AssetCollection).FindId(bson.ObjectIdHex(assetID)).One(&asset)
	if err != nil {
		return ""
	}
	return asset.FileURL
}

// renderOfferGrid fetches offers by comma-separated IDs and renders HTML cards.
func renderOfferGrid(props map[string]any) string {
	idsStr, _ := props["offerIds"].(string)
	if idsStr == "" {
		return ""
	}
	ids := parseObjectIDs(idsStr)
	if len(ids) == 0 {
		return ""
	}
	var offers []pkgmodels.Offer
	err := db.GetCollection(pkgmodels.OfferCollection).Find(bson.M{
		"_id":                   bson.M{"$in": ids},
		"timestamps.deleted_at": nil,
	}).All(&offers)
	if err != nil || len(offers) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, o := range offers {
		sb.WriteString("<div class=\"card\" style=\"text-align:center\">\n")
		sb.WriteString(fmt.Sprintf("<h3>%s</h3>\n", html.EscapeString(o.Title)))
		if o.Amount > 0 {
			sb.WriteString(fmt.Sprintf("<p style=\"font-size:1.5rem;font-weight:bold;margin:0.5rem 0\">$%.2f %s</p>\n", float64(o.Amount)/100, html.EscapeString(strings.ToUpper(o.Currency))))
		}
		sb.WriteString("<a class=\"cta-button\" href=\"#\">Get This Offer</a>\n")
		sb.WriteString("</div>\n")
	}
	return sb.String()
}

// renderProductGrid fetches products by comma-separated IDs and renders HTML cards.
// If productType is non-empty, filters by product_type (e.g. "course").
// When ids are empty and productType == "course", auto-loads up to 12 active
// courses for tenantID so the block is useful without explicit picks.
func renderProductGrid(props map[string]any, productType string, tenantID bson.ObjectId) string {
	idsKey := "productIds"
	if productType == "course" {
		idsKey = "courseIds"
	}
	idsStr, _ := props[idsKey].(string)
	ids := parseObjectIDs(idsStr)

	query := bson.M{"timestamps.deleted_at": nil}
	if productType != "" {
		query["product_type"] = productType
	}
	if len(ids) > 0 {
		query["_id"] = bson.M{"$in": ids}
	} else if productType == "course" && tenantID != "" {
		query["tenant_id"] = tenantID
		query["status"] = "active"
	} else {
		return ""
	}

	q := db.GetCollection(pkgmodels.ProductCollection).Find(query)
	if len(ids) == 0 {
		q = q.Sort("-timestamps.created_at").Limit(12)
	}
	var products []pkgmodels.Product
	err := q.All(&products)
	if err != nil || len(products) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, p := range products {
		sb.WriteString("<div class=\"card\" style=\"text-align:center\">\n")
		if p.ThumbnailURL != "" {
			sb.WriteString(fmt.Sprintf("<img src=\"%s\" alt=\"%s\" style=\"max-width:100%%;border-radius:8px;margin-bottom:12px\">\n", html.EscapeString(p.ThumbnailURL), html.EscapeString(p.Name)))
		}
		sb.WriteString(fmt.Sprintf("<h3>%s</h3>\n", html.EscapeString(p.Name)))
		if p.Description != "" {
			sb.WriteString(fmt.Sprintf("<p style=\"color:#6b7280;margin:0.5rem 0\">%s</p>\n", html.EscapeString(p.Description)))
		}
		if p.Price > 0 {
			sb.WriteString(fmt.Sprintf("<p style=\"font-size:1.2rem;font-weight:bold\">$%.2f</p>\n", p.Price))
		}
		sb.WriteString("</div>\n")
	}
	return sb.String()
}

// resolveFunnelURL looks up a funnel by ObjectId and returns its domain or a fallback URL.
func resolveFunnelURL(funnelID string) string {
	if !bson.IsObjectIdHex(funnelID) {
		return ""
	}
	var f pkgmodels.Funnel
	err := db.GetCollection(pkgmodels.FunnelCollection).FindId(bson.ObjectIdHex(funnelID)).One(&f)
	if err != nil {
		return ""
	}
	if f.Domain != "" {
		return f.Domain
	}
	return "/api/funnel/" + f.PublicId
}

// parseObjectIDs splits a comma-separated string of hex IDs into ObjectId slice.
func parseObjectIDs(s string) []bson.ObjectId {
	parts := strings.Split(s, ",")
	var ids []bson.ObjectId
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if bson.IsObjectIdHex(p) {
			ids = append(ids, bson.ObjectIdHex(p))
		}
	}
	return ids
}

// renderStubPage renders a minimal branded placeholder for pages with no content.
func renderStubPage(page *SitePage, site *pkgmodels.Site) string {
	title := page.Name
	if title == "" {
		title = "Page"
	}
	siteName := ""
	if site != nil {
		siteName = site.Name
	}
	var sb strings.Builder
	sb.WriteString("<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n")
	sb.WriteString("<meta charset=\"UTF-8\">\n<meta name=\"viewport\" content=\"width=device-width, initial-scale=1.0\">\n")
	sb.WriteString(fmt.Sprintf("<title>%s</title>\n", html.EscapeString(title)))
	sb.WriteString("<style>\n")
	sb.WriteString(buildGlobalStyleVars(site))
	sb.WriteString("* { margin:0; padding:0; box-sizing:border-box; }\n")
	sb.WriteString("body { font-family: var(--font-body, sans-serif); color: #1a1a1a; }\n")
	sb.WriteString(".nav { background: var(--color-secondary, #141414); color: white; padding: 16px 32px; display: flex; justify-content: space-between; align-items: center; }\n")
	sb.WriteString(".nav-brand { font-weight:700; color:white; text-decoration:none; }\n")
	sb.WriteString(".stub { text-align:center; padding:120px 20px; }\n")
	sb.WriteString(".stub h1 { font-size:2.5rem; margin-bottom:1rem; font-family: var(--font-heading, inherit); }\n")
	sb.WriteString(".stub p { color:#666; font-size:1.1rem; }\n")
	sb.WriteString("</style>\n</head>\n<body>\n")
	sb.WriteString(fmt.Sprintf("<nav class=\"nav\"><span class=\"nav-brand\">%s</span></nav>\n", html.EscapeString(siteName)))
	sb.WriteString(fmt.Sprintf("<div class=\"stub\"><h1>%s</h1><p>Content coming soon.</p></div>\n", html.EscapeString(title)))
	sb.WriteString("</body>\n</html>")
	return sb.String()
}

// buildGlobalStyleVars generates a :root CSS block from the site's GlobalStyle.
// Falls back to sensible defaults when a field is empty.
func buildGlobalStyleVars(s *pkgmodels.Site) string {
	primary := "#4f46e5"
	secondary := "#7c3aed"
	accent := "#f59e0b"
	headingFont := "-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif"
	bodyFont := "-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif"
	borderRadius := "8px"

	if s != nil && s.GlobalStyle != nil {
		gs := s.GlobalStyle
		if gs.PrimaryColor != "" {
			primary = gs.PrimaryColor
		}
		if gs.SecondaryColor != "" {
			secondary = gs.SecondaryColor
		}
		if gs.AccentColor != "" {
			accent = gs.AccentColor
		}
		if gs.HeadingFont != "" {
			headingFont = gs.HeadingFont
		}
		if gs.BodyFont != "" {
			bodyFont = gs.BodyFont
		}
		if gs.BorderRadius != "" {
			borderRadius = gs.BorderRadius
		}
	}

	return fmt.Sprintf(`:root {
  --color-primary: %s;
  --color-secondary: %s;
  --color-accent: %s;
  --font-heading: %s;
  --font-body: %s;
  --border-radius: %s;
}
`, primary, secondary, accent, headingFont, bodyFont, borderRadius)
}
