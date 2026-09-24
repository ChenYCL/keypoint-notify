// Package model defines the core domain types of Keypoint Notify.
//
// Field names and enum values are part of the public contract: the CLI, the
// web UI, the /api/v1/pack endpoint and every LLM consuming them all read the
// same JSON tags. Renaming a tag is a breaking change.
package model

import (
	"strings"
	"time"
	"unicode"
)

// ---------------------------------------------------------------------------
// Enums. String values are stable API surface.
// ---------------------------------------------------------------------------

// IdentityKind distinguishes a person from an automated agent session.
const (
	KindHuman = "human"
	KindAgent = "agent"
)

// Task kinds.
const (
	TaskBug      = "bug"
	TaskFeature  = "feature"
	TaskChore    = "chore"
	TaskResearch = "research"
	TaskReview   = "review"
	TaskIncident = "incident"
)

// TaskStatus is the lifecycle of a task. The kanban columns are exactly these.
const (
	StatusInbox    = "inbox"
	StatusReady    = "ready"
	StatusDoing    = "doing"
	StatusBlocked  = "blocked"
	StatusReview   = "review"
	StatusDone     = "done"
	StatusArchived = "archived"
)

// SideStatus is the lifecycle of one parallel work face of a task.
const (
	SideTodo    = "todo"
	SideDoing   = "doing"
	SideBlocked = "blocked"
	SideDone    = "done"
)

// Report types. A report is an append-only entry on a task timeline.
const (
	ReportProgress = "progress"
	ReportBlocker  = "blocker"
	ReportDecision = "decision"
	ReportHandoff  = "handoff"
	ReportResult   = "result"
	ReportQuestion = "question"
	// ReportFinding is a research finding or argument — something learned or a
	// point for or against an option, recorded before anything is decided.
	ReportFinding = "finding"
)

// Priorities, ordered.
var Priorities = []string{"P0", "P1", "P2", "P3"}

// SkeletonKeys are the fixed task-level segments every task carries a slot for.
// They are created empty on task creation and filled in as context arrives, so
// a consumer can always ask for `goal` and get either content or an explicit
// empty marker rather than a 404.
var SkeletonKeys = []string{
	"context",     // 背景
	"goal",        // 目标
	"deliverable", // 交付物
	"constraint",  // 约束
	"acceptance",  // 验收标准
	"interface",   // 接口 / 契约
	"files",       // 相关文件
}

// SkeletonTitles maps a skeleton key to its human title.
var SkeletonTitles = map[string]string{
	"context":     "背景",
	"goal":        "目标",
	"deliverable": "交付物",
	"constraint":  "约束",
	"acceptance":  "验收标准",
	"interface":   "接口/契约",
	"files":       "相关文件",
}

// IsSkeletonKey reports whether key is one of the fixed task-level segments.
func IsSkeletonKey(key string) bool {
	_, ok := SkeletonTitles[key]
	return ok
}

// ---------------------------------------------------------------------------
// Core entities
// ---------------------------------------------------------------------------

// Identity is an authenticated actor: a person or an agent session. It is the
// thing an API key binds to. Roles are attached here and may be changed at any
// time, which is what makes role re-binding non-destructive.
type Identity struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Kind       string    `json:"kind"`
	Roles      []string  `json:"roles"`
	ActiveRole string    `json:"active_role"`
	KeyPrefix  string    `json:"key_prefix,omitempty"`
	Disabled   bool      `json:"disabled"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// HasRole reports whether the identity currently holds role.
func (i Identity) HasRole(role string) bool {
	for _, r := range i.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// Role is a named capacity that work can be addressed to.
type Role struct {
	Key          string   `json:"key"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Capabilities []string `json:"capabilities,omitempty"`
	DefaultSides []string `json:"default_sides,omitempty"`
	Builtin      bool     `json:"builtin"`
}

// Link is an external pointer attached to a task (repo, branch, PR, doc).
type Link struct {
	Kind string `json:"kind"` // repo | branch | pr | url | doc
	URL  string `json:"url"`
	Note string `json:"note,omitempty"`
}

// Attachment is an uploaded file. URL is always fetchable with the same API key.
type Attachment struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	MIME    string `json:"mime"`
	Size    int64  `json:"size"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256,omitempty"`
	IsImage bool   `json:"is_image"`
}

// Segment is one addressable slice of a task's detail. Segments are the unit of
// copy: every segment has a stable key, can be fetched alone, and is rendered
// with its key visible so a reader can cite it back.
//
// SideID is empty for task-level segments and set for segments belonging to a
// side.
type Segment struct {
	ID          string       `json:"id"`
	TaskID      string       `json:"task_id,omitempty"`
	SideID      string       `json:"side_id,omitempty"`
	SideKey     string       `json:"side_key,omitempty"`
	Key         string       `json:"key"`
	Title       string       `json:"title"`
	Body        string       `json:"body"`
	Kind        string       `json:"kind"`   // skeleton | free
	Format      string       `json:"format"` // md | text | code
	Order       int          `json:"order"`
	UpdatedBy   string       `json:"updated_by,omitempty"`
	UpdatedAt   time.Time    `json:"updated_at"`
	Attachments []Attachment `json:"attachments,omitempty"`
	Empty       bool         `json:"empty,omitempty"`
}

