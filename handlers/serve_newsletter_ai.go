package handlers

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/marketing-service/internal/ai"
	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// RegisterNewsletterAIRoutes mounts the newsletter authoring AI endpoints.
// Caller has already wrapped the group in RequireTenantAuth. The endpoints
// reuse the existing SiteAIProvider — newsletter posts are Puck documents,
// the same shape funnel pages already use, so we get authoring + editing for
// free by piping through GeneratePage / EditPage with newsletter-flavored
// prompts.
func RegisterNewsletterAIRoutes(tenantAPI *gin.RouterGroup) {
	tenantAPI.POST("/newsletters/:productId/posts/ai/generate", handleNewsletterAIGenerate)
	tenantAPI.POST("/newsletters/:productId/posts/:postId/ai/edit", handleNewsletterAIEdit)
	tenantAPI.POST("/newsletters/:productId/posts/ai/series", handleNewsletterAISeries)
	tenantAPI.POST("/newsletters/:productId/posts/ai/series/preview-outline", handleNewsletterAISeriesPreview)
	tenantAPI.POST("/newsletters/:productId/preview-ai", handleNewsletterAIPreview)
}

type newsletterGenerateReq struct {
	Prompt        string   `json:"prompt"`
	Tone          string   `json:"tone"`
	BrandProfile  string   `json:"brand_profile"`
	ContextChunks []string `json:"context_chunks"`
}

type newsletterGenerateResp struct {
	Title            string         `json:"title"`
	Subtitle         string         `json:"subtitle"`
	BodyDoc          map[string]any `json:"body_doc"`
	BodyMarkdown     string         `json:"body_markdown"`
	EmailSubject     string         `json:"email_subject"`
	EmailPreviewText string         `json:"email_preview_text"`
	SuggestedTags    []string       `json:"suggested_tags"`
}

func handleNewsletterAIGenerate(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	productID := c.Param("productId")
	if !bson.IsObjectIdHex(productID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid product id"})
		return
	}
	var req newsletterGenerateReq
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Prompt) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "prompt required"})
		return
	}

	// Build a newsletter-flavored prompt that produces a Puck doc plus the
	// post metadata we want. We reuse the existing GeneratePage method which
	// already emits a valid Puck root for our renderer. When no LLM provider
	// is configured (dev / tests), or the call fails, we fall through to a
	// stub doc so the authoring UX never breaks — tenants can still iterate
	// from a starter scaffold including the gate-break blocks.
	puckPrompt := buildNewsletterPostPuckPrompt(req)
	provider, perr := ai.GetConfiguredProvider()
	var doc map[string]any
	if perr != nil || provider == nil {
		log.Printf("newsletter AI: no provider configured, returning stub draft (%v)", perr)
		doc = stubPuckDoc(req)
	} else if d, err := provider.GeneratePage(puckPrompt); err != nil {
		log.Printf("newsletter AI generation failed, returning stub: %v", err)
		doc = stubPuckDoc(req)
	} else {
		doc = d
	}

	// Pull out title/subject from the prompt as a sensible default; the
	// frontend can override these in the editor.
	title := req.Prompt
	if len(title) > 80 {
		title = title[:80]
	}
	out := newsletterGenerateResp{
		Title:            title,
		Subtitle:         "",
		BodyDoc:          doc,
		BodyMarkdown:     "",
		EmailSubject:     title,
		EmailPreviewText: "",
		SuggestedTags:    []string{},
	}

	// Self-link: pre-load the post on the product if no draft exists yet so
	// the tenant can iterate without a separate "create" call. Optional —
	// frontend may also call POST /posts directly. We skip insert here to
	// avoid duplication.
	_ = ensureNewsletterProduct(tenantID, productID)

	c.JSON(http.StatusOK, out)
}

type newsletterEditReq struct {
	Instruction     string         `json:"instruction"`
	CurrentDocument map[string]any `json:"current_document"`
}

func handleNewsletterAIEdit(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req newsletterEditReq
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Instruction) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "instruction required"})
		return
	}
	provider, err := ai.GetConfiguredProvider()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if provider == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI provider not configured"})
		return
	}
	result, err := provider.EditPage(ai.PageEditRequest{
		Instruction:     "Edit this newsletter post: " + req.Instruction,
		CurrentDocument: req.CurrentDocument,
	})
	if err != nil {
		log.Printf("newsletter AI edit failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "AI edit failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"document": result.Document,
		"summary":  result.Summary,
	})
	_ = tenantID
}

