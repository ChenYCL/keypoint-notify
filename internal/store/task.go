package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/light/keypoint-notify/internal/model"
)

const taskCols = `id, seq, code, title, kind, priority, status, summary,
	owner_identity, owner_role, labels, links, created_by, created_at, updated_at`

func scanTask(row interface{ Scan(...any) error }) (model.Task, error) {
	var (
		t                model.Task
		labels, links    string
		created, updated int64
	)
	err := row.Scan(&t.ID, &t.Seq, &t.Code, &t.Title, &t.Kind, &t.Priority, &t.Status, &t.Summary,
		&t.OwnerIdentity, &t.OwnerRole, &labels, &links, &t.CreatedBy, &created, &updated)
	if err != nil {
		return model.Task{}, err
	}
	t.Labels = scanStrings(labels)
	t.Links = scanLinks(links)
	t.CreatedAt = msToTime(created)
	t.UpdatedAt = msToTime(updated)
	return t, nil
}

// Assignee identifies a person plus the roles they hold.
type Assignee struct {
	Identity string
	Roles    []string
}

// TaskFilter is the shape of every task listing query. Zero values mean "no
// constraint", so an empty filter lists everything.
type TaskFilter struct {
	Status   []string
	Kind     []string
	Priority []string
	Label    []string
	Roles    []string // AND: owner_role, or any side assigned to one of these
	Identity string   // AND: identity *name*: owner, or any side assigned to it
	// AssignedTo is the "what's on my plate" filter. It is an OR across both
	// axes — owner identity, side assignee identity, owner role, side assignee
	// role — which is why it cannot be expressed by setting Identity and Roles
	// together: those two AND, and a task handed to a role but not yet to a
	// person would fall out.
	AssignedTo      *Assignee
	Query           string // free text over code/title/summary
	Since           time.Time
	UpdatedSince    time.Time
	IncludeArchived bool
	Limit           int
	Offset          int
}

// ListTasks returns tasks matching f, newest activity first, with report counts
// and side summaries but without segment bodies (call GetTask for those).
func (s *Store) ListTasks(f TaskFilter) ([]model.Task, error) {
	var (
		where []string
		args  []any
	)
	if len(f.Status) > 0 {
		where = append(where, "t.status IN ("+placeholders(len(f.Status))+")")
		for _, v := range f.Status {
			args = append(args, v)
		}
	} else if !f.IncludeArchived {
		where = append(where, "t.status <> ?")
		args = append(args, model.StatusArchived)
	}
	if len(f.Kind) > 0 {
		where = append(where, "t.kind IN ("+placeholders(len(f.Kind))+")")
		for _, v := range f.Kind {
			args = append(args, v)
		}
	}
	if len(f.Priority) > 0 {
		where = append(where, "t.priority IN ("+placeholders(len(f.Priority))+")")
		for _, v := range f.Priority {
			args = append(args, v)
		}
	}
	if len(f.Roles) > 0 {
		ph := placeholders(len(f.Roles))
		where = append(where, `(t.owner_role IN (`+ph+`) OR EXISTS(
			SELECT 1 FROM sides s WHERE s.task_id = t.id AND s.assignee_role IN (`+ph+`)))`)
		for _, r := range f.Roles {
			args = append(args, r)
		}
		for _, r := range f.Roles {
			args = append(args, r)
		}
	}
	if f.Identity != "" {
		where = append(where, `(t.owner_identity = ? OR EXISTS(
			SELECT 1 FROM sides s WHERE s.task_id = t.id AND s.assignee_identity = ?))`)
		args = append(args, f.Identity, f.Identity)
	}
	if a := f.AssignedTo; a != nil {
		var ors []string
		if a.Identity != "" {
			ors = append(ors, "t.owner_identity = ?",
				"EXISTS(SELECT 1 FROM sides s WHERE s.task_id = t.id AND s.assignee_identity = ?)")
			args = append(args, a.Identity, a.Identity)
		}
		if len(a.Roles) > 0 {
			ph := placeholders(len(a.Roles))
			ors = append(ors, "t.owner_role IN ("+ph+")",
				"EXISTS(SELECT 1 FROM sides s WHERE s.task_id = t.id AND s.assignee_role IN ("+ph+"))")
			for _, r := range a.Roles {
				args = append(args, r)
			}
			for _, r := range a.Roles {
				args = append(args, r)
			}
		}
		if len(ors) > 0 {
			where = append(where, "("+strings.Join(ors, " OR ")+")")
		}
	}
	if f.Query != "" {
		where = append(where, `(t.code LIKE ? OR t.title LIKE ? OR t.summary LIKE ? OR EXISTS(
			SELECT 1 FROM segments g WHERE g.task_id = t.id AND g.body LIKE ?))`)
		like := "%" + f.Query + "%"
		args = append(args, like, like, like, like)
	}
	if !f.Since.IsZero() {
		where = append(where, "t.created_at >= ?")
		args = append(args, f.Since.UTC().UnixMilli())
	}
	if !f.UpdatedSince.IsZero() {
		where = append(where, "t.updated_at >= ?")
		args = append(args, f.UpdatedSince.UTC().UnixMilli())
	}
	for _, l := range f.Label {
		// labels is a JSON array; match the quoted token to avoid substring hits.
		where = append(where, "t.labels LIKE ?")
		args = append(args, "%\""+l+"\"%")
	}

	q := `SELECT ` + prefixCols(taskCols, "t") + `,
		(SELECT COUNT(*) FROM reports r WHERE r.task_id = t.id)
		FROM tasks t`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY t.updated_at DESC, t.seq DESC"
	if f.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", f.Limit)
	}
	if f.Offset > 0 {
		q += fmt.Sprintf(" OFFSET %d", f.Offset)
	}

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Task{}
	for rows.Next() {
		var (
			t                model.Task
			labels, links    string
			created, updated int64
			reports          int
		)
		if err := rows.Scan(&t.ID, &t.Seq, &t.Code, &t.Title, &t.Kind, &t.Priority, &t.Status,
			&t.Summary, &t.OwnerIdentity, &t.OwnerRole, &labels, &links, &t.CreatedBy,
			&created, &updated, &reports); err != nil {
			return nil, err
		}
		t.Labels = scanStrings(labels)
		t.Links = scanLinks(links)
		t.CreatedAt = msToTime(created)
		t.UpdatedAt = msToTime(updated)
		t.ReportCount = reports
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		sides, err := s.Sides(out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Sides = sides
	}
	return out, nil
}

