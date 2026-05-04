package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/marketing-service/internal/ai"
	"github.com/josephalai/sentanyl/marketing-service/internal/site"
	"github.com/josephalai/sentanyl/pkg/auth"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// RegisterSiteDuplicateRoutes registers the site cloning endpoint.
func RegisterSiteDuplicateRoutes(tenantAPI *gin.RouterGroup) {
	tenantAPI.POST("/sites/duplicate-from-url", handleDuplicateSiteFromURL)
}

func handleDuplicateSiteFromURL(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	provider, err := ai.GetConfiguredProvider()
	if err != nil || provider == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI provider not configured"})
		return
	}

	var req struct {
		URL      string `json:"url" binding:"required"`
		SiteName string `json:"site_name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "url is required"})
		return
	}

	parsed, err := url.ParseRequestURI(req.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid url"})
		return
	}
	host := strings.ToLower(parsed.Hostname())
	for _, prefix := range []string{"localhost", "127.", "192.168.", "10.", "172."} {
		if host == prefix || strings.HasPrefix(host, prefix) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "private URLs not allowed"})
			return
		}
	}

	// Crawl via sandbox (Docker) or fallback to direct HTTP
	extracted, extractErr := crawlViaSandboxOrDirect(req.URL)
	if extractErr != nil {
		log.Printf("site duplication crawl failed for %s: %v", req.URL, extractErr)
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("failed to crawl URL: %v", extractErr)})
		return
	}

	siteName := req.SiteName
	if siteName == "" {
		siteName = extracted.PageTitle
		if siteName == "" {
			siteName = parsed.Hostname()
		}
	}

	dupReq := ai.SiteDuplicateRequest{
		SourceURL:      req.URL,
		SiteName:       siteName,
		NavLinks:       extracted.NavLinks,
		PageTitle:      extracted.PageTitle,
		MetaDesc:       extracted.MetaDesc,
		Sections:       extracted.Sections,
		PrimaryColor:   extracted.PrimaryColor,
		SecondaryColor: extracted.SecondaryColor,
		AccentColor:    extracted.AccentColor,
		HeadingFont:    extracted.HeadingFont,
		BodyFont:       extracted.BodyFont,
		ScreenshotB64:  extracted.ScreenshotB64,
	}

	result, err := provider.DuplicateSite(dupReq)
	if err != nil {
		log.Printf("AI site duplication failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "AI generation failed"})
		return
	}

	// Create site + pages
	newSite := pkgmodels.NewSite()
	newSite.TenantID = tenantID
	newSite.Name = siteName
	newSite.Status = "draft"
	if result.Theme != "" {
		newSite.Theme = result.Theme
	}
	if result.SEO != nil {
		newSite.SEO = &pkgmodels.SEOConfig{
			MetaTitle:       result.SEO.MetaTitle,
			MetaDescription: result.SEO.MetaDescription,
		}
	}
	if result.Navigation != nil {
		nav := pkgmodels.NavigationConfig{}
		for _, l := range result.Navigation.HeaderLinks {
			nav.HeaderNavLinks = append(nav.HeaderNavLinks, pkgmodels.NavLink{Label: l.Label, URL: l.URL})
		}
		for _, l := range result.Navigation.FooterLinks {
			nav.FooterNavLinks = append(nav.FooterNavLinks, pkgmodels.NavLink{Label: l.Label, URL: l.URL})
		}
		newSite.Navigation = &nav
	}
	newSite.GlobalStyle = &pkgmodels.GlobalStyle{
		PrimaryColor:   extracted.PrimaryColor,
		SecondaryColor: extracted.SecondaryColor,
		AccentColor:    extracted.AccentColor,
		HeadingFont:    extracted.HeadingFont,
		BodyFont:       extracted.BodyFont,
	}
	if err := site.CreateSite(newSite); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create site"})
		return
	}

	// Note: we intentionally no longer call provider.GenerateSiteHTML or write
	// PublishedHTML on cloned pages. The two-track storage (vision HTML +
	// Puck JSON) caused user edits in the editor to be invisible in the
	// public view, because the viewer prefers PublishedHTML when set. Puck
	// is now the single source of truth — RenderPuckDocumentToHTML paints
	// the same blocks the editor sees.

	var createdPages []site.SitePage
	for _, pageResult := range result.Pages {
		pg := site.NewSitePage(pageResult.Name, pageResult.Slug, newSite.Id, tenantID)
		pg.IsHome = pageResult.IsHome
		if pageResult.SEO != nil {
			pg.SEO = &pkgmodels.SEOConfig{
				MetaTitle:       pageResult.SEO.MetaTitle,
				MetaDescription: pageResult.SEO.MetaDescription,
			}
		}
		pg.DraftDocument = pageResult.PuckRoot

		if err := site.CreateSitePage(pg); err != nil {
			log.Printf("failed to create duplicated page %s: %v", pageResult.Name, err)
			continue
		}
		version := site.NewSitePageVersion(newSite.Id, pg.Id, tenantID, site.VersionTypeDraft, 1)
		version.PuckRoot = pageResult.PuckRoot
		version.SEO = pg.SEO
		version.Metadata = &site.SiteVersionMetadata{
			GeneratedBy: "duplicate",
			Prompt:      req.URL,
		}
		if err := site.CreateSitePageVersion(version); err == nil {
			_ = site.UpdateSitePage(pg.Id, tenantID, bson.M{"draft_version_id": version.PublicId})
		}
		createdPages = append(createdPages, *pg)
	}

	c.JSON(http.StatusOK, gin.H{
		"status":        "ok",
		"site_id":       newSite.Id.Hex(),
		"site_public_id": newSite.PublicId,
		"site_name":     siteName,
		"pages_created": len(createdPages),
		"style":         newSite.GlobalStyle,
	})
}

// ─── Crawl dispatch ────────────────────────────────────────────────────────

type crawledSite struct {
	PageTitle      string
	MetaDesc       string
	NavLinks       []ai.NavLinkResult
	Sections       []ai.ExtractedSection
	PrimaryColor   string
	SecondaryColor string
	AccentColor    string
	HeadingFont    string
	BodyFont       string
	ScreenshotB64  string // JPEG base64 from Playwright
}

// crawlViaSandboxOrDirect uses the Docker sandbox when available, falls back to direct.
func crawlViaSandboxOrDirect(targetURL string) (*crawledSite, error) {
	sandboxURL := os.Getenv("CLONE_SANDBOX_URL")
	if sandboxURL != "" {
		result, err := crawlViaSandbox(sandboxURL, targetURL)
		if err != nil {
			log.Printf("sandbox crawl failed (%v), falling back to direct", err)
		} else {
			return result, nil
		}
	}
	return crawlDirect(targetURL)
}

// sandboxResponse mirrors the JSON returned by site-sandbox/server.js.
type sandboxResponse struct {
	Title          string           `json:"title"`
	MetaDesc       string           `json:"metaDesc"`
	NavLinks       []sandboxNavLink `json:"navLinks"`
	Colors         []string         `json:"colors"`
	Fonts          []string         `json:"fonts"`
	Sections       []sandboxSection `json:"sections"`
	ScreenshotB64  string           `json:"screenshotBase64"`
}

type sandboxNavLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

type sandboxSection struct {
	Heading            string             `json:"heading"`
	HeadingLevel       int                `json:"headingLevel"`
	HeadingAccentColor string             `json:"headingAccentColor"`
	Body               string             `json:"body"`
	ImageURL           string             `json:"imageURL"`
	ImageAlt           string             `json:"imageAlt"`
	CTAText            string             `json:"ctaText"`
	CTAUrl             string             `json:"ctaUrl"`
	IsDark             bool               `json:"isDark"`
	BgColor            string             `json:"bgColor"`
	FormType           string             `json:"formType"`
	FormButtonText     string             `json:"formButtonText"`
	FormHasName        bool               `json:"formHasName"`
	GridItems          []sandboxGridItem  `json:"gridItems"`
}

type sandboxGridItem struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	ImageURL string `json:"imageUrl"`
}

// crawlViaSandbox calls the site-sandbox Docker service.
func crawlViaSandbox(sandboxBaseURL, targetURL string) (*crawledSite, error) {
	reqBody, _ := json.Marshal(map[string]string{"url": targetURL})
	httpResp, err := (&http.Client{Timeout: 40 * time.Second}).Post(
		sandboxBaseURL+"/crawl", "application/json", bytes.NewReader(reqBody),
	)
	if err != nil {
		return nil, fmt.Errorf("sandbox unreachable: %w", err)
	}
	defer httpResp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(httpResp.Body, 512*1024))
	if err != nil {
		return nil, fmt.Errorf("sandbox read failed: %w", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sandbox returned %d: %s", httpResp.StatusCode, string(respBytes))
	}

	var sr sandboxResponse
	if err := json.Unmarshal(respBytes, &sr); err != nil {
		return nil, fmt.Errorf("sandbox parse failed: %w", err)
	}

	result := &crawledSite{
		PageTitle: sr.Title,
		MetaDesc:  sr.MetaDesc,
	}
	for _, l := range sr.NavLinks {
		result.NavLinks = append(result.NavLinks, ai.NavLinkResult{Label: l.Label, URL: l.URL})
	}
	for _, s := range sr.Sections {
		var gridItems []ai.ExtractedGridItem
		for _, gi := range s.GridItems {
			gridItems = append(gridItems, ai.ExtractedGridItem{
				Title:    gi.Title,
				Body:     gi.Body,
				ImageURL: gi.ImageURL,
			})
		}
		result.Sections = append(result.Sections, ai.ExtractedSection{
			Heading:            s.Heading,
			HeadingLevel:       s.HeadingLevel,
			HeadingAccentColor: s.HeadingAccentColor,
			Body:               s.Body,
			ImageURL:           s.ImageURL,
			ImageAlt:           s.ImageAlt,
			CTAText:            s.CTAText,
			CTAUrl:             s.CTAUrl,
			IsDark:             s.IsDark,
			BgColor:            s.BgColor,
			FormType:           s.FormType,
			FormButtonText:     s.FormButtonText,
			FormHasName:        s.FormHasName,
			GridItems:          gridItems,
		})
	}
	// Assign colors: primary/secondary from darks, accent from heading accent color or vivid color
	if len(sr.Colors) > 0 {
		result.PrimaryColor = sr.Colors[0]
	}
	if len(sr.Colors) > 1 {
		result.SecondaryColor = sr.Colors[1]
	}
	if len(sr.Colors) > 2 {
		result.AccentColor = sr.Colors[2]
	}
	// Override accent with any color found on accent-colored heading spans — most authoritative signal
	for _, s := range sr.Sections {
		if s.HeadingAccentColor != "" {
			if hex := rgbStringToHex(s.HeadingAccentColor); hex != "" {
				result.AccentColor = hex
				break
			}
		}
	}
	if len(sr.Fonts) > 0 {
		result.HeadingFont = cleanFont(sr.Fonts[0])
		result.BodyFont = result.HeadingFont
	}
	if len(sr.Fonts) > 1 {
		result.BodyFont = cleanFont(sr.Fonts[1])
	}
	result.ScreenshotB64 = sr.ScreenshotB64
	return result, nil
}

// cleanFont strips quotes and picks first font family name.
func cleanFont(f string) string {
	f = strings.Split(f, ",")[0]
	f = strings.TrimSpace(strings.Trim(f, `"'`))
	return f
}

