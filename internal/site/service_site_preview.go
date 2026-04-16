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
		// Support Sentanyl asset pipeline: if assetId is provided, resolve asset URL.
		if assetID, ok := props["assetId"].(string); ok && assetID != "" && bson.IsObjectIdHex(assetID) {
			if assetURL := resolveAssetURL(assetID); assetURL != "" {
				src = assetURL
			}
		}
		sb.WriteString("<section class=\"section\" style=\"text-align:center\">\n")
		sb.WriteString(fmt.Sprintf("<img src=\"%s\" alt=\"%s\">\n", esc(src), esc(alt)))
		sb.WriteString("</section>\n")

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
		if cards := renderProductGrid(props, ""); cards != "" {
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
		if cards := renderProductGrid(props, "course"); cards != "" {
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
func renderProductGrid(props map[string]any, productType string) string {
	idsKey := "productIds"
	if productType == "course" {
		idsKey = "courseIds"
	}
	idsStr, _ := props[idsKey].(string)
	if idsStr == "" {
		return ""
	}
	ids := parseObjectIDs(idsStr)
	if len(ids) == 0 {
		return ""
	}
	query := bson.M{
		"_id":                   bson.M{"$in": ids},
		"timestamps.deleted_at": nil,
	}
	if productType != "" {
		query["product_type"] = productType
	}
	var products []pkgmodels.Product
	err := db.GetCollection(pkgmodels.ProductCollection).Find(query).All(&products)
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
