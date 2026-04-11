package models

import (
	"log"

	"github.com/josephalai/sentanyl/pkg/db"
	"github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/utils"
	"gopkg.in/mgo.v2/bson"
)

// Funnel - root container for web funnels (parallel to Story for email).
type Funnel struct {
	Id              bson.ObjectId   `bson:"_id" json:"id,omitempty"`
	PublicId        string          `bson:"public_id" json:"public_id,omitempty"`
	TenantID        bson.ObjectId   `bson:"tenant_id,omitempty" json:"tenant_id,omitempty"`
	SubscriberId    string          `bson:"subscriber_id" json:"subscriber_id,omitempty"`
	CreatorId       bson.ObjectId   `bson:"creator_id,omitempty" json:"creator_id,omitempty"`
	Name            string          `bson:"name" json:"name,omitempty"`
	Domain          string          `bson:"domain" json:"domain,omitempty"`
	RouteIds        *BsonRefIds     `bson:"route_ids" json:"-"`
	Routes          []*FunnelRoute  `bson:"routes,omitempty" json:"routes,omitempty"`
	StartTrigger    *RequiredBadge  `bson:"start_trigger,omitempty" json:"start_trigger,omitempty"`
	CompleteTrigger *RequiredBadge  `bson:"complete_trigger,omitempty" json:"complete_trigger,omitempty"`
	AIContext       *AIContextBlock `bson:"ai_context,omitempty" json:"ai_context,omitempty"`
	models.SoftDeletes `bson:"timestamps,omitempty" json:"timestamps,omitempty"`
}

func NewFunnel(name string, subscriberId string) *Funnel {
	return &Funnel{
		Id:           bson.NewObjectId(),
		PublicId:     utils.GeneratePublicId(),
		SubscriberId: subscriberId,
		Name:         name,
	}
}

func (f *Funnel) Hydrate() {
	if f.RouteIds != nil {
		ids := f.RouteIds
		f.Routes = nil
		for _, id := range ids.Ids {
			out := FunnelRoute{}
			db.GetCollection(ids.CollectionName).FindId(id).One(&out)
			f.Routes = append(f.Routes, &out)
			out.Hydrate()
		}
	}
	if f.StartTrigger != nil {
		f.StartTrigger.Hydrate()
	}
	if f.CompleteTrigger != nil {
		f.CompleteTrigger.Hydrate()
	}
	log.Println("Done Hydrating Funnel")
}

func (f *Funnel) ReadyMongoStore() []interface{} {
	var individuals []interface{}

	f.RouteIds = NewBsonRefIds()
	for _, route := range f.Routes {
		f.RouteIds.CollectionName = models.FunnelRouteCollection
		f.RouteIds.Ids = append(f.RouteIds.Ids, route.Id)
		individuals = append(individuals, route.ReadyMongoStore()...)
	}

	if f.StartTrigger != nil {
		individuals = append(individuals, f.StartTrigger.ReadyMongoStore()...)
	}
	if f.CompleteTrigger != nil {
		individuals = append(individuals, f.CompleteTrigger.ReadyMongoStore()...)
	}

	funnel := *f
	funnel.Routes = nil
	if funnel.StartTrigger != nil {
		funnel.StartTrigger.Badge = nil
	}
	if funnel.CompleteTrigger != nil {
		funnel.CompleteTrigger.Badge = nil
	}
	individuals = append(individuals, funnel)
	return individuals
}

func (f *Funnel) GetIdHex() string {
	return f.Id.Hex()
}

func (f *Funnel) GetId() bson.ObjectId {
	return f.Id
}

// FunnelRoute - branch based on badges (parallel to Storyline).
type FunnelRoute struct {
	Id           bson.ObjectId `bson:"_id" json:"id,omitempty"`
	PublicId     string        `bson:"public_id" json:"public_id,omitempty"`
	TenantID     bson.ObjectId `bson:"tenant_id,omitempty" json:"tenant_id,omitempty"`
	SubscriberId string        `bson:"subscriber_id" json:"subscriber_id,omitempty"`
	FunnelId     bson.ObjectId `bson:"funnel_id,omitempty" json:"funnel_id,omitempty"`
	Name         string        `bson:"name" json:"name,omitempty"`
	Order        int           `bson:"order,omitempty" json:"order,omitempty"`
	RequiredUserBadges struct {
		MustHave    []*RequiredBadge `bson:"must_have,omitempty" json:"must_have,omitempty"`
		MustNotHave []*RequiredBadge `bson:"must_not_have,omitempty" json:"must_not_have,omitempty"`
	} `json:"required_user_badges,omitempty" bson:"required_user_badges,omitempty"`
	StageIds    *BsonRefIds    `bson:"stage_ids" json:"-"`
	Stages      []*FunnelStage `bson:"stages,omitempty" json:"stages,omitempty"`
	models.SoftDeletes `bson:"timestamps,omitempty" json:"timestamps,omitempty"`
}

