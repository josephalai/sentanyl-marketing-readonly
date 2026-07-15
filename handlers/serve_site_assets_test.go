package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestPublicSDKVersionedAssets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterPublicSiteAssetRoutes(r)

	tests := []struct {
		path      string
		immutable bool
		marker    string
	}{
		{path: "/static/sentanyl.js", marker: "var SDK_VERSION = '2.0.0'"},
		{path: "/static/sentanyl-v1.js", immutable: true, marker: "var SDK_VERSION = '1.0.0'"},
		{path: "/static/sentanyl-v2.js", immutable: true, marker: "var SDK_VERSION = '2.0.0'"},
		{path: "/static/sentanyl-video.js", marker: "window.SentanylVideoVersion = SDK_VERSION"},
		{path: "/static/sentanyl-video-v1.js", immutable: true, marker: "window.SentanylVideoVersion = SDK_VERSION"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			res := httptest.NewRecorder()
			r.ServeHTTP(res, req)
			if res.Code != http.StatusOK {
				t.Fatalf("status = %d", res.Code)
			}
			if !strings.Contains(res.Body.String(), tt.marker) {
				t.Fatalf("asset missing version marker %q", tt.marker)
			}
			cache := res.Header().Get("Cache-Control")
			if tt.immutable != strings.Contains(cache, "immutable") {
				t.Fatalf("Cache-Control = %q, immutable=%v", cache, tt.immutable)
			}
			if got := res.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("X-Content-Type-Options = %q", got)
			}
		})
	}
}

func TestSentanylSDKDocumentsCallbackEvents(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterPublicSiteAssetRoutes(r)
	req := httptest.NewRequest(http.MethodGet, "/static/sentanyl-v2.js", nil)
	res := httptest.NewRecorder()
	r.ServeHTTP(res, req)
	body := res.Body.String()
	for _, event := range []string{
		"sentanyl:form:success", "sentanyl:form:error",
		"sentanyl:checkout:redirect", "sentanyl:checkout:error",
		"sentanyl:newsletter:success", "sentanyl:newsletter:error",
		"sentanyl:coaching:booked", "sentanyl:coaching:error",
		"sentanyl:quiz:submitted", "sentanyl:quiz:error",
	} {
		if !strings.Contains(body, event) {
			t.Errorf("SDK contract missing event %q", event)
		}
	}
	for _, marker := range []string{"/api/v1/public/context", "X-Sentanyl-Channel-Context", "/api/v1/public/forms/", "/api/v1/public/checkout/", "/api/v1/public/coaching/", "/api/v1/public/video/events"} {
		if !strings.Contains(body, marker) {
			t.Errorf("SDK v2 context contract missing %q", marker)
		}
	}
}
