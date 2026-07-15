package handlers

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/plans"
	"github.com/josephalai/sentanyl/pkg/utils"
)

// Public quiz surface for external-website embeds (data-sentanyl-quiz).
// GET returns a whitelist DTO with the answers stripped; POST grades the
// submission server-side and (when an email is supplied) captures the taker
// as a contact — a quiz doubles as a lead magnet on coded sites.

type publicQuizQuestion struct {
	Slug    string   `json:"slug"`
	Type    string   `json:"type"`
	Title   string   `json:"title"`
	Options []string `json:"options,omitempty"`
	Order   int      `json:"order"`
}

type publicQuizDTO struct {
	PublicId      string               `json:"public_id"`
	Title         string               `json:"title"`
	PassThreshold int                  `json:"pass_threshold"`
	Questions     []publicQuizQuestion `json:"questions"`
}

func loadPublicQuiz(tenantID bson.ObjectId, quizPublicID string) (*pkgmodels.LMSQuiz, bool) {
	var quiz pkgmodels.LMSQuiz
	err := db.GetCollection(pkgmodels.LMSQuizCollection).Find(bson.M{
		"public_id":             quizPublicID,
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).One(&quiz)
	return &quiz, err == nil
}

// handlePublicQuizGet is GET /api/public/quizzes/:quizId — the quiz with
// correct answers stripped.
func handlePublicQuizGet(c *gin.Context) {
	pubCtx := resolvePublicChannelRequest(c, "")
	if pubCtx == nil {
		return
	}
	quiz, ok := loadPublicQuiz(pubCtx.TenantID, c.Param("quizId"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "quiz not found"})
		return
	}
	out := publicQuizDTO{
		PublicId:      quiz.PublicId,
		Title:         quiz.Title,
		PassThreshold: quiz.PassThreshold,
		Questions:     make([]publicQuizQuestion, 0, len(quiz.Questions)),
	}
	for _, q := range quiz.Questions {
		if q == nil {
			continue
		}
		out.Questions = append(out.Questions, publicQuizQuestion{
			Slug: q.Slug, Type: q.Type, Title: q.Title, Options: q.Options, Order: q.Order,
		})
	}
	c.JSON(http.StatusOK, gin.H{"quiz": out})
}

// handlePublicQuizSubmit is POST /api/public/quizzes/:quizId/submit. Grades
// server-side; per-question correctness is returned but never the answer key.
func handlePublicQuizSubmit(c *gin.Context) {
	pubCtx := resolvePublicChannelRequest(c, "")
	if pubCtx == nil {
		return
	}
	quiz, ok := loadPublicQuiz(pubCtx.TenantID, c.Param("quizId"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "quiz not found"})
		return
	}

	var req struct {
		Email   string `json:"email"`
		Name    string `json:"name"`
		Answers []struct {
			QuestionSlug string `json:"question_slug"`
			AnswerIndex  *int   `json:"answer_index"`
			AnswerText   string `json:"answer_text"`
		} `json:"answers"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	answered := map[string]struct {
		index *int
		text  string
	}{}
	for _, a := range req.Answers {
		answered[a.QuestionSlug] = struct {
			index *int
			text  string
		}{a.AnswerIndex, a.AnswerText}
	}

	attempt := pkgmodels.QuizAttempt{
		Id:          bson.NewObjectId(),
		TenantID:    pubCtx.TenantID,
		QuizID:      quiz.Id,
		SubmittedAt: time.Now(),
	}
	correct, total := 0, 0
	results := map[string]bool{}
	for _, q := range quiz.Questions {
		if q == nil {
			continue
		}
		total++
		ans, has := answered[q.Slug]
		isCorrect := false
		aa := &pkgmodels.QuizAttemptAnswer{QuestionSlug: q.Slug}
		if has {
			switch q.Type {
			case "text":
				aa.AnswerText = ans.text
				isCorrect = q.CorrectText != "" &&
					strings.EqualFold(strings.TrimSpace(ans.text), strings.TrimSpace(q.CorrectText))
			default: // multiple choice
				if ans.index != nil {
					aa.AnswerIndex = *ans.index
					isCorrect = *ans.index == q.CorrectAnswer
				}
			}
		}
		aa.IsCorrect = isCorrect
		if isCorrect {
			correct++
		}
		results[q.Slug] = isCorrect
		attempt.Answers = append(attempt.Answers, aa)
	}
	score := 0
	if total > 0 {
		score = correct * 100 / total
	}
	attempt.Score = score
	attempt.Passed = quiz.PassThreshold == 0 || score >= quiz.PassThreshold

	// Lead capture: an email turns the anonymous taker into a contact.
	var contactPublicID string
	if email := strings.ToLower(strings.TrimSpace(req.Email)); email != "" {
		if contact := upsertQuizContact(pubCtx.TenantID, email, req.Name); contact != nil {
			attempt.ContactID = contact.Id
			contactPublicID = contact.PublicId
		}
	}

	if err := db.GetCollection(pkgmodels.QuizAttemptCollection).Insert(attempt); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to record attempt"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"score":             score,
		"passed":            attempt.Passed,
		"correct":           correct,
		"total":             total,
		"pass_threshold":    quiz.PassThreshold,
		"results":           results,
		"contact_public_id": contactPublicID,
	})
}

// upsertQuizContact mirrors the forms-executor contact upsert (dedupe by
// email+tenant, plan-limit hold on insert). Best-effort: a nil return only
// skips lead capture, never the attempt.
func upsertQuizContact(tenantID bson.ObjectId, email, name string) *pkgmodels.User {
	col := db.GetCollection(pkgmodels.UserCollection)
	var existing pkgmodels.User
	if err := col.Find(bson.M{
		"email":     pkgmodels.EmailAddress(email),
		"tenant_id": tenantID,
	}).One(&existing); err == nil {
		return &existing
	}
	now := time.Now()
	contact := pkgmodels.User{
		Id:       bson.NewObjectId(),
		PublicId: utils.GeneratePublicId(),
		TenantID: tenantID,
		Email:    pkgmodels.EmailAddress(email),
	}
	if parts := strings.Fields(name); len(parts) > 0 {
		contact.Name.First = parts[0]
		if len(parts) > 1 {
			contact.Name.Last = strings.Join(parts[1:], " ")
		}
	}
	contact.Subscribed = true
	contact.SoftDeletes.CreatedAt = &now
	plans.ApplyHold(&contact)
	if err := col.Insert(contact); err != nil {
		return nil
	}
	plans.Invalidate(tenantID)
	return &contact
}