func (r *FunnelRoute) Hydrate() {
	if r.StageIds != nil {
		ids := r.StageIds
		r.Stages = nil
		for _, id := range ids.Ids {
			out := FunnelStage{}
			db.GetCollection(ids.CollectionName).FindId(id).One(&out)
			r.Stages = append(r.Stages, &out)
			out.Hydrate()
		}
	}
	for _, rb := range r.RequiredUserBadges.MustHave {
		rb.Hydrate()
	}
	for _, rb := range r.RequiredUserBadges.MustNotHave {
		rb.Hydrate()
	}
}

func (r *FunnelRoute) ReadyMongoStore() []interface{} {
	var individuals []interface{}

	r.StageIds = NewBsonRefIds()
	for _, stage := range r.Stages {
		r.StageIds.CollectionName = models.FunnelStageCollection
		r.StageIds.Ids = append(r.StageIds.Ids, stage.Id)
		individuals = append(individuals, stage.ReadyMongoStore()...)
	}

	for _, rb := range r.RequiredUserBadges.MustHave {
		individuals = append(individuals, rb.ReadyMongoStore()...)
	}
	for _, rb := range r.RequiredUserBadges.MustNotHave {
		individuals = append(individuals, rb.ReadyMongoStore()...)
	}

	route := *r
	route.Stages = nil
	for _, rb := range route.RequiredUserBadges.MustHave {
		rb.Badge = nil
	}
	for _, rb := range route.RequiredUserBadges.MustNotHave {
		rb.Badge = nil
	}
	individuals = append(individuals, route)
	return individuals
}

func (r *FunnelRoute) GetIdHex() string {
	return r.Id.Hex()
}

func (r *FunnelRoute) GetId() bson.ObjectId {
	return r.Id
}

// FunnelStage - stateful milestone / URL path (parallel to Enactment).
type FunnelStage struct {
	Id           bson.ObjectId `bson:"_id" json:"id,omitempty"`
	PublicId     string        `bson:"public_id" json:"public_id,omitempty"`
	TenantID     bson.ObjectId `bson:"tenant_id,omitempty" json:"tenant_id,omitempty"`
	SubscriberId string        `bson:"subscriber_id" json:"subscriber_id,omitempty"`
	RouteId      bson.ObjectId `bson:"route_id,omitempty" json:"route_id,omitempty"`
	Name         string        `bson:"name" json:"name,omitempty"`
	Order        int           `bson:"order,omitempty" json:"order,omitempty"`
	Path         string        `bson:"path" json:"path,omitempty"`
	PageIds      *BsonRefIds   `bson:"page_ids" json:"-"`
	Pages        []*FunnelPage `bson:"pages,omitempty" json:"pages,omitempty"`
	TriggerIds   *BsonRefIds   `bson:"trigger_ids" json:"-"`
	Triggers     []*Trigger    `bson:"triggers,omitempty" json:"triggers,omitempty"`
	PDFConfig    *PDFConfig    `bson:"pdf_config,omitempty" json:"pdf_config,omitempty"`
	models.SoftDeletes `bson:"timestamps,omitempty" json:"timestamps,omitempty"`
}

func (s *FunnelStage) Hydrate() {
	if s.PageIds != nil {
		ids := s.PageIds
		s.Pages = nil
		for _, id := range ids.Ids {
			out := FunnelPage{}
			db.GetCollection(ids.CollectionName).FindId(id).One(&out)
			s.Pages = append(s.Pages, &out)
			out.Hydrate()
		}
	}
	if s.TriggerIds != nil {
		ids := s.TriggerIds
		s.Triggers = nil
		for _, id := range ids.Ids {
			out := Trigger{}
			db.GetCollection(ids.CollectionName).FindId(id).One(&out)
			s.Triggers = append(s.Triggers, &out)
			// TODO: out.Hydrate() when Trigger hydration is fully ported
		}
	}
}

