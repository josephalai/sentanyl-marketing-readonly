package handlers

import (
	"fmt"
	"strings"

	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// fetchSiteAIContext builds the business context string injected into site AI generation calls.
// Queries the tenant's active products, brand profile, and any specified context pack chunks.
// Caps output at ~800 tokens to avoid context overflow.
func fetchSiteAIContext(tenantID bson.ObjectId, contextPackIDs []string) string {
	var sb strings.Builder

	// Products
	var products []pkgmodels.Product
	_ = db.GetCollection(pkgmodels.ProductCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"status":                bson.M{"$ne": "archived"},
		"timestamps.deleted_at": nil,
	}).Limit(30).All(&products)

	if len(products) > 0 {
		sb.WriteString("### Products & Services\n")
		for _, p := range products {
			sb.WriteString(fmt.Sprintf("- **%s** (%s)", p.Name, p.ProductType))
			if p.Price > 0 {
				sb.WriteString(fmt.Sprintf(" — $%.2f %s", p.Price, p.Currency))
			}
			if p.Description != "" {
				desc := p.Description
				if len(desc) > 200 {
					desc = desc[:200] + "..."
				}
				sb.WriteString(fmt.Sprintf(": %s", desc))
			}
			sb.WriteString("\n")
		}
	}

	// Brand profile
	var brand pkgmodels.BrandProfile
	if err := db.GetCollection(pkgmodels.BrandProfileCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).One(&brand); err == nil {
		sb.WriteString("\n### Brand\n")
		if brand.VoiceTone != "" {
			sb.WriteString(fmt.Sprintf("Voice/Tone: %s\n", brand.VoiceTone))
		}
		if brand.Positioning != "" {
			sb.WriteString(fmt.Sprintf("Positioning: %s\n", brand.Positioning))
		}
		if brand.AvatarDescription != "" {
			sb.WriteString(fmt.Sprintf("Ideal customer: %s\n", brand.AvatarDescription))
		}
	}

	// Context pack chunks
	if len(contextPackIDs) > 0 {
		chunks := resolveContextPackChunks(tenantID, contextPackIDs)
		if len(chunks) > 0 {
			sb.WriteString("\n### Context Pack Reference\n")
			for _, chunk := range chunks {
				sb.WriteString(chunk)
				sb.WriteString("\n")
			}
		}
	}

	return sb.String()
}