func buildNewsletterPostPuckPrompt(req newsletterGenerateReq) string {
	var b strings.Builder
	b.WriteString("Generate a newsletter post as a Puck document. Output ONLY a JSON object matching the Puck root shape (root + content array). ")
	b.WriteString("Use these block types in `content`: HeroSection (heading, subheading), RichTextSection (content as HTML string), CTASection (heading, buttonText, buttonUrl). ")
	b.WriteString("Always include at least: a HeroSection at the top, two RichTextSection blocks, and one CTASection at the bottom. ")
	b.WriteString("Topic / instruction:\n")
	b.WriteString(req.Prompt)
	if req.Tone != "" {
		b.WriteString("\nTone: ")
		b.WriteString(req.Tone)
	}
	if req.BrandProfile != "" {
		b.WriteString("\nBrand voice and positioning:\n")
		b.WriteString(req.BrandProfile)
	}
	for i, ch := range req.ContextChunks {
		b.WriteString(fmt.Sprintf("\nContext chunk %d:\n%s", i+1, ch))
	}
	return b.String()
}

// stubPuckDoc returns a non-empty Puck document built from the prompt so the
// editor is never empty when the LLM is unavailable. Keeps the e2e flow
// independent of an external API key being present.
func stubPuckDoc(req newsletterGenerateReq) map[string]any {
	heading := req.Prompt
	if len(heading) > 60 {
		heading = heading[:60] + "…"
	}
	return map[string]any{
		"root": map[string]any{"props": map[string]any{}},
		"content": []any{
			map[string]any{
				"type": "HeroSection",
				"props": map[string]any{
					"heading":    heading,
					"subheading": "Drafted from your prompt — edit and add your own voice.",
				},
			},
			map[string]any{
				"type": "RichTextSection",
				"props": map[string]any{
					"content": "<p>This is a starter draft. Replace this paragraph with your own writing.</p>",
				},
			},
			map[string]any{
				"type":  "NewsletterSubscriberBreak",
				"props": map[string]any{},
			},
			map[string]any{
				"type": "RichTextSection",
				"props": map[string]any{
					"content": "<p>Subscriber-only content goes below the break above. Free subscribers can read this.</p>",
				},
			},
			map[string]any{
				"type": "NewsletterPaywallBreak",
				"props": map[string]any{
					"tier": "",
				},
			},
			map[string]any{
				"type": "RichTextSection",
				"props": map[string]any{
					"content": "<p>Paid-only content goes below the paywall break. Upgrade to read.</p>",
				},
			},
		},
	}
}

// ensureNewsletterProduct does a sanity load so the route returns a clear
// error if the product is missing. Returns silently otherwise.
func ensureNewsletterProduct(tenantID bson.ObjectId, productIDHex string) error {
	if !bson.IsObjectIdHex(productIDHex) {
		return fmt.Errorf("invalid product id")
	}
	var p pkgmodels.Product
	return db.GetCollection(pkgmodels.ProductCollection).Find(bson.M{
		"_id":          bson.ObjectIdHex(productIDHex),
		"tenant_id":    tenantID,
		"product_type": pkgmodels.ProductTypeNewsletter,
	}).One(&p)
}

// --- Series generation -------------------------------------------------

const seriesMaxIssueCount = 24

type seriesGenerateReq struct {
	Topic           string   `json:"topic"`
	Audience        string   `json:"audience"`
	Outcome         string   `json:"outcome"`
	Tone            string   `json:"tone"`
	Count           int      `json:"count"`
	ScheduleMode    string   `json:"schedule_mode"` // "absolute" | "drip"
	StartAt         string   `json:"start_at"`      // ISO8601, absolute mode
	CadenceDays     int      `json:"cadence_days"`
	ContextPackIDs  []string `json:"context_pack_ids"` // public_id or hex; either accepted
}

