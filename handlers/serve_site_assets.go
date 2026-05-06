package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/josephalai/sentanyl/marketing-service/internal/site/assets"
)

// RegisterPublicSiteAssetRoutes wires the public-edge static assets used by
// published pages. Caddyfile must forward /static/* to marketing-service so
// the asset is reachable from any tenant host. Caller has NOT applied auth —
// these routes are deliberately public.
//
// Phase 11A Step 1 introduced sentanyl-video.js as the activation layer for
// the Wistia-style video experience: augments any <video data-sentanyl> on
// a published page with chapters, mid-video turnstile lead-capture,
// end-of-video CTAs, and watch-event ingestion via /api/video/events.
func RegisterPublicSiteAssetRoutes(r *gin.Engine) {
	r.GET("/static/sentanyl-video.js", handleSentanylVideoJS)
}

func handleSentanylVideoJS(c *gin.Context) {
	c.Header("Content-Type", "application/javascript; charset=utf-8")
	c.Header("Cache-Control", "public, max-age=86400")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Status(http.StatusOK)
	_, _ = c.Writer.Write(assets.SentanylVideoJS())
}