// GetTask loads a task by code (e.g. "KP-12") or by id, with segments, sides and
// attachments populated.
func (s *Store) GetTask(codeOrID string) (model.Task, error) {
	row := s.db.QueryRow(
		`SELECT `+taskCols+` FROM tasks WHERE code = ? OR id = ?`, codeOrID, codeOrID)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Task{}, fmt.Errorf("%w: task %q", ErrNotFound, codeOrID)
	}
	if err != nil {
		return model.Task{}, err
	}
	if err := s.hydrate(&t); err != nil {
		return model.Task{}, err
	}
	return t, nil
}

func (s *Store) hydrate(t *model.Task) error {
	segs, err := s.Segments(t.ID)
	if err != nil {
		return err
	}
	t.Segments = segs
	sides, err := s.Sides(t.ID)
	if err != nil {
		return err
	}
	t.Sides = sides
	files, err := s.TaskFiles(t.ID)
	if err != nil {
		return err
	}
	t.Attachments = files
	watchers, err := s.Watchers(t.ID)
	if err != nil {
		return err
	}
	t.Watchers = watchers
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM reports WHERE task_id = ?`, t.ID).Scan(&t.ReportCount); err != nil {
		return err
	}
	return nil
}

// CreateTaskInput is the write shape for a new task. Skeleton segments may be
// supplied inline; the rest are created empty.
type CreateTaskInput struct {
	Title         string
	Kind          string
	Priority      string
	Status        string
	Summary       string
	OwnerIdentity string
	OwnerRole     string
	Labels        []string
	Links         []model.Link
	Segments      map[string]string // key -> markdown body
	Sides         []CreateSideInput
	CreatedBy     string
}

// CreateSideInput declares a side at task-creation time.
type CreateSideInput struct {
	Key              string
	Title            string
	AssigneeRole     string
	AssigneeIdentity string
	Status           string
	Deps             []string
	Repo             string
	Branch           string
	Segments         map[string]string
}

// CreateTask inserts a task, its skeleton segments and any declared sides in a
// single transaction, and returns the stored task.
func (s *Store) CreateTask(in CreateTaskInput) (model.Task, error) {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return model.Task{}, errors.New("task title is required")
	}
	if in.Kind == "" {
		in.Kind = model.TaskFeature
	}
	if in.Priority == "" {
		in.Priority = "P2"
	}
	if !model.ValidPriority(in.Priority) {
		return model.Task{}, fmt.Errorf("unknown priority %q (want one of %s)", in.Priority, strings.Join(model.Priorities, ", "))
	}
	if in.Status == "" {
		in.Status = model.StatusInbox
	}
	if !model.ValidStatus(in.Status) {
		return model.Task{}, fmt.Errorf("unknown status %q", in.Status)
	}

	var out model.Task
	err := s.tx(func(tx *sql.Tx) error {
		seq, err := nextCounter(tx, "task_seq")
		if err != nil {
			return err
		}
		now := nowMS()
		t := model.Task{
			ID:            NewID("tsk_"),
			Seq:           seq,
			Code:          fmt.Sprintf("KP-%d", seq),
			Title:         in.Title,
			Kind:          in.Kind,
			Priority:      in.Priority,
			Status:        in.Status,
			Summary:       in.Summary,
			OwnerIdentity: in.OwnerIdentity,
			OwnerRole:     in.OwnerRole,
			Labels:        in.Labels,
			Links:         in.Links,
			CreatedBy:     in.CreatedBy,
			CreatedAt:     msToTime(now),
			UpdatedAt:     msToTime(now),
		}
		if t.Labels == nil {
			t.Labels = []string{}
		}
		if _, err := tx.Exec(
			`INSERT INTO tasks(id, seq, code, title, kind, priority, status, summary,
				owner_identity, owner_role, labels, links, created_by, created_at, updated_at)
			 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			t.ID, t.Seq, t.Code, t.Title, t.Kind, t.Priority, t.Status, t.Summary,
			t.OwnerIdentity, t.OwnerRole, jsonStrings(t.Labels), jsonLinks(t.Links),
			t.CreatedBy, now, now); err != nil {
			return err
		}
		// Skeleton segments always exist so consumers can address them by key.
		for i, key := range model.SkeletonKeys {
			body := in.Segments[key]
			if _, err := tx.Exec(
				`INSERT INTO segments(id, task_id, side_id, key, title, body, kind, format, ord, updated_by, created_at, updated_at)
				 VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
				NewID("seg_"), t.ID, "", key, model.SkeletonTitles[key], body, "skeleton", "md",
				i, in.CreatedBy, now, now); err != nil {
				return err
			}
		}
		for _, si := range in.Sides {
			if _, err := insertSideTx(tx, t.ID, si, now, in.CreatedBy); err != nil {
				return err
			}
		}
		out = t
		return nil
	})
	if err != nil {
		return model.Task{}, err
	}
	return s.GetTask(out.Code)
}

// UpdateTaskInput is a sparse patch over the task's own metadata.
type UpdateTaskInput struct {
	Title         *string
	Kind          *string
	Priority      *string
	Status        *string
	Summary       *string
	OwnerIdentity *string
	OwnerRole     *string
	Labels        *[]string
	Links         *[]model.Link
}

// UpdateTask applies a patch and returns the task as stored. A status change to
// "done" also closes every non-done side, because a finished task with open
// work faces is a bookkeeping error rather than an intent.
func (s *Store) UpdateTask(codeOrID string, in UpdateTaskInput, actor string) (model.Task, []string, error) {
	cur, err := s.GetTask(codeOrID)
	if err != nil {
		return model.Task{}, nil, err
	}
	changed := []string{}
	if in.Title != nil && strings.TrimSpace(*in.Title) != "" && *in.Title != cur.Title {
		cur.Title = strings.TrimSpace(*in.Title)
		changed = append(changed, "title")
	}
	if in.Kind != nil && *in.Kind != cur.Kind {
		if *in.Kind == "" {
			return model.Task{}, nil, errors.New("kind cannot be empty")
		}
		cur.Kind = *in.Kind
		changed = append(changed, "kind")
	}
	if in.Priority != nil && *in.Priority != cur.Priority {
		if !model.ValidPriority(*in.Priority) {
			return model.Task{}, nil, fmt.Errorf("unknown priority %q", *in.Priority)
		}
		cur.Priority = *in.Priority
		changed = append(changed, "priority")
	}
	if in.Status != nil && *in.Status != cur.Status {
		if !model.ValidStatus(*in.Status) {
			return model.Task{}, nil, fmt.Errorf("unknown status %q", *in.Status)
		}
		cur.Status = *in.Status
		changed = append(changed, "status")
	}
	if in.Summary != nil && *in.Summary != cur.Summary {
		cur.Summary = *in.Summary
		changed = append(changed, "summary")
	}
	if in.OwnerIdentity != nil && *in.OwnerIdentity != cur.OwnerIdentity {
		cur.OwnerIdentity = *in.OwnerIdentity
		changed = append(changed, "owner_identity")
	}
	if in.OwnerRole != nil && *in.OwnerRole != cur.OwnerRole {
		if *in.OwnerRole != "" {
			if _, err := s.roleExists(*in.OwnerRole); err != nil {
				return model.Task{}, nil, err
			}
		}
		cur.OwnerRole = *in.OwnerRole
		changed = append(changed, "owner_role")
	}
	if in.Labels != nil {
		cur.Labels = *in.Labels
		changed = append(changed, "labels")
	}
	if in.Links != nil {
		cur.Links = *in.Links
		changed = append(changed, "links")
	}
	if len(changed) == 0 {
		return cur, nil, nil
	}
	now := nowMS()
	closeSides := cur.Status == model.StatusDone || cur.Status == model.StatusArchived
	err = s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(
			`UPDATE tasks SET title=?, kind=?, priority=?, status=?, summary=?,
				owner_identity=?, owner_role=?, labels=?, links=?, updated_at=? WHERE id=?`,
			cur.Title, cur.Kind, cur.Priority, cur.Status, cur.Summary,
			cur.OwnerIdentity, cur.OwnerRole, jsonStrings(cur.Labels), jsonLinks(cur.Links),
			now, cur.ID); err != nil {
			return err
		}
		if closeSides {
			if _, err := tx.Exec(
				`UPDATE sides SET status=?, updated_at=? WHERE task_id=? AND status <> ?`,
				model.SideDone, now, cur.ID, model.SideDone); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return model.Task{}, nil, err
	}
	updated, err := s.GetTask(cur.ID)
	return updated, changed, err
}

// TouchTask bumps updated_at so the board re-sorts after a child write.
func touchTask(tx *sql.Tx, taskID string, now int64) error {
	_, err := tx.Exec(`UPDATE tasks SET updated_at=? WHERE id=?`, now, taskID)
	return err
}

// DeleteTask removes a task and everything hanging off it.
func (s *Store) DeleteTask(codeOrID string) error {
	t, err := s.GetTask(codeOrID)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`DELETE FROM tasks WHERE id = ?`, t.ID)
	return err
}

func (s *Store) roleExists(key string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM roles WHERE key = ?`, key).Scan(&n)
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, fmt.Errorf("%w: role %q (see GET /api/v1/roles)", ErrNotFound, key)
	}
	return true, nil
}

