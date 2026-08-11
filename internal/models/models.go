package models

import (
	"encoding/json"
	"time"
)

// ── Shared ────────────────────────────────────────────────────────────────────

// Pages is the pagination envelope included in list responses.
type Pages struct {
	Count   int     `json:"count"`
	Current int     `json:"current"`
	First   string  `json:"first"`
	Last    string  `json:"last"`
	Next    *string `json:"next"`
	Prev    *string `json:"prev"`
}

// ── Users ─────────────────────────────────────────────────────────────────────

type UserProfile struct {
	FullName string    `json:"fullname"`
	Joined   time.Time `json:"joined"`
	Link     *string   `json:"link"`
	Image    *string   `json:"image"`
}

type User struct {
	UserID   string      `json:"userid"`
	Username string      `json:"username"`
	Profile  UserProfile `json:"profile"`
}

// EmojiUser is the compact user shape used inside emoji reaction lists.
type EmojiUser struct {
	ID   string  `json:"id"`
	Name string  `json:"name"`
	TN   *string `json:"tn"`
}

// CommentAuthor is the compact author shape embedded in comment objects.
type CommentAuthor struct {
	UserID   string  `json:"userid"`
	Username string  `json:"username"`
	TN       *string `json:"tn,omitempty"`
}

// ── Photos ────────────────────────────────────────────────────────────────────

type ImageInfo struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type TitleInfo struct {
	Text     string `json:"text"`
	UserID   string `json:"userid"`
	Username string `json:"username"`
	CanEdit  bool   `json:"canedit"`
}

type Label struct {
	LabelID  string `json:"labelid"`
	Name     string `json:"name"`
	Value    string `json:"value"`
	UserID   string `json:"userid"`
	Username string `json:"username"`
	// ColorHex is this label name's persisted color override (Phase 5a), if
	// any — omitted entirely when unset, in which case clients fall back to
	// a deterministic hash-based color derived from Name.
	ColorHex *string `json:"color,omitempty"`
	// Restricted mirrors label_names.restricted for this label's name
	// (Phase 5b): true means only Admin/LabelAdmin may add, modify, or
	// delete labels with this name. Existing labels remain visible to
	// everyone regardless.
	Restricted bool `json:"restricted"`
}

// LabelNameInfo describes a label name's shared, cross-photo attributes —
// returned by GET /api/v1/label-names and PATCH /api/v1/label-names.
type LabelNameInfo struct {
	Name       string  `json:"name"`
	ColorHex   *string `json:"color,omitempty"`
	Restricted bool    `json:"restricted"`
	// Enabled (Phase 6e/migration 017) defaults true for names with no
	// label_names row at all — see fetchLabelNameInfo's COALESCE.
	Enabled bool `json:"enabled"`
	// UsageCount (Phase 6e) — how many non-deleted labels currently use this
	// name; only populated by the admin listing endpoint.
	UsageCount int `json:"usage_count,omitempty"`
}

type Emoji struct {
	EmojiID   string      `json:"emojiid"`
	EmojiChar *string     `json:"emoji,omitempty"`
	ImageURL  *string     `json:"imageurl,omitempty"`
	AltText   string      `json:"alttext"`
	Count     int         `json:"count"`
	Users     []EmojiUser `json:"users"`
	UsersURL  *string     `json:"usersurl,omitempty"`
}

type RelatedPhoto struct {
	PhotoID  string `json:"photoid"`
	ImageURL string `json:"imageurl"`
	ClickURL string `json:"clickurl"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

type Comment struct {
	CommentID  string        `json:"commentid"`
	Author     CommentAuthor `json:"author"`
	Date       time.Time     `json:"date"`
	ReplyCount int           `json:"replycount"`
	Comment    string        `json:"comment"`
	Deleted    bool          `json:"deleted,omitempty"`
	RepliesURL string        `json:"repliesurl"`
}

// Photo is the full response for GET /api/v1/photo.
type Photo struct {
	PhotoID     string         `json:"photoid"`
	Image       ImageInfo      `json:"image"`
	Title       TitleInfo      `json:"title"`
	Description string         `json:"description"`
	Labels      []Label        `json:"labels"`
	LabelsURL   *string        `json:"labelsurl,omitempty"`
	Emojis      []Emoji        `json:"emojis"`
	EmojisURL   *string        `json:"emojisurl,omitempty"`
	Related     []RelatedPhoto `json:"related"`
	Comments    []Comment      `json:"comments"`
	CommentsURL *string        `json:"commentsurl,omitempty"`
}

// ── List responses ────────────────────────────────────────────────────────────

type LabelsResponse struct {
	PhotoID string  `json:"photoid"`
	Offset  int     `json:"offset"`
	Pages   Pages   `json:"pages"`
	Labels  []Label `json:"labels"`
}

type EmojisResponse struct {
	PhotoID string  `json:"photoid"`
	Offset  int     `json:"offset"`
	Pages   Pages   `json:"pages"`
	Emojis  []Emoji `json:"emojis"`
}

type EmojiUsersResponse struct {
	EmojiID string      `json:"emojiid"`
	Offset  int         `json:"offset"`
	Pages   Pages       `json:"pages"`
	Users   []EmojiUser `json:"users"`
}

type CommentsResponse struct {
	PhotoID  string    `json:"photoid"`
	ParentID *string   `json:"parentid,omitempty"`
	Offset   int       `json:"offset"`
	Pages    Pages     `json:"pages"`
	Comments []Comment `json:"comments"`
}

// ── Write request bodies ──────────────────────────────────────────────────────

type AddLabelRequest struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type UpdateLabelRequest struct {
	Name  *string `json:"name"`
	Value *string `json:"value"`
}

type AddEmojiReactionRequest struct {
	EmojiID string `json:"emojiid"`
}

type AddCommentRequest struct {
	Comment string `json:"comment"`
}

type UpdateCommentRequest struct {
	Comment string `json:"comment"`
}

type UpdatePhotoTitleRequest struct {
	Title string `json:"title"`
}

// ── Search ────────────────────────────────────────────────────────────────────

// SearchResult is a single photo match returned by the search endpoint.
type SearchResult struct {
	PhotoID  string `json:"photoid"`
	ImageURL string `json:"imageurl"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Title    string `json:"title"`
}

