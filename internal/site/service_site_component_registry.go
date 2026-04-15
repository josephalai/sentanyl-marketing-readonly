package site

// ComponentFieldType represents the type of a field in a component schema.
type ComponentFieldType string

const (
	FieldTypeText     ComponentFieldType = "text"
	FieldTypeTextarea ComponentFieldType = "textarea"
	FieldTypeNumber   ComponentFieldType = "number"
	FieldTypeSelect   ComponentFieldType = "select"
	FieldTypeToggle   ComponentFieldType = "toggle"
	FieldTypeArray    ComponentFieldType = "array"
	FieldTypeObject   ComponentFieldType = "object"
	FieldTypeColor    ComponentFieldType = "color"
	FieldTypeURL      ComponentFieldType = "url"
)

// ComponentField describes an editable field for a website component.
type ComponentField struct {
	Name         string             `json:"name"`
	Label        string             `json:"label"`
	Type         ComponentFieldType `json:"type"`
	Required     bool               `json:"required,omitempty"`
	DefaultValue any                `json:"default_value,omitempty"`
	Options      []string           `json:"options,omitempty"`
	ArrayOf      *ComponentField    `json:"array_of,omitempty"`
}

// ComponentCategory groups related components.
type ComponentCategory string

const (
	CategoryLayout   ComponentCategory = "Layout"
	CategoryContent  ComponentCategory = "Content"
	CategoryMedia    ComponentCategory = "Media"
	CategorySentanyl ComponentCategory = "Sentanyl"
)

// ComponentDef describes a component's server-side definition.
type ComponentDef struct {
	Type         string             `json:"type"`
	Label        string             `json:"label"`
	Category     ComponentCategory  `json:"category"`
	Fields       []ComponentField   `json:"fields"`
	DefaultProps map[string]any     `json:"default_props"`
}

// componentRegistryMap holds the full component registry.
var componentRegistryMap map[string]ComponentDef

func init() {
	componentRegistryMap = make(map[string]ComponentDef)
	for _, def := range buildComponentRegistry() {
		componentRegistryMap[def.Type] = def
	}
}

// GetComponentDef returns the definition for a component type.
func GetComponentDef(compType string) (ComponentDef, bool) {
	def, ok := componentRegistryMap[compType]
	return def, ok
}

// GetAllComponentDefs returns the full component registry.
func GetAllComponentDefs() []ComponentDef {
	return buildComponentRegistry()
}

// IsKnownComponentType checks if a component type is registered.
func IsKnownComponentType(compType string) bool {
	_, ok := componentRegistryMap[compType]
	return ok
}

// GetComponentsByCategory returns components grouped by category.
func GetComponentsByCategory() map[ComponentCategory][]ComponentDef {
	groups := map[ComponentCategory][]ComponentDef{}
	for _, def := range componentRegistryMap {
		groups[def.Category] = append(groups[def.Category], def)
	}
	return groups
}