// ─── Direct crawl (goquery fallback) ──────────────────────────────────────

func crawlDirect(targetURL string) (*crawledSite, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	req, err := http.NewRequest("GET", targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("read body failed: %w", err)
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("HTML parse failed: %w", err)
	}

	base, _ := url.Parse(targetURL)
	result := &crawledSite{}

	result.PageTitle = strings.TrimSpace(doc.Find("title").First().Text())
	doc.Find("meta").Each(func(_ int, s *goquery.Selection) {
		name, _ := s.Attr("name")
		prop, _ := s.Attr("property")
		content, _ := s.Attr("content")
		if (name == "description" || prop == "og:description") && result.MetaDesc == "" {
			result.MetaDesc = content
		}
	})

	// Nav links
	navSeen := map[string]bool{}
	doc.Find("nav a, header a, .nav a, .navbar a, .menu a, #menu a").Each(func(_ int, s *goquery.Selection) {
		text := strings.TrimSpace(s.Text())
		href, _ := s.Attr("href")
		if text == "" || href == "" || href == "#" || navSeen[text] {
			return
		}
		navSeen[text] = true
		if len(result.NavLinks) < 10 {
			result.NavLinks = append(result.NavLinks, ai.NavLinkResult{Label: text, URL: resolveAbsURL(base, href)})
		}
	})

	// CSS extraction
	var cssBuilder strings.Builder
	doc.Find("style").Each(func(_ int, s *goquery.Selection) {
		cssBuilder.WriteString(s.Text())
	})
	fetched := 0
	doc.Find("link[rel='stylesheet']").Each(func(_ int, s *goquery.Selection) {
		if fetched >= 3 {
			return
		}
		href, ok := s.Attr("href")
		if !ok {
			return
		}
		cssURL := resolveAbsURL(base, href)
		if r, err := client.Get(cssURL); err == nil {
			defer r.Body.Close()
			if b, err := io.ReadAll(io.LimitReader(r.Body, 150*1024)); err == nil {
				cssBuilder.Write(b)
				fetched++
			}
		}
	})

	css := cssBuilder.String()
	hexColors := extractHexColors(css)
	assignPalette(hexColors, result)
	result.HeadingFont, result.BodyFont = extractFonts(css)

	// Sections
	doc.Find("nav, header nav, script, style, noscript, iframe, .cookie-banner").Remove()
	result.Sections = extractGoquerySections(doc, base)

	return result, nil
}

