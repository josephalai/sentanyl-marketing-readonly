package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/marketing-service/internal/channel"
	"github.com/josephalai/sentanyl/marketing-service/routes"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/publicchannel"
)

// RegisterPublicChannelRoutes mounts the stable /api/public/* integration
// surface used by coded (tenant-hosted) websites and, over time, by builder
// pages. Every route resolves the tenant via the shared publicchannel
// resolver (verified tenant domain / channel public key / legacy site
// fallback) and returns whitelist DTOs only. Unauthenticated by design —
// these are the tenant-facing public primitives.
//
// The legacy builder routes (/api/marketing/site/form/submit,
// /api/marketing/site/checkout/start, /api/marketing/newsletters/subscribe)
// remain untouched; these wrappers reuse the same internals.
func RegisterPublicChannelRoutes(public *gin.RouterGroup) {
	public.GET("/channel", handlePublicChannelInfo)
	public.GET("/products", handlePublicChannelProducts)
	public.GET("/products/:id", handlePublicChannelProduct)
	public.GET("/offers", handlePublicChannelOffers)
	public.GET("/offers/:id", handlePublicChannelOffer)
	public.GET("/forms/:formId", handlePublicChannelFormGet)
	public.POST("/forms/:formId", handlePublicChannelFormSubmit)
	public.POST("/checkout/:offerId", handlePublicChannelCheckout)
	public.POST("/newsletters/:productId/subscribe", handlePublicChannelNewsletterSubscribe)
	public.GET("/quizzes/:quizId", handlePublicQuizGet)
	public.POST("/quizzes/:quizId/submit", handlePublicQuizSubmit)
}

// resolvePublicChannelRequest resolves + enforces origin, writing the error
// response itself. Returns nil when the request was already answered.
func resolvePublicChannelRequest(c *gin.Context, explicitDomain string) *publicchannel.PublicRequestContext {
	pubCtx, err := publicchannel.ResolvePublicRequestWithDomain(c, explicitDomain)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "unknown domain or public key"})
		return nil
	}
	if err := publicchannel.EnforceOrigin(pubCtx); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return nil
	}
	return pubCtx
}

func handlePublicChannelInfo(c *gin.Context) {
	pubCtx := resolvePublicChannelRequest(c, "")
	if pubCtx == nil {
		return
	}
	c.JSON(http.StatusOK, channel.ToPublicChannel(pubCtx.Channel, pubCtx.TenantID.Hex(), pubCtx.Domain))
}

