// Package funnel hosts the template materializer: given a FunnelTemplate,
// AI-generated slot output, and a target (domain + path + optional form), it
// produces a saved FunnelPage with its Funnel→Route→Stage parent chain and
// returns the public URL the page will be served at.
package funnel

import (
	"encoding/json"
	"fmt"
	"html"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/utils"
)

// MaterializeRequest is the typed input the materializer consumes.
// LLMOutput is expected to carry a "slots" map (the funnel-ai endpoint emits
// JSON with that key); StructuredInputs are tenant-supplied hard-data values
// (lead_magnet_url, transcript_url, …) that take precedence over LLM output
// for any matching slot key.
type MaterializeRequest struct {
	Template         *pkgmodels.FunnelTemplate
	LLMOutput        map[string]interface{}
	StructuredInputs map[string]interface{}
	FormPublicID     string
	DomainID         string // TenantDomain public_id; empty → use raw Hostname
	Hostname         string // overrides DomainID lookup
	Path             string // e.g. "/lead/coaching"; defaults to "/"
	Publish          bool
	Name             string // optional human label for the saved page
}

// MaterializeResult tells the caller where to find the new page.
type MaterializeResult struct {
	PageID         string `json:"page_id"`
	PagePublicID   string `json:"page_public_id"`
	FunnelID       string `json:"funnel_id"`
	FunnelPublicID string `json:"funnel_public_id"`
	URL            string `json:"url"`
	RenderedHTML   string `json:"rendered_html,omitempty"`
}

// Materialize executes the full pipeline. The template is required; the
// remainder of the request can be sparse (a bare LLMOutput with no form/
// domain still produces a saved page record).
func Materialize(tenantID bson.ObjectId, req MaterializeRequest) (*MaterializeResult, error) {
	if req.Template == nil {
		return nil, fmt.Errorf("template required")
	}

	hostname, err := resolveHostname(tenantID, req.DomainID, req.Hostname)
	if err != nil {
		return nil, err
	}
	path := strings.TrimSpace(req.Path)
	if path == "" {
		path = "/"
	}

	slotValues := mergeSlotValues(req.LLMOutput, req.StructuredInputs)

	rendered := renderTemplate(req.Template, slotValues)

	// Apply kind-specific behavior rules. Without these the runtime is purely
	// "whatever was in the source HTML" — checkout pages need a real Buy CTA,
	// thank-yous need an order confirmation block, webinars need a video
	// placeholder, lead magnets need the asset download link. Each rule is a
	// no-op when its required structured input is absent so legacy templates
	// still work.
	rendered = applyKindRules(tenantID, req.Template, slotValues, rendered)

	// Inject form HTML (if any) into the rendered output.
	if req.FormPublicID != "" {
		formHTML, fErr := buildFormHTML(tenantID, req.FormPublicID, slotValues, hostname, path)
		if fErr != nil {
			log.Printf("materialize: build form html: %v", fErr)
		} else {
			rendered = injectForm(rendered, formHTML)
		}
	}

	// Persist the Funnel→Route→Stage→Page chain. Each materialization gets a
	// fresh Funnel so concurrent materializations don't race on the same
	// document. The site renderer matches by domain (not by funnel id), so
	// the latest publish wins for that hostname.
	page := pkgmodels.NewFunnelPage()
	page.TenantID = tenantID
	page.Name = req.Name
	if page.Name == "" {
		page.Name = req.Template.Name
	}
	page.TemplateName = req.Template.Name
	if req.Publish {
		page.RenderedHTML = rendered
	}

	stage := pkgmodels.NewFunnelStage()
	stage.TenantID = tenantID
	stage.Name = page.Name
	stage.Path = path
	stage.Pages = []*pkgmodels.FunnelPage{page}
	page.StageId = stage.Id

	route := pkgmodels.NewFunnelRoute()
	route.TenantID = tenantID
	route.Name = "default"
	route.Stages = []*pkgmodels.FunnelStage{stage}

	funnel := pkgmodels.NewFunnel()
	funnel.TenantID = tenantID
	funnel.Name = page.Name
	funnel.Domain = hostname
	funnel.Routes = []*pkgmodels.FunnelRoute{route}

	now := time.Now()
	funnel.SoftDeletes.CreatedAt = &now
	route.SoftDeletes.CreatedAt = &now
	stage.SoftDeletes.CreatedAt = &now
	page.SoftDeletes.CreatedAt = &now
	route.FunnelId = funnel.Id
	stage.RouteId = route.Id

	for _, entity := range funnel.ReadyMongoStore() {
		coll := resolveFunnelCollection(entity)
		if err := db.GetCollection(coll).Insert(entity); err != nil {
			return nil, fmt.Errorf("insert %s: %w", coll, err)
		}
	}

	url := buildPublicURL(hostname, path)
	return &MaterializeResult{
		PageID:         page.Id.Hex(),
		PagePublicID:   page.PublicId,
		FunnelID:       funnel.Id.Hex(),
		FunnelPublicID: funnel.PublicId,
		URL:            url,
		RenderedHTML:   rendered,
	}, nil
}

