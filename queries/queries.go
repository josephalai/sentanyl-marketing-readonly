package queries

import (
	"github.com/josephalai/sentanyl/marketing-service/models"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"gopkg.in/mgo.v2/bson"
)

// FindFunnelByPublicId looks up a funnel by its public_id and hydrates it.
func FindFunnelByPublicId(publicId string) (*models.Funnel, error) {
	var funnel models.Funnel
	err := db.GetCollection(pkgmodels.FunnelCollection).Find(bson.M{
		"public_id":             publicId,
		"timestamps.deleted_at": nil,
	}).One(&funnel)
	if err != nil {
		return nil, err
	}
	funnel.Hydrate()
	return &funnel, nil
}

// FindFunnelsBySubscriber returns all non-deleted funnels for a subscriber.
func FindFunnelsBySubscriber(subscriberId string) ([]models.Funnel, error) {
	var funnels []models.Funnel
	err := db.GetCollection(pkgmodels.FunnelCollection).Find(bson.M{
		"subscriber_id":         subscriberId,
		"timestamps.deleted_at": nil,
	}).All(&funnels)
	return funnels, err
}

// FindOffersByTenant returns all non-deleted offers for a tenant.
func FindOffersByTenant(tenantID bson.ObjectId) ([]models.Offer, error) {
	var offers []models.Offer
	err := db.GetCollection(pkgmodels.OfferCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).All(&offers)
	return offers, err
}

// InsertEmail inserts an email into the instant email collection.
func InsertEmail(email *models.Email) error {
	return db.GetCollection(pkgmodels.InstantEmailCollection).Insert(email)
}