// SearchResponse is returned by GET /api/v1/search.
type SearchResponse struct {
	Query   string         `json:"query"`
	Total   int            `json:"total"`
	Results []SearchResult `json:"results"`
}

// ── Galleries ────────────────────────────────────────────────────────────────

// GallerySummary is one row in GET /api/v1/galleries.
type GallerySummary struct {
	GalleryID    string    `json:"galleryid"`
	Title        string    `json:"title"`
	SortOrder    int       `json:"sort_order"`
	DisplayCount int       `json:"display_count"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// GalleryDetail is the full response for GET /api/v1/galleries/:galleryid.
type GalleryDetail struct {
	GalleryID       string           `json:"galleryid"`
	Title           string           `json:"title"`
	SortOrder       int              `json:"sort_order"`
	PlacardsDefault json.RawMessage  `json:"placard_defaults,omitempty"`
	Displays        []DisplaySummary `json:"displays"`
	CreatedAt       time.Time        `json:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at"`
}

// GalleriesResponse is returned by GET /api/v1/galleries.
type GalleriesResponse struct {
	ExhibitionID string           `json:"exhibitionid"`
	Offset       int              `json:"offset"`
	Pages        Pages            `json:"pages"`
	Galleries    []GallerySummary `json:"galleries"`
}

// ── Displays ─────────────────────────────────────────────────────────────────

// TemplateSummary is the compact template shape embedded in display responses.
// Presentation and SlotPositions are only populated on GET
// /api/v1/displays/:displayid (the viewer/editor need them to render matte,
// frame, and slot geometry; list views don't).
type TemplateSummary struct {
	TemplateID    string          `json:"templateid"`
	Name          string          `json:"name"`
	PhotoCount    int             `json:"photo_count"`
	SlotPositions json.RawMessage `json:"slot_positions,omitempty"`
	Presentation  json.RawMessage `json:"presentation,omitempty"`
}

// DisplaySummary is a brief display entry embedded in GalleryDetail.
type DisplaySummary struct {
	DisplayID   string           `json:"displayid"`
	Name        string           `json:"name"`
	SortOrder   int              `json:"sort_order"`
	Template    *TemplateSummary `json:"template,omitempty"`
	SlotCount   int              `json:"slot_count"`
	FilledSlots int              `json:"filled_slots"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
}

// SlotPhoto is the photo shape embedded inside a display slot.
// Title is the photo's own title (distinct from the slot's rich_text
// caption) — available as a placard field source. Labels is populated so
// the frontend can resolve placard {LabelName} substitutions (see
// gallery placard_defaults) without a separate fetch per photo.
type SlotPhoto struct {
	PhotoID  string  `json:"photoid"`
	ImageURL string  `json:"imageurl"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	Title    string  `json:"title"`
	Labels   []Label `json:"labels,omitempty"`
}

// DisplaySlot is one slot in a DisplayDetail.
type DisplaySlot struct {
	SlotID    string          `json:"slotid"`
	SlotIndex int             `json:"slot_index"`
	Photo     *SlotPhoto      `json:"photo,omitempty"`
	RichText  *string         `json:"rich_text,omitempty"`
	Placard   json.RawMessage `json:"placard,omitempty"`
}

// DisplayDetail is the full response for GET /api/v1/displays/:displayid.
type DisplayDetail struct {
	DisplayID string           `json:"displayid"`
	GalleryID string           `json:"galleryid"`
	Name      string           `json:"name"`
	SortOrder int              `json:"sort_order"`
	Template  *TemplateSummary `json:"template,omitempty"`
	Slots     []DisplaySlot    `json:"slots"`
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
}

