package routes

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/db"
	"github.com/josephalai/sentanyl/pkg/entitlements"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// RegisterCustomerLibrarySummaryRoutes adds the per-type summary endpoints
// the portal uses to render the library landing page. The actual product
// detail and fulfillment routes (course player, service dashboard, download
// signing) already live alongside their per-type files; these summary
// routes return the row sets that page each section.
func RegisterCustomerLibrarySummaryRoutes(rg *gin.RouterGroup) {
	rg.GET("/library/downloads", handleListLibraryDownloads)
	rg.GET("/library/services", handleListLibraryServices)
	rg.GET("/library/coaching", handleListLibraryCoaching)
	rg.GET("/library/newsletters", handleListLibraryNewsletters)
	rg.GET("/library/grants", handleListLibraryGrants)
}

// handleListLibraryGrants returns EVERY access grant for the contact —
// including expired/suspended/revoked/completed — so the portal can render
// honest lifecycle states (DEL-015) instead of silently hiding products the
// customer used to have.
func handleListLibraryGrants(c *gin.Context) {
	tenantID, contactID, ok := resolveCustomerContext(c)
	if !ok {
		return
	}
	var grants []pkgmodels.AccessGrant
	_ = db.GetCollection(pkgmodels.AccessGrantCollection).Find(bson.M{
		"tenant_id":  tenantID,
		"contact_id": contactID,
	}).Sort("-timestamps.created_at").All(&grants)
	type row struct {
		PublicID      string `json:"public_id"`
		ProductID     string `json:"product_id"`
		ProductPublic string `json:"product_public_id,omitempty"`
		Title         string `json:"title"`
		ProductType   string `json:"product_type,omitempty"`
		ThumbnailURL  string `json:"thumbnail_url,omitempty"`
		Status        string `json:"status"`
		Source        string `json:"source,omitempty"`
		ExpiresAt     string `json:"expires_at,omitempty"`
		CreatedAt     string `json:"created_at,omitempty"`
	}
	out := make([]row, 0, len(grants))
	for _, g := range grants {
		var p pkgmodels.Product
		_ = db.GetCollection(pkgmodels.ProductCollection).FindId(g.ProductID).One(&p)
		r := row{
			PublicID:      g.PublicId,
			ProductID:     g.ProductID.Hex(),
			ProductPublic: p.PublicId,
			Title:         p.Name,
			ProductType:   p.ProductType,
			ThumbnailURL:  p.ThumbnailURL,
			Status:        g.Status,
			Source:        g.Source,
		}
		if g.ExpiresAt != nil {
			r.ExpiresAt = g.ExpiresAt.UTC().Format(time.RFC3339)
		}
		if g.CreatedAt != nil {
			r.CreatedAt = g.CreatedAt.UTC().Format(time.RFC3339)
		}
		out = append(out, r)
	}
	c.JSON(http.StatusOK, gin.H{"grants": out})
}

// handleListLibraryDownloads returns digital-download products the contact
// owns. Ownership uses the same badge-resolution flow as
// handleGetLibraryProducts so a single source of truth governs access.
func handleListLibraryDownloads(c *gin.Context) {
	tenantID, contactID, ok := resolveCustomerContext(c)
	if !ok {
		return
	}
	// DEL-001: same shared authority as the library list — grants ∪
	// transitional badges — so the downloads section can't diverge.
	productIDs := entitlements.EntitledProductIDs(tenantID, contactID)
	if len(productIDs) == 0 {
		c.JSON(http.StatusOK, gin.H{"downloads": []any{}})
		return
	}
	var products []pkgmodels.Product
	_ = db.GetCollection(pkgmodels.ProductCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"_id":                   bson.M{"$in": productIDs},
		"product_type":          pkgmodels.ProductTypeDigitalDownload,
		"timestamps.deleted_at": nil,
	}).All(&products)
	c.JSON(http.StatusOK, gin.H{"downloads": summarizeProducts(products)})
}

// handleListLibraryServices returns the contact's ServiceEnrollment rows
// alongside summarized product metadata so the portal can render a card
// per program with progress.
func handleListLibraryServices(c *gin.Context) {
	tenantID, contactID, ok := resolveCustomerContext(c)
	if !ok {
		return
	}
	var enrollments []pkgmodels.ServiceEnrollment
	_ = db.GetCollection(pkgmodels.ServiceEnrollmentCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"contact_id":            contactID,
		"status":                "active",
		"timestamps.deleted_at": nil,
	}).All(&enrollments)
	type row struct {
		EnrollmentID   string `json:"enrollment_id"`
		PublicID       string `json:"public_id"`
		ProductID      string `json:"product_id"`
		ProductPublic  string `json:"product_public_id"`
		Title          string `json:"title"`
		ThumbnailURL   string `json:"thumbnail_url,omitempty"`
		Status         string `json:"status"`
		InstancesTotal int    `json:"instances_total"`
		InstancesDone  int    `json:"instances_done"`
		EnrolledAt     string `json:"enrolled_at,omitempty"`
	}
	out := make([]row, 0, len(enrollments))
	for _, e := range enrollments {
		var p pkgmodels.Product
		_ = db.GetCollection(pkgmodels.ProductCollection).FindId(e.ProductID).One(&p)
		out = append(out, row{
			EnrollmentID:   e.Id.Hex(),
			PublicID:       e.PublicId,
			ProductID:      e.ProductID.Hex(),
			ProductPublic:  e.ProductPublicId,
			Title:          p.Name,
			ThumbnailURL:   p.ThumbnailURL,
			Status:         e.Status,
			InstancesTotal: e.InstancesTotal,
			InstancesDone:  e.InstancesDone,
			EnrolledAt:     formatTime(e.EnrolledAt),
		})
	}
	c.JSON(http.StatusOK, gin.H{"services": out})
}

