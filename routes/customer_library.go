package routes

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	"github.com/josephalai/sentanyl/pkg/i18n"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// resolveRequestLocale pulls the contact's stored preference (if any) and
// hands it to the i18n resolver alongside the request. Falls through to "" if
// no signal is present, which means base-language values are used.
func resolveRequestLocale(c *gin.Context, tenantID, contactID bson.ObjectId) string {
	var contact pkgmodels.User
	pref := ""
	if err := db.GetCollection(pkgmodels.UserCollection).Find(bson.M{
		"_id":       contactID,
		"tenant_id": tenantID,
	}).One(&contact); err == nil {
		pref = contact.PreferredLocale
	}
	return i18n.ResolveLocale(c, pref)
}

// resolveCustomerContext pulls tenant + contact ids from the JWT claims.
func resolveCustomerContext(c *gin.Context) (tenantID, contactID bson.ObjectId, ok bool) {
	tenantStr := auth.GetTenantID(c)
	contactStr := auth.GetContactID(c)
	if tenantStr == "" || contactStr == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return "", "", false
	}
	if !bson.IsObjectIdHex(tenantStr) || !bson.IsObjectIdHex(contactStr) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid token data"})
		return "", "", false
	}
	return bson.ObjectIdHex(tenantStr), bson.ObjectIdHex(contactStr), true
}

// contactBadgeNames resolves the badge string names for a contact so we can
// match against Offer.GrantedBadges (which stores names, not ids).
func contactBadgeNames(tenantID, contactID bson.ObjectId) ([]string, error) {
	var contact pkgmodels.User
	if err := db.GetCollection(pkgmodels.UserCollection).Find(bson.M{
		"_id":       contactID,
		"tenant_id": tenantID,
	}).One(&contact); err != nil {
		return nil, err
	}
	var names []string
	for _, badgeID := range contact.Badges {
		var badge pkgmodels.Badge
		if err := db.GetCollection(pkgmodels.BadgeCollection).FindId(badgeID).One(&badge); err == nil {
			names = append(names, badge.Name)
		}
	}
	return names, nil
}

// customerOwnsProduct returns the Product if any Offer the contact holds
// includes it. Uses the same resolution rules as handleGetLibraryProducts.
func customerOwnsProduct(tenantID, contactID bson.ObjectId, productIDOrPublic string) (*pkgmodels.Product, bool) {
	badgeNames, err := contactBadgeNames(tenantID, contactID)
	if err != nil || len(badgeNames) == 0 {
		return nil, false
	}

	productQuery := bson.M{
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}
	if bson.IsObjectIdHex(productIDOrPublic) {
		productQuery["_id"] = bson.ObjectIdHex(productIDOrPublic)
	} else {
		productQuery["public_id"] = productIDOrPublic
	}
	var product pkgmodels.Product
	if err := db.GetCollection(pkgmodels.ProductCollection).Find(productQuery).One(&product); err != nil {
		return nil, false
	}

	var offers []pkgmodels.Offer
	_ = db.GetCollection(pkgmodels.OfferCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"granted_badges":        bson.M{"$in": badgeNames},
		"included_products":     product.Id,
		"timestamps.deleted_at": nil,
	}).All(&offers)
	if len(offers) == 0 {
		return nil, false
	}
	return &product, true
}

// RegisterCustomerLibraryDetailRoutes adds the library sub-routes that serve
// the course player: product detail, enrollment state, progress updates,
// quizzes, certificates, and media event collection.
//
// Wired from marketing-service/cmd/main.go alongside the existing
// RegisterCustomerLibraryRoutes — both live on the customer API group which
// is protected by RequireCustomerAuth.
func RegisterCustomerLibraryDetailRoutes(rg *gin.RouterGroup) {
	rg.GET("/library/products/:id", handleGetLibraryProductDetail)
	rg.GET("/library/enrollments", handleGetLibraryEnrollment)
	rg.POST("/library/enrollments/:publicId/progress", handleUpdateLessonProgress)
	rg.GET("/library/quizzes/:slug", handleGetLibraryQuiz)
	rg.POST("/library/quizzes/:quizId/attempt", handleSubmitQuizAttempt)
	rg.GET("/library/certificates", handleGetLibraryCertificates)
	rg.POST("/library/video/events", handleLibraryVideoEvent)
}

// --- Product detail ---