func extractGoquerySections(doc *goquery.Document, base *url.URL) []ai.ExtractedSection {
	var sections []ai.ExtractedSection
	seen := map[string]bool{}
	containerSel := "section, article, .elementor-section, .wp-block-cover, .wp-block-group, [class*=section], [class*=hero], [class*=banner]"

	doc.Find(containerSel).Each(func(_ int, s *goquery.Selection) {
		if s.Parents().Filter(containerSel).Length() > 2 {
			return
		}
		sec := extractGoquerySection(s, base)
		if sec == nil {
			return
		}
		key := sec.Heading + "|" + sec.Body[:minInt(len(sec.Body), 40)]
		if seen[key] {
			return
		}
		seen[key] = true
		sections = append(sections, *sec)
		if len(sections) >= 12 {
			return
		}
	})

	if len(sections) == 0 {
		doc.Find("h1, h2").Each(func(_ int, h *goquery.Selection) {
			text := strings.TrimSpace(h.Text())
			if text == "" {
				return
			}
			level := 2
			if goquery.NodeName(h) == "h1" {
				level = 1
			}
			var bodyParts []string
			h.NextUntil("h1, h2").Find("p").Each(func(_ int, p *goquery.Selection) {
				bodyParts = append(bodyParts, strings.TrimSpace(p.Text()))
			})
			sections = append(sections, ai.ExtractedSection{
				Heading:      text,
				HeadingLevel: level,
				Body:         strings.Join(bodyParts, " "),
			})
		})
	}

	return sections
}

