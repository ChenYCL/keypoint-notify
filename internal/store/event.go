package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/light/keypoint-notify/internal/model"
)

// Event types. These are also the tokens a webhook subscribes to; a webhook
// listing a prefix like "task." receives every event under it.
const (
	EvTaskCreated    = "task.created"
	EvTaskUpdated    = "task.updated"
	EvTaskStatus     = "task.status_changed"
	EvTaskDeleted    = "task.deleted"
	EvSegmentSet     = "segment.upserted"
	EvSegmentDeleted = "segment.deleted"
	EvSideCreated    = "side.created"
	EvSideUpdated    = "side.updated"
	EvSideAssigned   = "side.assigned"
	EvSideDeleted    = "side.deleted"
	EvReportCreated  = "report.created"
	EvFileUploaded   = "file.uploaded"
	EvIdentityUpdate = "identity.updated"
)

// AllEventTypes is the vocabulary advertised by /api/v1/schema.
var AllEventTypes = []string{
	EvTaskCreated, EvTaskUpdated, EvTaskStatus, EvTaskDeleted,
	EvSegmentSet, EvSegmentDeleted,
	EvSideCreated, EvSideUpdated, EvSideAssigned, EvSideDeleted,
	EvReportCreated, EvFileUploaded, EvIdentityUpdate,
}

// EmitInput describes one thing that happened. Emit persists the event and
// derives the unread notifications from it in the same transaction, so an
// event and its inbox entries can never disagree.
type EmitInput struct {
	Type    string
	Actor   model.Identity
	Task    *model.Task
	SideID  string
	Kind    string // notification category, e.g. "mention" | "assigned" | "report"
	Title   string // one line shown in the inbox
	Payload map[string]any
	// Notify lists extra targets: identity names or role keys. Watchers, the
	// task owner and the involved side's assignee are always included.
	Notify []string
}

// Emit writes an event and fans it out to the inboxes that should see it.
func (s *Store) Emit(in EmitInput) (model.Event, error) {
	if in.Type == "" {
		return model.Event{}, errors.New("event type is required")
	}
	ev := model.Event{
		Type:      in.Type,
		ActorID:   in.Actor.ID,
		ActorName: in.Actor.Name,
		Payload:   in.Payload,
		CreatedAt: msToTime(nowMS()),
	}
	if in.Task != nil {
		ev.TaskID = in.Task.ID
		ev.TaskCode = in.Task.Code
	}
	ev.SideID = in.SideID

	err := s.tx(func(tx *sql.Tx) error {
		id, err := appendEventTx(tx, ev)
		if err != nil {
			return err
		}
		ev.ID = id
		return s.fanoutTx(tx, ev, in)
	})
	if err != nil {
		return model.Event{}, err
	}
	return ev, nil
}

