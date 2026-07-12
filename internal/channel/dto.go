package channel

import (
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// Public-safe DTOs for the /api/public/* surface. Whitelist projection only
// (same approach as the video service's sanitizeForPlayer): raw models leak
// tenant_id / internal ObjectIds / Stripe IDs through their json tags, so
// these structs are the only shapes public handlers may serialize.

// PublicChannel is the bootstrap payload for GET /api/public/channel.
// TenantID (hex) is intentionally exposed: the video event ingest endpoint
// identifies tenants by id in the request body, and hex tenant ids carry no
// privilege on their own.
type PublicChannel struct {
	PublicId          string `json:"public_id"`
	Type              string `json:"type"`
	Name              string `json:"name,omitempty"`
	TenantID          string `json:"tenant_id"`
	Domain            string `json:"domain,omitempty"`
	PortalBaseURL     string `json:"portal_base_url,omitempty"`
	DefaultSuccessURL string `json:"default_success_url,omitempty"`
	DefaultCancelURL  string `json:"default_cancel_url,omitempty"`
}

// PublicProduct is the public-safe product card.
type PublicProduct struct {
	PublicId       string  `json:"public_id"`
	Name           string  `json:"name"`
	Description    string  `json:"description,omitempty"`
	ProductType    string  `json:"product_type,omitempty"`
	ThumbnailURL   string  `json:"thumbnail_url,omitempty"`
	Price          float64 `json:"price,omitempty"`
	Currency       string  `json:"currency,omitempty"`
	InstructorName string  `json:"instructor_name,omitempty"`
	TotalLessons   int     `json:"total_lessons,omitempty"`
}

// PublicOffer is the public-safe offer card.
type PublicOffer struct {
	PublicId                 string   `json:"public_id"`
	Title                    string   `json:"title"`
	PricingModel             string   `json:"pricing_model,omitempty"`
	Amount                   int64    `json:"amount"`
	Currency                 string   `json:"currency,omitempty"`
	IncludedProductPublicIds []string `json:"included_product_public_ids,omitempty"`
}

// ToPublicChannel builds the bootstrap DTO. ch may be nil (legacy builder
// domain with no channel record) — the caller supplies tenant/domain.
func ToPublicChannel(ch *pkgmodels.FrontendChannel, tenantIDHex, domain string) PublicChannel {
	out := PublicChannel{
		Type:     pkgmodels.FrontendChannelTypeBuilderSite,
		TenantID: tenantIDHex,
		Domain:   domain,
	}
	if ch != nil {
		out.PublicId = ch.PublicId
		out.Type = ch.Type
		out.Name = ch.Name
		out.Domain = ch.Domain
		out.PortalBaseURL = ch.PortalBaseURL
		out.DefaultSuccessURL = ch.DefaultSuccessURL
		out.DefaultCancelURL = ch.DefaultCancelURL
	}
	return out
}

func ToPublicProduct(p *pkgmodels.Product) PublicProduct {
	return PublicProduct{
		PublicId:       p.PublicId,
		Name:           p.Name,
		Description:    p.Description,
		ProductType:    p.ProductType,
		ThumbnailURL:   p.ThumbnailURL,
		Price:          p.Price,
		Currency:       p.Currency,
		InstructorName: p.InstructorName,
		TotalLessons:   p.TotalLessons,
	}
}

// ToPublicOffer maps included product ObjectIds to their public_ids via the
// supplied lookup (built by the service layer from a tenant-scoped query).
func ToPublicOffer(o *pkgmodels.Offer, productPublicIds map[string]string) PublicOffer {
	out := PublicOffer{
		PublicId:     o.PublicId,
		Title:        o.Title,
		PricingModel: o.PricingModel,
		Amount:       o.Amount,
		Currency:     o.Currency,
	}
	for _, pid := range o.IncludedProducts {
		if pub, ok := productPublicIds[pid.Hex()]; ok {
			out.IncludedProductPublicIds = append(out.IncludedProductPublicIds, pub)
		}
	}
	return out
}