func extractGoquerySection(s *goquery.Selection, base *url.URL) *ai.ExtractedSection {
	sec := &ai.ExtractedSection{}

	s.Find("h1, h2, h3, h4").Each(func(_ int, h *goquery.Selection) {
		if sec.Heading != "" {
			return
		}
		text := strings.TrimSpace(h.Text())
		if text == "" {
			return
		}
		sec.Heading = text
		switch goquery.NodeName(h) {
		case "h1":
			sec.HeadingLevel = 1
		case "h2":
			sec.HeadingLevel = 2
		default:
			sec.HeadingLevel = 3
		}
	})

	var bodyParts []string
	s.Find("p").Each(func(_ int, p *goquery.Selection) {
		text := strings.TrimSpace(p.Text())
		if text == "" || len(text) < 20 {
			return
		}
		bodyParts = append(bodyParts, text)
	})
	sec.Body = strings.Join(bodyParts, " ")
	if len(sec.Body) > 600 {
		sec.Body = sec.Body[:600]
	}

	s.Find("img").Each(func(_ int, img *goquery.Selection) {
		if sec.ImageURL != "" {
			return
		}
		src, _ := img.Attr("src")
		if src == "" {
			src, _ = img.Attr("data-src")
		}
		if src == "" || strings.Contains(src, "data:image") {
			return
		}
		alt, _ := img.Attr("alt")
		sec.ImageURL = resolveAbsURL(base, src)
		sec.ImageAlt = alt
	})

	s.Find("a[href], button").Each(func(_ int, a *goquery.Selection) {
		if sec.CTAText != "" {
			return
		}
		text := strings.TrimSpace(a.Text())
		href, _ := a.Attr("href")
		if text == "" || len(text) > 50 {
			return
		}
		navWords := map[string]bool{"home": true, "about": true, "blog": true, "contact": true, "login": true, "subscribe": true}
		if navWords[strings.ToLower(text)] {
			return
		}
		sec.CTAText = text
		if href != "" && href != "#" {
			sec.CTAUrl = resolveAbsURL(base, href)
		}
	})

	if sec.Heading == "" && sec.Body == "" && sec.ImageURL == "" {
		return nil
	}

	style, _ := s.Attr("style")
	class, _ := s.Attr("class")
	combined := strings.ToLower(style + " " + class)
	darkIndicators := []string{"bg-dark", "bg-black", "dark-bg", "section-dark", "background:#0", "background:#1", "background:#2"}
	for _, ind := range darkIndicators {
		if strings.Contains(combined, ind) {
			sec.IsDark = true
			break
		}
	}

	return sec
}

