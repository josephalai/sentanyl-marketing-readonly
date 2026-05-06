package funnel

import (
	"fmt"
	"html"
	"strings"

	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// applyKindRules layers semantic behavior on top of the slot-substituted HTML
// based on the template's TemplateKind. Each rule is additive when its
// required structured input is missing — never breaks an otherwise-valid
// page.
//
// The recognized kinds map to constants on pkgmodels (TemplateKindCheckout,
// TemplateKindThankYou, TemplateKindWebinar, TemplateKindLeadMagnet). New
// kinds should add a case here rather than spreading kind logic across the
// codebase.
func applyKindRules(tenantID bson.ObjectId, tmpl *pkgmodels.FunnelTemplate, slots map[string]interface{}, rendered string) string {
	if tmpl == nil {
		return rendered
	}
	switch strings.ToLower(strings.TrimSpace(tmpl.TemplateKind)) {
	case pkgmodels.TemplateKindCheckout:
		return applyCheckoutRule(tenantID, slots, rendered)
	case pkgmodels.TemplateKindThankYou:
		return applyThankYouRule(slots, rendered)
	case pkgmodels.TemplateKindWebinar:
		return applyWebinarRule(slots, rendered)
	case pkgmodels.TemplateKindLeadMagnet:
		return applyLeadMagnetRule(tenantID, slots, rendered)
	}
	return rendered
}

// applyCheckoutRule wires the primary CTA on a checkout page to a real Stripe
// Checkout call. Resolves the offer from a structured input named
// `offer_id` (preferred) or `attach_offer_id` (mirrors the form executor).
// If the placeholder `{{checkout_cta}}` is present it gets replaced;
// otherwise the rule appends the snippet just before </body>.
func applyCheckoutRule(tenantID bson.ObjectId, slots map[string]interface{}, rendered string) string {
	offerID := stringSlot(slots, "offer_id")
	if offerID == "" {
		offerID = stringSlot(slots, "attach_offer_id")
	}
	if offerID == "" {
		return rendered
	}
	// Resolve the offer to its hex id; the offer_id slot can carry either
	// public_id or hex.
	hex, title, ok := resolveOfferIdentity(tenantID, offerID)
	if !ok {
		return rendered
	}
	cta := buildCheckoutCTA(hex, title)
	if strings.Contains(rendered, "{{checkout_cta}}") {
		return strings.ReplaceAll(rendered, "{{checkout_cta}}", cta)
	}
	return appendBeforeBody(rendered, cta)
}

func resolveOfferIdentity(tenantID bson.ObjectId, idOrPublicID string) (string, string, bool) {
	q := bson.M{"tenant_id": tenantID, "timestamps.deleted_at": nil}
	if bson.IsObjectIdHex(idOrPublicID) {
		q["_id"] = bson.ObjectIdHex(idOrPublicID)
	} else {
		q["public_id"] = idOrPublicID
	}
	var offer pkgmodels.Offer
	if err := db.GetCollection(pkgmodels.OfferCollection).Find(q).One(&offer); err != nil {
		return "", "", false
	}
	return offer.Id.Hex(), offer.Title, true
}

func buildCheckoutCTA(offerHex, title string) string {
	var sb strings.Builder
	sb.WriteString(`<div class="sentanyl-checkout-cta" data-offer-id="`)
	sb.WriteString(html.EscapeString(offerHex))
	sb.WriteString(`">`)
	sb.WriteString(`<form onsubmit="return sentanylStartCheckout(event,this)">`)
	sb.WriteString(`<input type="email" name="email" placeholder="you@example.com" required autocomplete="email" />`)
	sb.WriteString(fmt.Sprintf(`<button type="submit">Buy %s</button>`, html.EscapeString(title)))
	sb.WriteString(`<div class="sentanyl-checkout-msg" data-role="msg"></div>`)
	sb.WriteString(`</form></div>`)
	sb.WriteString(`<script>
window.sentanylStartCheckout = window.sentanylStartCheckout || function(ev, form){
  ev.preventDefault();
  var box = form.closest('.sentanyl-checkout-cta');
  var msg = form.querySelector('[data-role=msg]');
  msg.textContent = 'Starting checkout…';
  fetch('/api/marketing/site/checkout/start', {
    method:'POST', headers:{'Content-Type':'application/json'},
    body: JSON.stringify({offer_id: box.getAttribute('data-offer-id'), domain: location.host, email: form.email.value.trim()})
  }).then(function(r){ return r.json().then(function(j){ return {status:r.status, body:j}; }); })
    .then(function(res){
      var j = res.body;
      if (res.status === 200 && j.checkout_url) { window.location.href = j.checkout_url; return; }
      if (res.status === 409 && j.redirect_url) { window.location.href = j.redirect_url; return; }
      msg.textContent = (j && j.error) || 'Checkout failed — try again.';
    })
    .catch(function(){ msg.textContent = 'Network error — try again.'; });
  return false;
};
</script>`)
	return sb.String()
}

// applyThankYouRule injects a confirmation banner with optional auto-redirect
// when `redirect_url` is provided. The banner is appended only when the
// template doesn't already opt into a `{{thank_you_block}}` placeholder.
func applyThankYouRule(slots map[string]interface{}, rendered string) string {
	redirect := stringSlot(slots, "redirect_url")
	headline := stringSlot(slots, "thank_you_headline")
	if headline == "" {
		headline = "Thanks — you're all set."
	}
	body := stringSlot(slots, "thank_you_body")
	if body == "" && redirect == "" {
		return rendered
	}
	var sb strings.Builder
	sb.WriteString(`<div class="sentanyl-thank-you-block">`)
	sb.WriteString(fmt.Sprintf(`<h2>%s</h2>`, html.EscapeString(headline)))
	if body != "" {
		sb.WriteString(fmt.Sprintf(`<p>%s</p>`, html.EscapeString(body)))
	}
	if redirect != "" {
		sb.WriteString(fmt.Sprintf(`<p><a href="%s">Continue →</a></p>`, html.EscapeString(redirect)))
		// Soft auto-redirect after a short pause; the link is the fallback for
		// users with JS disabled or who prefer a manual click.
		sb.WriteString(fmt.Sprintf(`<script>setTimeout(function(){window.location.href=%q;}, 4000);</script>`, redirect))
	}
	sb.WriteString(`</div>`)
	if strings.Contains(rendered, "{{thank_you_block}}") {
		return strings.ReplaceAll(rendered, "{{thank_you_block}}", sb.String())
	}
	return appendBeforeBody(rendered, sb.String())
}

// applyWebinarRule injects a video placeholder keyed off the structured input
// `video_id`. video-service is responsible for the eventual playback URL;
// this rule only ensures the embed slot is rendered.
func applyWebinarRule(slots map[string]interface{}, rendered string) string {
	videoID := stringSlot(slots, "video_id")
	if videoID == "" {
		return rendered
	}
	embed := fmt.Sprintf(`<div class="sentanyl-webinar-embed" data-video-id="%s"><iframe src="/api/video/embed/%s" allowfullscreen></iframe></div>`,
		html.EscapeString(videoID), html.EscapeString(videoID))
	if strings.Contains(rendered, "{{webinar_embed}}") {
		return strings.ReplaceAll(rendered, "{{webinar_embed}}", embed)
	}
	return appendBeforeBody(rendered, embed)
}

// applyLeadMagnetRule renders a signed download link for the asset specified
// by structured input `attach_asset_id`. The actual signing happens later
// when the lead submits the form (the form executor's deliver-download
// path). Until then we render a placeholder anchor that the form submit
// handler replaces post-conversion.
func applyLeadMagnetRule(tenantID bson.ObjectId, slots map[string]interface{}, rendered string) string {
	assetID := stringSlot(slots, "attach_asset_id")
	if assetID == "" {
		return rendered
	}
	// Confirm the asset exists for this tenant so we never render a stale
	// reference.
	var product pkgmodels.Product
	q := bson.M{"tenant_id": tenantID, "timestamps.deleted_at": nil}
	if bson.IsObjectIdHex(assetID) {
		q["_id"] = bson.ObjectIdHex(assetID)
	} else {
		q["public_id"] = assetID
	}
	if err := db.GetCollection(pkgmodels.ProductCollection).Find(q).One(&product); err != nil {
		return rendered
	}
	link := fmt.Sprintf(`<div class="sentanyl-lead-magnet-block" data-asset-id="%s"><p>Your free %s will be available right after you submit the form below.</p></div>`,
		html.EscapeString(product.Id.Hex()), html.EscapeString(product.Name))
	if strings.Contains(rendered, "{{lead_magnet_block}}") {
		return strings.ReplaceAll(rendered, "{{lead_magnet_block}}", link)
	}
	return appendBeforeBody(rendered, link)
}

// stringSlot reads a top-level slot string. Returns "" for any other type;
// callers should rely on emptiness as the no-op signal.
func stringSlot(slots map[string]interface{}, key string) string {
	if v, ok := slots[key]; ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// appendBeforeBody is the conventional injection point used by kind rules and
// form injection alike — the snippet lands inside <body> when present, falls
// back to the document end otherwise.
func appendBeforeBody(rendered, snippet string) string {
	if i := strings.LastIndex(rendered, "</body>"); i >= 0 {
		return rendered[:i] + snippet + rendered[i:]
	}
	return rendered + snippet
}