func handlePublicChannelProducts(c *gin.Context) {
	pubCtx := resolvePublicChannelRequest(c, "")
	if pubCtx == nil {
		return
	}
	products, err := channel.ListPublicProducts(pubCtx.TenantID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list products"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"products": products})
}

func handlePublicChannelProduct(c *gin.Context) {
	pubCtx := resolvePublicChannelRequest(c, "")
	if pubCtx == nil {
		return
	}
	p, err := channel.GetPublicProduct(pubCtx.TenantID, c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "product not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"product": p})
}

func handlePublicChannelOffers(c *gin.Context) {
	pubCtx := resolvePublicChannelRequest(c, "")
	if pubCtx == nil {
		return
	}
	offers, err := channel.ListPublicOffers(pubCtx.TenantID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list offers"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"offers": offers})
}

func handlePublicChannelOffer(c *gin.Context) {
	pubCtx := resolvePublicChannelRequest(c, "")
	if pubCtx == nil {
		return
	}
	o, err := channel.GetPublicOffer(pubCtx.TenantID, c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "offer not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"offer": o})
}

// handlePublicChannelFormSubmit is POST /api/public/forms/:formId. Accepts
// the same JSON / form-encoded bodies as the legacy submit route; the form
// public_id comes from the path instead of the body.
func handlePublicChannelFormSubmit(c *gin.Context) {
	var req publicFormRequest
	contentType := c.GetHeader("Content-Type")
	isJSON := strings.Contains(contentType, "application/json")
	if isJSON {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
			return
		}
	} else {
		if err := c.ShouldBind(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid form data"})
			return
		}
		if req.Fields == nil {
			req.Fields = map[string]string{}
		}
		for k, vs := range c.Request.PostForm {
			if len(vs) == 0 {
				continue
			}
			switch k {
			case "domain", "name", "email", "phone", "message", "form_id", "next_url", "public_key":
				continue
			}
			// Repeated keys (multiselect checkboxes) join comma-separated.
			req.Fields[k] = strings.Join(vs, ",")
		}
	}

	pubCtx := resolvePublicChannelRequest(c, req.Domain)
	if pubCtx == nil {
		return
	}
	req.Domain = pubCtx.Domain
	runFormSubmission(c, pubCtx.TenantID, c.Param("formId"), &req, isJSON, "coded_embed")
}

// handlePublicChannelFormGet is GET /api/public/forms/:formId — a whitelisted
// form definition (name + renderable fields only, never the on_submit chain
// or tenant internals) so coded sites can render forms dynamically.
func handlePublicChannelFormGet(c *gin.Context) {
	pubCtx := resolvePublicChannelRequest(c, "")
	if pubCtx == nil {
		return
	}
	var form pkgmodels.PageForm
	if err := db.GetCollection(pkgmodels.PageFormCollection).Find(bson.M{
		"public_id":             c.Param("formId"),
		"tenant_id":             pubCtx.TenantID,
		"timestamps.deleted_at": nil,
	}).One(&form); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "form not found"})
		return
	}
	type publicField struct {
		FieldName   string   `json:"field_name"`
		FieldType   string   `json:"field_type"`
		Required    bool     `json:"required,omitempty"`
		Options     []string `json:"options,omitempty"`
		Placeholder string   `json:"placeholder,omitempty"`
	}
	fields := make([]publicField, 0, len(form.Fields))
	for _, f := range form.Fields {
		if f == nil || f.FieldName == "" {
			continue
		}
		fields = append(fields, publicField{
			FieldName:   f.FieldName,
			FieldType:   f.FieldType,
			Required:    f.Required,
			Options:     f.Options,
			Placeholder: f.Placeholder,
		})
	}
	c.JSON(http.StatusOK, gin.H{"form": gin.H{
		"public_id": form.PublicId,
		"name":      form.Name,
		"form_type": form.FormType,
		"fields":    fields,
	}})
}

// publicChannelCheckoutRequest is the body for POST /api/public/checkout/:offerId.
type publicChannelCheckoutRequest struct {
	Domain         string `json:"domain"`
	Email          string `json:"email"`
	SuccessURL     string `json:"success_url"`
	CancelURL      string `json:"cancel_url"`
	VideoSessionID string `json:"video_session_id"`
}

// handlePublicChannelCheckout is POST /api/public/checkout/:offerId — the
// coded-website checkout start. The offer is addressed by public_id (hex
// also accepted); tenant scope comes from the resolved domain/key, so no
// published builder Site is required.
func handlePublicChannelCheckout(c *gin.Context) {
	var req publicChannelCheckoutRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	pubCtx := resolvePublicChannelRequest(c, req.Domain)
	if pubCtx == nil {
		return
	}
	offer, err := channel.FindOfferForTenant(pubCtx.TenantID, c.Param("offerId"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "offer not found"})
		return
	}
	startCheckoutSession(c, pubCtx, offer, req.Email, req.SuccessURL, req.CancelURL, req.VideoSessionID)
}

// publicChannelSubscribeRequest is the body for
// POST /api/public/newsletters/:productId/subscribe.
type publicChannelSubscribeRequest struct {
	Domain string `json:"domain"`
	Email  string `json:"email"`
	TierID string `json:"tier_id"`
	Source string `json:"source"`
}

func handlePublicChannelNewsletterSubscribe(c *gin.Context) {
	var req publicChannelSubscribeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "email required"})
		return
	}
	pubCtx := resolvePublicChannelRequest(c, req.Domain)
	if pubCtx == nil {
		return
	}
	source := req.Source
	if source == "" {
		source = "coded_website"
	}
	routes.RunPublicNewsletterSubscribe(c, pubCtx.TenantID, pubCtx.Domain, c.Param("productId"), email, req.TierID, source)
}
