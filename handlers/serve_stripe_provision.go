package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// provisionProductPurchase routes a purchased product to the right
// fulfillment system. Idempotent: every branch upserts on
// (tenant, contact, product) so Stripe retries don't double-provision.
func provisionProductPurchase(tenantID, contactID, productID, offerID bson.ObjectId) error {
	var product pkgmodels.Product
	if err := db.GetCollection(pkgmodels.ProductCollection).FindId(productID).One(&product); err != nil {
		return fmt.Errorf("product lookup: %w", err)
	}
	switch product.ProductType {
	case pkgmodels.ProductTypeCourse:
		return callInternalEnroll(tenantID, contactID, productID)
	case pkgmodels.ProductTypeService:
		return provisionServiceEnrollment(tenantID, contactID, &product, offerID)
	case pkgmodels.ProductTypeCoaching:
		return provisionCoachingEnrollment(tenantID, contactID, &product, offerID)
	case pkgmodels.ProductTypeDigitalDownload:
		// Downloads gain access via the badge grant earlier in the webhook;
		// no extra row is required here. Returning nil keeps the loop happy.
		return nil
	case pkgmodels.ProductTypeNewsletter:
		// Newsletter tier upgrade fires earlier in the webhook for any offer
		// bound to a newsletter tier. No additional provisioning needed.
		return nil
	}
	// Unknown type — surface so the caller can log without bringing the
	// whole webhook down.
	return fmt.Errorf("unsupported product_type %q for product %s", product.ProductType, productID.Hex())
}

// provisionServiceEnrollment writes a ServiceEnrollment for a service product
// purchase. Counts every ServiceInstanceTemplate the product declares so the
// customer dashboard knows the cap.
func provisionServiceEnrollment(tenantID, contactID bson.ObjectId, product *pkgmodels.Product, offerID bson.ObjectId) error {
	col := db.GetCollection(pkgmodels.ServiceEnrollmentCollection)

	// Idempotent on (tenant, contact, product, offer) so a Stripe retry
	// doesn't write a duplicate enrollment.
	var existing pkgmodels.ServiceEnrollment
	err := col.Find(bson.M{
		"tenant_id":             tenantID,
		"contact_id":            contactID,
		"product_id":            product.Id,
		"offer_id":              offerID,
		"timestamps.deleted_at": nil,
	}).One(&existing)
	if err == nil {
		// If the previous attempt revoked the enrollment (refund then re-buy),
		// re-activate.
		if existing.Status != "active" {
			now := time.Now()
			_ = col.UpdateId(existing.Id, bson.M{
				"$set":   bson.M{"status": "active", "timestamps.updated_at": now},
				"$unset": bson.M{"revoked_at": 1},
			})
		}
		return nil
	}

	instances := 1
	if product.Service != nil && len(product.Service.InstanceTemplates) > 0 {
		instances = len(product.Service.InstanceTemplates)
	}
	enrollment := pkgmodels.NewServiceEnrollment(tenantID, contactID, product.Id, product.PublicId, instances)
	enrollment.OfferID = offerID
	now := time.Now()
	enrollment.SoftDeletes.CreatedAt = &now
	if err := col.Insert(enrollment); err != nil {
		return fmt.Errorf("insert service enrollment: %w", err)
	}

	// Pre-allocate one ServiceInstance row per template so the customer
	// dashboard has scheduled fulfillment items to display.
	if product.Service != nil {
		for _, tmpl := range product.Service.InstanceTemplates {
			if tmpl == nil {
				continue
			}
			inst := pkgmodels.NewServiceInstance(tenantID, product.Id, enrollment.Id, contactID, tmpl.Id, tmpl.Order, tmpl.Title)
			inst.SoftDeletes.CreatedAt = &now
			if err := db.GetCollection(pkgmodels.ServiceInstanceCollection).Insert(inst); err != nil {
				log.Printf("[stripe webhook] service instance insert: %v", err)
			}
		}
	}
	return nil
}

// provisionCoachingEnrollment notifies coaching-service that a coaching
// product was purchased. The coaching service owns the coaching enrollment
// shape (it tracks per-program session caps and Calendly state); the
// webhook just hands off (tenant, contact, product, offer) and lets that
// service decide what rows to create.
func provisionCoachingEnrollment(tenantID, contactID bson.ObjectId, product *pkgmodels.Product, offerID bson.ObjectId) error {
	url := os.Getenv("COACHING_SERVICE_URL")
	if url == "" {
		url = "http://coaching-service:8086"
	}
	body, _ := json.Marshal(map[string]string{
		"tenant_id":  tenantID.Hex(),
		"contact_id": contactID.Hex(),
		"product_id": product.Id.Hex(),
		"offer_id":   offerID.Hex(),
	})
	resp, err := http.Post(url+"/internal/provision-coaching", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("coaching provision request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("coaching provision returned %d", resp.StatusCode)
	}
	return nil
}

// revokeProductEntitlements is the refund-side counterpart. Mirrors the
// dispatch in provisionProductPurchase so a refund undoes whatever a
// purchase created. callable from the charge.refunded handler.
func revokeProductEntitlements(tenantID, contactID, productID bson.ObjectId, offerID bson.ObjectId) {
	var product pkgmodels.Product
	if err := db.GetCollection(pkgmodels.ProductCollection).FindId(productID).One(&product); err != nil {
		log.Printf("[stripe webhook] revoke: product %s lookup: %v", productID.Hex(), err)
		return
	}
	now := time.Now()
	switch product.ProductType {
	case pkgmodels.ProductTypeService:
		_, _ = db.GetCollection(pkgmodels.ServiceEnrollmentCollection).UpdateAll(
			bson.M{
				"tenant_id":  tenantID,
				"contact_id": contactID,
				"product_id": productID,
				"offer_id":   offerID,
			},
			bson.M{"$set": bson.M{"status": "revoked", "revoked_at": now}},
		)
	case pkgmodels.ProductTypeCoaching:
		url := os.Getenv("COACHING_SERVICE_URL")
		if url == "" {
			url = "http://coaching-service:8086"
		}
		body, _ := json.Marshal(map[string]string{
			"tenant_id":  tenantID.Hex(),
			"contact_id": contactID.Hex(),
			"product_id": productID.Hex(),
			"offer_id":   offerID.Hex(),
		})
		resp, err := http.Post(url+"/internal/revoke-coaching", "application/json", bytes.NewReader(body))
		if err != nil {
			log.Printf("[stripe webhook] revoke coaching: %v", err)
			return
		}
		defer resp.Body.Close()
	}
}