// buildComponentRegistry creates the full list of component definitions.
func buildComponentRegistry() []ComponentDef {
	return []ComponentDef{
		// ——————— Generic Layout/Content Components ———————
		{
			Type:     "HeroSection",
			Label:    "Hero Section",
			Category: CategoryLayout,
			Fields: []ComponentField{
				{Name: "heading", Label: "Heading", Type: FieldTypeText, Required: true},
				{Name: "subheading", Label: "Subheading", Type: FieldTypeText},
				{Name: "ctaText", Label: "CTA Text", Type: FieldTypeText},
				{Name: "ctaUrl", Label: "CTA URL", Type: FieldTypeURL},
				{Name: "backgroundImage", Label: "Background Image", Type: FieldTypeURL},
				{Name: "alignment", Label: "Alignment", Type: FieldTypeSelect, Options: []string{"left", "center", "right"}, DefaultValue: "center"},
			},
			DefaultProps: map[string]any{
				"heading":    "Welcome",
				"subheading": "Your tagline here",
				"ctaText":    "Get Started",
				"ctaUrl":     "#",
				"alignment":  "center",
			},
		},
		{
			Type:     "RichTextSection",
			Label:    "Rich Text",
			Category: CategoryContent,
			Fields: []ComponentField{
				{Name: "content", Label: "Content (HTML)", Type: FieldTypeTextarea, Required: true},
			},
			DefaultProps: map[string]any{
				"content": "<p>Enter your content here.</p>",
			},
		},
		{
			Type:     "ImageSection",
			Label:    "Image",
			Category: CategoryMedia,
			Fields: []ComponentField{
				{Name: "src", Label: "Image URL", Type: FieldTypeURL, Required: true},
				{Name: "alt", Label: "Alt Text", Type: FieldTypeText},
				{Name: "caption", Label: "Caption", Type: FieldTypeText},
			},
			DefaultProps: map[string]any{
				"src": "https://via.placeholder.com/800x400",
				"alt": "Image",
			},
		},
		{
			Type:     "VideoSection",
			Label:    "Video",
			Category: CategoryMedia,
			Fields: []ComponentField{
				{Name: "videoUrl", Label: "Video URL", Type: FieldTypeURL, Required: true},
				{Name: "autoplay", Label: "Autoplay", Type: FieldTypeToggle, DefaultValue: false},
			},
			DefaultProps: map[string]any{
				"videoUrl": "",
				"autoplay": false,
			},
		},
		{
			Type:     "CTASection",
			Label:    "Call to Action",
			Category: CategoryLayout,
			Fields: []ComponentField{
				{Name: "heading", Label: "Heading", Type: FieldTypeText, Required: true},
				{Name: "description", Label: "Description", Type: FieldTypeTextarea},
				{Name: "buttonText", Label: "Button Text", Type: FieldTypeText},
				{Name: "buttonUrl", Label: "Button URL", Type: FieldTypeURL},
			},
			DefaultProps: map[string]any{
				"heading":    "Ready to get started?",
				"buttonText": "Sign Up Now",
				"buttonUrl":  "#",
			},
		},
		{
			Type:     "TestimonialsSection",
			Label:    "Testimonials",
			Category: CategoryContent,
			Fields: []ComponentField{
				{Name: "heading", Label: "Section Heading", Type: FieldTypeText, DefaultValue: "Testimonials"},
				{Name: "items", Label: "Testimonial Items", Type: FieldTypeArray, ArrayOf: &ComponentField{
					Type: FieldTypeObject,
				}},
			},
			DefaultProps: map[string]any{
				"heading": "What People Say",
				"items":   []any{},
			},
		},
		{
			Type:     "FAQSection",
			Label:    "FAQ",
			Category: CategoryContent,
			Fields: []ComponentField{
				{Name: "heading", Label: "Section Heading", Type: FieldTypeText, DefaultValue: "FAQ"},
				{Name: "items", Label: "FAQ Items", Type: FieldTypeArray, ArrayOf: &ComponentField{
					Type: FieldTypeObject,
				}},
			},
			DefaultProps: map[string]any{
				"heading": "Frequently Asked Questions",
				"items":   []any{},
			},
		},
		{
			Type:     "NavigationBar",
			Label:    "Navigation Bar",
			Category: CategoryLayout,
			Fields: []ComponentField{
				{Name: "siteName", Label: "Site Name", Type: FieldTypeText},
				{Name: "links", Label: "Nav Links", Type: FieldTypeArray},
			},
			DefaultProps: map[string]any{
				"siteName": "My Website",
				"links":    []any{},
			},
		},
		{
			Type:     "Footer",
			Label:    "Footer",
			Category: CategoryLayout,
			Fields: []ComponentField{
				{Name: "text", Label: "Footer Text", Type: FieldTypeText},
				{Name: "links", Label: "Footer Links", Type: FieldTypeArray},
			},
			DefaultProps: map[string]any{
				"text":  "© 2024 All rights reserved.",
				"links": []any{},
			},
		},
		{
			Type:     "Spacer",
			Label:    "Spacer",
			Category: CategoryLayout,
			Fields: []ComponentField{
				{Name: "height", Label: "Height (px)", Type: FieldTypeText, DefaultValue: "40px"},
			},
			DefaultProps: map[string]any{
				"height": "40px",
			},
		},
		{
			Type:     "Columns",
			Label:    "Columns",
			Category: CategoryLayout,
			Fields: []ComponentField{
				{Name: "columnCount", Label: "Number of Columns", Type: FieldTypeNumber, DefaultValue: 2},
				{Name: "columns", Label: "Column Contents", Type: FieldTypeArray},
			},
			DefaultProps: map[string]any{
				"columnCount": 2,
				"columns":     []any{},
			},
		},
		{
			Type:     "Button",
			Label:    "Button",
			Category: CategoryContent,
			Fields: []ComponentField{
				{Name: "label", Label: "Label", Type: FieldTypeText, Required: true},
				{Name: "href", Label: "Link URL", Type: FieldTypeURL},
				{Name: "variant", Label: "Variant", Type: FieldTypeSelect, Options: []string{"primary", "secondary", "outline"}, DefaultValue: "primary"},
			},
			DefaultProps: map[string]any{
				"label":   "Click Me",
				"href":    "#",
				"variant": "primary",
			},
		},

		// ——————— Sentanyl-Aware Components ———————
		{
			Type:     "SentanylLeadForm",
			Label:    "Lead Capture Form",
			Category: CategorySentanyl,
			Fields: []ComponentField{
				{Name: "title", Label: "Form Title", Type: FieldTypeText, DefaultValue: "Get Started"},
				{Name: "formId", Label: "Form ID (Sentanyl)", Type: FieldTypeText},
				{Name: "buttonText", Label: "Submit Button Text", Type: FieldTypeText, DefaultValue: "Submit"},
				{Name: "fields", Label: "Form Fields", Type: FieldTypeArray},
			},
			DefaultProps: map[string]any{
				"title":      "Get Started",
				"buttonText": "Submit",
			},
		},
		{
			Type:     "SentanylContactForm",
			Label:    "Contact Form",
			Category: CategorySentanyl,
			Fields: []ComponentField{
				{Name: "title", Label: "Form Title", Type: FieldTypeText, DefaultValue: "Contact Us"},
				{Name: "formId", Label: "Form ID (Sentanyl)", Type: FieldTypeText},
				{Name: "buttonText", Label: "Submit Button Text", Type: FieldTypeText, DefaultValue: "Send Message"},
				{Name: "includePhone", Label: "Include Phone Field", Type: FieldTypeToggle, DefaultValue: false},
				{Name: "includeMessage", Label: "Include Message Field", Type: FieldTypeToggle, DefaultValue: true},
			},
			DefaultProps: map[string]any{
				"title":          "Contact Us",
				"buttonText":     "Send Message",
				"includePhone":   false,
				"includeMessage": true,
			},
		},
		{
			Type:     "SentanylCheckoutForm",
			Label:    "Checkout Form",
			Category: CategorySentanyl,
			Fields: []ComponentField{
				{Name: "offerId", Label: "Offer ID", Type: FieldTypeText},
				{Name: "productId", Label: "Product ID", Type: FieldTypeText},
				{Name: "heading", Label: "Heading", Type: FieldTypeText, DefaultValue: "Complete Your Purchase"},
				{Name: "showPriceBreakdown", Label: "Show Price Breakdown", Type: FieldTypeToggle, DefaultValue: true},
			},
			DefaultProps: map[string]any{
				"heading":            "Complete Your Purchase",
				"showPriceBreakdown": true,
			},
		},
		{
			Type:     "SentanylOfferCard",
			Label:    "Offer Card",
			Category: CategorySentanyl,
			Fields: []ComponentField{
				{Name: "offerId", Label: "Offer ID", Type: FieldTypeText},
				{Name: "title", Label: "Title Override", Type: FieldTypeText},
				{Name: "showPrice", Label: "Show Price", Type: FieldTypeToggle, DefaultValue: true},
				{Name: "ctaText", Label: "CTA Text", Type: FieldTypeText, DefaultValue: "Get This Offer"},
			},
			DefaultProps: map[string]any{
				"showPrice": true,
				"ctaText":   "Get This Offer",
			},
		},
		{
			Type:     "SentanylOfferGrid",
			Label:    "Offer Grid",
			Category: CategorySentanyl,
			Fields: []ComponentField{
				{Name: "heading", Label: "Section Heading", Type: FieldTypeText, DefaultValue: "Our Offers"},
				{Name: "offerIds", Label: "Offer IDs (comma-separated)", Type: FieldTypeText},
				{Name: "columns", Label: "Grid Columns", Type: FieldTypeNumber, DefaultValue: 3},
			},
			DefaultProps: map[string]any{
				"heading": "Our Offers",
				"columns": 3,
			},
		},
		{
			Type:     "SentanylProductGrid",
			Label:    "Product Grid",
			Category: CategorySentanyl,
			Fields: []ComponentField{
				{Name: "heading", Label: "Section Heading", Type: FieldTypeText, DefaultValue: "Our Products"},
				{Name: "productIds", Label: "Product IDs (comma-separated)", Type: FieldTypeText},
				{Name: "columns", Label: "Grid Columns", Type: FieldTypeNumber, DefaultValue: 3},
			},
			DefaultProps: map[string]any{
				"heading": "Our Products",
				"columns": 3,
			},
		},
		{
			Type:     "SentanylVideoPlayer",
			Label:    "Video Player",
			Category: CategorySentanyl,
			Fields: []ComponentField{
				{Name: "videoId", Label: "Sentanyl Video ID", Type: FieldTypeText},
				{Name: "videoUrl", Label: "Video URL (fallback)", Type: FieldTypeURL},
				{Name: "autoplay", Label: "Autoplay", Type: FieldTypeToggle, DefaultValue: false},
				{Name: "showControls", Label: "Show Controls", Type: FieldTypeToggle, DefaultValue: true},
			},
			DefaultProps: map[string]any{
				"autoplay":     false,
				"showControls": true,
			},
		},
		{
			Type:     "SentanylCourseGrid",
			Label:    "Course Grid",
			Category: CategorySentanyl,
			Fields: []ComponentField{
				{Name: "heading", Label: "Section Heading", Type: FieldTypeText, DefaultValue: "Our Courses"},
				{Name: "courseIds", Label: "Course IDs (comma-separated)", Type: FieldTypeText},
				{Name: "columns", Label: "Grid Columns", Type: FieldTypeNumber, DefaultValue: 3},
			},
			DefaultProps: map[string]any{
				"heading": "Our Courses",
				"columns": 3,
			},
		},
		{
			Type:     "SentanylTestimonials",
			Label:    "Sentanyl Testimonials",
			Category: CategorySentanyl,
			Fields: []ComponentField{
				{Name: "heading", Label: "Section Heading", Type: FieldTypeText, DefaultValue: "Testimonials"},
				{Name: "items", Label: "Testimonial Items", Type: FieldTypeArray},
				{Name: "style", Label: "Style", Type: FieldTypeSelect, Options: []string{"cards", "carousel", "list"}, DefaultValue: "cards"},
			},
			DefaultProps: map[string]any{
				"heading": "Testimonials",
				"items":   []any{},
				"style":   "cards",
			},
		},
		{
			Type:     "SentanylCountdown",
			Label:    "Countdown Timer",
			Category: CategorySentanyl,
			Fields: []ComponentField{
				{Name: "targetDate", Label: "Target Date (ISO 8601)", Type: FieldTypeText},
				{Name: "heading", Label: "Heading", Type: FieldTypeText},
				{Name: "expiredMessage", Label: "Expired Message", Type: FieldTypeText, DefaultValue: "This offer has expired"},
			},
			DefaultProps: map[string]any{
				"expiredMessage": "This offer has expired",
			},
		},
		{
			Type:     "SentanylQuiz",
			Label:    "Quiz",
			Category: CategorySentanyl,
			Fields: []ComponentField{
				{Name: "quizId", Label: "Quiz ID", Type: FieldTypeText},
				{Name: "title", Label: "Quiz Title", Type: FieldTypeText, DefaultValue: "Take the Quiz"},
			},
			DefaultProps: map[string]any{
				"title": "Take the Quiz",
			},
		},
		{
			Type:     "SentanylCalendarEmbed",
			Label:    "Calendar Embed",
			Category: CategorySentanyl,
			Fields: []ComponentField{
				{Name: "calendarUrl", Label: "Calendar URL", Type: FieldTypeURL},
				{Name: "heading", Label: "Heading", Type: FieldTypeText, DefaultValue: "Schedule a Call"},
			},
			DefaultProps: map[string]any{
				"heading": "Schedule a Call",
			},
		},
		{
			Type:     "SentanylLibraryLink",
			Label:    "Library Link",
			Category: CategorySentanyl,
			Fields: []ComponentField{
				{Name: "libraryId", Label: "Library ID", Type: FieldTypeText},
				{Name: "label", Label: "Link Label", Type: FieldTypeText, DefaultValue: "Access Library"},
				{Name: "href", Label: "Link URL", Type: FieldTypeURL},
			},
			DefaultProps: map[string]any{
				"label": "Access Library",
			},
		},
		{
			Type:     "SentanylFunnelLink",
			Label:    "Funnel Link",
			Category: CategorySentanyl,
			Fields: []ComponentField{
				{Name: "funnelId", Label: "Funnel ID", Type: FieldTypeText},
				{Name: "label", Label: "Link Label", Type: FieldTypeText, DefaultValue: "Enter Funnel"},
				{Name: "href", Label: "Link URL", Type: FieldTypeURL},
			},
			DefaultProps: map[string]any{
				"label": "Enter Funnel",
			},
		},
		{
			Type:     "SentanylFunnelCTA",
			Label:    "Funnel CTA",
			Category: CategorySentanyl,
			Fields: []ComponentField{
				{Name: "funnelId", Label: "Funnel ID", Type: FieldTypeText},
				{Name: "heading", Label: "Heading", Type: FieldTypeText},
				{Name: "description", Label: "Description", Type: FieldTypeTextarea},
				{Name: "buttonText", Label: "Button Text", Type: FieldTypeText, DefaultValue: "Get Started"},
				{Name: "buttonUrl", Label: "Button URL", Type: FieldTypeURL},
			},
			DefaultProps: map[string]any{
				"buttonText": "Get Started",
			},
		},
	}
}
