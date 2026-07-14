package migration

import (
	"strings"

	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/db"
	models "github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/utils"
)

// MIG-009: Kajabi automations require SEMANTIC translation, not blind copy.
// automations.csv rows are translated through an explicit feasibility table;
// feasible automations become DRAFT Story shells the tenant finishes in the
// builder, infeasible ones are reported per-item with the reason. Nothing
// activates automatically.

// SourceAutomation is one parsed automations.csv row.
type SourceAutomation struct {
	SourceID     string
	Name         string
	Trigger      string // e.g. "form_submitted", "tag_added", "offer_purchased"
	TriggerValue string
	Actions      []string // e.g. "send_email:Welcome", "add_tag:vip"
	Row          int
}

// ParseAutomations parses automations.csv. `actions` is a semicolon list of
// "verb:argument" pairs.
func ParseAutomations(content []byte) ([]SourceAutomation, []ParseError) {
	rows, err := readCSV(content)
	if err != nil || len(rows) == 0 {
		return nil, []ParseError{{Kind: "automations", Message: "unreadable CSV: " + errString(err)}}
	}
	h := rows[0]
	iID := headerIndex(h, "id", "automation_id")
	iName := headerIndex(h, "name", "title", "automation_name")
	iTrigger := headerIndex(h, "trigger", "when", "trigger_type")
	iTriggerVal := headerIndex(h, "trigger_value", "trigger_ref", "when_value")
	iActions := headerIndex(h, "actions", "then", "steps")
	if iName < 0 || iTrigger < 0 {
		return nil, []ParseError{{Kind: "automations", Message: "name and trigger columns are required"}}
	}
	var out []SourceAutomation
	var errs []ParseError
	for n, rec := range rows[1:] {
		row := n + 2
		name := cell(rec, iName)
		if name == "" {
			errs = append(errs, ParseError{Kind: "automations", Row: row, Message: "missing automation name"})
			continue
		}
		a := SourceAutomation{Name: name, Row: row}
		a.SourceID = cell(rec, iID)
		if a.SourceID == "" {
			a.SourceID = strings.ToLower(name)
		}
		a.Trigger = strings.ToLower(strings.ReplaceAll(cell(rec, iTrigger), " ", "_"))
		a.TriggerValue = cell(rec, iTriggerVal)
		for _, act := range strings.Split(cell(rec, iActions), ";") {
			if act = strings.TrimSpace(act); act != "" {
				a.Actions = append(a.Actions, act)
			}
		}
		out = append(out, a)
	}
	return out, errs
}

// Feasibility tables: what the Story/SentanylScript engine can express today.
var supportedTriggers = map[string]string{
	"form_submitted":  "form submission starts a story (FormOnSubmit.start_story_ids)",
	"form_submission": "form submission starts a story (FormOnSubmit.start_story_ids)",
	"tag_added":       "tag/badge assignment trigger",
	"badge_added":     "tag/badge assignment trigger",
	"offer_purchased": "purchase.completed event → story enrollment",
	"purchase":        "purchase.completed event → story enrollment",
	"member_signup":   "contact creation → story enrollment",
	"contact_created": "contact creation → story enrollment",
}

var supportedActions = map[string]string{
	"send_email":   "story message step",
	"add_tag":      "badge/tag transaction",
	"remove_tag":   "badge/tag transaction",
	"add_badge":    "badge transaction",
	"remove_badge": "badge transaction",
	"grant_offer":  "product/offer grant action",
	"wait":         "story step delay",
	"delay":        "story step delay",
}

// AutomationTranslation is one automation's feasibility verdict.
type AutomationTranslation struct {
	SourceID     string   `bson:"source_id" json:"source_id"`
	Name         string   `bson:"name" json:"name"`
	Trigger      string   `bson:"trigger" json:"trigger"`
	Feasible     bool     `bson:"feasible" json:"feasible"`
	Mapping      []string `bson:"mapping,omitempty" json:"mapping,omitempty"`
	Unsupported  []string `bson:"unsupported,omitempty" json:"unsupported,omitempty"`
	DraftCreated bool     `bson:"draft_created" json:"draft_created"`
}

// translateAutomation applies the feasibility tables.
func translateAutomation(a SourceAutomation) AutomationTranslation {
	t := AutomationTranslation{SourceID: a.SourceID, Name: a.Name, Trigger: a.Trigger}
	if m, ok := supportedTriggers[a.Trigger]; ok {
		t.Mapping = append(t.Mapping, "trigger "+a.Trigger+" → "+m)
	} else {
		t.Unsupported = append(t.Unsupported, "trigger "+a.Trigger+" has no platform equivalent")
	}
	supportedActionCount := 0
	for _, act := range a.Actions {
		verb := strings.ToLower(strings.SplitN(act, ":", 2)[0])
		verb = strings.ReplaceAll(strings.TrimSpace(verb), " ", "_")
		if m, ok := supportedActions[verb]; ok {
			t.Mapping = append(t.Mapping, "action "+act+" → "+m)
			supportedActionCount++
		} else {
			t.Unsupported = append(t.Unsupported, "action "+act+" is not translatable")
		}
	}
	t.Feasible = len(t.Unsupported) == 0 && supportedActionCount > 0 &&
		supportedTriggers[a.Trigger] != ""
	return t
}

// importAutomations translates every automation and creates a draft Story
// shell for each feasible one. The translation report lands on the project
// report as `automation_translation` (MIG-009).
func (r *run) importAutomations() []AutomationTranslation {
	out := make([]AutomationTranslation, 0, len(r.ex.Automations))
	for _, a := range r.ex.Automations {
		t := translateAutomation(a)
		if !t.Feasible {
			out = append(out, t)
			continue
		}
		if _, ok := r.lookupMap("automation", a.SourceID); ok {
			r.matched["automation"]++
			t.DraftCreated = true
			out = append(out, t)
			continue
		}
		if r.dry {
			r.created["automation"]++
			t.DraftCreated = true
			out = append(out, t)
			continue
		}
		story := &models.Story{
			Id: bson.NewObjectId(), PublicId: utils.GeneratePublicId(),
			TenantID: r.p.TenantID, SubscriberId: r.p.TenantID.Hex(),
			Name:         "Imported: " + a.Name,
			StorylineIds: models.NewBsonCollectionIds(),
		}
		story.StorylineIds.CollectionName = models.StorylineCollection
		if err := db.GetCollection(models.StoryCollection).Insert(story); err != nil {
			r.rowError("automation", a.SourceID, a.Row, "draft story insert: "+err.Error())
			out = append(out, t)
			continue
		}
		t.DraftCreated = true
		r.record("automation", a.SourceID, models.StoryCollection, story.Id, true)
		out = append(out, t)
	}
	return out
}
