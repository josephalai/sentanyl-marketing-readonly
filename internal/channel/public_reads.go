package channel

import (
	"fmt"

	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// publicVisibleStatus excludes draft/archived products but keeps legacy rows
// with no status set.
var publicVisibleStatus = bson.M{"$nin": []string{
	pkgmodels.ProductStatusDraft,
	pkgmodels.ProductStatusArchived,
}}

var publicVisibleOfferStatus = bson.M{"$nin": []string{
	pkgmodels.OfferStatusDraft,
	pkgmodels.OfferStatusArchived,
}}

// ListPublicProducts returns public-safe product cards for a tenant.
func ListPublicProducts(tenantID bson.ObjectId) ([]PublicProduct, error) {
	var products []pkgmodels.Product
	err := db.GetCollection(pkgmodels.ProductCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"status":                publicVisibleStatus,
		"timestamps.deleted_at": nil,
	}).All(&products)
	if err != nil {
		return nil, err
	}
	out := make([]PublicProduct, 0, len(products))
	for i := range products {
		out = append(out, ToPublicProduct(&products[i]))
	}
	return out, nil
}

// GetPublicProduct returns one public-safe product by public_id or hex id.
func GetPublicProduct(tenantID bson.ObjectId, idParam string) (*PublicProduct, error) {
	q := bson.M{
		"tenant_id":             tenantID,
		"status":                publicVisibleStatus,
		"timestamps.deleted_at": nil,
	}
	if bson.IsObjectIdHex(idParam) {
		q["_id"] = bson.ObjectIdHex(idParam)
	} else {
		q["public_id"] = idParam
	}
	var p pkgmodels.Product
	if err := db.GetCollection(pkgmodels.ProductCollection).Find(q).One(&p); err != nil {
		return nil, fmt.Errorf("product not found")
	}
	dto := ToPublicProduct(&p)
	return &dto, nil
}

// ListPublicOffers returns public-safe offer cards for a tenant.
func ListPublicOffers(tenantID bson.ObjectId) ([]PublicOffer, error) {
	var offers []pkgmodels.Offer
	err := db.GetCollection(pkgmodels.OfferCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"status":                publicVisibleOfferStatus,
		"timestamps.deleted_at": nil,
	}).All(&offers)
	if err != nil {
		return nil, err
	}
	lookup := productPublicIdLookup(tenantID)
	out := make([]PublicOffer, 0, len(offers))
	for i := range offers {
		out = append(out, ToPublicOffer(&offers[i], lookup))
	}
	return out, nil
}

// GetPublicOffer returns one public-safe offer by public_id or hex id.
func GetPublicOffer(tenantID bson.ObjectId, idParam string) (*PublicOffer, error) {
	o, err := FindOfferForTenant(tenantID, idParam)
	if err != nil {
		return nil, err
	}
	dto := ToPublicOffer(o, productPublicIdLookup(tenantID))
	return &dto, nil
}

// FindOfferForTenant loads a raw tenant-scoped offer by public_id or hex id.
// Used by the public checkout route, which needs the full model.
func FindOfferForTenant(tenantID bson.ObjectId, idParam string) (*pkgmodels.Offer, error) {
	q := bson.M{
		"tenant_id":             tenantID,
		"status":                publicVisibleOfferStatus,
		"timestamps.deleted_at": nil,
	}
	if bson.IsObjectIdHex(idParam) {
		q["_id"] = bson.ObjectIdHex(idParam)
	} else {
		q["public_id"] = idParam
	}
	var o pkgmodels.Offer
	if err := db.GetCollection(pkgmodels.OfferCollection).Find(q).One(&o); err != nil {
		return nil, fmt.Errorf("offer not found")
	}
	return &o, nil
}

func productPublicIdLookup(tenantID bson.ObjectId) map[string]string {
	var products []pkgmodels.Product
	_ = db.GetCollection(pkgmodels.ProductCollection).Find(bson.M{
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).Select(bson.M{"_id": 1, "public_id": 1}).All(&products)
	lookup := make(map[string]string, len(products))
	for _, p := range products {
		lookup[p.Id.Hex()] = p.PublicId
	}
	return lookup
}
