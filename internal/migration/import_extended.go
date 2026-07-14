package migration

import (
	"strings"
	"time"

	"gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/db"
	models "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/utils"
)

// CP14a entity importers: subscriptions (MIG-007), forms + pages (MIG-008).

// importSubscriptions lands recurring-billing rows as NON-CHARGING
// MigratedSubscription records in takeover state "imported". No Stripe
// object is ever created or touched here — activation is a separate,
// audited owner workflow (routes/migration_subscriptions.go).
func (r *run) importSubscriptions() {
	for _, ss := range r.ex.Subscriptions {
		if _, ok := r.lookupMap(models.SourceTypeSubscription, ss.SourceID); ok {
			r.matched[models.SourceTypeSubscription]++
			continue
		}
		contactID, ok := r.contactByRef[ss.Email]
		if !ok {
			if id, found := r.findLocalContact(ss.Email); found {
				contactID = id
			} else {
				r.rowError(models.SourceTypeSubscription, ss.SourceID, ss.Row, "unknown contact "+ss.Email)
				continue
			}
		}
		offerID, ok := r.offerByRef[strings.ToLower(ss.OfferRef)]
		if !ok && !r.dry {
			r.rowError(models.SourceTypeSubscription, ss.SourceID, ss.Row, "unresolved offer "+ss.OfferRef)
			continue
		}
		if r.dry {
			r.created[models.SourceTypeSubscription]++
			continue
		}
		now := time.Now()
		msub := &models.MigratedSubscription{
			Id: bson.NewObjectId(), PublicId: utils.GeneratePublicId(),
			TenantID: r.p.TenantID, ProjectID: r.p.Id,
			SourceID: ss.SourceID, ContactID: contactID, OfferID: offerID,
			SourceStatus: ss.Status, AmountMinor: ss.AmountMinor,
			Currency: ss.Currency, Interval: ss.Interval,
			TakeoverState: models.MigratedSubStateImported,
			CreatedAt:     now, UpdatedAt: now,
		}
		if !ss.NextBillingAt.IsZero() {
			t := ss.NextBillingAt
			msub.NextBillingAt = &t
		}
		if err := db.GetCollection(models.MigratedSubscriptionCollection).Insert(msub); err != nil {
			if !mgo.IsDup(err) {
				r.rowError(models.SourceTypeSubscription, ss.SourceID, ss.Row, "insert: "+err.Error())
			}
			continue
		}
		r.record(models.SourceTypeSubscription, ss.SourceID, models.MigratedSubscriptionCollection, msub.Id, true)
	}
}

// importForms creates standalone PageForm drafts (no page binding — the
// tenant attaches them in the builder). Field declarations map onto the
// native FormField shape; capture-only defaults (upsert contact + write
// attributes) mirror what a Kajabi form did.
func (r *run) importForms() {
	for _, sf := range r.ex.Forms {
		if _, ok := r.lookupMap(models.SourceTypeForm, sf.SourceID); ok {
			r.matched[models.SourceTypeForm]++
			continue
		}
		if r.dry {
			r.created[models.SourceTypeForm]++
			continue
		}
		form := &models.PageForm{
			Id: bson.NewObjectId(), PublicId: utils.GeneratePublicId(),
			TenantID: r.p.TenantID, SubscriberId: r.p.TenantID.Hex(),
			Name: sf.Name, FormType: "capture",
			OnSubmit: &models.FormOnSubmit{UpsertContact: true, WriteAttributes: true},
		}
		for _, fld := range sf.Fields {
			form.Fields = append(form.Fields, &models.FormField{
				FieldName: fld.Name, FieldType: fld.Type, Required: fld.Required,
			})
		}
		if len(form.Fields) == 0 {
			form.Fields = []*models.FormField{{FieldName: "Email", FieldType: "email", Required: true}}
		}
		if err := db.GetCollection(models.PageFormCollection).Insert(form); err != nil {
			r.rowError(models.SourceTypeForm, sf.SourceID, sf.Row, "insert: "+err.Error())
			continue
		}
		r.record(models.SourceTypeForm, sf.SourceID, models.PageFormCollection, form.Id, true)
	}
}

// importPages creates one draft "Imported from Kajabi" Site holding a
// placeholder FunnelPage per pages.csv row (title/slug only — content is
// externally blocked). Nothing publishes; the tenant rebuilds bodies in the
// builder and publishes deliberately.
func (r *run) importPages() {
	if len(r.ex.Pages) == 0 {
		return
	}
	siteSourceID := "site:" + r.p.PublicId
	siteID, haveSite := r.lookupMap(models.SourceTypeSite, siteSourceID)
	if haveSite {
		r.matched[models.SourceTypeSite]++
	} else if r.dry {
		r.created[models.SourceTypeSite]++
	} else {
		site := models.NewSite()
		site.TenantID = r.p.TenantID
		site.SubscriberId = r.p.TenantID.Hex()
		site.Name = "Imported from Kajabi"
		site.Status = "draft"
		site.PageIds = models.NewBsonCollectionIds()
		site.PageIds.CollectionName = models.FunnelPageCollection
		if err := db.GetCollection(models.SiteCollection).Insert(site); err != nil {
			r.rowError(models.SourceTypeSite, siteSourceID, 0, "site insert: "+err.Error())
			return
		}
		siteID = site.Id
		r.record(models.SourceTypeSite, siteSourceID, models.SiteCollection, site.Id, true)
	}

	var newPageIDs []bson.ObjectId
	for _, sp := range r.ex.Pages {
		if _, ok := r.lookupMap(models.SourceTypePage, sp.SourceID); ok {
			r.matched[models.SourceTypePage]++
			continue
		}
		if r.dry {
			r.created[models.SourceTypePage]++
			continue
		}
		page := &models.FunnelPage{
			Id: bson.NewObjectId(), PublicId: utils.GeneratePublicId(),
			TenantID: r.p.TenantID, SubscriberId: r.p.TenantID.Hex(),
			Name:         sp.Title,
			TemplateName: "imported-placeholder",
		}
		if err := db.GetCollection(models.FunnelPageCollection).Insert(page); err != nil {
			r.rowError(models.SourceTypePage, sp.SourceID, sp.Row, "page insert: "+err.Error())
			continue
		}
		newPageIDs = append(newPageIDs, page.Id)
		r.record(models.SourceTypePage, sp.SourceID, models.FunnelPageCollection, page.Id, true)
	}
	if !r.dry && len(newPageIDs) > 0 && siteID != "" {
		if err := db.GetCollection(models.SiteCollection).Update(
			bson.M{"_id": siteID, "tenant_id": r.p.TenantID},
			bson.M{"$push": bson.M{"page_ids.ids": bson.M{"$each": newPageIDs}}},
		); err != nil {
			r.rowError(models.SourceTypeSite, siteSourceID, 0, "site page linkage: "+err.Error())
		}
	}
}