func appendEventTx(tx *sql.Tx, ev model.Event) (int64, error) {
	res, err := tx.Exec(
		`INSERT INTO events(type, actor_id, actor_name, task_id, task_code, side_id, payload, created_at)
		 VALUES(?,?,?,?,?,?,?,?)`,
		ev.Type, ev.ActorID, ev.ActorName, ev.TaskID, ev.TaskCode, ev.SideID,
		jsonAny(ev.Payload), ev.CreatedAt.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// fanoutTx turns an event into notification rows for every identity that
// should see it: explicit targets, watchers, the task owner, and whoever owns
// the side the event happened on. The actor never notifies themselves.
func (s *Store) fanoutTx(tx *sql.Tx, ev model.Event, in EmitInput) error {
	targets := map[string]bool{}

	resolve := func(nameOrRole string) error {
		nameOrRole = strings.TrimPrefix(strings.TrimSpace(nameOrRole), "@")
		if nameOrRole == "" {
			return nil
		}
		if idn, err := identityByNameTx(tx, nameOrRole); err == nil {
			targets[idn.ID] = true
			return nil
		}
		// Not an identity name: treat it as a role and fan out to holders.
		ids, err := identitiesWithRole(tx, nameOrRole)
		if err != nil {
			return err
		}
		for _, id := range ids {
			targets[id] = true
		}
		return nil
	}

	for _, n := range in.Notify {
		if err := resolve(n); err != nil {
			return err
		}
	}
	if ev.TaskID != "" {
		rows, err := tx.Query(`SELECT identity_id FROM watchers WHERE task_id = ?`, ev.TaskID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			targets[id] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if in.Task != nil && in.Task.OwnerIdentity != "" {
			if idn, err := identityByNameTx(tx, in.Task.OwnerIdentity); err == nil {
				targets[idn.ID] = true
			}
		}
	}
	if ev.SideID != "" {
		var role, identity string
		if err := tx.QueryRow(`SELECT assignee_role, assignee_identity FROM sides WHERE id=?`, ev.SideID).
			Scan(&role, &identity); err == nil {
			if identity != "" {
				if idn, err := identityByNameTx(tx, identity); err == nil {
					targets[idn.ID] = true
				}
			}
			if role != "" {
				ids, err := identitiesWithRole(tx, role)
				if err != nil {
					return err
				}
				for _, id := range ids {
					targets[id] = true
				}
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}

	kind := in.Kind
	if kind == "" {
		kind = ev.Type
	}
	now := nowMS()
	for id := range targets {
		if id == ev.ActorID {
			continue
		}
		url := ""
		if ev.TaskCode != "" {
			url = "/t/" + ev.TaskCode
			if ev.SideID != "" {
				url += "#side-" + ev.SideID
			}
		}
		if _, err := tx.Exec(
			`INSERT INTO notifications(id, identity_id, event_id, task_id, task_code, kind, title, url, created_at)
			 VALUES(?,?,?,?,?,?,?,?,?)
			 ON CONFLICT(identity_id, event_id, kind) DO NOTHING`,
			NewID("ntf_"), id, ev.ID, ev.TaskID, ev.TaskCode, kind, in.Title, url, now); err != nil {
			return err
		}
	}
	return nil
}

func identitiesWithRole(tx *sql.Tx, role string) ([]string, error) {
	rows, err := tx.Query(`SELECT id, roles FROM identities WHERE disabled = 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		if containsString(scanStrings(raw), role) {
			out = append(out, id)
		}
	}
	return out, rows.Err()
}

// EventFilter describes an event stream query.
type EventFilter struct {
	Since    int64 // exclusive event id cursor
	TaskID   string
	Types    []string
	Limit    int
	OrderAsc bool
}

// ListEvents returns events after the cursor, oldest-first by default when a
// cursor is given (a stream) and newest-first when listing.
func (s *Store) ListEvents(f EventFilter) ([]model.Event, error) {
	var (
		where []string
		args  []any
	)
	if f.Since > 0 {
		where = append(where, "id > ?")
		args = append(args, f.Since)
	}
	if f.TaskID != "" {
		where = append(where, "task_id = ?")
		args = append(args, f.TaskID)
	}
	if len(f.Types) > 0 {
		or := make([]string, 0, len(f.Types))
		for _, t := range f.Types {
			or = append(or, "(type = ? OR type LIKE ?)")
			args = append(args, t, strings.TrimSuffix(t, ".")+".%")
		}
		where = append(where, "("+strings.Join(or, " OR ")+")")
	}

	q := `SELECT id, type, actor_id, actor_name, task_id, task_code, side_id, payload, created_at FROM events`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	if f.Since > 0 || f.OrderAsc {
		q += " ORDER BY id ASC"
	} else {
		q += " ORDER BY id DESC"
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	q += fmt.Sprintf(" LIMIT %d", limit)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Event{}
	for rows.Next() {
		var (
			ev      model.Event
			payload string
			created int64
		)
		if err := rows.Scan(&ev.ID, &ev.Type, &ev.ActorID, &ev.ActorName, &ev.TaskID, &ev.TaskCode,
			&ev.SideID, &payload, &created); err != nil {
			return nil, err
		}
		ev.Payload = scanAny(payload)
		ev.CreatedAt = msToTime(created)
		out = append(out, ev)
	}
	return out, rows.Err()
}

// LatestEventID returns the current stream head, used to seed SSE clients.
func (s *Store) LatestEventID() (int64, error) {
	var id sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(id) FROM events`).Scan(&id); err != nil {
		return 0, err
	}
	return id.Int64, nil
}

// ---------------------------------------------------------------------------
// Inbox
// ---------------------------------------------------------------------------

// InboxFilter describes an inbox query.
type InboxFilter struct {
	IdentityID string
	UnreadOnly bool
	Limit      int
}

// Inbox returns an identity's notifications, newest first.
func (s *Store) Inbox(f InboxFilter) ([]model.Notification, error) {
	q := `SELECT id, identity_id, event_id, task_id, task_code, kind, title, url, read_at, created_at
	      FROM notifications WHERE identity_id = ?`
	args := []any{f.IdentityID}
	if f.UnreadOnly {
		q += ` AND read_at IS NULL`
	}
	q += ` ORDER BY event_id DESC`
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	q += fmt.Sprintf(" LIMIT %d", limit)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Notification{}
	for rows.Next() {
		var (
			n       model.Notification
			readAt  sql.NullInt64
			created int64
		)
		if err := rows.Scan(&n.ID, &n.IdentityID, &n.EventID, &n.TaskID, &n.TaskCode,
			&n.Kind, &n.Title, &n.URL, &readAt, &created); err != nil {
			return nil, err
		}
		n.ReadAt = msToTimePtr(readAt)
		n.CreatedAt = msToTime(created)
		out = append(out, n)
	}
	return out, rows.Err()
}

// UnreadCount is the badge number for an identity.
func (s *Store) UnreadCount(identityID string) (int, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM notifications WHERE identity_id = ? AND read_at IS NULL`, identityID).Scan(&n)
	return n, err
}

// MarkRead marks specific notifications read; an empty ids slice marks all of
// the identity's unread notifications.
func (s *Store) MarkRead(identityID string, ids []string) (int64, error) {
	now := nowMS()
	if len(ids) == 0 {
		res, err := s.db.Exec(
			`UPDATE notifications SET read_at = ? WHERE identity_id = ? AND read_at IS NULL`, now, identityID)
		if err != nil {
			return 0, err
		}
		return res.RowsAffected()
	}
	n := int64(0)
	err := s.tx(func(tx *sql.Tx) error {
		for _, id := range ids {
			res, err := tx.Exec(
				`UPDATE notifications SET read_at = ? WHERE id = ? AND identity_id = ? AND read_at IS NULL`,
				now, id, identityID)
			if err != nil {
				return err
			}
			affected, _ := res.RowsAffected()
			n += affected
		}
		return nil
	})
	return n, err
}

// ---------------------------------------------------------------------------
// Webhooks
// ---------------------------------------------------------------------------

// WebhookCursorKey is the meta key holding one subscription's delivery
// position. Both the store (at creation) and the dispatcher (at delivery) use
// it, so they must agree on the spelling.
func WebhookCursorKey(id string) string { return "webhook_cursor:" + id }

// UpsertWebhook creates or replaces a webhook subscription.
//
// A brand-new subscription starts its delivery cursor at the current head of
// the event log. Seeding it here rather than on the dispatcher's first tick
// closes a race that would otherwise silently drop events created in the
// seconds between adding a hook and the first delivery pass.
func (s *Store) UpsertWebhook(id, url, secret string, events []string, enabled bool) (model.Webhook, error) {
	if strings.TrimSpace(url) == "" {
		return model.Webhook{}, errors.New("webhook url is required")
	}
	isNew := id == ""
	if isNew {
		id = NewID("whk_")
	}
	err := s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(
			`INSERT INTO webhooks(id, url, secret, events, enabled, created_at) VALUES(?,?,?,?,?,?)
			 ON CONFLICT(id) DO UPDATE SET url=excluded.url, secret=excluded.secret,
			   events=excluded.events, enabled=excluded.enabled`,
			id, url, secret, jsonStrings(events), boolToInt(enabled), nowMS()); err != nil {
			return err
		}
		if !isNew {
			return nil
		}
		var head sql.NullInt64
		if err := tx.QueryRow(`SELECT MAX(id) FROM events`).Scan(&head); err != nil {
			return err
		}
		_, err := tx.Exec(
			`INSERT INTO meta(key, value) VALUES(?,?)
			 ON CONFLICT(key) DO NOTHING`, WebhookCursorKey(id), strconv.FormatInt(head.Int64, 10))
		return err
	})
	if err != nil {
		return model.Webhook{}, err
	}
	return s.Webhook(id)
}

// Webhook loads one subscription.
func (s *Store) Webhook(id string) (model.Webhook, error) {
	var (
		w         model.Webhook
		events    string
		enabled   int
		lastAt    sql.NullInt64
		createdAt int64
	)
	err := s.db.QueryRow(
		`SELECT id, url, secret, events, enabled, last_status, last_error, last_at, created_at
		 FROM webhooks WHERE id = ?`, id).
		Scan(&w.ID, &w.URL, &w.Secret, &events, &enabled, &w.LastStatus, &w.LastError, &lastAt, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Webhook{}, fmt.Errorf("%w: webhook %s", ErrNotFound, id)
	}
	if err != nil {
		return model.Webhook{}, err
	}
	w.Events = scanStrings(events)
	w.Enabled = enabled != 0
	w.LastAt = msToTimePtr(lastAt)
	w.CreatedAt = msToTime(createdAt)
	return w, nil
}

// ListWebhooks returns every subscription.
func (s *Store) ListWebhooks() ([]model.Webhook, error) {
	rows, err := s.db.Query(`SELECT id FROM webhooks ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []model.Webhook{}
	for _, id := range ids {
		w, err := s.Webhook(id)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, nil
}

// DeleteWebhook removes a subscription.
func (s *Store) DeleteWebhook(id string) error {
	_, err := s.db.Exec(`DELETE FROM webhooks WHERE id = ?`, id)
	return err
}

// RecordWebhookResult stores the outcome of the most recent delivery attempt.
func (s *Store) RecordWebhookResult(id string, status int, errMsg string) error {
	_, err := s.db.Exec(
		`UPDATE webhooks SET last_status=?, last_error=?, last_at=? WHERE id=?`,
		status, errMsg, nowMS(), id)
	return err
}