// libraryLessonDTO mirrors CourseLesson for the customer library response,
// adding `available_at` / `is_locked` / `lock_reason` and stripping playable
// fields when the lesson hasn't dripped yet (cheap server-side gate; client
// cannot bypass by hitting the URL directly because video_url is omitted).
type libraryLessonDTO struct {
	Slug             string                 `json:"slug"`
	Title            string                 `json:"title"`
	Order            int                    `json:"order"`
	VideoURL         string                 `json:"video_url,omitempty"`
	MediaPublicId    string                 `json:"media_public_id,omitempty"`
	Duration         string                 `json:"duration,omitempty"`
	DurationSec      int64                  `json:"duration_sec,omitempty"`
	ContentHTML      string                 `json:"content_html,omitempty"`
	IsFree           bool                   `json:"is_free,omitempty"`
	IsDraft          bool                   `json:"is_draft,omitempty"`
	DripDays         int                    `json:"drip_days,omitempty"`
	DripHours        int                    `json:"drip_hours,omitempty"`
	DripMinutes      int                    `json:"drip_minutes,omitempty"`
	VideoMode        pkgmodels.VideoMode    `json:"video_mode,omitempty"`
	AvailableAt      string                 `json:"available_at,omitempty"`
	LiveStartsAt     string                 `json:"live_starts_at,omitempty"`
	LiveEndsAt       string                 `json:"live_ends_at,omitempty"`
	IsLocked         bool                   `json:"is_locked"`
	LockReason       string                 `json:"lock_reason,omitempty"`
}

type libraryModuleDTO struct {
	Slug     string             `json:"slug"`
	Title    string             `json:"title"`
	Order    int                `json:"order"`
	QuizSlug string             `json:"quiz_slug,omitempty"`
	Lessons  []libraryLessonDTO `json:"lessons,omitempty"`
}

// libraryProductDTO maps Product.CourseModules onto the frontend-expected
// `modules` field so ProductView renders without needing a separate mapper.
type libraryProductDTO struct {
	ID             string             `json:"id"`
	PublicID       string             `json:"public_id"`
	Name           string             `json:"name,omitempty"`
	Title          string             `json:"title,omitempty"`
	Description    string             `json:"description,omitempty"`
	ProductType    string             `json:"product_type,omitempty"`
	ThumbnailURL   string             `json:"thumbnail_url,omitempty"`
	Status         string             `json:"status,omitempty"`
	Modules        []libraryModuleDTO `json:"modules,omitempty"`
	TotalLessons   int                `json:"total_lessons,omitempty"`
	TotalDurationS int64              `json:"total_duration_sec,omitempty"`
}

// getProduct loads a product by id, scoped to a tenant. Returns nil on miss.
func getProduct(tenantID, productID bson.ObjectId) *pkgmodels.Product {
	var p pkgmodels.Product
	if err := db.GetCollection(pkgmodels.ProductCollection).Find(bson.M{
		"_id":       productID,
		"tenant_id": tenantID,
	}).One(&p); err != nil {
		return nil
	}
	return &p
}

// lessonContext finds a lesson on a product and computes the two pieces of
// gating context the resolveGate predicates need: whether the immediately-
// preceding lesson (display order) is complete, and whether every prior
// module's quiz (if any) has been passed.
func lessonContext(p *pkgmodels.Product, enrollment pkgmodels.CourseEnrollment, moduleSlug, lessonSlug string) (
	lesson *pkgmodels.CourseLesson,
	priorLessonComplete bool,
	priorQuizPassed bool,
) {
	priorLessonComplete = true
	priorQuizPassed = true
	progress := indexProgress(&enrollment)
	for _, m := range p.CourseModules {
		for _, l := range m.Lessons {
			if m.Slug == moduleSlug && l.Slug == lessonSlug {
				lesson = l
				return
			}
			if pp, ok := progress[progressKey{m.Slug, l.Slug}]; ok && pp.Completed {
				priorLessonComplete = true
			} else {
				priorLessonComplete = false
			}
		}
		if m.QuizSlug != "" {
			passed := false
			for _, pp := range enrollment.GetProgressOrEmpty() {
				if pp.ModuleSlug == m.Slug && pp.QuizPassed != nil && *pp.QuizPassed {
					passed = true
					break
				}
			}
			priorQuizPassed = passed
		}
	}
	return
}

// findEnrollment returns the active enrollment for a contact on a product, if
// one exists. Used to compute drip release windows for the customer DTO.
func findEnrollment(tenantID, contactID, productID bson.ObjectId) *pkgmodels.CourseEnrollment {
	var e pkgmodels.CourseEnrollment
	if err := db.GetCollection(pkgmodels.CourseEnrollmentCollection).Find(bson.M{
		"tenant_id":  tenantID,
		"contact_id": contactID,
		"product_id": productID,
	}).One(&e); err != nil {
		return nil
	}
	return &e
}

// dripAnchor returns the time the drip clock starts ticking for this product.
// "enrollment" (default) uses the contact's EnrolledAt; "fixed_date" uses the
// product's DripAnchorDate so an entire cohort unlocks together.
func dripAnchor(p *pkgmodels.Product, enrollment *pkgmodels.CourseEnrollment) time.Time {
	if p != nil && p.DripAnchor == "fixed_date" && p.DripAnchorDate != nil {
		return *p.DripAnchorDate
	}
	if enrollment != nil {
		return enrollment.EnrolledAt
	}
	return time.Time{}
}