// ── Display Templates ────────────────────────────────────────────────────────

// DisplayTemplate is the full shape of a display template.
type DisplayTemplate struct {
	TemplateID    string          `json:"templateid"`
	Name          string          `json:"name"`
	PhotoCount    int             `json:"photo_count"`
	SlotPositions json.RawMessage `json:"slot_positions"`
	Presentation  json.RawMessage `json:"presentation"`
}

// TemplatesResponse is returned by GET /api/v1/display-templates.
type TemplatesResponse struct {
	Templates []DisplayTemplate `json:"templates"`
}

// ── Gallery / Display / Template write request bodies ────────────────────────

type CreateGalleryRequest struct {
	Title     string `json:"title"`
	SortOrder *int   `json:"sort_order"`
}

type UpdateGalleryRequest struct {
	Title           *string         `json:"title"`
	SortOrder       *int            `json:"sort_order"`
	PlacardsDefault json.RawMessage `json:"placard_defaults"` // nil/absent = no change
	DisplayOrder    []string        `json:"display_order"`    // displayids in new order
}

type CreateDisplayRequest struct {
	TemplateID *string `json:"templateid"`
	SortOrder  *int    `json:"sort_order"`
}

// SlotUpdate sets the full state of one slot. PhotoID "" clears the photo.
// RichText "" clears rich text. Placard nil/absent leaves placard unchanged;
// Placard []byte("null") or empty clears the placard.
type SlotUpdate struct {
	SlotIndex int             `json:"slot_index"`
	PhotoID   string          `json:"photoid"`   // "" to clear
	RichText  string          `json:"rich_text"` // "" to clear
	Placard   json.RawMessage `json:"placard"`   // null/absent to clear
}

// Name is nil for "no change"; a blank/whitespace-only value is ignored
// rather than saved (see displays.go's Update) so a display can't be left
// with an empty name.
type UpdateDisplayRequest struct {
	Name       *string      `json:"name"`
	TemplateID *string      `json:"templateid"` // nil = no change; "" = clear
	SortOrder  *int         `json:"sort_order"`
	Slots      []SlotUpdate `json:"slots"`
}

type CreateTemplateRequest struct {
	Name          string          `json:"name"`
	PhotoCount    int             `json:"photo_count"`
	SlotPositions json.RawMessage `json:"slot_positions"`
	Presentation  json.RawMessage `json:"presentation"`
}

type UpdateTemplateRequest struct {
	Name          *string         `json:"name"`
	PhotoCount    *int            `json:"photo_count"`
	SlotPositions json.RawMessage `json:"slot_positions"` // nil/absent = no change
	Presentation  json.RawMessage `json:"presentation"`   // nil/absent = no change
}

// ── Photo list (wall) ─────────────────────────────────────────────────────────

// PhotoListItem is the compact photo shape returned by GET /api/v1/photos.
type PhotoListItem struct {
	PhotoID  string `json:"photoid"`
	ImageURL string `json:"imageurl"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

// PhotoListResponse is returned by GET /api/v1/photos.
type PhotoListResponse struct {
	Total  int             `json:"total"`
	Offset int             `json:"offset"`
	Limit  int             `json:"limit"`
	Photos []PhotoListItem `json:"photos"`
}

// ── Photo upload ──────────────────────────────────────────────────────────────

// UploadResult is the per-file outcome of a POST /api/v1/photos/upload request.
// Status is "ok" or "error"; Error is populated only when Status is "error".
type UploadResult struct {
	Filename string `json:"filename"`
	PhotoID  string `json:"photoid,omitempty"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
}

// UploadPhotosResponse is returned by POST /api/v1/photos/upload. Each input
// file gets its own result — one file failing does not fail the batch.
type UploadPhotosResponse struct {
	Results []UploadResult `json:"results"`
}

// ── EmojiTypeResponse ────────────────────────────────────────────────────────

// EmojiTypeResponse is returned after uploading a new emoji type.
type EmojiTypeResponse struct {
	EmojiID      string  `json:"emojiid"`
	EmojiChar    *string `json:"emoji,omitempty"`
	ImageURL     *string `json:"imageurl,omitempty"`
	AltText      string  `json:"alttext"`
	IsActive     bool    `json:"is_active"`
	HasSkintones bool    `json:"has_skintones,omitempty"` // true if skintone variants exist
	Skintone     *string `json:"skintone,omitempty"`      // set on variant rows
	Hexcode      string  `json:"hexcode,omitempty"`       // needed to fetch variants
	UsageCount   int     `json:"usage_count,omitempty"`   // total reactions across all photos (Phase 5c popularity sort)
	// OrganizationID (Phase 1b) is unset/omitted for global emoji types
	// (importable via cmd/import-emojis, usable in every organization) and
	// set for custom emoji types uploaded through an organization's own
	// exhibition (POST /api/v1/emoji/types).
	OrganizationID *string `json:"organizationid,omitempty"`
}