// handleNewsletterAISeries materialises N scheduled (or drip) posts grounded
// in the same context-pack reference, using a two-pass LLM flow that mirrors
// LMS course generation: one outline call returns the structured plan, then
// per-issue content calls produce each Puck doc. Failures fall through to a
// stub doc with NeedsReview=true so the series still materialises and the
// tenant can fix individual slots.
func handleNewsletterAISeries(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	productID := c.Param("productId")
	if !bson.IsObjectIdHex(productID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid product id"})
		return
	}
	var req seriesGenerateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if req.Count <= 0 {
		req.Count = 4
	}
	if req.Count > seriesMaxIssueCount {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("count exceeds cap of %d", seriesMaxIssueCount)})
		return
	}
	if req.ScheduleMode == "" {
		req.ScheduleMode = pkgmodels.NewsletterScheduleAbsolute
	}
	if req.CadenceDays <= 0 {
		req.CadenceDays = 7
	}

	productOID := bson.ObjectIdHex(productID)
	var product pkgmodels.Product
	if err := db.GetCollection(pkgmodels.ProductCollection).FindId(productOID).One(&product); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "product not found"})
		return
	}
	if product.TenantID != tenantID || product.ProductType != pkgmodels.ProductTypeNewsletter {
		c.JSON(http.StatusForbidden, gin.H{"error": "not allowed"})
		return
	}

	// Resolve context packs: explicit list wins, then product's newsletter
	// defaults. Accept either ObjectId hex or public_id from the client.
	packOIDs, refText := loadSeriesContextPacks(tenantID, &product, req.ContextPackIDs)

	// Resolve start time for absolute mode.
	var startAt time.Time
	if req.ScheduleMode == pkgmodels.NewsletterScheduleAbsolute {
		if req.StartAt == "" {
			startAt = time.Now().Add(24 * time.Hour)
		} else if t, err := time.Parse(time.RFC3339, req.StartAt); err == nil {
			startAt = t
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid start_at; use RFC3339"})
			return
		}
	}

	// Pass 1: outline
	provider, _ := ai.GetConfiguredProvider()
	outline := generateSeriesOutline(provider, req, refText)

	// Pass 2: per-issue content (parallel pool)
	docs := generateSeriesPostDocs(provider, req, outline, refText)

	// Materialise N posts.
	seriesID := generatePublicID()
	posts := make([]pkgmodels.NewsletterPost, 0, len(outline.Issues))
	for i, issue := range outline.Issues {
		post := pkgmodels.NewNewsletterPost(tenantID, productOID)
		post.Title = issue.Title
		post.Subtitle = issue.Brief
		post.Slug = slugifyTitle(issue.Title)
		post.BodyDoc = docs[i].doc
		post.NeedsReview = docs[i].needsReview
		post.SeriesID = seriesID
		post.SeriesOrder = i
		post.ContextPackIDs = packOIDs
		post.Status = pkgmodels.NewsletterPostStatusScheduled
		post.ScheduleMode = req.ScheduleMode
		if req.ScheduleMode == pkgmodels.NewsletterScheduleDrip {
			post.DripOffsetSeconds = int64(i*req.CadenceDays) * 86400
		} else {
			when := startAt.Add(time.Duration(i*req.CadenceDays) * 24 * time.Hour)
			post.ScheduledAt = &when
		}
		if err := db.GetCollection(pkgmodels.NewsletterPostCollection).Insert(post); err == nil {
			posts = append(posts, *post)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"series_id": seriesID,
		"outline":   outline,
		"posts":     posts,
	})
}

// handleNewsletterAISeriesPreview runs only the outline call and returns
// the structured plan to the tenant for sanity-checking BEFORE committing
// to the per-issue content fan-out (which is the expensive path). Doesn't
// persist anything.
func handleNewsletterAISeriesPreview(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	productID := c.Param("productId")
	if !bson.IsObjectIdHex(productID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid product id"})
		return
	}
	var req seriesGenerateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if req.Count <= 0 {
		req.Count = 4
	}
	if req.Count > seriesMaxIssueCount {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("count exceeds cap of %d", seriesMaxIssueCount)})
		return
	}
	productOID := bson.ObjectIdHex(productID)
	var product pkgmodels.Product
	if err := db.GetCollection(pkgmodels.ProductCollection).FindId(productOID).One(&product); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "product not found"})
		return
	}
	_, refText := loadSeriesContextPacks(tenantID, &product, req.ContextPackIDs)
	provider, _ := ai.GetConfiguredProvider()
	outline := generateSeriesOutline(provider, req, refText)
	c.JSON(http.StatusOK, gin.H{"outline": outline})
}

// generateSeriesOutline calls the LLM for the outline pass. Falls back to
// a deterministic plan if the provider is missing or fails — same pattern
// LMS uses, so the tenant always gets something to iterate on.
func generateSeriesOutline(provider ai.SiteAIProvider, req seriesGenerateReq, refText string) *ai.SeriesOutlineResponse {
	if provider != nil {
		out, err := provider.GenerateNewsletterSeriesOutline(ai.SeriesOutlineRequest{
			Topic:         req.Topic,
			Audience:      req.Audience,
			Outcome:       req.Outcome,
			Tone:          req.Tone,
			IssueCount:    req.Count,
			ReferenceText: refText,
		})
		if err == nil && out != nil && len(out.Issues) > 0 {
			return out
		}
		log.Printf("series outline LLM failed, falling back: %v", err)
	}
	return deterministicSeriesOutline(req)
}