func (s *FunnelStage) ReadyMongoStore() []interface{} {
	var individuals []interface{}

	s.PageIds = NewBsonRefIds()
	for _, page := range s.Pages {
		s.PageIds.CollectionName = models.FunnelPageCollection
		s.PageIds.Ids = append(s.PageIds.Ids, page.Id)
		individuals = append(individuals, page.ReadyMongoStore()...)
	}

	s.TriggerIds = NewBsonRefIds()
	for _, trigger := range s.Triggers {
		s.TriggerIds.CollectionName = models.TriggerCollection
		s.TriggerIds.Ids = append(s.TriggerIds.Ids, trigger.Id)
		individuals = append(individuals, trigger.ReadyMongoStore()...)
	}

	stage := *s
	stage.Pages = nil
	stage.Triggers = nil
	individuals = append(individuals, stage)
	return individuals
}

func (s *FunnelStage) GetIdHex() string {
	return s.Id.Hex()
}

func (s *FunnelStage) GetId() bson.ObjectId {
	return s.Id
}

// FunnelPage - actual UI layout (supports A/B testing).
type FunnelPage struct {
	Id           bson.ObjectId   `bson:"_id" json:"id,omitempty"`
	PublicId     string          `bson:"public_id" json:"public_id,omitempty"`
	TenantID     bson.ObjectId   `bson:"tenant_id,omitempty" json:"tenant_id,omitempty"`
	SubscriberId string          `bson:"subscriber_id" json:"subscriber_id,omitempty"`
	StageId      bson.ObjectId   `bson:"stage_id,omitempty" json:"stage_id,omitempty"`
	Name         string          `bson:"name" json:"name,omitempty"`
	TemplateName string          `bson:"template_name,omitempty" json:"template_name,omitempty"`
	BlockIds     *BsonRefIds     `bson:"block_ids" json:"-"`
	Blocks       []*PageBlock    `bson:"blocks,omitempty" json:"blocks,omitempty"`
	FormIds      *BsonRefIds     `bson:"form_ids" json:"-"`
	Forms        []*PageForm     `bson:"forms,omitempty" json:"forms,omitempty"`
	AIContext    *AIContextBlock `bson:"ai_context,omitempty" json:"ai_context,omitempty"`
	RenderedHTML string          `bson:"rendered_html,omitempty" json:"rendered_html,omitempty"`
	models.SoftDeletes `bson:"timestamps,omitempty" json:"timestamps,omitempty"`
}

func (p *FunnelPage) Hydrate() {
	if p.BlockIds != nil {
		ids := p.BlockIds
		p.Blocks = nil
		for _, id := range ids.Ids {
			out := PageBlock{}
			db.GetCollection(ids.CollectionName).FindId(id).One(&out)
			p.Blocks = append(p.Blocks, &out)
		}
	}
	if p.FormIds != nil {
		ids := p.FormIds
		p.Forms = nil
		for _, id := range ids.Ids {
			out := PageForm{}
			db.GetCollection(ids.CollectionName).FindId(id).One(&out)
			p.Forms = append(p.Forms, &out)
		}
	}
}

func (p *FunnelPage) ReadyMongoStore() []interface{} {
	var individuals []interface{}

	p.BlockIds = NewBsonRefIds()
	for _, block := range p.Blocks {
		p.BlockIds.CollectionName = models.PageBlockCollection
		p.BlockIds.Ids = append(p.BlockIds.Ids, block.Id)
		individuals = append(individuals, block.ReadyMongoStore()...)
	}

	p.FormIds = NewBsonRefIds()
	for _, form := range p.Forms {
		p.FormIds.CollectionName = models.PageFormCollection
		p.FormIds.Ids = append(p.FormIds.Ids, form.Id)
		individuals = append(individuals, form.ReadyMongoStore()...)
	}

	page := *p
	page.Blocks = nil
	page.Forms = nil
	individuals = append(individuals, page)
	return individuals
}

func (p *FunnelPage) GetIdHex() string {
	return p.Id.Hex()
}

func (p *FunnelPage) GetId() bson.ObjectId {
	return p.Id
}