// lessonProgressMap indexes the contact's progress by (module_slug, lesson_slug).
type progressKey struct{ module, lesson string }

func indexProgress(enrollment *pkgmodels.CourseEnrollment) map[progressKey]*pkgmodels.LessonProgress {
	out := map[progressKey]*pkgmodels.LessonProgress{}
	if enrollment == nil {
		return out
	}
	for _, p := range enrollment.Progress {
		out[progressKey{p.ModuleSlug, p.LessonSlug}] = p
	}
	return out
}

// gateState holds the result of resolving every lock predicate for a single
// lesson. Reason is the first predicate that failed (precedence: live → drip
// → sequential → quiz).
type gateState struct {
	locked     bool
	availableAt time.Time
	reason     string
}

// resolveGate computes the lock state for a single lesson. priorLessonComplete
// signals whether the immediately-preceding lesson (display order) has been
// completed. priorModuleQuizPassed is true when no prior module has a quiz, OR
// the most-recent prior module's quiz was passed.
func resolveGate(
	now time.Time,
	p *pkgmodels.Product,
	l *pkgmodels.CourseLesson,
	enrollment *pkgmodels.CourseEnrollment,
	priorLessonComplete bool,
	priorModuleQuizPassed bool,
) gateState {
	g := gateState{}
	// Live event window — applies to everyone, even free lessons.
	if l.LiveStartsAt != nil && now.Before(*l.LiveStartsAt) {
		g.locked = true
		g.availableAt = *l.LiveStartsAt
		g.reason = "live_not_started"
		return g
	}
	if l.LiveEndsAt != nil && now.After(*l.LiveEndsAt) {
		g.locked = true
		g.availableAt = *l.LiveEndsAt
		g.reason = "live_ended"
		return g
	}
	// Free lessons bypass the remaining gates so previews stay viewable.
	if l.IsFree {
		return g
	}
	// Drip window relative to the configured anchor.
	if enrollment != nil {
		anchor := dripAnchor(p, enrollment)
		avail := anchor.Add(l.DripDuration())
		g.availableAt = avail
		if now.Before(avail) {
			g.locked = true
			g.reason = "drip"
			return g
		}
	}
	// Sequential gating: previous lesson must be completed.
	if p != nil && p.SequentialGating && !priorLessonComplete {
		g.locked = true
		g.reason = "sequential"
		return g
	}
	// Quiz pass gate: any prior module with a quiz must have been passed.
	if p != nil && p.RequireQuizPass && !priorModuleQuizPassed {
		g.locked = true
		g.reason = "quiz_required"
		return g
	}
	return g
}

// applyLessonTranslation overrides title/content fields if a matching locale
// (or a "language-only" fallback like "en" for "en-US") is present.
func applyLessonTranslation(l *pkgmodels.CourseLesson, locale string) (title, contentHTML string) {
	title, contentHTML = l.Title, l.ContentHTML
	if locale == "" || len(l.Translations) == 0 {
		return
	}
	pickKey := func(k string) *pkgmodels.LessonTranslation {
		if t, ok := l.Translations[k]; ok && t != nil {
			return t
		}
		return nil
	}
	tr := pickKey(locale)
	if tr == nil && strings.Contains(locale, "-") {
		tr = pickKey(strings.SplitN(locale, "-", 2)[0])
	}
	if tr == nil {
		return
	}
	if tr.Title != "" {
		title = tr.Title
	}
	if tr.ContentHTML != "" {
		contentHTML = tr.ContentHTML
	}
	return
}

// applyProductTranslation overrides course title + description for a locale.
// Same fallback chain as applyLessonTranslation — "es-MX" → "es" → base.
func applyProductTranslation(p *pkgmodels.Product, locale string) (title, description string) {
	title, description = p.Name, p.Description
	if locale == "" || len(p.Translations) == 0 {
		return
	}
	pick := func(k string) *pkgmodels.ProductTranslation {
		if t, ok := p.Translations[k]; ok && t != nil {
			return t
		}
		return nil
	}
	tr := pick(locale)
	if tr == nil && strings.Contains(locale, "-") {
		tr = pick(strings.SplitN(locale, "-", 2)[0])
	}
	if tr == nil {
		return
	}
	if tr.Title != "" {
		title = tr.Title
	}
	if tr.Description != "" {
		description = tr.Description
	}
	return
}

// applyModuleTranslation overrides module title for a locale.
func applyModuleTranslation(m *pkgmodels.CourseModule, locale string) string {
	if locale == "" || len(m.Translations) == 0 {
		return m.Title
	}
	pick := func(k string) *pkgmodels.ModuleTranslation {
		if t, ok := m.Translations[k]; ok && t != nil {
			return t
		}
		return nil
	}
	tr := pick(locale)
	if tr == nil && strings.Contains(locale, "-") {
		tr = pick(strings.SplitN(locale, "-", 2)[0])
	}
	if tr == nil || tr.Title == "" {
		return m.Title
	}
	return tr.Title
}