// resolveHostname maps the request's DomainID (TenantDomain public_id) to a
// hostname. Falls back to the literal Hostname value if DomainID is empty
// (useful for previews against lvh.me / localhost).
func resolveHostname(tenantID bson.ObjectId, domainID, fallback string) (string, error) {
	if domainID != "" {
		var d pkgmodels.TenantDomain
		if err := db.GetCollection(pkgmodels.DomainCollection).Find(bson.M{
			"public_id":             domainID,
			"tenant_id":             tenantID,
			"timestamps.deleted_at": nil,
		}).One(&d); err != nil {
			return "", fmt.Errorf("domain %s not found", domainID)
		}
		return d.Hostname, nil
	}
	if h := strings.TrimSpace(fallback); h != "" {
		return h, nil
	}
	return "", fmt.Errorf("either domain_id or hostname is required")
}

// mergeSlotValues unions LLM output slots with caller-provided structured
// inputs. Structured inputs take priority on overlap so tenants can pin
// hard-data fields (urls, ids) the LLM shouldn't invent.
func mergeSlotValues(llmOutput, structured map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{}

	if llmOutput != nil {
		// The funnel-ai handler returns a top-level "slots" key when present;
		// fall back to merging the whole map so callers can also pass a flat
		// shape.
		if slots, ok := llmOutput["slots"].(map[string]interface{}); ok {
			for k, v := range slots {
				out[k] = v
			}
		} else {
			for k, v := range llmOutput {
				if k == "slots" {
					continue
				}
				out[k] = v
			}
		}
	}
	for k, v := range structured {
		out[k] = v
	}
	return out
}

