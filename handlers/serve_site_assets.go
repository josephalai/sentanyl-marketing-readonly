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
	r.GET("/static/sentanyl-video-v1.js", handleSentanylVideoV1JS)
	r.GET("/static/sentanyl.js", handleSentanylJS)
	r.GET("/static/sentanyl-v1.js", handleSentanylV1JS)
}

func handleSentanylVideoJS(c *gin.Context) {
	serveEmbeddedJS(c, assets.SentanylVideoJS(), false)
}

func handleSentanylVideoV1JS(c *gin.Context) {
	serveEmbeddedJS(c, assets.SentanylVideoJS(), true)
}

// handleSentanylJS serves the frontend-channel browser SDK used by coded
// (tenant-hosted) websites: Sentanyl.init/mountAll + data-sentanyl-*
// declarative embeds over the /api/public/* contract.
func handleSentanylJS(c *gin.Context) {
	serveEmbeddedJS(c, assets.SentanylJS(), false)
}

func handleSentanylV1JS(c *gin.Context) {
	serveEmbeddedJS(c, assets.SentanylJS(), true)
}

func serveEmbeddedJS(c *gin.Context, body []byte, immutable bool) {
	c.Header("Content-Type", "application/javascript; charset=utf-8")
	if immutable {
		c.Header("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		c.Header("Cache-Control", "public, max-age=86400")
	}
	c.Header("X-Content-Type-Options", "nosniff")
	c.Status(http.StatusOK)
	_, _ = c.Writer.Write(body)
}