func toLibraryProductDTO(p *pkgmodels.Product, enrollment *pkgmodels.CourseEnrollment, locale string) libraryProductDTO {
	now := time.Now()
	progress := indexProgress(enrollment)
	mods := make([]libraryModuleDTO, 0, len(p.CourseModules))

	// Track running state for sequential + quiz gates.
	priorLessonComplete := true   // first lesson has nothing before it
	priorModuleQuizPassed := true // before any module has a quiz, this is vacuously true

	for _, m := range p.CourseModules {
		dto := libraryModuleDTO{
			Slug:     m.Slug,
			Title:    applyModuleTranslation(m, locale),
			Order:    m.Order,
			QuizSlug: m.QuizSlug,
		}
		for _, l := range m.Lessons {
			gate := resolveGate(now, p, l, enrollment, priorLessonComplete, priorModuleQuizPassed)
			title, contentHTML := applyLessonTranslation(l, locale)
			ldto := libraryLessonDTO{
				Slug:          l.Slug,
				Title:         title,
				Order:         l.Order,
				VideoURL:      l.VideoURL,
				MediaPublicId: l.MediaPublicId,
				Duration:      l.Duration,
				DurationSec:   l.DurationSec,
				ContentHTML:   contentHTML,
				IsFree:        l.IsFree,
				IsDraft:       l.IsDraft,
				DripDays:      l.DripDays,
				DripHours:     l.DripHours,
				DripMinutes:   l.DripMinutes,
				VideoMode:     l.VideoMode,
			}
			if l.LiveStartsAt != nil {
				ldto.LiveStartsAt = l.LiveStartsAt.Format(time.RFC3339)
			}
			if l.LiveEndsAt != nil {
				ldto.LiveEndsAt = l.LiveEndsAt.Format(time.RFC3339)
			}
			if !gate.availableAt.IsZero() {
				ldto.AvailableAt = gate.availableAt.Format(time.RFC3339)
			}
			if gate.locked {
				ldto.IsLocked = true
				ldto.LockReason = gate.reason
				ldto.VideoURL = ""
				ldto.MediaPublicId = ""
				ldto.ContentHTML = ""
			}
			dto.Lessons = append(dto.Lessons, ldto)

			// Update running state for the NEXT lesson in display order.
			if pp, ok := progress[progressKey{m.Slug, l.Slug}]; ok && pp.Completed {
				priorLessonComplete = true
			} else {
				priorLessonComplete = false
			}
		}
		mods = append(mods, dto)

		// After finishing a module: if it has a quiz, the next module's quiz
		// gate depends on whether this module's quiz was passed.
		if m.QuizSlug != "" {
			passed := false
			for _, p := range enrollment.GetProgressOrEmpty() {
				if p.ModuleSlug == m.Slug && p.QuizPassed != nil && *p.QuizPassed {
					passed = true
					break
				}
			}
			priorModuleQuizPassed = passed
		}
	}
	title, description := applyProductTranslation(p, locale)
	return libraryProductDTO{
		ID:             p.Id.Hex(),
		PublicID:       p.PublicId,
		Name:           title,
		Title:          title,
		Description:    description,
		ProductType:    p.ProductType,
		ThumbnailURL:   p.ThumbnailURL,
		Status:         p.Status,
		Modules:        mods,
		TotalLessons:   p.TotalLessons,
		TotalDurationS: p.TotalDurationSec,
	}
}

func handleGetLibraryProductDetail(c *gin.Context) {
	tenantID, contactID, ok := resolveCustomerContext(c)
	if !ok {
		return
	}
	product, owned := customerOwnsProduct(tenantID, contactID, c.Param("id"))
	if !owned {
		c.JSON(http.StatusNotFound, gin.H{"error": "product not found"})
		return
	}
	enrollment := findEnrollment(tenantID, contactID, product.Id)
	c.JSON(http.StatusOK, toLibraryProductDTO(product, enrollment, resolveRequestLocale(c, tenantID, contactID)))
}

// --- Enrollments ---

func handleGetLibraryEnrollment(c *gin.Context) {
	tenantID, contactID, ok := resolveCustomerContext(c)
	if !ok {
		return
	}
	productKey := c.Query("product_id")
	if productKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "product_id required"})
		return
	}

	productQuery := bson.M{"tenant_id": tenantID, "timestamps.deleted_at": nil}
	if bson.IsObjectIdHex(productKey) {
		productQuery["_id"] = bson.ObjectIdHex(productKey)
	} else {
		productQuery["public_id"] = productKey
	}
	var product pkgmodels.Product
	if err := db.GetCollection(pkgmodels.ProductCollection).Find(productQuery).One(&product); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "product not found"})
		return
	}

	var enrollment pkgmodels.CourseEnrollment
	err := db.GetCollection(pkgmodels.CourseEnrollmentCollection).Find(bson.M{
		"tenant_id":  tenantID,
		"contact_id": contactID,
		"product_id": product.Id,
	}).One(&enrollment)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "enrollment not found"})
		return
	}
	c.JSON(http.StatusOK, enrollment)
}