// resolveStylePreset loads a tenant's style preset by public id (nil if unset/not found).
func resolveStylePreset(tenantID bson.ObjectId, presetID string) *pkgmodels.StylePreset {
	if presetID == "" {
		return nil
	}
	var p pkgmodels.StylePreset
	if err := db.GetCollection(pkgmodels.StylePresetCollection).Find(bson.M{
		"public_id":             presetID,
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).One(&p); err != nil {
		return nil
	}
	return &p
}

// stylePresetDirective renders a preset as a prompt directive appended to the
// business context, so generated copy + block styling stay on-brand.
func stylePresetDirective(p *pkgmodels.StylePreset) string {
	if p == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n### Brand Style Preset — MATCH THIS\n")
	if p.BrandVoice != "" {
		sb.WriteString(fmt.Sprintf("Voice: %s\n", p.BrandVoice))
	}
	gs := p.GlobalStyle
	var vis []string
	if gs.PrimaryColor != "" {
		vis = append(vis, "primary "+gs.PrimaryColor)
	}
	if gs.AccentColor != "" {
		vis = append(vis, "accent "+gs.AccentColor)
	}
	if gs.HeadingFont != "" {
		vis = append(vis, "heading font "+gs.HeadingFont)
	}
	if gs.BodyFont != "" {
		vis = append(vis, "body font "+gs.BodyFont)
	}
	if len(vis) > 0 {
		sb.WriteString("Visual tokens: " + strings.Join(vis, ", ") + "\n")
	}
	if p.DefaultTone != "" {
		sb.WriteString(fmt.Sprintf("Prefer section band tone: %s\n", p.DefaultTone))
	}
	return sb.String()
}

// applyPresetToSite writes the preset's GlobalStyle onto the site so the SSR
// tokens (buildGlobalStyleVars / the worker's EditorDesignTokens) reflect the
// intended brand on published pages.
func applyPresetToSite(tenantID, siteID bson.ObjectId, gs pkgmodels.GlobalStyle) {
	_ = db.GetCollection(pkgmodels.SiteCollection).Update(
		bson.M{"_id": siteID, "tenant_id": tenantID},
		bson.M{"$set": bson.M{"global_style": gs}},
	)
}

// applySitePresetForGen resolves the preset, appends its directive to the
// business-context string, and applies its GlobalStyle to the site. Returns the
// (possibly augmented) business context. No-op when presetID is empty.
func applySitePresetForGen(tenantID, siteID bson.ObjectId, presetID, bizCtx string) string {
	p := resolveStylePreset(tenantID, presetID)
	if p == nil {
		return bizCtx
	}
	if siteID.Valid() {
		applyPresetToSite(tenantID, siteID, p.GlobalStyle)
	}
	return bizCtx + stylePresetDirective(p)
}

// resolveContextPackChunks fetches text chunks from the given context pack public IDs.
func resolveContextPackChunks(tenantID bson.ObjectId, packIDs []string) []string {
	var chunks []string
	for _, pid := range packIDs {
		var pack pkgmodels.ContextPack
		if err := db.GetCollection(pkgmodels.ContextPackCollection).Find(bson.M{
			"public_id":             pid,
			"tenant_id":             tenantID,
			"timestamps.deleted_at": nil,
		}).One(&pack); err != nil {
			continue
		}
		for _, chunk := range pack.Chunks {
			if chunk.Text != "" {
				text := chunk.Text
				if len(text) > 400 {
					text = text[:400]
				}
				chunks = append(chunks, text)
			}
			if len(chunks) >= 8 {
				break
			}
		}
		if len(chunks) >= 8 {
			break
		}
	}
	return chunks
}

// buildProductSummaryForSuggest builds a concise product list for page suggestion prompts.
func buildProductSummaryForSuggest(tenantID bson.ObjectId) string {
	var products []pkgmodels.Product
	_ = db.GetCollection(pkgmodels.ProductCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"status":                bson.M{"$ne": "archived"},
		"timestamps.deleted_at": nil,
	}).Limit(50).All(&products)

	if len(products) == 0 {
		return "No products found."
	}

	var sb strings.Builder
	sb.WriteString("Products:\n")
	for _, p := range products {
		sb.WriteString(fmt.Sprintf("- %s (%s", p.Name, p.ProductType))
		if p.Price > 0 {
			sb.WriteString(fmt.Sprintf(", $%.2f", p.Price))
		}
		sb.WriteString(")\n")
	}
	return sb.String()
}

// fetchProductsByIDs fetches products by their public IDs for the given tenant.
func fetchProductsByIDs(tenantID bson.ObjectId, publicIDs []string) []pkgmodels.Product {
	var products []pkgmodels.Product
	_ = db.GetCollection(pkgmodels.ProductCollection).Find(bson.M{
		"public_id":             bson.M{"$in": publicIDs},
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).All(&products)
	return products
}

// buildProductDetailsForGeneration formats product details for "generate from products" prompts.
func buildProductDetailsForGeneration(products []pkgmodels.Product) string {
	var sb strings.Builder
	for _, p := range products {
		sb.WriteString(fmt.Sprintf("## %s\n", p.Name))
		sb.WriteString(fmt.Sprintf("Type: %s\n", p.ProductType))
		if p.Price > 0 {
			sb.WriteString(fmt.Sprintf("Price: $%.2f %s\n", p.Price, p.Currency))
		}
		if p.Description != "" {
			sb.WriteString(fmt.Sprintf("Description: %s\n", p.Description))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}