// PageBlock - atomic AI-generated section within a page.
type PageBlock struct {
	Id              bson.ObjectId    `bson:"_id" json:"id,omitempty"`
	PublicId        string           `bson:"public_id" json:"public_id,omitempty"`
	TenantID        bson.ObjectId    `bson:"tenant_id,omitempty" json:"tenant_id,omitempty"`
	SubscriberId    string           `bson:"subscriber_id" json:"subscriber_id,omitempty"`
	PageId          bson.ObjectId    `bson:"page_id,omitempty" json:"page_id,omitempty"`
	SectionID       string           `bson:"section_id" json:"section_id,omitempty"`
	BlockType       string           `bson:"block_type,omitempty" json:"block_type,omitempty"`
	SourceURL       string           `bson:"source_url,omitempty" json:"source_url,omitempty"`
	MediaPublicId   string           `bson:"media_public_id,omitempty" json:"media_public_id,omitempty"`
	PlayerPresetId  string           `bson:"player_preset_id,omitempty" json:"player_preset_id,omitempty"`
	Autoplay        bool             `bson:"autoplay,omitempty" json:"autoplay,omitempty"`
	ContentGen      *ContentGenConfig `bson:"content_gen,omitempty" json:"content_gen,omitempty"`
	RenderedContent string           `bson:"rendered_content,omitempty" json:"rendered_content,omitempty"`
	AIContext       *AIContextBlock  `bson:"ai_context,omitempty" json:"ai_context,omitempty"`
	models.SoftDeletes `bson:"timestamps,omitempty" json:"timestamps,omitempty"`
}

func (b *PageBlock) ReadyMongoStore() []interface{} {
	return []interface{}{*b}
}

func (b *PageBlock) GetIdHex() string {
	return b.Id.Hex()
}

func (b *PageBlock) GetId() bson.ObjectId {
	return b.Id
}

// PageForm - interactive capture + purchase form.
type PageForm struct {
	Id           bson.ObjectId `bson:"_id" json:"id,omitempty"`
	PublicId     string        `bson:"public_id" json:"public_id,omitempty"`
	TenantID     bson.ObjectId `bson:"tenant_id,omitempty" json:"tenant_id,omitempty"`
	SubscriberId string        `bson:"subscriber_id" json:"subscriber_id,omitempty"`
	PageId       bson.ObjectId `bson:"page_id,omitempty" json:"page_id,omitempty"`
	Name         string        `bson:"name" json:"name,omitempty"`
	FormType     string        `bson:"form_type" json:"form_type,omitempty"`
	Fields       []*FormField  `bson:"fields,omitempty" json:"fields,omitempty"`
	ProductId    string        `bson:"product_id,omitempty" json:"product_id,omitempty"`
	OfferID      string        `bson:"offer_id,omitempty" json:"offer_id,omitempty"`
	OrderBumps   []*OrderBump  `bson:"order_bumps,omitempty" json:"order_bumps,omitempty"`
	models.SoftDeletes `bson:"timestamps,omitempty" json:"timestamps,omitempty"`
}

func (f *PageForm) ReadyMongoStore() []interface{} {
	return []interface{}{*f}
}

func (f *PageForm) GetIdHex() string {
	return f.Id.Hex()
}

func (f *PageForm) GetId() bson.ObjectId {
	return f.Id
}

// FormField - individual form field.
type FormField struct {
	FieldName   string `bson:"field_name" json:"field_name,omitempty"`
	FieldType   string `bson:"field_type" json:"field_type,omitempty"`
	Required    bool   `bson:"required" json:"required,omitempty"`
	CustomField string `bson:"custom_field,omitempty" json:"custom_field,omitempty"`
}

// ContentGenConfig - AI generation job ticket.
type ContentGenConfig struct {
	Length       string   `bson:"length" json:"length,omitempty"`
	ContextURLs []string `bson:"context_urls,omitempty" json:"context_urls,omitempty"`
	PromptAppend string  `bson:"prompt_append,omitempty" json:"prompt_append,omitempty"`
	Status       string  `bson:"status,omitempty" json:"status,omitempty"`
}

// AIContextBlock - shared AI context configuration.
type AIContextBlock struct {
	ContextURLs []string `bson:"context_urls,omitempty" json:"context_urls,omitempty"`
	ContextRefs []string `bson:"context_refs,omitempty" json:"context_refs,omitempty"`
	ContextMode string   `bson:"context_mode,omitempty" json:"context_mode,omitempty"`
}