// --- Lesson progress ---

func handleUpdateLessonProgress(c *gin.Context) {
	tenantID, contactID, ok := resolveCustomerContext(c)
	if !ok {
		return
	}
	publicID := c.Param("publicId")
	if publicID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "enrollment public id required"})
		return
	}
	var req struct {
		LessonSlug      string `json:"lesson_slug" binding:"required"`
		ModuleSlug      string `json:"module_slug"`
		WatchPercent    int    `json:"watch_percent"`
		LastPositionSec int    `json:"last_position_sec"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	col := db.GetCollection(pkgmodels.CourseEnrollmentCollection)
	var enrollment pkgmodels.CourseEnrollment
	if err := col.Find(bson.M{
		"public_id":  publicID,
		"tenant_id":  tenantID,
		"contact_id": contactID,
	}).One(&enrollment); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "enrollment not found"})
		return
	}

	now := time.Now()

	// Resolve the product so we can run the same gate the DTO does. Reject
	// any progress update for a lesson that's currently locked (drip / live /
	// sequential / quiz) so a malicious client can't smash through.
	product := getProduct(tenantID, enrollment.ProductID)
	if product != nil {
		lesson, priorComplete, priorQuizPassed := lessonContext(product, enrollment, req.ModuleSlug, req.LessonSlug)
		if lesson != nil {
			gate := resolveGate(now, product, lesson, &enrollment, priorComplete, priorQuizPassed)
			if gate.locked {
				resp := gin.H{"error": "lesson_locked", "lock_reason": gate.reason}
				if !gate.availableAt.IsZero() {
					resp["available_at"] = gate.availableAt.Format(time.RFC3339)
				}
				c.JSON(http.StatusConflict, resp)
				return
			}
		}
	}

	completed := req.WatchPercent >= 95
	wasCompleted := false
	previousWatchPercent := 0
	found := false
	for i, p := range enrollment.Progress {
		if p.LessonSlug == req.LessonSlug && p.ModuleSlug == req.ModuleSlug {
			found = true
			previousWatchPercent = p.WatchPercent
			wasCompleted = p.Completed
			if req.WatchPercent > p.WatchPercent {
				enrollment.Progress[i].WatchPercent = req.WatchPercent
			}
			if req.LastPositionSec > p.LastPositionSec {
				enrollment.Progress[i].LastPositionSec = req.LastPositionSec
			}
			if completed && !p.Completed {
				enrollment.Progress[i].Completed = true
				enrollment.Progress[i].CompletedAt = &now
			}
			break
		}
	}
	if !found {
		lp := &pkgmodels.LessonProgress{
			LessonSlug:      req.LessonSlug,
			ModuleSlug:      req.ModuleSlug,
			WatchPercent:    req.WatchPercent,
			LastPositionSec: req.LastPositionSec,
			Completed:       completed,
		}
		if completed {
			lp.CompletedAt = &now
		}
		enrollment.Progress = append(enrollment.Progress, lp)
	}
	justLessonCompleted := completed && !wasCompleted

	// Evaluate lesson-level badge rules: any rule whose threshold the contact
	// has just crossed grants its configured badge. Idempotent via OncePerViewer.
	if product != nil {
		lesson, _, _ := lessonContext(product, enrollment, req.ModuleSlug, req.LessonSlug)
		if lesson != nil && len(lesson.BadgeRules) > 0 {
			evaluateLessonBadgeRules(tenantID, contactID, lesson.BadgeRules, previousWatchPercent, req.WatchPercent, justLessonCompleted)
		}
	}

	// Audit: write an immutable LessonCompletion the first time a lesson goes
	// from incomplete → complete. Best-effort — failure here doesn't fail the
	// progress write.
	if justLessonCompleted {
		_ = db.GetCollection(pkgmodels.LessonCompletionCollection).Insert(&pkgmodels.LessonCompletion{
			Id:           bson.NewObjectId(),
			TenantID:     tenantID,
			ContactID:    contactID,
			ProductID:    enrollment.ProductID,
			EnrollmentID: enrollment.Id,
			ModuleSlug:   req.ModuleSlug,
			LessonSlug:   req.LessonSlug,
			WatchPercent: req.WatchPercent,
			CompletedAt:  now,
		})
	}

	overall := computeOverallPercent(tenantID, enrollment.ProductID, enrollment.Progress)
	enrollment.OverallPercent = overall
	justCompleted := false
	if overall >= 100 && enrollment.CompletedAt == nil {
		enrollment.CompletedAt = &now
		enrollment.Status = "completed"
		justCompleted = true
	}

	_ = col.UpdateId(enrollment.Id, bson.M{"$set": bson.M{
		"progress":              enrollment.Progress,
		"overall_percent":       enrollment.OverallPercent,
		"status":                enrollment.Status,
		"completed_at":          enrollment.CompletedAt,
		"timestamps.updated_at": now,
	}})

	if justCompleted {
		// Only issue a certificate if the course (or, by inheritance, the
		// tenant default) wants one. Default is enabled.
		var tenant pkgmodels.Tenant
		_ = db.GetCollection(pkgmodels.TenantCollection).FindId(tenantID).One(&tenant)
		if shouldIssueCertificate(&tenant, product) {
			go issueCertificateAsync(enrollment.Id.Hex(), now)
		}
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok", "overall_percent": enrollment.OverallPercent})
}

// shouldIssueCertificate resolves the per-course override against the tenant
// default. Course-level explicit `false` always disables; explicit `true`
// always enables; nil inherits from the tenant (which itself defaults to true
// when unset).
func shouldIssueCertificate(tenant *pkgmodels.Tenant, product *pkgmodels.Product) bool {
	if product != nil && product.CertificateEnabled != nil {
		return *product.CertificateEnabled
	}
	return tenant.CertificatesEnabledDefault()
}

// evaluateLessonBadgeRules walks the rules attached to a lesson and grants any
// badge whose threshold the contact's watch_percent just crossed. Operators
// supported: ">=", ">", "==" (default >=). Honors OncePerViewer by checking
// whether the contact already has the badge before granting.
func evaluateLessonBadgeRules(
	tenantID, contactID bson.ObjectId,
	rules []*pkgmodels.MediaBadgeRule,
	previous, current int,
	justCompleted bool,
) {
	for _, r := range rules {
		if r == nil || !r.Enabled {
			continue
		}
		shouldFire := false
		switch r.EventName {
		case "complete":
			shouldFire = justCompleted
		case "progress", "":
			cmp := strings.TrimSpace(r.Operator)
			if cmp == "" {
				cmp = ">="
			}
			cross := func(v int) bool {
				switch cmp {
				case ">":
					return v > r.Threshold
				case "==":
					return v == r.Threshold
				default: // ">="
					return v >= r.Threshold
				}
			}
			// Fire only when this update crosses the threshold (was below, now at/above).
			shouldFire = !cross(previous) && cross(current)
		}
		if !shouldFire || r.BadgePublicId == "" {
			continue
		}
		var badge pkgmodels.Badge
		if err := db.GetCollection(pkgmodels.BadgeCollection).Find(bson.M{
			"tenant_id":             tenantID,
			"public_id":             r.BadgePublicId,
			"timestamps.deleted_at": nil,
		}).One(&badge); err != nil {
			continue
		}
		_ = db.GetCollection(pkgmodels.UserCollection).Update(
			bson.M{"_id": contactID},
			bson.M{"$addToSet": bson.M{"badges": badge.Id}},
		)
	}
}

// issueCertificateAsync calls the lms-service /internal/certificates endpoint
// to insert a Certificate for the just-completed enrollment. Idempotent on
// enrollment_id, so retries are safe.
func issueCertificateAsync(enrollmentIDHex string, completedAt time.Time) {
	lmsURL := os.Getenv("LMS_SERVICE_URL")
	if lmsURL == "" {
		lmsURL = "http://lms-service:8083"
	}
	body, _ := json.Marshal(map[string]string{
		"enrollment_id": enrollmentIDHex,
		"completed_at":  completedAt.Format(time.RFC3339),
	})
	req, err := http.NewRequest("POST", lmsURL+"/internal/certificates", bytes.NewReader(body))
	if err != nil {
		log.Printf("issueCertificateAsync: build request: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("issueCertificateAsync: post: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		log.Printf("issueCertificateAsync: lms-service returned %d for enrollment %s", resp.StatusCode, enrollmentIDHex)
	}
}

// computeOverallPercent counts lessons across the product's CourseModules and
// returns the percentage completed. Products without modules return 0.
func computeOverallPercent(tenantID, productID bson.ObjectId, progress []*pkgmodels.LessonProgress) int {
	var product pkgmodels.Product
	if err := db.GetCollection(pkgmodels.ProductCollection).Find(bson.M{
		"_id":       productID,
		"tenant_id": tenantID,
	}).One(&product); err != nil {
		return 0
	}
	total := 0
	for _, mod := range product.CourseModules {
		total += len(mod.Lessons)
	}
	if total == 0 {
		return 0
	}
	completed := 0
	for _, p := range progress {
		if p.Completed {
			completed++
		}
	}
	return (completed * 100) / total
}

// --- Quizzes ---

func handleGetLibraryQuiz(c *gin.Context) {
	tenantID, contactID, ok := resolveCustomerContext(c)
	if !ok {
		return
	}
	slug := c.Param("slug")
	query := bson.M{"tenant_id": tenantID, "timestamps.deleted_at": nil}
	if bson.IsObjectIdHex(slug) {
		query["_id"] = bson.ObjectIdHex(slug)
	} else {
		query["$or"] = []bson.M{{"slug": slug}, {"public_id": slug}}
	}
	var quiz pkgmodels.LMSQuiz
	if err := db.GetCollection(pkgmodels.LMSQuizCollection).Find(query).One(&quiz); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "quiz not found"})
		return
	}
	if !customerEnrolledInProduct(tenantID, contactID, quiz.ProductID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not enrolled"})
		return
	}
	// Strip answer keys before sending to the client.
	locale := resolveRequestLocale(c, tenantID, contactID)
	c.JSON(http.StatusOK, sanitizeQuizForCustomer(&quiz, locale))
}

func customerEnrolledInProduct(tenantID, contactID, productID bson.ObjectId) bool {
	n, _ := db.GetCollection(pkgmodels.CourseEnrollmentCollection).Find(bson.M{
		"tenant_id":  tenantID,
		"contact_id": contactID,
		"product_id": productID,
	}).Count()
	return n > 0
}

// pickQuizTranslation returns the resolved QuizTranslation for the locale,
// falling back to the language-only tag (es-MX → es) before giving up. Nil
// means "use base values"; per-field empty strings still fall back to base.
func pickQuizTranslation(q *pkgmodels.LMSQuiz, locale string) *pkgmodels.QuizTranslation {
	if locale == "" || len(q.Translations) == 0 {
		return nil
	}
	if t, ok := q.Translations[locale]; ok && t != nil {
		return t
	}
	if i := strings.Index(locale, "-"); i > 0 {
		if t, ok := q.Translations[locale[:i]]; ok && t != nil {
			return t
		}
	}
	return nil
}

// translateQuestion applies a per-question override. Options is positional —
// an empty translated entry falls back to the base option at that index so a
// partial translation never erases an option.
func translateQuestion(qu *pkgmodels.LMSQuizQuestion, qt *pkgmodels.QuizTranslation) (title string, options []string) {
	title = qu.Title
	options = append([]string(nil), qu.Options...)
	if qt == nil || len(qt.Questions) == 0 {
		return
	}
	tr, ok := qt.Questions[qu.Slug]
	if !ok || tr == nil {
		return
	}
	if tr.Title != "" {
		title = tr.Title
	}
	if len(tr.Options) > 0 {
		merged := make([]string, len(options))
		copy(merged, options)
		for i, v := range tr.Options {
			if i >= len(merged) {
				merged = append(merged, v)
			} else if v != "" {
				merged[i] = v
			}
		}
		options = merged
	}
	return
}

func sanitizeQuizForCustomer(q *pkgmodels.LMSQuiz, locale string) gin.H {
	qt := pickQuizTranslation(q, locale)
	title := q.Title
	if qt != nil && qt.Title != "" {
		title = qt.Title
	}
	questions := make([]gin.H, 0, len(q.Questions))
	for _, qu := range q.Questions {
		qTitle, qOptions := translateQuestion(qu, qt)
		questions = append(questions, gin.H{
			"slug":    qu.Slug,
			"type":    qu.Type,
			"title":   qTitle,
			"options": qOptions,
			"order":   qu.Order,
		})
	}
	return gin.H{
		"id":             q.Id.Hex(),
		"public_id":      q.PublicId,
		"slug":           q.Slug,
		"title":          title,
		"module_slug":    q.ModuleSlug,
		"pass_threshold": q.PassThreshold,
		"max_attempts":   q.MaxAttempts,
		"questions":      questions,
	}
}

func handleSubmitQuizAttempt(c *gin.Context) {
	tenantID, contactID, ok := resolveCustomerContext(c)
	if !ok {
		return
	}
	quizKey := c.Param("quizId")
	quizQuery := bson.M{"tenant_id": tenantID, "timestamps.deleted_at": nil}
	if bson.IsObjectIdHex(quizKey) {
		quizQuery["_id"] = bson.ObjectIdHex(quizKey)
	} else {
		quizQuery["$or"] = []bson.M{{"slug": quizKey}, {"public_id": quizKey}}
	}
	var quiz pkgmodels.LMSQuiz
	if err := db.GetCollection(pkgmodels.LMSQuizCollection).Find(quizQuery).One(&quiz); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "quiz not found"})
		return
	}
	if !customerEnrolledInProduct(tenantID, contactID, quiz.ProductID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not enrolled"})
		return
	}

	var req struct {
		EnrollmentID string `json:"enrollment_id"`
		Answers      []struct {
			QuestionSlug string `json:"question_slug"`
			AnswerIndex  int    `json:"answer_index"`
			AnswerText   string `json:"answer_text"`
		} `json:"answers"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	correct := 0
	graded := make([]*pkgmodels.QuizAttemptAnswer, 0, len(req.Answers))
	for _, ans := range req.Answers {
		isCorrect := false
		for _, q := range quiz.Questions {
			if q.Slug != ans.QuestionSlug {
				continue
			}
			if q.Type == "text" || q.Type == "short_answer" {
				if strings.EqualFold(strings.TrimSpace(q.CorrectText), strings.TrimSpace(ans.AnswerText)) {
					isCorrect = true
				}
			} else if ans.AnswerIndex == q.CorrectAnswer {
				isCorrect = true
			}
			break
		}
		if isCorrect {
			correct++
		}
		graded = append(graded, &pkgmodels.QuizAttemptAnswer{
			QuestionSlug: ans.QuestionSlug,
			AnswerIndex:  ans.AnswerIndex,
			AnswerText:   ans.AnswerText,
			IsCorrect:    isCorrect,
		})
	}
	total := len(quiz.Questions)
	score := 0
	if total > 0 {
		score = (correct * 100) / total
	}
	threshold := quiz.PassThreshold
	if threshold == 0 {
		threshold = 70
	}
	passed := score >= threshold

	// Count existing attempts for this quiz+contact to compute attempt_number.
	priorCount, _ := db.GetCollection(pkgmodels.QuizAttemptCollection).Find(bson.M{
		"tenant_id":  tenantID,
		"contact_id": contactID,
		"quiz_id":    quiz.Id,
	}).Count()

	attempt := pkgmodels.QuizAttempt{
		Id:            bson.NewObjectId(),
		TenantID:      tenantID,
		ContactID:     contactID,
		QuizID:        quiz.Id,
		Answers:       graded,
		Score:         score,
		Passed:        passed,
		AttemptNumber: priorCount + 1,
		SubmittedAt:   time.Now(),
	}
	if req.EnrollmentID != "" {
		var enroll pkgmodels.CourseEnrollment
		if err := db.GetCollection(pkgmodels.CourseEnrollmentCollection).Find(bson.M{
			"public_id":  req.EnrollmentID,
			"tenant_id":  tenantID,
			"contact_id": contactID,
		}).One(&enroll); err == nil {
			attempt.EnrollmentID = enroll.Id
			// Mark lessons tied to this quiz's module as quiz_passed so the
			// sidebar checkmarks update.
			if passed {
				updated := false
				for i, p := range enroll.Progress {
					if p.ModuleSlug == quiz.ModuleSlug {
						truthy := true
						enroll.Progress[i].QuizPassed = &truthy
						updated = true
					}
				}
				if updated {
					_ = db.GetCollection(pkgmodels.CourseEnrollmentCollection).UpdateId(enroll.Id, bson.M{"$set": bson.M{
						"progress":              enroll.Progress,
						"timestamps.updated_at": time.Now(),
					}})
				}
			}
		}
	}
	_ = db.GetCollection(pkgmodels.QuizAttemptCollection).Insert(attempt)

	c.JSON(http.StatusOK, gin.H{
		"score":          score,
		"passed":         passed,
		"attempt_number": attempt.AttemptNumber,
	})
}

