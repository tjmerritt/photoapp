package models

import "time"

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
}