// PDFConfig - PDF generation configuration for lead magnets.
type PDFConfig struct {
	AIGenerated bool            `bson:"ai_generated" json:"ai_generated,omitempty"`
	AIContext   *AIContextBlock `bson:"ai_context,omitempty" json:"ai_context,omitempty"`
	FileURL     string          `bson:"file_url,omitempty" json:"file_url,omitempty"`
	Status      string          `bson:"status,omitempty" json:"status,omitempty"`
}

// Product - digital deliverable content container (no price - pricing is on Offer).
type Product struct {
	Id           bson.ObjectId `bson:"_id" json:"id,omitempty"`
	PublicId     string        `bson:"public_id" json:"public_id,omitempty"`
	TenantID     bson.ObjectId `bson:"tenant_id,omitempty" json:"tenant_id,omitempty"`
	SubscriberId string        `bson:"subscriber_id" json:"subscriber_id,omitempty"`
	Name         string        `bson:"name" json:"name,omitempty"`
	Description  string        `bson:"description,omitempty" json:"description,omitempty"`
	ProductType  string        `bson:"product_type,omitempty" json:"product_type,omitempty"`
	ThumbnailURL string        `bson:"thumbnail_url,omitempty" json:"thumbnail_url,omitempty"`
	Status       string        `bson:"status,omitempty" json:"status,omitempty"`
	Price        float64       `bson:"price" json:"price,omitempty"`
	Currency     string        `bson:"currency,omitempty" json:"currency,omitempty"`
	StripeId     string        `bson:"stripe_id,omitempty" json:"stripe_id,omitempty"`
	Modules      []*Module     `bson:"modules,omitempty" json:"modules,omitempty"`
	models.SoftDeletes `bson:"timestamps,omitempty" json:"timestamps,omitempty"`
}

func (p *Product) ReadyMongoStore() []interface{} {
	return []interface{}{*p}
}

func (p *Product) GetIdHex() string {
	return p.Id.Hex()
}

func (p *Product) GetId() bson.ObjectId {
	return p.Id
}

// Module represents a section/module within a Product (course).
type Module struct {
	Id      bson.ObjectId `bson:"_id" json:"id,omitempty"`
	Title   string        `bson:"title" json:"title,omitempty"`
	Order   int           `bson:"order,omitempty" json:"order,omitempty"`
	Lessons []*Lesson     `bson:"lessons,omitempty" json:"lessons,omitempty"`
}

// Lesson represents a single lesson within a Module.
type Lesson struct {
	Id            bson.ObjectId `bson:"_id" json:"id,omitempty"`
	Title         string        `bson:"title" json:"title,omitempty"`
	VideoURL      string        `bson:"video_url,omitempty" json:"video_url,omitempty"`
	MediaPublicId string        `bson:"media_public_id,omitempty" json:"media_public_id,omitempty"`
	ContentHTML   string        `bson:"content_html,omitempty" json:"content_html,omitempty"`
	IsDraft       bool          `bson:"is_draft,omitempty" json:"is_draft,omitempty"`
	Order         int           `bson:"order,omitempty" json:"order,omitempty"`
}

// PurchaseLog - record of a purchase.
type PurchaseLog struct {
	Id             bson.ObjectId `bson:"_id" json:"id,omitempty"`
	PublicId       string        `bson:"public_id" json:"public_id,omitempty"`
	TenantID       bson.ObjectId `bson:"tenant_id,omitempty" json:"tenant_id,omitempty"`
	SubscriberId   string        `bson:"subscriber_id" json:"subscriber_id,omitempty"`
	UserId         bson.ObjectId `bson:"user_id" json:"user_id,omitempty"`
	ProductId      bson.ObjectId `bson:"product_id" json:"product_id,omitempty"`
	OfferID        bson.ObjectId `bson:"offer_id,omitempty" json:"offer_id,omitempty"`
	FunnelId       bson.ObjectId `bson:"funnel_id,omitempty" json:"funnel_id,omitempty"`
	StageId        bson.ObjectId `bson:"stage_id,omitempty" json:"stage_id,omitempty"`
	Amount         float64       `bson:"amount" json:"amount,omitempty"`
	Currency       string        `bson:"currency,omitempty" json:"currency,omitempty"`
	StripeChargeId string        `bson:"stripe_charge_id,omitempty" json:"stripe_charge_id,omitempty"`
	Status         string        `bson:"status,omitempty" json:"status,omitempty"`
	models.SoftDeletes `bson:"timestamps,omitempty" json:"timestamps,omitempty"`
}

