package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	mgo "gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/auth"
	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// provisionProductPurchase routes a purchased product to the right
// fulfillment system. Idempotency is keyed on the PurchaseItem (FUL-001):
// a webhook retry reuses the allocation, a genuine repurchase (new item)
// creates a fresh one.
func provisionProductPurchase(tenantID, contactID, productID, offerID, purchaseItemID bson.ObjectId) error {
	var product pkgmodels.Product
	if err := db.GetCollection(pkgmodels.ProductCollection).FindId(productID).One(&product); err != nil {
		return fmt.Errorf("product lookup: %w", err)
	}
	switch product.ProductType {
	case pkgmodels.ProductTypeCourse:
		return callInternalEnroll(tenantID, contactID, productID, offerID, purchaseItemID)
	case pkgmodels.ProductTypeService:
		return provisionServiceEnrollment(tenantID, contactID, &product, offerID, purchaseItemID)
	case pkgmodels.ProductTypeCoaching:
		return provisionCoachingEnrollment(tenantID, contactID, &product, offerID, purchaseItemID)
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
// purchase and its per-template ServiceInstance slots.
//
// FUL-001: idempotency is keyed on the PurchaseItem when present, so a
// repurchase creates a new enrollment. FUL-002: instance slots are upserted
// by their stable (enrollment, template) identity and the route fails —
// letting the webhook retry and repair — unless every expected slot exists;
// success is never reported over missing instances.
func provisionServiceEnrollment(tenantID, contactID bson.ObjectId, product *pkgmodels.Product, offerID, purchaseItemID bson.ObjectId) error {
	col := db.GetCollection(pkgmodels.ServiceEnrollmentCollection)

	var query bson.M
	if purchaseItemID.Valid() {
		query = bson.M{"purchase_item_id": purchaseItemID}
	} else {
		// Legacy callers without a purchase item keep the old collapse.
		query = bson.M{
			"tenant_id":             tenantID,
			"contact_id":            contactID,
			"product_id":            product.Id,
			"offer_id":              offerID,
			"timestamps.deleted_at": nil,
		}
	}
	now := time.Now()
	var enrollment pkgmodels.ServiceEnrollment
	err := col.Find(query).One(&enrollment)
	if err != nil {
		instances := 1
		if product.Service != nil && len(product.Service.InstanceTemplates) > 0 {
			instances = len(product.Service.InstanceTemplates)
		}
		fresh := pkgmodels.NewServiceEnrollment(tenantID, contactID, product.Id, product.PublicId, instances)
		fresh.OfferID = offerID
		fresh.PurchaseItemID = purchaseItemID
		fresh.Status = "provisioning"
		fresh.SoftDeletes.CreatedAt = &now
		if ierr := col.Insert(fresh); ierr != nil {
			if !mgo.IsDup(ierr) {
				return fmt.Errorf("insert service enrollment: %w", ierr)
			}
			if ferr := col.Find(query).One(&enrollment); ferr != nil {
				return fmt.Errorf("service enrollment lost race: %w", ferr)
			}
		} else {
			enrollment = *fresh
		}
	}

	// Upsert every expected instance slot by its stable identity. Retries
	// repair whatever a crashed earlier attempt failed to create.
	var slotErrs []string
	if product.Service != nil {
		instCol := db.GetCollection(pkgmodels.ServiceInstanceCollection)
		for _, tmpl := range product.Service.InstanceTemplates {
			if tmpl == nil {
				continue
			}
			inst := pkgmodels.NewServiceInstance(tenantID, product.Id, enrollment.Id, contactID, tmpl.Id, tmpl.Order, tmpl.Title)
			if len(product.Service.IntakeQuestions) > 0 {
				// FUL-015: products with intake questions hold fulfillment
				// until the customer submits their intake.
				inst.Status = pkgmodels.ServiceInstanceStatusAwaitingIntake
			}
			inst.SoftDeletes.CreatedAt = &now
			if _, uerr := instCol.Upsert(bson.M{
				"enrollment_id":        enrollment.Id,
				"instance_template_id": tmpl.Id,
			}, bson.M{"$setOnInsert": inst}); uerr != nil {
				slotErrs = append(slotErrs, fmt.Sprintf("template %s: %v", tmpl.Id.Hex(), uerr))
			}
		}
	}
	if len(slotErrs) > 0 {
		_ = col.UpdateId(enrollment.Id, bson.M{"$set": bson.M{"status": "provisioning_failed", "timestamps.updated_at": now}})
		return fmt.Errorf("service instance provisioning incomplete: %s", strings.Join(slotErrs, "; "))
	}
	// All expected slots exist — the allocation is (re)active. Also covers
	// refund-then-rebuy re-activation for legacy-keyed enrollments.
	_ = col.UpdateId(enrollment.Id, bson.M{
		"$set":   bson.M{"status": "active", "timestamps.updated_at": now},
		"$unset": bson.M{"revoked_at": 1},
	})
	return nil
}

// provisionCoachingEnrollment notifies coaching-service that a coaching
// product was purchased. The coaching service owns the coaching enrollment
// shape (it tracks per-program session caps and Calendly state); the
// webhook just hands off (tenant, contact, product, offer) and lets that
// service decide what rows to create.
func provisionCoachingEnrollment(tenantID, contactID bson.ObjectId, product *pkgmodels.Product, offerID, purchaseItemID bson.ObjectId) error {
	url := os.Getenv("COACHING_SERVICE_URL")
	if url == "" {
		url = "http://coaching-service:8086"
	}
	payload := map[string]string{
		"tenant_id":  tenantID.Hex(),
		"contact_id": contactID.Hex(),
		"product_id": product.Id.Hex(),
		"offer_id":   offerID.Hex(),
	}
	if purchaseItemID.Valid() {
		payload["purchase_item_id"] = purchaseItemID.Hex()
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, url+"/internal/provision-coaching", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("coaching provision request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	auth.AttachServiceAuth(req, "marketing") // API-001 signed service identity
	resp, err := http.DefaultClient.Do(req)
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
func revokeProductEntitlements(tenantID, contactID, productID bson.ObjectId, offerID, purchaseItemID bson.ObjectId) {
	var product pkgmodels.Product
	if err := db.GetCollection(pkgmodels.ProductCollection).FindId(productID).One(&product); err != nil {
		log.Printf("[stripe webhook] revoke: product %s lookup: %v", productID.Hex(), err)
		return
	}
	now := time.Now()
	switch product.ProductType {
	case pkgmodels.ProductTypeService:
		q := bson.M{"tenant_id": tenantID, "purchase_item_id": purchaseItemID}
		_, _ = db.GetCollection(pkgmodels.ServiceEnrollmentCollection).UpdateAll(
			q,
			bson.M{"$set": bson.M{"status": "revoked", "revoked_at": now}},
		)
	case pkgmodels.ProductTypeCoaching:
		url := os.Getenv("COACHING_SERVICE_URL")
		if url == "" {
			url = "http://coaching-service:8086"
		}
		body, _ := json.Marshal(map[string]string{
			"tenant_id":        tenantID.Hex(),
			"contact_id":       contactID.Hex(),
			"product_id":       productID.Hex(),
			"offer_id":         offerID.Hex(),
			"purchase_item_id": purchaseItemID.Hex(),
		})
		req, err := http.NewRequest(http.MethodPost, url+"/internal/revoke-coaching", bytes.NewReader(body))
		if err != nil {
			log.Printf("[stripe webhook] revoke coaching: %v", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		auth.AttachServiceAuth(req, "marketing") // API-001 signed service identity
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Printf("[stripe webhook] revoke coaching: %v", err)
			return
		}
		defer resp.Body.Close()
	}
}
