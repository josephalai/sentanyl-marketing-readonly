package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/marketing-service/internal/site"
	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// RegisterSiteThemeRoutes wires theme and starter kit endpoints.
func RegisterSiteThemeRoutes(tenantAPI *gin.RouterGroup) {
	tenantAPI.GET("/site-themes", handleListSiteThemes)
	tenantAPI.GET("/site-themes/:themeId/starter-kits", handleListStarterKits)
}

// ─── Seeded Themes ────────────────────────────────────────────────────────────

type ThemeTokens struct {
	FontHeading  string `json:"font_heading"`
	FontBody     string `json:"font_body"`
	ColorPrimary string `json:"color_primary"`
	ColorSecondary string `json:"color_secondary"`
	ColorAccent  string `json:"color_accent"`
	ColorBg      string `json:"color_bg"`
	ColorText    string `json:"color_text"`
	BorderRadius string `json:"border_radius"`
	ButtonStyle  string `json:"button_style"` // pill | square | rounded
}

type SiteTheme struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Tokens      ThemeTokens `json:"tokens"`
}

type StarterKit struct {
	ID          string         `json:"id"`
	ThemeID     string         `json:"theme_id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	PuckDocument map[string]any `json:"puck_document"`
}

var seededThemes = []SiteTheme{
	{
		ID: "modern", Name: "Modern", Description: "Clean, minimal design with bold typography",
		Tokens: ThemeTokens{
			FontHeading: "Inter, system-ui, sans-serif", FontBody: "Inter, system-ui, sans-serif",
			ColorPrimary: "#6366f1", ColorSecondary: "#8b5cf6", ColorAccent: "#ec4899",
			ColorBg: "#ffffff", ColorText: "#111827",
			BorderRadius: "8px", ButtonStyle: "rounded",
		},
	},
	{
		ID: "classic", Name: "Classic", Description: "Professional layout with traditional proportions",
		Tokens: ThemeTokens{
			FontHeading: "Georgia, serif", FontBody: "Trebuchet MS, sans-serif",
			ColorPrimary: "#1d4ed8", ColorSecondary: "#1e40af", ColorAccent: "#dc2626",
			ColorBg: "#f9fafb", ColorText: "#1f2937",
			BorderRadius: "4px", ButtonStyle: "square",
		},
	},
	{
		ID: "minimal", Name: "Minimal", Description: "Ultra-clean whitespace-focused design",
		Tokens: ThemeTokens{
			FontHeading: "DM Sans, sans-serif", FontBody: "DM Sans, sans-serif",
			ColorPrimary: "#18181b", ColorSecondary: "#3f3f46", ColorAccent: "#f97316",
			ColorBg: "#ffffff", ColorText: "#18181b",
			BorderRadius: "2px", ButtonStyle: "pill",
		},
	},
	{
		ID: "vibrant", Name: "Vibrant", Description: "Bold colors and energetic feel for high-conversion pages",
		Tokens: ThemeTokens{
			FontHeading: "Poppins, sans-serif", FontBody: "Poppins, sans-serif",
			ColorPrimary: "#f59e0b", ColorSecondary: "#ef4444", ColorAccent: "#10b981",
			ColorBg: "#fffbeb", ColorText: "#1c1917",
			BorderRadius: "12px", ButtonStyle: "pill",
		},
	},
	{
		ID: "dark", Name: "Dark", Description: "Dark-mode aesthetic with high contrast accents",
		Tokens: ThemeTokens{
			FontHeading: "Space Grotesk, sans-serif", FontBody: "Space Grotesk, sans-serif",
			ColorPrimary: "#a78bfa", ColorSecondary: "#7c3aed", ColorAccent: "#34d399",
			ColorBg: "#0f172a", ColorText: "#f1f5f9",
			BorderRadius: "6px", ButtonStyle: "rounded",
		},
	},
}

// Minimal starter kit Puck documents — a hero section + CTA block.
func heroStarterDocument() map[string]any {
	return map[string]any{
		"content": []map[string]any{
			{
				"type": "Hero",
				"props": map[string]any{
					"id":       "hero-1",
					"headline": "Your Powerful Headline Here",
					"subheadline": "A compelling subheadline that explains your value proposition in one sentence.",
					"ctaText": "Get Started",
					"ctaUrl":  "#",
				},
			},
			{
				"type": "TextBlock",
				"props": map[string]any{
					"id":   "text-1",
					"text": "Add your content here. Describe your offer, product, or service clearly.",
				},
			},
		},
		"root": map[string]any{"props": map[string]any{}},
	}
}

func salesStarterDocument() map[string]any {
	return map[string]any{
		"content": []map[string]any{
			{"type": "Hero", "props": map[string]any{"id": "hero-1", "headline": "Limited Time Offer", "subheadline": "Don't miss out on this exclusive deal.", "ctaText": "Claim Now", "ctaUrl": "#"}},
			{"type": "FeatureGrid", "props": map[string]any{"id": "features-1", "heading": "What You Get", "features": []map[string]any{{"title": "Feature 1", "description": "Description"}, {"title": "Feature 2", "description": "Description"}, {"title": "Feature 3", "description": "Description"}}}},
			{"type": "CTABanner", "props": map[string]any{"id": "cta-1", "headline": "Ready to get started?", "buttonText": "Join Now", "buttonUrl": "#"}},
		},
		"root": map[string]any{"props": map[string]any{}},
	}
}

var seededStarterKits = []StarterKit{
	{ID: "hero-blank", ThemeID: "modern", Name: "Hero + Content", Description: "Simple hero section with text block", PuckDocument: heroStarterDocument()},
	{ID: "sales-page", ThemeID: "modern", Name: "Sales Page", Description: "Hero, features grid, and CTA banner", PuckDocument: salesStarterDocument()},
	{ID: "classic-hero", ThemeID: "classic", Name: "Classic Hero", Description: "Traditional hero with headline and CTA", PuckDocument: heroStarterDocument()},
	{ID: "classic-sales", ThemeID: "classic", Name: "Classic Sales", Description: "Full sales page layout", PuckDocument: salesStarterDocument()},
	{ID: "minimal-hero", ThemeID: "minimal", Name: "Minimal Hero", Description: "Clean hero section", PuckDocument: heroStarterDocument()},
	{ID: "minimal-sales", ThemeID: "minimal", Name: "Minimal Sales", Description: "Minimal sales page", PuckDocument: salesStarterDocument()},
	{ID: "vibrant-hero", ThemeID: "vibrant", Name: "Vibrant Hero", Description: "Bold hero section", PuckDocument: heroStarterDocument()},
	{ID: "vibrant-sales", ThemeID: "vibrant", Name: "Vibrant Sales", Description: "High-energy sales page", PuckDocument: salesStarterDocument()},
	{ID: "dark-hero", ThemeID: "dark", Name: "Dark Hero", Description: "Dark mode hero", PuckDocument: heroStarterDocument()},
	{ID: "dark-sales", ThemeID: "dark", Name: "Dark Sales Page", Description: "Dark mode sales page", PuckDocument: salesStarterDocument()},
}

func handleListSiteThemes(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	c.JSON(http.StatusOK, seededThemes)
}

func handleListStarterKits(c *gin.Context) {
	tenantID := auth.GetTenantObjectID(c)
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	themeID := c.Param("themeId")
	var kits []StarterKit
	for _, k := range seededStarterKits {
		if k.ThemeID == themeID {
			kits = append(kits, k)
		}
	}
	if kits == nil {
		kits = []StarterKit{}
	}
	c.JSON(http.StatusOK, kits)
}

// GetStarterKitDocument returns the Puck document for a starter kit by ID.
// Used by the page creation handler to seed DraftDocument.
func GetStarterKitDocument(kitID string) map[string]any {
	for _, k := range seededStarterKits {
		if k.ID == kitID {
			return k.PuckDocument
		}
	}
	return nil
}

// ApplyPageContextPacks resolves context pack chunks and injects them into the AI prompt.
// Returns a combined string of all chunk texts for the given pack IDs.
func ResolvePageContextPacks(tenantID bson.ObjectId, packIDs []string) []string {
	var chunks []string
	for _, pid := range packIDs {
		var pack pkgmodels.ContextPack
		if err := db.GetCollection(pkgmodels.ContextPackCollection).Find(bson.M{
			"public_id": pid,
			"tenant_id": tenantID,
		}).One(&pack); err != nil {
			continue
		}
		for _, c := range pack.Chunks {
			chunks = append(chunks, c.Text)
		}
	}
	return chunks
}

// SerializeDocument safely JSON-encodes a Puck document for prompt injection.
func SerializeDocument(doc map[string]any) string {
	if doc == nil {
		return "{}"
	}
	b, _ := json.Marshal(doc)
	return string(b)
}

// PatchAIEditWithPackContext is called by the AI edit handler to inject context into the prompt.
// It returns an extended instruction with context pack content prepended.
func BuildContextAwareInstruction(instruction string, chunks []string, brandSummary string) string {
	if len(chunks) == 0 && brandSummary == "" {
		return instruction
	}
	var sb strings.Builder
	if brandSummary != "" {
		sb.WriteString("Brand context:\n")
		sb.WriteString(brandSummary)
		sb.WriteString("\n\n")
	}
	if len(chunks) > 0 {
		sb.WriteString("Reference material:\n")
		for i, chunk := range chunks {
			if i > 0 {
				sb.WriteString("\n---\n")
			}
			sb.WriteString(chunk)
		}
		sb.WriteString("\n\n")
	}
	sb.WriteString("Instruction:\n")
	sb.WriteString(instruction)
	return sb.String()
}

// PatchPageCreationWithStarterKit applies a starter kit document to a newly created page.
func PatchPageCreationWithStarterKit(pageID bson.ObjectId, tenantID bson.ObjectId, kitID string) {
	doc := GetStarterKitDocument(kitID)
	if doc == nil {
		return
	}
	_ = site.UpdateSitePage(pageID, tenantID, bson.M{"draft_document": doc})
}
