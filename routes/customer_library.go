package routes

import (
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

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

// libraryProductDTO maps Product.CourseModules onto the frontend-expected
// `modules` field so ProductView renders without needing a separate mapper.
type libraryProductDTO struct {
	ID              string                     `json:"id"`
	PublicID        string                     `json:"public_id"`
	Name            string                     `json:"name,omitempty"`
	Title           string                     `json:"title,omitempty"`
	Description     string                     `json:"description,omitempty"`
	ProductType     string                     `json:"product_type,omitempty"`
	ThumbnailURL    string                     `json:"thumbnail_url,omitempty"`
	Status          string                     `json:"status,omitempty"`
	Modules         []*pkgmodels.CourseModule  `json:"modules,omitempty"`
	TotalLessons    int                        `json:"total_lessons,omitempty"`
	TotalDurationS  int64                      `json:"total_duration_sec,omitempty"`
}

func toLibraryProductDTO(p *pkgmodels.Product) libraryProductDTO {
	return libraryProductDTO{
		ID:             p.Id.Hex(),
		PublicID:       p.PublicId,
		Name:           p.Name,
		Title:          p.Name,
		Description:    p.Description,
		ProductType:    p.ProductType,
		ThumbnailURL:   p.ThumbnailURL,
		Status:         p.Status,
		Modules:        p.CourseModules,
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
	c.JSON(http.StatusOK, toLibraryProductDTO(product))
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
	completed := req.WatchPercent >= 95
	found := false
	for i, p := range enrollment.Progress {
		if p.LessonSlug == req.LessonSlug && p.ModuleSlug == req.ModuleSlug {
			found = true
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

	overall := computeOverallPercent(tenantID, enrollment.ProductID, enrollment.Progress)
	enrollment.OverallPercent = overall
	if overall >= 100 && enrollment.CompletedAt == nil {
		enrollment.CompletedAt = &now
		enrollment.Status = "completed"
	}

	_ = col.UpdateId(enrollment.Id, bson.M{"$set": bson.M{
		"progress":              enrollment.Progress,
		"overall_percent":       enrollment.OverallPercent,
		"status":                enrollment.Status,
		"completed_at":          enrollment.CompletedAt,
		"timestamps.updated_at": now,
	}})

	c.JSON(http.StatusOK, gin.H{"status": "ok", "overall_percent": enrollment.OverallPercent})
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
	c.JSON(http.StatusOK, sanitizeQuizForCustomer(&quiz))
}

func customerEnrolledInProduct(tenantID, contactID, productID bson.ObjectId) bool {
	n, _ := db.GetCollection(pkgmodels.CourseEnrollmentCollection).Find(bson.M{
		"tenant_id":  tenantID,
		"contact_id": contactID,
		"product_id": productID,
	}).Count()
	return n > 0
}

func sanitizeQuizForCustomer(q *pkgmodels.LMSQuiz) gin.H {
	questions := make([]gin.H, 0, len(q.Questions))
	for _, qu := range q.Questions {
		questions = append(questions, gin.H{
			"slug":    qu.Slug,
			"type":    qu.Type,
			"title":   qu.Title,
			"options": qu.Options,
			"order":   qu.Order,
		})
	}
	return gin.H{
		"id":             q.Id.Hex(),
		"public_id":      q.PublicId,
		"slug":           q.Slug,
		"title":          q.Title,
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
