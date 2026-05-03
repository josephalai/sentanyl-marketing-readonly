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
	sb.WriteString(builtinSiteCSS)
	sb.WriteString("</style>\n")
	sb.WriteString("</head>\n<body>\n")

	// Render navigation if site has one.
	if site != nil && site.Navigation != nil && len(site.Navigation.HeaderNavLinks) > 0 {
		sb.WriteString("<nav class=\"site-nav\"><a class=\"nav-brand\" href=\"/\">")
		sb.WriteString(html.EscapeString(site.Name))
		sb.WriteString("</a><ul>")
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
		sb.WriteString("<footer class=\"site-footer\"><p>")
		for i, link := range site.Navigation.FooterNavLinks {
			if i > 0 {
				sb.WriteString(" · ")
			}
			sb.WriteString(fmt.Sprintf("<a href=\"%s\">%s</a>", html.EscapeString(link.URL), html.EscapeString(link.Label)))
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
		renderHeroSection(sb, props, esc)

	case "RichTextSection":
		content, _ := props["content"].(string)
		tone := normalizeTone(props["tone"])
		pad := normalizePadding(props["paddingY"])
		sb.WriteString(fmt.Sprintf("<section class=\"section %s %s\">\n<div class=\"container container--normal\"><div class=\"prose\">\n", toneClass(tone), padClass(pad)))
		sb.WriteString(content) // Rich text is intentionally unescaped HTML
		sb.WriteString("\n</div></div></section>\n")

	case "ImageSection":
		src, _ := props["src"].(string)
		alt, _ := props["alt"].(string)
		caption, _ := props["caption"].(string)
		if assetID, ok := props["assetId"].(string); ok && assetID != "" && bson.IsObjectIdHex(assetID) {
			if assetURL := resolveAssetURL(assetID); assetURL != "" {
				src = assetURL
			}
		}
		if src != "" {
			tone := normalizeTone(props["tone"])
			pad := normalizePadding(props["paddingY"])
			sb.WriteString(fmt.Sprintf("<section class=\"section %s %s\">\n<div class=\"container container--wide\">\n<div class=\"img-wide\"><img src=\"%s\" alt=\"%s\"></div>\n", toneClass(tone), padClass(pad), esc(src), esc(alt)))
			if caption != "" {
				sb.WriteString(fmt.Sprintf("<p class=\"img-caption\">%s</p>\n", esc(caption)))
			}
			sb.WriteString("</div></section>\n")
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
		renderCTASection(sb, props, esc)

	case "TestimonialsSection", "SentanylTestimonials":
		renderTestimonialsSection(sb, props, esc)

	case "FAQSection":
		renderFAQSection(sb, props, esc)

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

	case "Section":
		renderSectionContainer(sb, props, tenantID)

	case "Stack":
		renderStackContainer(sb, props, tenantID)

	case "Grid":
		renderGridContainer(sb, props, tenantID)

	case "Container":
		// Bare container = a Section with default tone, no extra padding band.
		renderSectionContainer(sb, props, tenantID)

	case "MediaSection":
		renderMediaSection(sb, props, esc)

	case "FeatureGrid":
		renderFeatureGrid(sb, props, esc)

	case "Pricing":
		renderPricing(sb, props, esc)

	case "Stats":
		renderStats(sb, props, esc)

	case "LogoCloud":
		renderLogoCloud(sb, props, esc)

	default:
		// Unknown component — render as a generic section.
		sb.WriteString(fmt.Sprintf("<section class=\"section\"><div class=\"container container--normal\"><p>[%s]</p></div></section>\n", esc(compType)))
	}
}

// --- New-style component renderers ------------------------------------------

// normalizeTone coerces a tone prop value into a known tone token.
func normalizeTone(v any) string {
	s, _ := v.(string)
	switch s {
	case "default", "muted", "inverse", "branded", "accent":
		return s
	case "dark":
		return "inverse"
	case "":
		return "default"
	}
	return "default"
}

// normalizePadding coerces a paddingY prop into a known step.
func normalizePadding(v any) string {
	s, _ := v.(string)
	switch s {
	case "sm", "md", "lg", "xl":
		return s
	}
	return "md"
}

// normalizeMaxWidth coerces a maxWidth prop into a known container size.
func normalizeMaxWidth(v any) string {
	s, _ := v.(string)
	switch s {
	case "narrow", "normal", "wide", "full":
		return s
	}
	return "normal"
}

// normalizeGap coerces a gap prop into a known step.
func normalizeGap(v any) string {
	s, _ := v.(string)
	switch s {
	case "sm", "md", "lg":
		return s
	}
	return "md"
}

// toneClass maps a tone token to the CSS class that paints background/foreground.
func toneClass(tone string) string {
	switch tone {
	case "muted":
		return "section--tone-muted"
	case "inverse":
		return "section--tone-inverse"
	case "branded":
		return "section--tone-branded"
	case "accent":
		return "section--tone-accent"
	}
	return "section--tone-default"
}

// padClass maps a padding step to the section--<step> class.
func padClass(step string) string {
	switch step {
	case "sm":
		return "section--sm"
	case "lg":
		return "section--lg"
	case "xl":
		return "section--xl"
	}
	return ""
}

// gapClass maps a gap step to a stack/grid gap class.
func gapClass(prefix, step string) string {
	switch step {
	case "sm":
		return prefix + "--gap-sm"
	case "lg":
		return prefix + "--gap-lg"
	case "xl":
		return prefix + "--gap-xl"
	}
	return prefix + "--gap-md"
}

// renderChildren renders a content array (containers' nested blocks) by
// calling renderComponent on each entry. Tolerates the same map/bson shapes
// as the top-level renderer.
func renderChildren(sb *strings.Builder, content any, tenantID bson.ObjectId) {
	for _, item := range coerceContentSlice(content) {
		comp := coerceMap(item)
		if comp == nil {
			continue
		}
		renderComponent(sb, comp, tenantID)
	}
}

// renderSectionContainer renders a <section> wrapper with tone/padding/maxWidth
// and recurses into props.content for nested children.
func renderSectionContainer(sb *strings.Builder, props map[string]any, tenantID bson.ObjectId) {
	tone := normalizeTone(props["tone"])
	if tone == "default" {
		// Allow legacy "variant" alias used in earlier prompts.
		tone = normalizeTone(props["variant"])
	}
	pad := normalizePadding(props["paddingY"])
	mw := normalizeMaxWidth(props["maxWidth"])
	bg, _ := props["backgroundImage"].(string)
	id, _ := props["id"].(string)

	bgAttr := ""
	if bg != "" {
		bgAttr = fmt.Sprintf(` data-bg-image="1" style="background-image:url('%s')"`, html.EscapeString(bg))
	}
	idAttr := ""
	if id != "" {
		idAttr = fmt.Sprintf(` id="%s"`, html.EscapeString(id))
	}
	sb.WriteString(fmt.Sprintf("<section class=\"section %s %s\"%s%s>\n<div class=\"container container--%s\">\n",
		toneClass(tone), padClass(pad), idAttr, bgAttr, mw))
	renderChildren(sb, props["content"], tenantID)
	sb.WriteString("</div></section>\n")
}

// renderStackContainer renders a vertical flex stack of children.
func renderStackContainer(sb *strings.Builder, props map[string]any, tenantID bson.ObjectId) {
	gap := normalizeGap(props["gap"])
	align, _ := props["align"].(string)
	cls := "stack " + gapClass("stack", gap)
	if align == "center" {
		cls += " stack--center"
	} else if align == "end" {
		cls += " stack--end"
	}
	sb.WriteString(fmt.Sprintf("<div class=\"%s\">\n", cls))
	renderChildren(sb, props["content"], tenantID)
	sb.WriteString("</div>\n")
}

// renderGridContainer renders a CSS grid of children. Supports col counts 2/3/4/12.
func renderGridContainer(sb *strings.Builder, props map[string]any, tenantID bson.ObjectId) {
	cols := 2
	switch v := props["cols"].(type) {
	case int:
		cols = v
	case float64:
		cols = int(v)
	case string:
		fmt.Sscanf(v, "%d", &cols)
	}
	if cols < 2 {
		cols = 2
	}
	if cols > 12 {
		cols = 12
	}
	colsClass := "grid-cols-2"
	switch cols {
	case 2:
		colsClass = "grid-cols-2"
	case 3:
		colsClass = "grid-cols-3"
	case 4:
		colsClass = "grid-cols-4"
	case 12:
		colsClass = "grid-cols-12"
	default:
		colsClass = "grid-cols-3"
	}
	gap := normalizeGap(props["gap"])
	sb.WriteString(fmt.Sprintf("<div class=\"grid %s %s\">\n", colsClass, gapClass("grid", gap)))
	renderChildren(sb, props["content"], tenantID)
	sb.WriteString("</div>\n")
}

// renderHeroSection draws a Hero with one of: centered, split, gradient, image.
func renderHeroSection(sb *strings.Builder, props map[string]any, esc func(string) string) {
	heading, _ := props["heading"].(string)
	subheading, _ := props["subheading"].(string)
	description, _ := props["description"].(string)
	ctaText, _ := props["ctaText"].(string)
	ctaURL, _ := props["ctaUrl"].(string)
	secondaryCTAText, _ := props["secondaryCtaText"].(string)
	secondaryCTAURL, _ := props["secondaryCtaUrl"].(string)
	imageURL, _ := props["imageUrl"].(string)
	if imageURL == "" {
		imageURL, _ = props["imageSrc"].(string)
	}
	bgImage, _ := props["backgroundImage"].(string)
	eyebrow, _ := props["eyebrow"].(string)

	variant, _ := props["variant"].(string)
	if variant == "" {
		// Auto-pick: split if image exists, else centered.
		if imageURL != "" && bgImage == "" {
			variant = "split"
		} else if bgImage != "" {
			variant = "image"
		} else {
			variant = "centered"
		}
	}
	tone := normalizeTone(props["tone"])
	if variant == "image" || variant == "gradient" {
		tone = "inverse"
	}
	pad := normalizePadding(props["paddingY"])
	if pad == "md" {
		pad = "xl"
	}

	bgAttr := ""
	if variant == "image" && bgImage != "" {
		bgAttr = fmt.Sprintf(` data-bg-image="1" style="background-image:url('%s')"`, html.EscapeString(bgImage))
	}
	sb.WriteString(fmt.Sprintf("<section class=\"section %s %s\"%s>\n<div class=\"container container--wide\">\n", toneClass(tone), padClass(pad), bgAttr))

	classes := "hero"
	switch variant {
	case "split":
		classes += " hero--split"
	case "centered":
		classes += " hero--centered"
	case "gradient":
		classes += " hero--gradient hero--centered"
	default:
		classes += " hero--centered"
	}
	sb.WriteString(fmt.Sprintf("<div class=\"%s\">\n", classes))

	// Text column
	sb.WriteString("<div class=\"stack stack--md\">\n")
	if eyebrow != "" {
		sb.WriteString(fmt.Sprintf("<span class=\"eyebrow\">%s</span>\n", esc(eyebrow)))
	}
	if heading != "" {
		sb.WriteString(fmt.Sprintf("<h1>%s</h1>\n", esc(heading)))
	}
	leadText := subheading
	if leadText == "" {
		leadText = description
	}
	if leadText != "" {
		sb.WriteString(fmt.Sprintf("<p class=\"lead\">%s</p>\n", esc(leadText)))
	}
	if description != "" && subheading != "" {
		sb.WriteString(fmt.Sprintf("<p>%s</p>\n", esc(description)))
	}
	if ctaText != "" || secondaryCTAText != "" {
		sb.WriteString("<div class=\"btn-row\">")
		if ctaText != "" {
			if ctaURL == "" {
				ctaURL = "#"
			}
			btnClass := "btn btn--accent btn--lg"
			if tone == "inverse" || tone == "branded" {
				btnClass = "btn btn--inverse btn--lg"
			}
			sb.WriteString(fmt.Sprintf("<a class=\"%s\" href=\"%s\">%s</a>", btnClass, esc(ctaURL), esc(ctaText)))
		}
		if secondaryCTAText != "" {
			if secondaryCTAURL == "" {
				secondaryCTAURL = "#"
			}
			sb.WriteString(fmt.Sprintf("<a class=\"btn btn--outline btn--lg\" href=\"%s\">%s</a>", esc(secondaryCTAURL), esc(secondaryCTAText)))
		}
		sb.WriteString("</div>\n")
	}
	sb.WriteString("</div>\n")

	// Image column for split variant
	if variant == "split" && imageURL != "" {
		sb.WriteString(fmt.Sprintf("<img src=\"%s\" alt=\"%s\">\n", esc(imageURL), esc(heading)))
	}

	sb.WriteString("</div>\n</div></section>\n")
}

// renderCTASection draws a CTA with one of: centered, split, banner.
func renderCTASection(sb *strings.Builder, props map[string]any, esc func(string) string) {
	heading, _ := props["heading"].(string)
	description, _ := props["description"].(string)
	buttonText, _ := props["buttonText"].(string)
	buttonURL, _ := props["buttonUrl"].(string)
	secondaryText, _ := props["secondaryButtonText"].(string)
	secondaryURL, _ := props["secondaryButtonUrl"].(string)

	variant, _ := props["variant"].(string)
	if variant == "" {
		variant = "centered"
	}
	tone := normalizeTone(props["tone"])
	if tone == "default" {
		// Legacy: theme=dark mapped to inverse tone.
		if t, _ := props["theme"].(string); t == "dark" {
			tone = "inverse"
		}
	}
	pad := normalizePadding(props["paddingY"])
	if pad == "md" {
		pad = "lg"
	}

	if variant == "banner" {
		sb.WriteString(fmt.Sprintf("<section class=\"section %s\">\n<div class=\"container container--wide\">\n<div class=\"cta cta--banner cta--centered\">\n", padClass(pad)))
	} else {
		sb.WriteString(fmt.Sprintf("<section class=\"section %s %s\">\n<div class=\"container container--normal\">\n", toneClass(tone), padClass(pad)))
		if variant == "split" {
			sb.WriteString("<div class=\"cta cta--split\">\n")
		} else {
			sb.WriteString("<div class=\"cta cta--centered\">\n")
		}
	}

	sb.WriteString("<div class=\"stack stack--sm\">\n")
	if heading != "" {
		sb.WriteString(fmt.Sprintf("<h2>%s</h2>\n", esc(heading)))
	}
	if description != "" {
		sb.WriteString(fmt.Sprintf("<p>%s</p>\n", esc(description)))
	}
	sb.WriteString("</div>\n")

	if buttonText != "" || secondaryText != "" {
		sb.WriteString("<div class=\"btn-row\" style=\"display:flex;gap:12px;flex-wrap:wrap;justify-content:center\">")
		if buttonText != "" {
			if buttonURL == "" {
				buttonURL = "#"
			}
			btnClass := "btn btn--accent btn--lg"
			if variant == "banner" || tone == "inverse" || tone == "branded" {
				btnClass = "btn btn--inverse btn--lg"
			}
			sb.WriteString(fmt.Sprintf("<a class=\"%s\" href=\"%s\">%s</a>", btnClass, esc(buttonURL), esc(buttonText)))
		}
		if secondaryText != "" {
			if secondaryURL == "" {
				secondaryURL = "#"
			}
			sb.WriteString(fmt.Sprintf("<a class=\"btn btn--outline btn--lg\" href=\"%s\">%s</a>", esc(secondaryURL), esc(secondaryText)))
		}
		sb.WriteString("</div>\n")
	}

	sb.WriteString("</div>\n</div></section>\n")
}

// renderTestimonialsSection draws testimonials in cards/quote/marquee variants.
func renderTestimonialsSection(sb *strings.Builder, props map[string]any, esc func(string) string) {
	heading, _ := props["heading"].(string)
	eyebrow, _ := props["eyebrow"].(string)
	tone := normalizeTone(props["tone"])
	pad := normalizePadding(props["paddingY"])
	if pad == "md" {
		pad = "lg"
	}
	variant, _ := props["variant"].(string)
	if variant == "" {
		variant = "cards"
	}

	sb.WriteString(fmt.Sprintf("<section class=\"section %s %s\">\n<div class=\"container container--wide\">\n", toneClass(tone), padClass(pad)))
	if heading != "" {
		sb.WriteString("<div class=\"stack stack--sm stack--center\" style=\"margin-bottom:48px\">\n")
		if eyebrow != "" {
			sb.WriteString(fmt.Sprintf("<span class=\"eyebrow\">%s</span>\n", esc(eyebrow)))
		}
		sb.WriteString(fmt.Sprintf("<h2>%s</h2>\n", esc(heading)))
		sb.WriteString("</div>\n")
	}

	items, _ := props["items"].([]any)

	switch variant {
	case "quote":
		// Render the first item as a centered quote.
		if len(items) > 0 {
			if t, ok := items[0].(map[string]any); ok {
				quote, _ := t["quote"].(string)
				author, _ := t["author"].(string)
				role, _ := t["role"].(string)
				sb.WriteString("<div class=\"testimonial testimonial--quote\">\n")
				sb.WriteString(fmt.Sprintf("<blockquote>“%s”</blockquote>\n", esc(quote)))
				meta := author
				if role != "" {
					meta = fmt.Sprintf("%s · %s", author, role)
				}
				sb.WriteString(fmt.Sprintf("<p class=\"author\">— %s</p>\n", esc(meta)))
				sb.WriteString("</div>\n")
			}
		}
	default:
		gridCols := "grid-cols-3"
		if len(items) == 2 {
			gridCols = "grid-cols-2"
		} else if len(items) == 4 {
			gridCols = "grid-cols-4"
		}
		sb.WriteString(fmt.Sprintf("<div class=\"grid %s\">\n", gridCols))
		for _, item := range items {
			if t, ok := item.(map[string]any); ok {
				quote, _ := t["quote"].(string)
				author, _ := t["author"].(string)
				role, _ := t["role"].(string)
				sb.WriteString("<div class=\"testimonial testimonial-card\">\n")
				sb.WriteString(fmt.Sprintf("<blockquote>“%s”</blockquote>\n", esc(quote)))
				meta := author
				if role != "" {
					meta = fmt.Sprintf("%s · %s", author, role)
				}
				sb.WriteString(fmt.Sprintf("<p class=\"author\">%s</p>\n", esc(meta)))
				sb.WriteString("</div>\n")
			}
		}
		sb.WriteString("</div>\n")
	}

	sb.WriteString("</div></section>\n")
}

// renderFAQSection draws a FAQ list with optional 2-column layout.
func renderFAQSection(sb *strings.Builder, props map[string]any, esc func(string) string) {
	heading, _ := props["heading"].(string)
	tone := normalizeTone(props["tone"])
	pad := normalizePadding(props["paddingY"])
	if pad == "md" {
		pad = "lg"
	}
	variant, _ := props["variant"].(string)

	sb.WriteString(fmt.Sprintf("<section class=\"section %s %s\">\n<div class=\"container container--normal\">\n", toneClass(tone), padClass(pad)))
	if heading != "" {
		sb.WriteString(fmt.Sprintf("<div class=\"stack stack--sm stack--center\" style=\"margin-bottom:32px\"><h2>%s</h2></div>\n", esc(heading)))
	}

	listClass := "faq"
	if variant == "cols" {
		listClass = "faq faq--cols"
	}
	sb.WriteString(fmt.Sprintf("<div class=\"%s\">\n", listClass))
	if items, ok := props["items"].([]any); ok {
		for _, item := range items {
			if faq, ok := item.(map[string]any); ok {
				q, _ := faq["question"].(string)
				a, _ := faq["answer"].(string)
				sb.WriteString("<div class=\"faq__item\">\n")
				sb.WriteString(fmt.Sprintf("<h3>%s</h3>\n", esc(q)))
				sb.WriteString(fmt.Sprintf("<p>%s</p>\n", esc(a)))
				sb.WriteString("</div>\n")
			}
		}
	}
	sb.WriteString("</div>\n</div></section>\n")
}

// renderMediaSection draws a side-by-side image+text block (left/right).
func renderMediaSection(sb *strings.Builder, props map[string]any, esc func(string) string) {
	heading, _ := props["heading"].(string)
	body, _ := props["body"].(string)
	eyebrow, _ := props["eyebrow"].(string)
	imageSrc, _ := props["imageSrc"].(string)
	if imageSrc == "" {
		imageSrc, _ = props["imageUrl"].(string)
	}
	imageAlt, _ := props["imageAlt"].(string)
	ctaText, _ := props["ctaText"].(string)
	ctaURL, _ := props["ctaUrl"].(string)
	tone := normalizeTone(props["tone"])
	pad := normalizePadding(props["paddingY"])
	if pad == "md" {
		pad = "lg"
	}
	layout, _ := props["layout"].(string)
	if layout == "" {
		layout = "right"
	}
	mediaClass := "media media--right"
	if layout == "left" {
		mediaClass = "media media--left"
	}

	sb.WriteString(fmt.Sprintf("<section class=\"section %s %s\">\n<div class=\"container container--wide\">\n<div class=\"%s\">\n", toneClass(tone), padClass(pad), mediaClass))

	textFirst := layout == "right"
	writeText := func() {
		sb.WriteString("<div class=\"stack stack--md\">\n")
		if eyebrow != "" {
			sb.WriteString(fmt.Sprintf("<span class=\"eyebrow\">%s</span>\n", esc(eyebrow)))
		}
		if heading != "" {
			sb.WriteString(fmt.Sprintf("<h2>%s</h2>\n", esc(heading)))
		}
		if body != "" {
			sb.WriteString(fmt.Sprintf("<p>%s</p>\n", esc(body)))
		}
		if ctaText != "" {
			if ctaURL == "" {
				ctaURL = "#"
			}
			btnClass := "btn btn--accent"
			if tone == "inverse" || tone == "branded" {
				btnClass = "btn btn--inverse"
			}
			sb.WriteString(fmt.Sprintf("<a class=\"%s\" href=\"%s\" style=\"align-self:flex-start\">%s</a>\n", btnClass, esc(ctaURL), esc(ctaText)))
		}
		sb.WriteString("</div>\n")
	}
	writeImage := func() {
		if imageSrc != "" {
			sb.WriteString(fmt.Sprintf("<img src=\"%s\" alt=\"%s\">\n", esc(imageSrc), esc(imageAlt)))
		}
	}
	if textFirst {
		writeText()
		writeImage()
	} else {
		writeImage()
		writeText()
	}

	sb.WriteString("</div>\n</div></section>\n")
}

// renderFeatureGrid draws a grid of feature cards.
func renderFeatureGrid(sb *strings.Builder, props map[string]any, esc func(string) string) {
	heading, _ := props["heading"].(string)
	subheading, _ := props["subheading"].(string)
	eyebrow, _ := props["eyebrow"].(string)
	tone := normalizeTone(props["tone"])
	pad := normalizePadding(props["paddingY"])
	if pad == "md" {
		pad = "lg"
	}
	cols := 3
	switch v := props["columns"].(type) {
	case float64:
		cols = int(v)
	case int:
		cols = v
	}
	if cols < 2 {
		cols = 2
	}
	if cols > 4 {
		cols = 4
	}
	gridCols := fmt.Sprintf("grid-cols-%d", cols)
	cardStyle, _ := props["cardStyle"].(string)
	cardClass := "card card--lift"
	if cardStyle == "quiet" {
		cardClass = "card card--quiet"
	} else if cardStyle == "ghost" {
		cardClass = "card card--ghost"
	}

	sb.WriteString(fmt.Sprintf("<section class=\"section %s %s feature-grid\">\n<div class=\"container container--wide\">\n", toneClass(tone), padClass(pad)))
	if heading != "" || subheading != "" {
		sb.WriteString("<div class=\"stack stack--sm stack--center\" style=\"margin-bottom:48px;max-width:680px;margin-inline:auto\">\n")
		if eyebrow != "" {
			sb.WriteString(fmt.Sprintf("<span class=\"eyebrow\">%s</span>\n", esc(eyebrow)))
		}
		if heading != "" {
			sb.WriteString(fmt.Sprintf("<h2>%s</h2>\n", esc(heading)))
		}
		if subheading != "" {
			sb.WriteString(fmt.Sprintf("<p class=\"lead\">%s</p>\n", esc(subheading)))
		}
		sb.WriteString("</div>\n")
	}
	sb.WriteString(fmt.Sprintf("<div class=\"grid %s\">\n", gridCols))
	if items, ok := props["items"].([]any); ok {
		for _, item := range items {
			if f, ok := item.(map[string]any); ok {
				icon, _ := f["icon"].(string)
				title, _ := f["title"].(string)
				body, _ := f["body"].(string)
				sb.WriteString(fmt.Sprintf("<div class=\"%s\">\n", cardClass))
				if icon != "" {
					sb.WriteString(fmt.Sprintf("<div class=\"icon\">%s</div>\n", esc(icon)))
				}
				if title != "" {
					sb.WriteString(fmt.Sprintf("<h3>%s</h3>\n", esc(title)))
				}
				if body != "" {
					sb.WriteString(fmt.Sprintf("<p>%s</p>\n", esc(body)))
				}
				sb.WriteString("</div>\n")
			}
		}
	}
	sb.WriteString("</div>\n</div></section>\n")
}

// renderPricing draws a pricing-tier comparison.
func renderPricing(sb *strings.Builder, props map[string]any, esc func(string) string) {
	heading, _ := props["heading"].(string)
	subheading, _ := props["subheading"].(string)
	tone := normalizeTone(props["tone"])
	pad := normalizePadding(props["paddingY"])
	if pad == "md" {
		pad = "lg"
	}
	tiers, _ := props["tiers"].([]any)
	cols := len(tiers)
	if cols < 2 {
		cols = 2
	}
	if cols > 4 {
		cols = 4
	}

	sb.WriteString(fmt.Sprintf("<section class=\"section %s %s\">\n<div class=\"container container--wide\">\n", toneClass(tone), padClass(pad)))
	if heading != "" || subheading != "" {
		sb.WriteString("<div class=\"stack stack--sm stack--center\" style=\"margin-bottom:48px;max-width:680px;margin-inline:auto\">\n")
		if heading != "" {
			sb.WriteString(fmt.Sprintf("<h2>%s</h2>\n", esc(heading)))
		}
		if subheading != "" {
			sb.WriteString(fmt.Sprintf("<p class=\"lead\">%s</p>\n", esc(subheading)))
		}
		sb.WriteString("</div>\n")
	}
	sb.WriteString(fmt.Sprintf("<div class=\"pricing grid grid-cols-%d\">\n", cols))
	for _, tier := range tiers {
		t, ok := tier.(map[string]any)
		if !ok {
			continue
		}
		name, _ := t["name"].(string)
		price, _ := t["price"].(string)
		cadence, _ := t["cadence"].(string)
		desc, _ := t["description"].(string)
		ctaText, _ := t["ctaText"].(string)
		ctaURL, _ := t["ctaUrl"].(string)
		featured, _ := t["featured"].(bool)
		features, _ := t["features"].([]any)

		cls := "pricing__tier"
		if featured {
			cls += " pricing__tier--featured"
		}
		sb.WriteString(fmt.Sprintf("<div class=\"%s\">\n", cls))
		if name != "" {
			sb.WriteString(fmt.Sprintf("<h3>%s</h3>\n", esc(name)))
		}
		if desc != "" {
			sb.WriteString(fmt.Sprintf("<p style=\"color:var(--color-muted-fg)\">%s</p>\n", esc(desc)))
		}
		if price != "" {
			sb.WriteString(fmt.Sprintf("<div class=\"pricing__price\">%s", esc(price)))
			if cadence != "" {
				sb.WriteString(fmt.Sprintf(" <small>%s</small>", esc(cadence)))
			}
			sb.WriteString("</div>\n")
		}
		if len(features) > 0 {
			sb.WriteString("<ul class=\"pricing__features\">\n")
			for _, f := range features {
				if s, ok := f.(string); ok {
					sb.WriteString(fmt.Sprintf("<li>%s</li>\n", esc(s)))
				}
			}
			sb.WriteString("</ul>\n")
		}
		if ctaText != "" {
			if ctaURL == "" {
				ctaURL = "#"
			}
			btnClass := "btn btn--primary"
			if featured {
				btnClass = "btn btn--accent"
			}
			sb.WriteString(fmt.Sprintf("<a class=\"%s\" href=\"%s\" style=\"justify-content:center\">%s</a>\n", btnClass, esc(ctaURL), esc(ctaText)))
		}
		sb.WriteString("</div>\n")
	}
	sb.WriteString("</div>\n</div></section>\n")
}

// renderStats draws a metric callout grid.
func renderStats(sb *strings.Builder, props map[string]any, esc func(string) string) {
	heading, _ := props["heading"].(string)
	tone := normalizeTone(props["tone"])
	if tone == "default" {
		tone = "muted"
	}
	pad := normalizePadding(props["paddingY"])

	sb.WriteString(fmt.Sprintf("<section class=\"section %s %s\">\n<div class=\"container container--wide\">\n", toneClass(tone), padClass(pad)))
	if heading != "" {
		sb.WriteString(fmt.Sprintf("<div class=\"stack stack--sm stack--center\" style=\"margin-bottom:32px\"><h2>%s</h2></div>\n", esc(heading)))
	}
	items, _ := props["items"].([]any)
	cols := len(items)
	if cols < 2 {
		cols = 2
	}
	if cols > 4 {
		cols = 4
	}
	sb.WriteString(fmt.Sprintf("<div class=\"stats grid grid-cols-%d\">\n", cols))
	for _, item := range items {
		if s, ok := item.(map[string]any); ok {
			num, _ := s["value"].(string)
			if num == "" {
				num, _ = s["number"].(string)
			}
			label, _ := s["label"].(string)
			sb.WriteString("<div class=\"stats__item\">\n")
			sb.WriteString(fmt.Sprintf("<div class=\"num\">%s</div>\n", esc(num)))
			sb.WriteString(fmt.Sprintf("<div class=\"label\">%s</div>\n", esc(label)))
			sb.WriteString("</div>\n")
		}
	}
	sb.WriteString("</div>\n</div></section>\n")
}

// renderLogoCloud draws a row of partner/customer logos.
func renderLogoCloud(sb *strings.Builder, props map[string]any, esc func(string) string) {
	heading, _ := props["heading"].(string)
	tone := normalizeTone(props["tone"])
	if tone == "default" {
		tone = "muted"
	}
	pad := normalizePadding(props["paddingY"])
	if pad == "md" {
		pad = "sm"
	}

	sb.WriteString(fmt.Sprintf("<section class=\"section %s %s\">\n<div class=\"container container--wide\">\n", toneClass(tone), padClass(pad)))
	if heading != "" {
		sb.WriteString(fmt.Sprintf("<p class=\"text-center\" style=\"color:var(--color-muted-fg);margin-bottom:24px\">%s</p>\n", esc(heading)))
	}
	sb.WriteString("<div class=\"logo-cloud\">\n")
	if logos, ok := props["logos"].([]any); ok {
		for _, l := range logos {
			if m, ok := l.(map[string]any); ok {
				src, _ := m["src"].(string)
				alt, _ := m["alt"].(string)
				name, _ := m["name"].(string)
				if src != "" {
					sb.WriteString(fmt.Sprintf("<img src=\"%s\" alt=\"%s\">\n", esc(src), esc(alt)))
				} else if name != "" {
					sb.WriteString(fmt.Sprintf("<span>%s</span>\n", esc(name)))
				}
			} else if s, ok := l.(string); ok {
				sb.WriteString(fmt.Sprintf("<span>%s</span>\n", esc(s)))
			}
		}
	}
	sb.WriteString("</div>\n</div></section>\n")
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

// RenderStubPage renders a minimal branded placeholder for pages with no content.
func RenderStubPage(page *SitePage, s *pkgmodels.Site) string {
	return renderStubPage(page, s)
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
// Falls back to sensible defaults when a field is empty. Emits a full design
// token set (colors, spacing scale, type scale, radius) consumed by
// builtinSiteCSS below.
func buildGlobalStyleVars(s *pkgmodels.Site) string {
	primary := "#0f172a"
	secondary := "#0a0a0a"
	accent := "#22c55e"
	headingFont := "'Inter', -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif"
	bodyFont := "'Inter', -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif"
	borderRadius := "10px"

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
  --color-bg: #ffffff;
  --color-fg: #0a0a0a;
  --color-muted-bg: #f7f7f8;
  --color-muted-fg: #525866;
  --color-border: #e5e7eb;
  --color-border-strong: #cbd5e1;
  --color-inverse-bg: var(--color-secondary);
  --color-inverse-fg: #f8fafc;
  --color-inverse-muted: rgba(248,250,252,0.72);
  --font-heading: %s;
  --font-body: %s;
  --radius: %s;
  --radius-sm: calc(var(--radius) * 0.5);
  --radius-lg: calc(var(--radius) * 1.6);
  --space-1: 4px;
  --space-2: 8px;
  --space-3: 12px;
  --space-4: 16px;
  --space-5: 24px;
  --space-6: 32px;
  --space-7: 48px;
  --space-8: 64px;
  --space-9: 96px;
  --space-10: 128px;
  --shadow-sm: 0 1px 2px 0 rgba(0,0,0,0.04);
  --shadow-md: 0 8px 24px -8px rgba(15,23,42,0.12);
  --shadow-lg: 0 24px 48px -12px rgba(15,23,42,0.18);
  --container-narrow: 720px;
  --container-normal: 1080px;
  --container-wide: 1280px;
  --container-full: 100%%;
}
`, primary, secondary, accent, headingFont, bodyFont, borderRadius)
}

// builtinSiteCSS is the design-system stylesheet emitted into every published
// page. Sized small enough to inline (single round-trip) and intentionally
// flat — utility classes, section variants, and component styles cohabit.
// Consumers: every component-render branch in renderComponent.
const builtinSiteCSS = `
*, *::before, *::after { box-sizing: border-box; }
* { margin: 0; padding: 0; }
html { -webkit-text-size-adjust: 100%; scroll-behavior: smooth; }
body {
  font-family: var(--font-body);
  font-size: 16px;
  line-height: 1.6;
  color: var(--color-fg);
  background: var(--color-bg);
  -webkit-font-smoothing: antialiased;
  -moz-osx-font-smoothing: grayscale;
}
img, video { display: block; max-width: 100%; height: auto; }
a { color: inherit; }
h1, h2, h3, h4, h5, h6 {
  font-family: var(--font-heading);
  font-weight: 700;
  line-height: 1.15;
  letter-spacing: -0.015em;
  color: inherit;
}
h1 { font-size: clamp(2rem, 4vw + 1rem, 3.75rem); }
h2 { font-size: clamp(1.6rem, 2.4vw + 0.8rem, 2.5rem); }
h3 { font-size: clamp(1.25rem, 1vw + 0.9rem, 1.5rem); }
p { font-size: 1rem; line-height: 1.65; }
.lead { font-size: clamp(1.05rem, 0.4vw + 1rem, 1.25rem); color: var(--color-muted-fg); }
.eyebrow { display: inline-block; text-transform: uppercase; letter-spacing: 0.12em; font-size: 0.78rem; font-weight: 700; color: var(--color-accent); margin-bottom: var(--space-3); }

/* --- Layout primitives --- */
.section { padding-block: var(--space-8); }
.section--sm { padding-block: var(--space-6); }
.section--lg { padding-block: var(--space-9); }
.section--xl { padding-block: var(--space-10); }
.section--tone-default { background: var(--color-bg); color: var(--color-fg); }
.section--tone-muted { background: var(--color-muted-bg); color: var(--color-fg); }
.section--tone-inverse { background: var(--color-inverse-bg); color: var(--color-inverse-fg); }
.section--tone-inverse p { color: var(--color-inverse-muted); }
.section--tone-branded { background: var(--color-primary); color: var(--color-inverse-fg); }
.section--tone-branded p { color: rgba(255,255,255,0.86); }
.section--tone-accent { background: var(--color-accent); color: #0a0a0a; }
.section[data-bg-image] { background-size: cover; background-position: center; color: var(--color-inverse-fg); }
.section[data-bg-image]::before { content: ""; position: absolute; inset: 0; background: linear-gradient(180deg, rgba(0,0,0,0.55), rgba(0,0,0,0.65)); }
.section[data-bg-image] { position: relative; isolation: isolate; }
.section[data-bg-image] > * { position: relative; }
.container { width: 100%; margin-inline: auto; padding-inline: var(--space-5); }
.container--narrow { max-width: var(--container-narrow); }
.container--normal { max-width: var(--container-normal); }
.container--wide { max-width: var(--container-wide); }
.container--full { max-width: var(--container-full); padding-inline: var(--space-6); }
.stack { display: flex; flex-direction: column; }
.stack--sm { gap: var(--space-3); }
.stack--md { gap: var(--space-5); }
.stack--lg { gap: var(--space-7); }
.stack--xl { gap: var(--space-8); }
.stack--center { align-items: center; text-align: center; }
.stack--end { align-items: flex-end; text-align: right; }
.grid { display: grid; gap: var(--space-5); }
.grid--gap-sm { gap: var(--space-3); }
.grid--gap-md { gap: var(--space-5); }
.grid--gap-lg { gap: var(--space-7); }
.grid-cols-2 { grid-template-columns: repeat(2, minmax(0, 1fr)); }
.grid-cols-3 { grid-template-columns: repeat(3, minmax(0, 1fr)); }
.grid-cols-4 { grid-template-columns: repeat(4, minmax(0, 1fr)); }
.grid-cols-12 { grid-template-columns: repeat(12, minmax(0, 1fr)); }
.text-center { text-align: center; }
.text-right { text-align: right; }
@media (max-width: 960px) {
  .grid-cols-3, .grid-cols-4 { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}
@media (max-width: 640px) {
  .grid-cols-2, .grid-cols-3, .grid-cols-4 { grid-template-columns: minmax(0, 1fr); }
  .container { padding-inline: var(--space-4); }
}

/* --- Buttons --- */
.btn { display: inline-flex; align-items: center; justify-content: center; gap: var(--space-2); padding: 14px 28px; border-radius: var(--radius); font-weight: 600; font-size: 0.95rem; line-height: 1; text-decoration: none; transition: transform 0.15s ease, box-shadow 0.15s ease, background 0.15s ease; cursor: pointer; border: 1px solid transparent; white-space: nowrap; }
.btn:hover { transform: translateY(-1px); box-shadow: var(--shadow-md); }
.btn--primary { background: var(--color-primary); color: #ffffff; }
.btn--accent { background: var(--color-accent); color: #0a0a0a; }
.btn--inverse { background: #ffffff; color: var(--color-fg); }
.btn--outline { background: transparent; color: currentColor; border-color: currentColor; }
.btn--ghost { background: transparent; color: currentColor; border-color: transparent; }
.btn--lg { padding: 18px 36px; font-size: 1.05rem; }

/* --- Nav --- */
.site-nav { background: var(--color-inverse-bg); color: var(--color-inverse-fg); padding: var(--space-4) var(--space-5); display: flex; justify-content: space-between; align-items: center; position: sticky; top: 0; z-index: 100; }
.site-nav .nav-brand { font-weight: 700; font-size: 1.05rem; color: inherit; text-decoration: none; }
.site-nav ul { display: flex; gap: var(--space-5); list-style: none; flex-wrap: wrap; }
.site-nav ul a { color: var(--color-inverse-muted); text-decoration: none; font-size: 0.92rem; font-weight: 500; }
.site-nav ul a:hover { color: var(--color-inverse-fg); }

/* --- Footer --- */
.site-footer { background: var(--color-inverse-bg); color: var(--color-inverse-muted); padding: var(--space-7) var(--space-5); text-align: center; font-size: 0.9rem; }
.site-footer a { color: inherit; margin: 0 var(--space-2); text-decoration: none; }

/* --- Hero variants --- */
.hero { display: grid; gap: var(--space-7); align-items: center; }
.hero--centered { justify-items: center; text-align: center; max-width: 760px; margin-inline: auto; }
.hero--split { grid-template-columns: 1.05fr 1fr; gap: var(--space-8); }
.hero--split img { width: 100%; aspect-ratio: 4 / 3; object-fit: cover; border-radius: var(--radius-lg); box-shadow: var(--shadow-lg); }
.hero--gradient { background: linear-gradient(135deg, var(--color-secondary), var(--color-primary)); color: var(--color-inverse-fg); padding: var(--space-9) var(--space-6); border-radius: var(--radius-lg); }
.hero--gradient p { color: rgba(255,255,255,0.85); }
.hero h1 { margin-bottom: var(--space-4); }
.hero p { margin-bottom: var(--space-5); max-width: 60ch; }
.hero .btn-row { display: flex; gap: var(--space-3); flex-wrap: wrap; }
@media (max-width: 860px) { .hero--split { grid-template-columns: 1fr; } }

/* --- CTA variants --- */
.cta { display: grid; gap: var(--space-5); align-items: center; }
.cta--centered { justify-items: center; text-align: center; max-width: 720px; margin-inline: auto; }
.cta--split { grid-template-columns: 1.4fr 1fr; }
.cta--banner { padding: var(--space-7) var(--space-6); border-radius: var(--radius-lg); background: var(--color-primary); color: #ffffff; }
.cta--banner p { color: rgba(255,255,255,0.85); }
@media (max-width: 740px) { .cta--split { grid-template-columns: 1fr; text-align: center; } }

/* --- Cards & feature grids --- */
.card { background: var(--color-bg); border: 1px solid var(--color-border); border-radius: var(--radius-lg); padding: var(--space-5); transition: transform 0.2s ease, box-shadow 0.2s ease; display: flex; flex-direction: column; gap: var(--space-3); }
.card--quiet { background: var(--color-muted-bg); border-color: transparent; }
.card--ghost { background: transparent; border-color: transparent; padding-inline: 0; }
.card--lift:hover { transform: translateY(-4px); box-shadow: var(--shadow-lg); }
.card .icon { width: 44px; height: 44px; border-radius: var(--radius); background: color-mix(in srgb, var(--color-accent) 18%, transparent); display: grid; place-items: center; font-size: 1.4rem; }

/* --- Feature grid --- */
.feature-grid h3 { margin-bottom: var(--space-2); }
.feature-grid .card p { color: var(--color-muted-fg); }

/* --- Pricing --- */
.pricing { display: grid; gap: var(--space-5); }
.pricing__tier { background: var(--color-bg); border: 1px solid var(--color-border); border-radius: var(--radius-lg); padding: var(--space-6); display: flex; flex-direction: column; gap: var(--space-4); }
.pricing__tier--featured { border-color: var(--color-accent); box-shadow: var(--shadow-lg); transform: translateY(-4px); }
.pricing__price { font-family: var(--font-heading); font-size: 2.5rem; font-weight: 800; line-height: 1; }
.pricing__price small { font-size: 0.95rem; font-weight: 500; color: var(--color-muted-fg); }
.pricing__features { list-style: none; display: flex; flex-direction: column; gap: var(--space-2); font-size: 0.95rem; }
.pricing__features li { display: flex; gap: var(--space-2); align-items: flex-start; }
.pricing__features li::before { content: "✓"; color: var(--color-accent); font-weight: 800; }

/* --- Stats --- */
.stats { display: grid; gap: var(--space-5); text-align: center; }
.stats__item .num { font-family: var(--font-heading); font-size: clamp(2rem, 3vw + 1rem, 3.5rem); font-weight: 800; line-height: 1; color: var(--color-accent); }
.stats__item .label { font-size: 0.92rem; color: var(--color-muted-fg); margin-top: var(--space-2); text-transform: uppercase; letter-spacing: 0.08em; }
.section--tone-inverse .stats__item .label { color: var(--color-inverse-muted); }

/* --- Logo cloud --- */
.logo-cloud { display: grid; grid-template-columns: repeat(auto-fit, minmax(120px, 1fr)); gap: var(--space-6); align-items: center; opacity: 0.85; }
.logo-cloud img { max-height: 36px; width: auto; margin: 0 auto; filter: grayscale(1) contrast(0.8); }
.logo-cloud span { color: var(--color-muted-fg); text-align: center; font-weight: 600; font-size: 0.95rem; }

/* --- Testimonials --- */
.testimonial { background: var(--color-muted-bg); padding: var(--space-5); border-radius: var(--radius-lg); display: flex; flex-direction: column; gap: var(--space-3); }
.testimonial blockquote { font-size: 1.05rem; line-height: 1.5; }
.testimonial .author { font-weight: 600; font-size: 0.9rem; color: var(--color-muted-fg); }
.testimonial--quote { background: transparent; padding: 0; text-align: center; max-width: 720px; margin-inline: auto; }
.testimonial--quote blockquote { font-size: 1.4rem; font-style: italic; }
.testimonial--marquee { background: transparent; padding: 0; flex-direction: row; gap: var(--space-4); flex-wrap: nowrap; overflow-x: auto; }
.testimonial--marquee .testimonial-card { min-width: 320px; }

/* --- FAQ --- */
.faq { display: flex; flex-direction: column; }
.faq__item { border-top: 1px solid var(--color-border); padding: var(--space-4) 0; display: flex; flex-direction: column; gap: var(--space-2); }
.faq__item:last-child { border-bottom: 1px solid var(--color-border); }
.faq__item h3 { font-size: 1.1rem; }
.faq__item p { color: var(--color-muted-fg); }
.faq--cols { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: var(--space-4) var(--space-7); }
@media (max-width: 740px) { .faq--cols { grid-template-columns: 1fr; } }

/* --- Image / Media --- */
.media { display: grid; gap: var(--space-7); align-items: center; }
.media--right { grid-template-columns: 1fr 1.05fr; }
.media--left { grid-template-columns: 1.05fr 1fr; }
.media img { width: 100%; aspect-ratio: 4 / 3; object-fit: cover; border-radius: var(--radius-lg); }
.media .stack p { color: var(--color-muted-fg); }
@media (max-width: 740px) { .media--left, .media--right { grid-template-columns: 1fr; } }

/* --- Forms --- */
.form { display: flex; flex-direction: column; gap: var(--space-3); max-width: 480px; }
.form input, .form textarea { padding: 12px 14px; border: 1px solid var(--color-border-strong); border-radius: var(--radius); font: inherit; font-size: 1rem; background: var(--color-bg); color: var(--color-fg); }
.form input:focus, .form textarea:focus { outline: 2px solid var(--color-accent); outline-offset: 1px; border-color: var(--color-accent); }
.form button { padding: 14px; border: none; border-radius: var(--radius); background: var(--color-accent); color: #0a0a0a; font-weight: 700; font-size: 1rem; cursor: pointer; }

/* --- Image-wide block --- */
.img-wide { width: 100%; max-height: 600px; overflow: hidden; border-radius: var(--radius-lg); }
.img-wide img { width: 100%; height: 100%; object-fit: cover; }
.img-caption { color: var(--color-muted-fg); font-size: 0.85rem; text-align: center; margin-top: var(--space-2); }

/* --- Rich text helpers --- */
.prose { max-width: 68ch; }
.prose h2 { margin-top: var(--space-6); margin-bottom: var(--space-3); }
.prose h3 { margin-top: var(--space-5); margin-bottom: var(--space-2); }
.prose p { margin-bottom: var(--space-4); }
.prose ul, .prose ol { margin: 0 0 var(--space-4) var(--space-5); }
.prose li { margin-bottom: var(--space-2); }
.prose a { color: var(--color-primary); text-decoration: underline; text-underline-offset: 2px; }

/* --- Legacy support: keep old class hooks rendering reasonably --- */
.cta-button { display: inline-flex; align-items: center; padding: 14px 28px; background: var(--color-accent); color: #0a0a0a; text-decoration: none; border-radius: var(--radius); font-weight: 700; }
.nav, .nav-brand, .nav-links, .nav-links a, .nav-links a:hover { /* superseded by .site-nav */ }
.footer { background: var(--color-inverse-bg); color: var(--color-inverse-muted); padding: var(--space-7) var(--space-5); text-align: center; font-size: 0.9rem; }
.footer a { color: inherit; margin: 0 var(--space-2); text-decoration: none; }
.columns { display: grid; grid-template-columns: repeat(auto-fit, minmax(280px, 1fr)); gap: var(--space-5); }
.cta-section { padding: var(--space-8) var(--space-5); }
.cta-section-dark { background: var(--color-inverse-bg); color: var(--color-inverse-fg); padding: var(--space-9) var(--space-5); text-align: center; }
.lead-form { display: flex; gap: var(--space-3); justify-content: center; margin-top: var(--space-5); flex-wrap: wrap; }
.lead-form input { padding: 12px 18px; border: 1px solid var(--color-border-strong); border-radius: var(--radius); font: inherit; font-size: 1rem; min-width: 240px; }
.lead-form button { padding: 12px 24px; background: var(--color-accent); color: #0a0a0a; border: none; border-radius: var(--radius); font-weight: 700; cursor: pointer; font-size: 1rem; }
`