func extractHexColors(css string) []string {
	hexRe := regexp.MustCompile(`#([0-9a-fA-F]{6}|[0-9a-fA-F]{3})\b`)
	counts := map[string]int{}
	for _, m := range hexRe.FindAllString(css, -1) {
		c := strings.ToLower(m)
		if c == "#ffffff" || c == "#fff" || c == "#000000" || c == "#000" {
			continue
		}
		counts[c]++
	}
	type kv struct{ k string; v int }
	var sorted []kv
	for k, v := range counts {
		sorted = append(sorted, kv{k, v})
	}
	for i := range sorted {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].v > sorted[i].v {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	result := make([]string, 0, len(sorted))
	for _, kv := range sorted {
		result = append(result, kv.k)
	}
	return result
}

func assignPalette(colors []string, result *crawledSite) {
	for i, c := range colors {
		switch i {
		case 0:
			result.PrimaryColor = c
		case 1:
			result.SecondaryColor = c
		case 2:
			result.AccentColor = c
			return
		}
	}
}

func extractFonts(css string) (heading, body string) {
	fontRe := regexp.MustCompile(`font-family:\s*([^;}{]+)`)
	counts := map[string]int{}
	for _, m := range fontRe.FindAllStringSubmatch(css, -1) {
		f := strings.TrimSpace(strings.Split(m[1], ",")[0])
		f = strings.TrimSpace(strings.Trim(f, `"'`))
		if f == "" || strings.EqualFold(f, "inherit") || strings.EqualFold(f, "initial") {
			continue
		}
		counts[f]++
	}
	maxCount := 0
	for f, c := range counts {
		if c > maxCount {
			maxCount = c
			heading = f
		}
	}
	body = heading
	return
}

func resolveAbsURL(base *url.URL, href string) string {
	if href == "" {
		return ""
	}
	if strings.HasPrefix(href, "//") {
		href = base.Scheme + ":" + href
	}
	ref, err := url.Parse(href)
	if err != nil {
		return href
	}
	return base.ResolveReference(ref).String()
}

// rgbStringToHex converts "rgb(101, 212, 110)" to "#65d46e"
func rgbStringToHex(rgb string) string {
	re := regexp.MustCompile(`rgba?\((\d+),\s*(\d+),\s*(\d+)`)
	m := re.FindStringSubmatch(rgb)
	if len(m) < 4 {
		return ""
	}
	toInt := func(s string) int {
		n := 0
		for _, c := range s {
			n = n*10 + int(c-'0')
		}
		return n
	}
	r, g, b := toInt(m[1]), toInt(m[2]), toInt(m[3])
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