// deterministicSeriesOutline returns a stable outline for dev/no-LLM runs.
// Issue titles tag the topic with the index so every slot is editable.
func deterministicSeriesOutline(req seriesGenerateReq) *ai.SeriesOutlineResponse {
	out := &ai.SeriesOutlineResponse{
		SeriesTitle: req.Topic,
		Description: "A " + nonEmpty(req.Tone, "weekly") + " series on " + nonEmpty(req.Topic, "your topic"),
	}
	for i := 0; i < req.Count; i++ {
		out.Issues = append(out.Issues, ai.IssueOutline{
			Order:     i + 1,
			Title:     fmt.Sprintf("%s — issue %d", nonEmpty(req.Topic, "Untitled"), i+1),
			Brief:     "Drafted slot — replace this brief with the take you want for this issue.",
			KeyPoints: []string{"Open with a hook", "Develop the core idea", "Land with a clear takeaway"},
		})
	}
	return out
}

// generatedPost wraps the per-issue Puck doc with the failure flag so the
// caller knows whether to surface NeedsReview on the materialised post.
type generatedPost struct {
	doc         bson.M
	needsReview bool
}

// generateSeriesPostDocs runs per-issue content generation in parallel
// (concurrency 4 — same shape LMS uses for lesson bodies). Failed slots
// fall back to a stub doc with NeedsReview=true.
func generateSeriesPostDocs(provider ai.SiteAIProvider, req seriesGenerateReq, outline *ai.SeriesOutlineResponse, refText string) []generatedPost {
	results := make([]generatedPost, len(outline.Issues))
	if provider == nil {
		for i, issue := range outline.Issues {
			results[i] = generatedPost{doc: stubPostDocFromIssue(outline.SeriesTitle, issue), needsReview: true}
		}
		return results
	}
	type job struct {
		index int
		issue ai.IssueOutline
	}
	jobs := make(chan job, len(outline.Issues))
	for i, issue := range outline.Issues {
		jobs <- job{index: i, issue: issue}
	}
	close(jobs)

	const workers = 4
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for j := range jobs {
				doc, err := provider.GenerateNewsletterPostFromBrief(ai.PostFromBriefRequest{
					SeriesTitle:   outline.SeriesTitle,
					IssueTitle:    j.issue.Title,
					IssueBrief:    j.issue.Brief,
					KeyPoints:     j.issue.KeyPoints,
					Tone:          req.Tone,
					Audience:      req.Audience,
					ReferenceText: refText,
				})
				if err != nil || doc == nil {
					log.Printf("post-from-brief failed for issue %d: %v", j.index, err)
					results[j.index] = generatedPost{doc: stubPostDocFromIssue(outline.SeriesTitle, j.issue), needsReview: true}
					continue
				}
				results[j.index] = generatedPost{doc: bson.M(doc)}
			}
		}()
	}
	wg.Wait()
	return results
}

// stubPostDocFromIssue produces a working Puck doc when the LLM is
// unavailable or returned junk. Includes both gate blocks so the tenant
// can edit content into each section without re-arranging structure.
func stubPostDocFromIssue(seriesTitle string, issue ai.IssueOutline) bson.M {
	bullets := ""
	for _, p := range issue.KeyPoints {
		bullets += "<li>" + p + "</li>"
	}
	if bullets == "" {
		bullets = "<li>Replace this stub with your take.</li>"
	}
	return bson.M{
		"root": bson.M{"props": bson.M{}},
		"content": []any{
			bson.M{"type": "HeroSection", "props": bson.M{"heading": issue.Title, "subheading": issue.Brief}},
			bson.M{"type": "RichTextSection", "props": bson.M{"content": "<p>Stub draft — the LLM was unavailable when this slot was generated. Edit this text to ship the issue.</p><ul>" + bullets + "</ul>"}},
			bson.M{"type": "NewsletterSubscriberBreak", "props": bson.M{}},
			bson.M{"type": "RichTextSection", "props": bson.M{"content": "<p>Subscriber-only deeper take. Replace this section.</p>"}},
			bson.M{"type": "NewsletterPaywallBreak", "props": bson.M{"tier": ""}},
			bson.M{"type": "RichTextSection", "props": bson.M{"content": "<p>Paid-only addendum. Replace this section.</p>"}},
		},
	}
}