// slotPlaceholder matches Handlebars-style `{{ key }}` placeholders. Keys may
// contain dots, brackets, and digits to match the importer's slot grammar
// (e.g. `page.bio.paragraphs[0]`).
var slotPlaceholder = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_.\[\]\-]+)\s*\}\}`)

// renderTemplate substitutes every `{{ key }}` placeholder in the template's
// HTMLContent with the matching slot value. Unknown keys are blanked rather
// than left visible to avoid leaking template syntax to public visitors.
// Array placeholders use bracket notation: `paragraphs[0]` resolves the first
// element of the `paragraphs` array.
func renderTemplate(tmpl *pkgmodels.FunnelTemplate, values map[string]interface{}) string {
	html := tmpl.HTMLContent
	if tmpl.GlobalCSS != "" && !strings.Contains(html, "<style") {
		html = "<style>" + tmpl.GlobalCSS + "</style>\n" + html
	}
	out := slotPlaceholder.ReplaceAllStringFunc(html, func(match string) string {
		key := strings.TrimSpace(match[2 : len(match)-2])
		v := lookupSlot(values, key)
		if v == nil {
			return ""
		}
		return slotValueToString(v)
	})
	return out
}

// lookupSlot resolves a dotted/indexed slot key against the values map.
// Tries the literal key first (so importer manifests with verbatim keys like
// `page.hero.headline` work), then walks dotted segments and bracket
// indexing.
func lookupSlot(values map[string]interface{}, key string) interface{} {
	if v, ok := values[key]; ok {
		return v
	}
	// Walk segments. `page.bio.paragraphs[0]` → ["page","bio","paragraphs[0]"].
	parts := strings.Split(key, ".")
	var cur interface{} = values
	for _, part := range parts {
		idx := -1
		if openIdx := strings.Index(part, "["); openIdx > -1 && strings.HasSuffix(part, "]") {
			if n, err := strconv.Atoi(part[openIdx+1 : len(part)-1]); err == nil {
				idx = n
			}
			part = part[:openIdx]
		}
		switch t := cur.(type) {
		case map[string]interface{}:
			cur = t[part]
		default:
			return nil
		}
		if idx >= 0 {
			arr, ok := cur.([]interface{})
			if !ok || idx >= len(arr) {
				return nil
			}
			cur = arr[idx]
		}
	}
	return cur
}

func slotValueToString(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case bool:
		return strconv.FormatBool(t)
	case nil:
		return ""
	default:
		// Lists, objects: emit JSON so the downstream HTML still parses (the
		// tenant should pick a typed slot for arrays/maps; this is safety).
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// buildFormHTML renders a server-side form whose POST hits the canonical
// public submit endpoint with the form's public_id pre-bound. Field names
// match PageForm.Fields[].FieldName so the executor can run MapsTo.
func buildFormHTML(tenantID bson.ObjectId, formPublicID string, slotValues map[string]interface{}, hostname, path string) (string, error) {
	var form pkgmodels.PageForm
	if err := db.GetCollection(pkgmodels.PageFormCollection).Find(bson.M{
		"public_id":             formPublicID,
		"tenant_id":             tenantID,
		"timestamps.deleted_at": nil,
	}).One(&form); err != nil {
		return "", fmt.Errorf("form %s not found", formPublicID)
	}

	var sb strings.Builder
	// Materialized forms post form-encoded so a static HTML page works
	// without JS. The handler accepts both JSON and form-encoded bodies.
	sb.WriteString(`<form class="sentanyl-form" method="POST" action="/api/marketing/site/form/submit">`)
	sb.WriteString(`<input type="hidden" name="form_id" value="`)
	sb.WriteString(html.EscapeString(form.PublicId))
	sb.WriteString(`">`)
	if next, ok := slotValues["form.redirect_url"].(string); ok && next != "" {
		sb.WriteString(`<input type="hidden" name="next_url" value="`)
		sb.WriteString(html.EscapeString(next))
		sb.WriteString(`">`)
	}
	for _, f := range form.Fields {
		if f == nil || f.FieldName == "" {
			continue
		}
		sb.WriteString(`<label class="sentanyl-form-field">`)
		sb.WriteString(html.EscapeString(humanize(f.FieldName)))
		inputType := "text"
		switch strings.ToLower(f.FieldType) {
		case "email":
			inputType = "email"
		case "number":
			inputType = "number"
		case "tel", "phone":
			inputType = "tel"
		}
		sb.WriteString(`<input type="`)
		sb.WriteString(inputType)
		sb.WriteString(`" name="`)
		sb.WriteString(html.EscapeString(f.FieldName))
		sb.WriteString(`"`)
		if f.Required {
			sb.WriteString(` required`)
		}
		sb.WriteString(`/></label>`)
	}
	sb.WriteString(`<button type="submit">Submit</button>`)
	sb.WriteString(`</form>`)
	return sb.String(), nil
}

func humanize(s string) string {
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// injectForm puts the form HTML where the template expects it. Honors a
// `{{form_html}}` placeholder when present; otherwise appends just before
// </body>; otherwise appends to the end of the document.
func injectForm(rendered, formHTML string) string {
	if strings.Contains(rendered, "{{form_html}}") {
		return strings.ReplaceAll(rendered, "{{form_html}}", formHTML)
	}
	if i := strings.LastIndex(rendered, "</body>"); i >= 0 {
		return rendered[:i] + formHTML + rendered[i:]
	}
	return rendered + formHTML
}

// buildPublicURL builds the externally-visible URL, defaulting to https for
// real hostnames and http for the local-dev *.lvh.me / localhost shortcuts.
func buildPublicURL(hostname, path string) string {
	scheme := "https"
	if strings.Contains(hostname, "lvh.me") || strings.Contains(hostname, "localhost") {
		scheme = "http"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return scheme + "://" + hostname + path
}

// resolveFunnelCollection mirrors marketing-service/routes/internal.go's
// resolveCollection but is duplicated here to avoid a circular import.
// It only handles the entity types the materializer can emit.
func resolveFunnelCollection(entity interface{}) string {
	switch entity.(type) {
	case pkgmodels.Funnel:
		return pkgmodels.FunnelCollection
	case pkgmodels.FunnelRoute:
		return pkgmodels.FunnelRouteCollection
	case pkgmodels.FunnelStage:
		return pkgmodels.FunnelStageCollection
	case pkgmodels.FunnelPage:
		return pkgmodels.FunnelPageCollection
	case pkgmodels.PageBlock:
		return pkgmodels.PageBlockCollection
	case pkgmodels.PageForm:
		return pkgmodels.PageFormCollection
	case pkgmodels.Trigger:
		return pkgmodels.TriggerCollection
	case pkgmodels.Action:
		return pkgmodels.ActionCollection
	}
	// Fall through: best-effort use the type name lowercased + "s".
	return fmt.Sprintf("%T", entity)
}

// silence unused import in case a future helper drops one of these packages
var _ = utils.GeneratePublicId
