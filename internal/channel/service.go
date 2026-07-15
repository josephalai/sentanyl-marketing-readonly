package channel

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/publicchannel"
)

// ChannelUpsertRequest is the tenant-facing create/update payload.
type ChannelUpsertRequest struct {
	Type              string   `json:"type"`
	Name              string   `json:"name"`
	Status            string   `json:"status"`
	Domain            string   `json:"domain"`
	AllowedOrigins    []string `json:"allowed_origins"`
	SiteID            string   `json:"site_id"`
	PortalBaseURL     string   `json:"portal_base_url"`
	DefaultSuccessURL string   `json:"default_success_url"`
	DefaultCancelURL  string   `json:"default_cancel_url"`
}

func ServiceListChannels(tenantID bson.ObjectId) ([]pkgmodels.FrontendChannel, error) {
	var channels []pkgmodels.FrontendChannel
	err := db.GetCollection(pkgmodels.FrontendChannelCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).Sort("-_id").All(&channels)
	return channels, err
}

func ServiceGetChannel(tenantID bson.ObjectId, idParam string) (*pkgmodels.FrontendChannel, error) {
	q := bson.M{
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}
	if bson.IsObjectIdHex(idParam) {
		q["_id"] = bson.ObjectIdHex(idParam)
	} else {
		q["public_id"] = idParam
	}
	var ch pkgmodels.FrontendChannel
	if err := db.GetCollection(pkgmodels.FrontendChannelCollection).Find(q).One(&ch); err != nil {
		return nil, fmt.Errorf("channel not found")
	}
	return &ch, nil
}

func ServiceCreateChannel(req ChannelUpsertRequest, tenantID bson.ObjectId) (*pkgmodels.FrontendChannel, error) {
	if strings.TrimSpace(req.Name) == "" {
		return nil, fmt.Errorf("name is required")
	}
	channelType := req.Type
	if channelType == "" {
		channelType = pkgmodels.FrontendChannelTypeCodedWebsite
	}
	if !pkgmodels.IsValidFrontendChannelType(channelType) {
		return nil, fmt.Errorf("invalid channel type %q", channelType)
	}

	ch := pkgmodels.NewFrontendChannel(strings.TrimSpace(req.Name), channelType, tenantID)
	if err := applyUpsert(ch, req, tenantID); err != nil {
		return nil, err
	}

	// Coded websites get a public key at birth so snippets are copyable
	// immediately.
	if channelType == pkgmodels.FrontendChannelTypeCodedWebsite {
		key, err := auth.GeneratePublicKey()
		if err != nil {
			return nil, fmt.Errorf("failed to generate public key")
		}
		ch.PublicKey = key
	}

	now := time.Now()
	ch.SoftDeletes.CreatedAt = &now
	if ch.Domain != "" {
		if err := publicchannel.ClaimHost(ch.Domain, tenantID, publicchannel.HostClaimChannel, ch.Id); err != nil {
			return nil, err
		}
	}
	if err := db.GetCollection(pkgmodels.FrontendChannelCollection).Insert(ch); err != nil {
		_ = publicchannel.ReleaseHost(ch.Domain, tenantID, publicchannel.HostClaimChannel, ch.Id)
		return nil, fmt.Errorf("failed to create channel")
	}
	return ch, nil
}

func ServiceUpdateChannel(tenantID bson.ObjectId, idParam string, req ChannelUpsertRequest) (*pkgmodels.FrontendChannel, error) {
	ch, err := ServiceGetChannel(tenantID, idParam)
	if err != nil {
		return nil, err
	}
	oldDomain := ch.Domain
	if req.Name != "" {
		ch.Name = strings.TrimSpace(req.Name)
	}
	if req.Type != "" && req.Type != ch.Type {
		if !pkgmodels.IsValidFrontendChannelType(req.Type) {
			return nil, fmt.Errorf("invalid channel type %q", req.Type)
		}
		ch.Type = req.Type
	}
	if err := applyUpsert(ch, req, tenantID); err != nil {
		return nil, err
	}
	now := time.Now()
	ch.SoftDeletes.UpdatedAt = &now
	if ch.Domain != "" {
		if err := publicchannel.ClaimHost(ch.Domain, tenantID, publicchannel.HostClaimChannel, ch.Id); err != nil {
			return nil, err
		}
	}
	if err := db.GetCollection(pkgmodels.FrontendChannelCollection).UpdateId(ch.Id, ch); err != nil {
		if ch.Domain != oldDomain {
			_ = publicchannel.ReleaseHost(ch.Domain, tenantID, publicchannel.HostClaimChannel, ch.Id)
		}
		return nil, fmt.Errorf("failed to update channel")
	}
	if oldDomain != "" && oldDomain != ch.Domain {
		_ = publicchannel.ReleaseHost(oldDomain, tenantID, publicchannel.HostClaimChannel, ch.Id)
	}
	return ch, nil
}