// handleListLibraryCoaching returns CoachingEnrollment rows for this contact.
// The data lives in the marketing DB (same Mongo) so we can read it
// directly without round-tripping through coaching-service.
func handleListLibraryCoaching(c *gin.Context) {
	tenantID, contactID, ok := resolveCustomerContext(c)
	if !ok {
		return
	}
	var enrollments []pkgmodels.CoachingEnrollment
	_ = db.GetCollection(pkgmodels.CoachingEnrollmentCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"contact_id":            contactID,
		"status":                "active",
		"timestamps.deleted_at": nil,
	}).All(&enrollments)
	type row struct {
		EnrollmentID   string `json:"enrollment_id"`
		PublicID       string `json:"public_id"`
		ProductID      string `json:"product_id"`
		ProductPublic  string `json:"product_public_id"`
		Title          string `json:"title"`
		ThumbnailURL   string `json:"thumbnail_url,omitempty"`
		Status         string `json:"status"`
		SessionsTotal  int    `json:"sessions_total"`
		SessionsBooked int    `json:"sessions_booked"`
		SessionsDone   int    `json:"sessions_done"`
		EnrolledAt     string `json:"enrolled_at,omitempty"`
	}
	out := make([]row, 0, len(enrollments))
	for _, e := range enrollments {
		var p pkgmodels.Product
		_ = db.GetCollection(pkgmodels.ProductCollection).FindId(e.ProductID).One(&p)
		out = append(out, row{
			EnrollmentID:   e.Id.Hex(),
			PublicID:       e.PublicId,
			ProductID:      e.ProductID.Hex(),
			ProductPublic:  e.ProductPublicId,
			Title:          p.Name,
			ThumbnailURL:   p.ThumbnailURL,
			Status:         e.Status,
			SessionsTotal:  e.SessionsTotal,
			SessionsBooked: e.SessionsBooked,
			SessionsDone:   e.SessionsDone,
			EnrolledAt:     formatTime(e.EnrolledAt),
		})
	}
	c.JSON(http.StatusOK, gin.H{"coaching": out})
}

// handleListLibraryNewsletters returns active newsletter subscriptions
// for the contact. The webhook upgrades these on purchase; this endpoint
// is the read side that lets the portal show "you're subscribed to X".
func handleListLibraryNewsletters(c *gin.Context) {
	tenantID, contactID, ok := resolveCustomerContext(c)
	if !ok {
		return
	}
	var subs []pkgmodels.NewsletterSubscription
	_ = db.GetCollection(pkgmodels.NewsletterSubscriptionCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"contact_id":            contactID,
		"status":                "active",
		"timestamps.deleted_at": nil,
	}).All(&subs)
	type row struct {
		SubscriptionID string `json:"subscription_id"`
		PublicID       string `json:"public_id"`
		ProductID      string `json:"product_id"`
		Title          string `json:"title"`
		Tagline        string `json:"tagline,omitempty"`
		Status         string `json:"status"`
	}
	out := make([]row, 0, len(subs))
	for _, s := range subs {
		var p pkgmodels.Product
		_ = db.GetCollection(pkgmodels.ProductCollection).FindId(s.ProductID).One(&p)
		title := p.Name
		tagline := ""
		if p.Newsletter != nil {
			tagline = p.Newsletter.Tagline
		}
		out = append(out, row{
			SubscriptionID: s.Id.Hex(),
			PublicID:       s.PublicId,
			ProductID:      s.ProductID.Hex(),
			Title:          title,
			Tagline:        tagline,
			Status:         s.Status,
		})
	}
	c.JSON(http.StatusOK, gin.H{"newsletters": out})
}

// uniqueProductIDs reduces a list of offers to the set of included
// product ids. Order is undefined; the resulting slice is suitable as a
// `$in` filter argument.
func uniqueProductIDs(offers []pkgmodels.Offer) []bson.ObjectId {
	seen := map[bson.ObjectId]struct{}{}
	for _, o := range offers {
		for _, pid := range o.IncludedProducts {
			seen[pid] = struct{}{}
		}
	}
	out := make([]bson.ObjectId, 0, len(seen))
	for pid := range seen {
		out = append(out, pid)
	}
	return out
}

type productSummary struct {
	ID           string `json:"id"`
	PublicID     string `json:"public_id"`
	Name         string `json:"name"`
	ThumbnailURL string `json:"thumbnail_url,omitempty"`
	ProductType  string `json:"product_type,omitempty"`
}

func summarizeProducts(products []pkgmodels.Product) []productSummary {
	out := make([]productSummary, 0, len(products))
	for _, p := range products {
		out = append(out, productSummary{
			ID:           p.Id.Hex(),
			PublicID:     p.PublicId,
			Name:         p.Name,
			ThumbnailURL: p.ThumbnailURL,
			ProductType:  p.ProductType,
		})
	}
	return out
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}