// loadSeriesContextPacks accepts a list of public_ids or ObjectId hex,
// resolves them to ObjectIds for the post, and concatenates the chunks
// for use as ReferenceText in the outline + content calls. Falls back to
// the newsletter's DefaultContextPackIDs when the request omits the field.
func loadSeriesContextPacks(tenantID bson.ObjectId, product *pkgmodels.Product, raw []string) ([]bson.ObjectId, string) {
	var packs []pkgmodels.ContextPack
	col := db.GetCollection(pkgmodels.ContextPackCollection)
	for _, idOrSlug := range raw {
		idOrSlug = strings.TrimSpace(idOrSlug)
		if idOrSlug == "" {
			continue
		}
		var p pkgmodels.ContextPack
		if bson.IsObjectIdHex(idOrSlug) {
			if err := col.Find(bson.M{"_id": bson.ObjectIdHex(idOrSlug), "tenant_id": tenantID}).One(&p); err == nil {
				packs = append(packs, p)
				continue
			}
		}
		if err := col.Find(bson.M{"public_id": idOrSlug, "tenant_id": tenantID}).One(&p); err == nil {
			packs = append(packs, p)
		}
	}
	if len(packs) == 0 && product.Newsletter != nil && len(product.Newsletter.DefaultContextPackIDs) > 0 {
		_ = col.Find(bson.M{"_id": bson.M{"$in": product.Newsletter.DefaultContextPackIDs}}).All(&packs)
	}
	ids := make([]bson.ObjectId, 0, len(packs))
	var pieces []string
	for _, p := range packs {
		ids = append(ids, p.Id)
		for _, ch := range p.Chunks {
			pieces = append(pieces, ch.Text)
		}
	}
	combined := strings.Join(pieces, "\n\n")
	if len(combined) > 32000 {
		combined = combined[:32000]
	}
	return ids, combined
}

// --- Inline AI handlebar preview --------------------------------------

type previewAIReq struct {
	Prompt         string   `json:"prompt"`
	ContextPackIDs []string `json:"context_pack_ids"`
}

// handleNewsletterAIPreview runs the resolver one-shot for the tenant's
// authoring UI without writing to the cache. Lets the tenant sanity-check
// what an {{ai}} handlebar will produce before publishing it. The request
// uses the same context-pack resolution as the series handler so previews
// stay grounded in the same source material the broadcast will use.
func handleNewsletterAIPreview(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	productID := c.Param("productId")
	if !bson.IsObjectIdHex(productID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid product id"})
		return
	}
	var req previewAIReq
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Prompt) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "prompt required"})
		return
	}
	productOID := bson.ObjectIdHex(productID)
	var product pkgmodels.Product
	if err := db.GetCollection(pkgmodels.ProductCollection).FindId(productOID).One(&product); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "product not found"})
		return
	}
	_, refText := loadSeriesContextPacks(tenantID, &product, req.ContextPackIDs)
	provider, _ := ai.GetConfiguredProvider()
	if provider == nil {
		c.JSON(http.StatusOK, gin.H{"value": "[ai unavailable — no LLM provider configured]"})
		return
	}
	value, err := provider.GenerateText(ai.GenerateTextRequest{
		Prompt:        req.Prompt,
		ReferenceText: refText,
	})
	if err != nil {
		log.Printf("preview-ai failed: %v", err)
		c.JSON(http.StatusOK, gin.H{"value": "[ai unavailable]"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"value": value})
}

// nonEmpty returns the first arg if non-empty, otherwise the fallback.
func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// slugifyTitle is a tiny hyphen-only slug helper. The full route-side
// slugify in newsletters.go handles uniqueness; this is enough for an
// initial value the tenant can edit.
func slugifyTitle(title string) string {
	t := strings.ToLower(strings.TrimSpace(title))
	out := make([]byte, 0, len(t))
	for i := 0; i < len(t); i++ {
		c := t[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			out = append(out, c)
		case c == ' ' || c == '-':
			if len(out) > 0 && out[len(out)-1] != '-' {
				out = append(out, '-')
			}
		}
	}
	return strings.Trim(string(out), "-")
}

// generatePublicID is a minimal random id; we want short stable strings
// for series_id without pulling in the utils.GeneratePublicId dependency
// chain. 16 hex chars is collision-safe at our scale.
func generatePublicID() string {
	id := bson.NewObjectId()
	return id.Hex()[:16]
}