func ServiceDeleteChannel(tenantID bson.ObjectId, idParam string) error {
	ch, err := ServiceGetChannel(tenantID, idParam)
	if err != nil {
		return err
	}
	now := time.Now()
	err = db.GetCollection(pkgmodels.FrontendChannelCollection).UpdateId(ch.Id, bson.M{
		"$set": bson.M{"timestamps.deleted_at": now, "timestamps.updated_at": now},
	})
	if err == nil {
		_ = publicchannel.ReleaseHost(ch.Domain, tenantID, publicchannel.HostClaimChannel, ch.Id)
	}
	return err
}

// ServiceRotateChannelKey mints a fresh public key for a channel.
func ServiceRotateChannelKey(tenantID bson.ObjectId, idParam string) (*pkgmodels.FrontendChannel, error) {
	ch, err := ServiceGetChannel(tenantID, idParam)
	if err != nil {
		return nil, err
	}
	key, err := auth.GeneratePublicKey()
	if err != nil {
		return nil, fmt.Errorf("failed to generate public key")
	}
	now := time.Now()
	if err := db.GetCollection(pkgmodels.FrontendChannelCollection).UpdateId(ch.Id, bson.M{
		"$set": bson.M{"public_key": key, "timestamps.updated_at": now},
	}); err != nil {
		return nil, fmt.Errorf("failed to rotate key")
	}
	ch.PublicKey = key
	return ch, nil
}

// applyUpsert copies mutable fields from the request onto the channel,
// normalizing the domain and enforcing per-tenant active-domain uniqueness.
func applyUpsert(ch *pkgmodels.FrontendChannel, req ChannelUpsertRequest, tenantID bson.ObjectId) error {
	if req.Status != "" {
		switch req.Status {
		case pkgmodels.FrontendChannelStatusDraft,
			pkgmodels.FrontendChannelStatusActive,
			pkgmodels.FrontendChannelStatusDisabled:
			ch.Status = req.Status
		default:
			return fmt.Errorf("invalid status %q", req.Status)
		}
	}
	if req.Domain != "" {
		domain, err := publicchannel.CanonicalHost(req.Domain)
		if err != nil {
			return fmt.Errorf("invalid domain")
		}
		if domain != ch.Domain {
			if err := validateDomainAvailable(domain, tenantID, ch.Id); err != nil {
				return err
			}
			ch.Domain = domain
		}
	}
	if req.AllowedOrigins != nil {
		origins, err := publicchannel.CanonicalOrigins(req.AllowedOrigins)
		if err != nil {
			return err
		}
		ch.AllowedOrigins = origins
	}
	if req.SiteID != "" {
		if !bson.IsObjectIdHex(req.SiteID) {
			return fmt.Errorf("invalid site_id")
		}
		// ACQ-012: a hex site_id is not trusted on its face — it must resolve
		// to a live Site owned by THIS tenant, so a channel can never bind to
		// another tenant's site.
		siteID := bson.ObjectIdHex(req.SiteID)
		n, _ := db.GetCollection(pkgmodels.SiteCollection).Find(bson.M{
			"_id":                   siteID,
			"tenant_id":             tenantID,
			"timestamps.deleted_at": nil,
		}).Count()
		if n == 0 {
			return fmt.Errorf("site_id does not resolve to a site in this tenant")
		}
		ch.SiteID = siteID
	}
	if req.PortalBaseURL != "" {
		ch.PortalBaseURL = strings.TrimSpace(req.PortalBaseURL)
	}
	if req.DefaultSuccessURL != "" {
		ch.DefaultSuccessURL = strings.TrimSpace(req.DefaultSuccessURL)
	}
	if req.DefaultCancelURL != "" {
		ch.DefaultCancelURL = strings.TrimSpace(req.DefaultCancelURL)
	}
	if ch.Status == pkgmodels.FrontendChannelStatusActive && ch.Domain == "" {
		return fmt.Errorf("active channel requires a domain")
	}
	return nil
}

func validateDomainAvailable(domain string, tenantID, channelID bson.ObjectId) error {
	exact := bson.RegEx{Pattern: "^" + regexp.QuoteMeta(domain) + "$", Options: "i"}
	// A public hostname may resolve to only one channel, even inside a tenant;
	// otherwise host-based resolution depends on Mongo result order.
	n, err := db.GetCollection(pkgmodels.FrontendChannelCollection).Find(bson.M{
		"domain": exact, "_id": bson.M{"$ne": channelID}, "timestamps.deleted_at": nil,
	}).Count()
	if err != nil {
		return fmt.Errorf("failed to validate domain ownership")
	}
	if n > 0 {
		return publicchannel.ErrHostClaimed
	}
	// A verified/custom domain or published Site belonging to another tenant
	// is also an ownership claim. The error deliberately reveals no tenant.
	for _, check := range []struct {
		collection string
		query      bson.M
	}{
		{pkgmodels.DomainCollection, bson.M{"hostname": exact, "tenant_id": bson.M{"$ne": tenantID}, "timestamps.deleted_at": nil}},
		{pkgmodels.SiteCollection, bson.M{"attached_domains": exact, "tenant_id": bson.M{"$ne": tenantID}, "timestamps.deleted_at": nil}},
	} {
		n, err = db.GetCollection(check.collection).Find(check.query).Count()
		if err != nil {
			return fmt.Errorf("failed to validate domain ownership")
		}
		if n > 0 {
			return publicchannel.ErrHostClaimed
		}
	}
	return nil
}