// Side is one parallel work face of a task: a named slice of the work with its
// own detail, its own owner and its own dependency edges.
type Side struct {
	ID               string    `json:"id"`
	TaskID           string    `json:"task_id,omitempty"`
	Key              string    `json:"key"`
	Title            string    `json:"title"`
	AssigneeRole     string    `json:"assignee_role,omitempty"`
	AssigneeIdentity string    `json:"assignee_identity,omitempty"`
	Status           string    `json:"status"`
	Deps             []string  `json:"deps"`
	Repo             string    `json:"repo,omitempty"`
	Branch           string    `json:"branch,omitempty"`
	Order            int       `json:"order"`
	Segments         []Segment `json:"segments,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// Task is the primary object of the system.
type Task struct {
	ID            string       `json:"id"`
	Code          string       `json:"code"` // stable short handle, e.g. KP-12
	Seq           int          `json:"seq"`
	Title         string       `json:"title"`
	Kind          string       `json:"kind"`
	Priority      string       `json:"priority"`
	Status        string       `json:"status"`
	Summary       string       `json:"summary,omitempty"`
	OwnerIdentity string       `json:"owner_identity,omitempty"`
	OwnerRole     string       `json:"owner_role,omitempty"`
	Labels        []string     `json:"labels"`
	Links         []Link       `json:"links,omitempty"`
	Segments      []Segment    `json:"segments,omitempty"`
	Sides         []Side       `json:"sides,omitempty"`
	Attachments   []Attachment `json:"attachments,omitempty"`
	Watchers      []string     `json:"watchers,omitempty"`
	CreatedBy     string       `json:"created_by,omitempty"`
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
	ReportCount   int          `json:"report_count"`
	UnreadCount   int          `json:"unread_count,omitempty"`
}

// Report is an append-only entry on a task timeline. Reports are what "上报"
// produces: a progress note, a blocker, a decision, a handoff, a result.
type Report struct {
	ID           string       `json:"id"`
	TaskID       string       `json:"task_id,omitempty"`
	TaskCode     string       `json:"task_code,omitempty"`
	SideID       string       `json:"side_id,omitempty"`
	SideKey      string       `json:"side_key,omitempty"`
	IdentityID   string       `json:"identity_id,omitempty"`
	IdentityName string       `json:"identity_name,omitempty"`
	Role         string       `json:"role,omitempty"`
	Type         string       `json:"type"`
	Priority     string       `json:"priority,omitempty"`
	Body         string       `json:"body"`
	Segments     []Segment    `json:"segments,omitempty"`
	Mentions     []string     `json:"mentions,omitempty"`
	Attachments  []Attachment `json:"attachments,omitempty"`
	CreatedAt    time.Time    `json:"created_at"`
	EditedAt     *time.Time   `json:"edited_at,omitempty"`
}

// Event is a single fact about something that happened. Every mutation emits
// one. Events drive the unread inbox, the webhook fan-out and the SSE stream.
type Event struct {
	ID        int64          `json:"id"`
	Type      string         `json:"type"`
	ActorID   string         `json:"actor_id,omitempty"`
	ActorName string         `json:"actor_name,omitempty"`
	TaskID    string         `json:"task_id,omitempty"`
	TaskCode  string         `json:"task_code,omitempty"`
	SideID    string         `json:"side_id,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

// Notification is one identity's unread/read marker over an event.
type Notification struct {
	ID         string     `json:"id"`
	IdentityID string     `json:"identity_id"`
	EventID    int64      `json:"event_id"`
	TaskID     string     `json:"task_id,omitempty"`
	TaskCode   string     `json:"task_code,omitempty"`
	Kind       string     `json:"kind"`
	Title      string     `json:"title"`
	URL        string     `json:"url,omitempty"`
	ReadAt     *time.Time `json:"read_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// Webhook is an outbound subscription.
type Webhook struct {
	ID         string     `json:"id"`
	URL        string     `json:"url"`
	Secret     string     `json:"secret,omitempty"`
	Events     []string   `json:"events"`
	Enabled    bool       `json:"enabled"`
	LastStatus int        `json:"last_status,omitempty"`
	LastError  string     `json:"last_error,omitempty"`
	LastAt     *time.Time `json:"last_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// ValidPriority reports whether p is a known priority.
func ValidPriority(p string) bool {
	for _, x := range Priorities {
		if x == p {
			return true
		}
	}
	return false
}

var validStatuses = map[string]bool{
	StatusInbox: true, StatusReady: true, StatusDoing: true, StatusBlocked: true,
	StatusReview: true, StatusDone: true, StatusArchived: true,
}

// ValidStatus reports whether s is a known task status.
func ValidStatus(s string) bool { return validStatuses[s] }

var validSideStatuses = map[string]bool{
	SideTodo: true, SideDoing: true, SideBlocked: true, SideDone: true,
}

// ValidSideStatus reports whether s is a known side status.
func ValidSideStatus(s string) bool { return validSideStatuses[s] }

var validReportTypes = map[string]bool{
	ReportProgress: true, ReportBlocker: true, ReportDecision: true,
	ReportHandoff: true, ReportResult: true, ReportQuestion: true,
	ReportFinding: true,
}

// ValidReportType reports whether t is a known report type.
func ValidReportType(t string) bool { return validReportTypes[t] }

// Slug normalises a free-form name into a segment/side key. Letters (including
// CJK), digits, dash and underscore survive; runs of separators collapse to a
// single dash. CJK is kept rather than transliterated so that a Chinese title
// still yields a key a human can type.
func Slug(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	prevDash := true // leading separators are dropped
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if unicode.IsUpper(r) {
				r = unicode.ToLower(r)
			}
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
