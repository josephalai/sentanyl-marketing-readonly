package models

import (
	"github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/utils"
	"gopkg.in/mgo.v2/bson"
)

// Offer is the checkout vehicle and pricing rules for one or more Products.
type Offer struct {
	Id               bson.ObjectId   `bson:"_id" json:"id,omitempty"`
	PublicId         string          `bson:"public_id" json:"public_id,omitempty"`
	TenantID         bson.ObjectId   `bson:"tenant_id" json:"tenant_id,omitempty"`
	SubscriberId     string          `bson:"subscriber_id" json:"subscriber_id,omitempty"`
	Title            string          `bson:"title" json:"title,omitempty"`
	StripeProductID  string          `bson:"stripe_product_id,omitempty" json:"stripe_product_id,omitempty"`
	StripePriceID    string          `bson:"stripe_price_id,omitempty" json:"stripe_price_id,omitempty"`
	PricingModel     string          `bson:"pricing_model" json:"pricing_model,omitempty"`
	Amount           int64           `bson:"amount" json:"amount,omitempty"`
	Currency         string          `bson:"currency,omitempty" json:"currency,omitempty"`
	GrantedBadges    []string        `bson:"granted_badges,omitempty" json:"granted_badges,omitempty"`
	IncludedProducts []bson.ObjectId `bson:"included_products,omitempty" json:"included_products,omitempty"`
	models.SoftDeletes `bson:"timestamps,omitempty" json:"timestamps,omitempty"`
}

func NewOffer(title string, tenantID bson.ObjectId) *Offer {
	return &Offer{
		Id:       bson.NewObjectId(),
		PublicId: utils.GeneratePublicId(),
		TenantID: tenantID,
		Title:    title,
		Currency: "usd",
	}
}

func (o *Offer) ReadyMongoStore() []interface{} {
	return []interface{}{*o}
}

func (o *Offer) GetIdHex() string {
	return o.Id.Hex()
}

func (o *Offer) GetId() bson.ObjectId {
	return o.Id
}

// Coupon represents a discount that can be applied to specific Offers.
type Coupon struct {
	Id             bson.ObjectId `bson:"_id" json:"id,omitempty"`
	PublicId       string        `bson:"public_id" json:"public_id,omitempty"`
	TenantID       bson.ObjectId `bson:"tenant_id" json:"tenant_id,omitempty"`
	SubscriberId   string        `bson:"subscriber_id" json:"subscriber_id,omitempty"`
	Code           string        `bson:"code" json:"code,omitempty"`
	DiscountType   string        `bson:"discount_type" json:"discount_type,omitempty"`
	Value          int64         `bson:"value" json:"value,omitempty"`
	StripeCouponID string        `bson:"stripe_coupon_id,omitempty" json:"stripe_coupon_id,omitempty"`
	Duration       string        `bson:"duration,omitempty" json:"duration,omitempty"`
	MaxRedemptions int           `bson:"max_redemptions,omitempty" json:"max_redemptions,omitempty"`
	models.SoftDeletes `bson:"timestamps,omitempty" json:"timestamps,omitempty"`
}

func NewCoupon(code string, tenantID bson.ObjectId) *Coupon {
	return &Coupon{
		Id:       bson.NewObjectId(),
		PublicId: utils.GeneratePublicId(),
		TenantID: tenantID,
		Code:     code,
	}
}

func (c *Coupon) ReadyMongoStore() []interface{} {
	return []interface{}{*c}
}

func (c *Coupon) GetIdHex() string {
	return c.Id.Hex()
}

func (c *Coupon) GetId() bson.ObjectId {
	return c.Id
}

// Subscription tracks recurring payment state for a Contact.
type Subscription struct {
	Id                   bson.ObjectId `bson:"_id" json:"id,omitempty"`
	PublicId             string        `bson:"public_id" json:"public_id,omitempty"`
	TenantID             bson.ObjectId `bson:"tenant_id" json:"tenant_id,omitempty"`
	ContactID            bson.ObjectId `bson:"contact_id" json:"contact_id,omitempty"`
	OfferID              bson.ObjectId `bson:"offer_id" json:"offer_id,omitempty"`
	StripeSubscriptionID string        `bson:"stripe_subscription_id,omitempty" json:"stripe_subscription_id,omitempty"`
	Status               string        `bson:"status" json:"status,omitempty"`
	models.SoftDeletes `bson:"timestamps,omitempty" json:"timestamps,omitempty"`
}

func NewSubscription(tenantID, contactID, offerID bson.ObjectId) *Subscription {
	return &Subscription{
		Id:        bson.NewObjectId(),
		PublicId:  utils.GeneratePublicId(),
		TenantID:  tenantID,
		ContactID: contactID,
		OfferID:   offerID,
		Status:    "active",
	}
}

func (s *Subscription) ReadyMongoStore() []interface{} {
	return []interface{}{*s}
}

func (s *Subscription) GetIdHex() string {
	return s.Id.Hex()
}

func (s *Subscription) GetId() bson.ObjectId {
	return s.Id
}