// --- Certificates ---

func handleGetLibraryCertificates(c *gin.Context) {
	tenantID, contactID, ok := resolveCustomerContext(c)
	if !ok {
		return
	}
	query := bson.M{
		"tenant_id":             tenantID,
		"contact_id":            contactID,
		"timestamps.deleted_at": nil,
	}
	if productKey := c.Query("product_id"); productKey != "" {
		productQuery := bson.M{"tenant_id": tenantID}
		if bson.IsObjectIdHex(productKey) {
			productQuery["_id"] = bson.ObjectIdHex(productKey)
		} else {
			productQuery["public_id"] = productKey
		}
		var product pkgmodels.Product
		if err := db.GetCollection(pkgmodels.ProductCollection).Find(productQuery).One(&product); err == nil {
			query["product_id"] = product.Id
		}
	}
	var certs []pkgmodels.Certificate
	_ = db.GetCollection(pkgmodels.CertificateCollection).Find(query).All(&certs)
	c.JSON(http.StatusOK, gin.H{"certificates": certs})
}

// --- Video events ---

func handleLibraryVideoEvent(c *gin.Context) {
	tenantID, contactID, ok := resolveCustomerContext(c)
	if !ok {
		return
	}
	var req struct {
		EventType    string                 `json:"event_type"`
		MediaID      string                 `json:"media_id"`
		CurrentTime  int                    `json:"current_time"`
		Duration     int                    `json:"duration"`
		WatchPercent int                    `json:"watch_percent"`
		Payload      map[string]interface{} `json:"payload"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	doc := bson.M{
		"_id":           bson.NewObjectId(),
		"tenant_id":     tenantID,
		"contact_id":    contactID,
		"event_type":    req.EventType,
		"media_id":      req.MediaID,
		"current_time":  req.CurrentTime,
		"duration":      req.Duration,
		"watch_percent": req.WatchPercent,
		"payload":       req.Payload,
		"created_at":    time.Now(),
	}
	if err := db.GetCollection(pkgmodels.MediaEventCollection).Insert(doc); err != nil {
		log.Printf("library video event insert failed: %v", err)
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