// ---------------------------------------------------------------------------
// Watchers
// ---------------------------------------------------------------------------

// AddWatcher subscribes an identity to a task's events.
func (s *Store) AddWatcher(taskID, identityID string) error {
	_, err := s.db.Exec(
		`INSERT INTO watchers(task_id, identity_id, created_at) VALUES(?,?,?)
		 ON CONFLICT DO NOTHING`, taskID, identityID, nowMS())
	return err
}

// RemoveWatcher unsubscribes an identity.
func (s *Store) RemoveWatcher(taskID, identityID string) error {
	_, err := s.db.Exec(`DELETE FROM watchers WHERE task_id=? AND identity_id=?`, taskID, identityID)
	return err
}

// Watchers returns the identity ids watching a task.
func (s *Store) Watchers(taskID string) ([]string, error) {
	rows, err := s.db.Query(`SELECT identity_id FROM watchers WHERE task_id=? ORDER BY created_at`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// SQL helpers
// ---------------------------------------------------------------------------

func placeholders(n int) string {
	if n <= 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// prefixCols rewrites "id, seq, ..." into "t.id, t.seq, ..." so a column list
// constant can be reused under an alias.
func prefixCols(cols, alias string) string {
	parts := strings.Split(cols, ",")
	for i, p := range parts {
		p = strings.TrimSpace(p)
		parts[i] = alias + "." + p
	}
	return strings.Join(parts, ", ")
}

func jsonLinks(links []model.Link) string {
	if links == nil {
		return "[]"
	}
	return jsonAny(links)
}

func scanLinks(raw string) []model.Link {
	out := []model.Link{}
	if raw == "" || raw == "null" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	if out == nil {
		out = []model.Link{}
	}
	return out
}
