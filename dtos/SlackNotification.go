package dtos

// SlackApplication is the immutable application identity assigned at startup.
type SlackApplication string

const (
	SlackApplicationOperations    SlackApplication = "operations"
	SlackApplicationEventiApp     SlackApplication = "eventiapp"
	SlackApplicationCafettonHouse SlackApplication = "cafettonhouse"
	SlackApplicationITBEM         SlackApplication = "itbem"
	SlackApplicationWorkers       SlackApplication = "workers"
)

type SlackSection string

const SlackSectionGeneral SlackSection = "general"

// SlackRoute identifies one allow-listed section inside one application.
// It is startup configuration, never message input.
type SlackRoute struct {
	Application SlackApplication
	Section     SlackSection
}

type SlackSeverity string

const (
	SlackSeverityInfo    SlackSeverity = "info"
	SlackSeveritySuccess SlackSeverity = "success"
	SlackSeverityWarning SlackSeverity = "warning"
	SlackSeverityError   SlackSeverity = "error"
)

type SlackField struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type SlackAction struct {
	Label string `json:"label"`
	URL   string `json:"url"`
	Style string `json:"style,omitempty"` // optional: primary or danger
}

// SlackNotification is the application-facing, presentation-independent
// notification contract.
type SlackNotification struct {
	Severity     SlackSeverity `json:"severity"`
	Title        string        `json:"title"`
	Summary      string        `json:"summary"`
	Fields       []SlackField  `json:"fields,omitempty"`
	Context      []string      `json:"context,omitempty"`
	ImageURL     string        `json:"image_url,omitempty"`
	ImageAlt     string        `json:"image_alt,omitempty"`
	ThumbnailURL string        `json:"thumbnail_url,omitempty"`
	Actions      []SlackAction `json:"actions,omitempty"`
}

// SlackPayload is the serialized Incoming Webhook request.
type SlackPayload struct {
	Text        string            `json:"text"`
	Blocks      []map[string]any  `json:"blocks,omitempty"`
	Attachments []SlackAttachment `json:"attachments,omitempty"`
}

type SlackAttachment struct {
	Color  string           `json:"color,omitempty"`
	Blocks []map[string]any `json:"blocks"`
}