func (pl *PurchaseLog) ReadyMongoStore() []interface{} {
	return []interface{}{*pl}
}

func (pl *PurchaseLog) GetIdHex() string {
	return pl.Id.Hex()
}

func (pl *PurchaseLog) GetId() bson.ObjectId {
	return pl.Id
}

// FunnelTemplate represents a reusable page template.
type FunnelTemplate struct {
	Id           bson.ObjectId `bson:"_id" json:"id,omitempty"`
	PublicId     string        `bson:"public_id" json:"public_id,omitempty"`
	TenantID     bson.ObjectId `bson:"tenant_id,omitempty" json:"tenant_id,omitempty"`
	SubscriberId string        `bson:"subscriber_id" json:"subscriber_id,omitempty"`
	Name         string        `bson:"name" json:"name,omitempty"`
	HTMLContent  string        `bson:"html_content,omitempty" json:"html_content,omitempty"`
	GlobalCSS    string        `bson:"global_css,omitempty" json:"global_css,omitempty"`
	models.SoftDeletes `bson:"timestamps,omitempty" json:"timestamps,omitempty"`
}

func (t *FunnelTemplate) ReadyMongoStore() []interface{} {
	return []interface{}{*t}
}

func (t *FunnelTemplate) GetIdHex() string {
	return t.Id.Hex()
}

func (t *FunnelTemplate) GetId() bson.ObjectId {
	return t.Id
}

// Site represents a global website structure.
type Site struct {
	Id           bson.ObjectId     `bson:"_id" json:"id,omitempty"`
	PublicId     string            `bson:"public_id" json:"public_id,omitempty"`
	TenantID     bson.ObjectId     `bson:"tenant_id,omitempty" json:"tenant_id,omitempty"`
	SubscriberId string            `bson:"subscriber_id" json:"subscriber_id,omitempty"`
	CreatorId    bson.ObjectId     `bson:"creator_id,omitempty" json:"creator_id,omitempty"`
	Name         string            `bson:"name" json:"name,omitempty"`
	Domain       string            `bson:"domain" json:"domain,omitempty"`
	Theme        string            `bson:"theme,omitempty" json:"theme,omitempty"`
	SEO          *SEOConfig        `bson:"seo,omitempty" json:"seo,omitempty"`
	Navigation   *NavigationConfig `bson:"navigation,omitempty" json:"navigation,omitempty"`
	PageIds      *BsonRefIds       `bson:"page_ids" json:"-"`
	Pages        []*FunnelPage     `bson:"pages,omitempty" json:"pages,omitempty"`
	models.SoftDeletes `bson:"timestamps,omitempty" json:"timestamps,omitempty"`
}

func (s *Site) Hydrate() {
	if s.PageIds != nil {
		ids := s.PageIds
		s.Pages = nil
		for _, id := range ids.Ids {
			out := FunnelPage{}
			db.GetCollection(ids.CollectionName).FindId(id).One(&out)
			s.Pages = append(s.Pages, &out)
			out.Hydrate()
		}
	}
}

func (s *Site) ReadyMongoStore() []interface{} {
	var individuals []interface{}

	s.PageIds = NewBsonRefIds()
	for _, page := range s.Pages {
		s.PageIds.CollectionName = models.FunnelPageCollection
		s.PageIds.Ids = append(s.PageIds.Ids, page.Id)
		individuals = append(individuals, page.ReadyMongoStore()...)
	}

	site := *s
	site.Pages = nil
	individuals = append(individuals, site)
	return individuals
}

// SEOConfig holds global or page-level SEO metadata.
type SEOConfig struct {
	MetaTitle         string `bson:"meta_title,omitempty" json:"meta_title,omitempty"`
	MetaDescription   string `bson:"meta_description,omitempty" json:"meta_description,omitempty"`
	OpenGraphImageURL string `bson:"og_image_url,omitempty" json:"og_image_url,omitempty"`
}

// NavigationConfig holds header/footer link mappings.
type NavigationConfig struct {
	HeaderLinks map[string]string `bson:"header_links,omitempty" json:"header_links,omitempty"`
	FooterLinks map[string]string `bson:"footer_links,omitempty" json:"footer_links,omitempty"`
}

// OrderBump represents a secondary offer shown on a checkout page.
type OrderBump struct {
	OfferID bson.ObjectId `bson:"offer_id" json:"offer_id,omitempty"`
	Text    string        `bson:"text" json:"text,omitempty"`
}
