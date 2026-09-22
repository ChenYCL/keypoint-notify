package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/light/keypoint-notify/internal/model"
)

const sideCols = `id, task_id, key, title, assignee_role, assignee_identity, status, deps,
	repo, branch, ord, created_at, updated_at`

func scanSide(row interface{ Scan(...any) error }) (model.Side, error) {
	var (
		sd               model.Side
		deps             string
		created, updated int64
	)
	err := row.Scan(&sd.ID, &sd.TaskID, &sd.Key, &sd.Title, &sd.AssigneeRole, &sd.AssigneeIdentity,
		&sd.Status, &deps, &sd.Repo, &sd.Branch, &sd.Order, &created, &updated)
	if err != nil {
		return model.Side{}, err
	}
	sd.Deps = scanStrings(deps)
	sd.CreatedAt = msToTime(created)
	sd.UpdatedAt = msToTime(updated)
	return sd, nil
}

// Sides returns a task's work faces in declaration order, each with its own
// segments attached.
func (s *Store) Sides(taskID string) ([]model.Side, error) {
	rows, err := s.db.Query(`SELECT `+sideCols+` FROM sides WHERE task_id=? ORDER BY ord, created_at`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Side{}
	for rows.Next() {
		sd, err := scanSide(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sd)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		segs, err := s.sideSegments(out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Segments = segs
	}
	return out, nil
}

// Side resolves a side by task id plus side key or id.
func (s *Store) Side(taskID, keyOrID string) (model.Side, error) {
	row := s.db.QueryRow(
		`SELECT `+sideCols+` FROM sides WHERE task_id=? AND (key=? OR id=?)`, taskID, keyOrID, keyOrID)
	sd, err := scanSide(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Side{}, fmt.Errorf("%w: side %q", ErrNotFound, keyOrID)
	}
	if err != nil {
		return model.Side{}, err
	}
	segs, err := s.sideSegments(sd.ID)
	if err != nil {
		return model.Side{}, err
	}
	sd.Segments = segs
	return sd, nil
}

func insertSideTx(tx *sql.Tx, taskID string, in CreateSideInput, now int64, actor string) (model.Side, error) {
	key := model.Slug(in.Key)
	if key == "" {
		key = model.Slug(in.Title)
	}
	if key == "" {
		return model.Side{}, errors.New("side key or title is required")
	}
	title := in.Title
	if title == "" {
		title = key
	}
	status := in.Status
	if status == "" {
		status = model.SideTodo
	}
	if !model.ValidSideStatus(status) {
		return model.Side{}, fmt.Errorf("unknown side status %q", status)
	}
	var ord int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(ord)+1, 0) FROM sides WHERE task_id=?`, taskID).Scan(&ord); err != nil {
		return model.Side{}, err
	}
	id := NewID("sid_")
	if _, err := tx.Exec(
		`INSERT INTO sides(id, task_id, key, title, assignee_role, assignee_identity, status, deps,
			repo, branch, ord, created_at, updated_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, taskID, key, title, in.AssigneeRole, in.AssigneeIdentity, status, jsonStrings(in.Deps),
		in.Repo, in.Branch, ord, now, now); err != nil {
		if isUniqueViolation(err) {
			return model.Side{}, fmt.Errorf("%w: side %q already exists on this task", ErrConflict, key)
		}
		return model.Side{}, err
	}
	// Side segments are free-form: whatever the caller declared.
	i := 0
	for k, body := range in.Segments {
		segKey := model.Slug(k)
		if segKey == "" {
			continue
		}
		if _, err := tx.Exec(
			`INSERT INTO segments(id, task_id, side_id, key, title, body, kind, format, ord, updated_by, created_at, updated_at)
			 VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			NewID("seg_"), taskID, id, segKey, k, body, "free", "md", i, actor, now, now); err != nil {
			return model.Side{}, err
		}
		i++
	}
	return model.Side{
		ID: id, TaskID: taskID, Key: key, Title: title,
		AssigneeRole: in.AssigneeRole, AssigneeIdentity: in.AssigneeIdentity,
		Status: status, Deps: in.Deps, Repo: in.Repo, Branch: in.Branch, Order: ord,
		CreatedAt: msToTime(now), UpdatedAt: msToTime(now),
	}, nil
}

// AddSide appends a work face to an existing task.
func (s *Store) AddSide(taskID string, in CreateSideInput, actor string) (model.Side, error) {
	if in.AssigneeRole != "" {
		if _, err := s.roleExists(in.AssigneeRole); err != nil {
			return model.Side{}, err
		}
	}
	var out model.Side
	err := s.tx(func(tx *sql.Tx) error {
		now := nowMS()
		sd, err := insertSideTx(tx, taskID, in, now, actor)
		if err != nil {
			return err
		}
		if err := touchTask(tx, taskID, now); err != nil {
			return err
		}
		out = sd
		return nil
	})
	return out, err
}

// UpdateSideInput is a sparse patch over a side.
type UpdateSideInput struct {
	Title            *string
	AssigneeRole     *string
	AssigneeIdentity *string
	Status           *string
	Deps             *[]string
	Repo             *string
	Branch           *string
	ClearAssignee    bool
}

// UpdateSide applies a patch. AssigneeIdentity must name a real identity when
// set, so a typo cannot silently create an orphan assignment.
func (s *Store) UpdateSide(taskID, keyOrID string, in UpdateSideInput, actor string) (model.Side, error) {
	cur, err := s.Side(taskID, keyOrID)
	if err != nil {
		return model.Side{}, err
	}
	statusBefore := cur.Status
	if in.Title != nil && strings.TrimSpace(*in.Title) != "" {
		cur.Title = strings.TrimSpace(*in.Title)
	}
	if in.AssigneeRole != nil {
		if *in.AssigneeRole != "" {
			if _, err := s.roleExists(*in.AssigneeRole); err != nil {
				return model.Side{}, err
			}
		}
		cur.AssigneeRole = *in.AssigneeRole
	}
	if in.AssigneeIdentity != nil {
		if *in.AssigneeIdentity != "" {
			idn, err := s.IdentityByName(*in.AssigneeIdentity)
			if err != nil {
				return model.Side{}, fmt.Errorf("assignee %q: %w", *in.AssigneeIdentity, err)
			}
			cur.AssigneeIdentity = idn.Name
			if cur.AssigneeRole == "" {
				cur.AssigneeRole = idn.ActiveRole
			}
		} else {
			cur.AssigneeIdentity = ""
		}
	}
	if in.ClearAssignee {
		cur.AssigneeIdentity = ""
		cur.AssigneeRole = ""
	}
	if in.Status != nil && *in.Status != cur.Status {
		if !model.ValidSideStatus(*in.Status) {
			return model.Side{}, fmt.Errorf("unknown side status %q", *in.Status)
		}
		cur.Status = *in.Status
	}
	if in.Deps != nil {
		cur.Deps = *in.Deps
		if err := s.validateDeps(cur); err != nil {
			return model.Side{}, err
		}
	}
	if in.Repo != nil {
		cur.Repo = *in.Repo
	}
	if in.Branch != nil {
		cur.Branch = *in.Branch
	}

	justFinished := cur.Status == model.SideDone && statusBefore != model.SideDone

	err = s.tx(func(tx *sql.Tx) error {
		now := nowMS()
		if _, err := tx.Exec(
			`UPDATE sides SET title=?, assignee_role=?, assignee_identity=?, status=?, deps=?,
				repo=?, branch=?, updated_at=? WHERE id=?`,
			cur.Title, cur.AssigneeRole, cur.AssigneeIdentity, cur.Status, jsonStrings(cur.Deps),
			cur.Repo, cur.Branch, now, cur.ID); err != nil {
			return err
		}
		if err := touchTask(tx, taskID, now); err != nil {
			return err
		}
		if !justFinished {
			return nil
		}
		// Finishing a work face is what unblocks the ones waiting on it. Doing
		// this here — in the same commit, right after the status write — is what
		// lets a downstream session be woken instead of having to poll for the
		// fact that its dependency landed.
		released, err := releaseDependentsTx(tx, taskID, cur.Key, now)
		if err != nil {
			return err
		}
		if len(released) == 0 {
			return nil
		}
		emitter := model.Identity{Name: actor}
		if idn, err := identityByNameTx(tx, actor); err == nil {
			emitter = idn
		}
		task, err := taskTx(tx, taskID)
		if err != nil {
			return err
		}
		for _, sd := range released {
			who := "@" + sd.AssigneeRole
			if sd.AssigneeIdentity != "" {
				who = sd.AssigneeIdentity
			}
			if sd.AssigneeRole == "" && sd.AssigneeIdentity == "" {
				who = "还没有人认领"
			}
			if _, err := emitTx(tx, EmitInput{
				Type: EvSideUnblocked, Actor: emitter, Task: &task, SideID: sd.ID,
				Kind: "unblocked",
				Title: fmt.Sprintf("%s · %s 依赖已完成（%s），可以开工了",
					task.Code, sd.Key, cur.Key),
				Notify: []string{sd.AssigneeRole},
				Payload: map[string]any{
					"side": sd.Key, "unblocked_by": cur.Key,
					"assignee_role": sd.AssigneeRole, "assignee": who,
				},
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return model.Side{}, err
	}
	return s.Side(taskID, cur.Key)
}

// validateDeps rejects self-references and dependency cycles. A cycle here
// would make the board's "blocked" computation non-terminating, so it is worth
// the traversal.
func (s *Store) validateDeps(sd model.Side) error {
	for _, d := range sd.Deps {
		if d == sd.Key {
			return fmt.Errorf("side %q cannot depend on itself", sd.Key)
		}
	}
	all, err := s.Sides(sd.TaskID)
	if err != nil {
		return err
	}
	byKey := map[string]model.Side{}
	for _, x := range all {
		byKey[x.Key] = x
	}
	for _, d := range sd.Deps {
		if _, ok := byKey[d]; !ok {
			return fmt.Errorf("side %q depends on unknown side %q", sd.Key, d)
		}
	}
	// Depth-first walk over the *proposed* graph.
	var visit func(key string, seen map[string]bool) error
	visit = func(key string, seen map[string]bool) error {
		if seen[key] {
			return fmt.Errorf("dependency cycle involving side %q", key)
		}
		seen[key] = true
		cur, ok := byKey[key]
		if !ok {
			return nil
		}
		deps := cur.Deps
		if key == sd.Key {
			deps = sd.Deps
		}
		for _, d := range deps {
			if err := visit(d, seen); err != nil {
				return err
			}
		}
		delete(seen, key)
		return nil
	}
	return visit(sd.Key, map[string]bool{})
}

// AssignSide is the shorthand for "give this work face to this role/identity".
func (s *Store) AssignSide(taskID, keyOrID, role, identity string, actor string) (model.Side, error) {
	in := UpdateSideInput{}
	if role != "" {
		in.AssigneeRole = &role
	}
	if identity != "" {
		in.AssigneeIdentity = &identity
	}
	return s.UpdateSide(taskID, keyOrID, in, actor)
}

// DeleteSide removes a work face and its segments.
func (s *Store) DeleteSide(taskID, keyOrID string) error {
	sd, err := s.Side(taskID, keyOrID)
	if err != nil {
		return err
	}
	return s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM segments WHERE side_id = ?`, sd.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM sides WHERE id = ?`, sd.ID); err != nil {
			return err
		}
		return touchTask(tx, taskID, nowMS())
	})
}

// SidesForRole returns every open side assigned to a role across all tasks.
// This is what a "what is on my plate" query is built from.
func (s *Store) SidesForRole(role, identity string) ([]model.Side, error) {
	q := `SELECT ` + sideCols + ` FROM sides WHERE status <> ?`
	args := []any{model.SideDone}
	if role != "" {
		q += ` AND assignee_role = ?`
		args = append(args, role)
	}
	if identity != "" {
		q += ` AND assignee_identity = ?`
		args = append(args, identity)
	}
	q += ` ORDER BY updated_at DESC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Side{}
	for rows.Next() {
		sd, err := scanSide(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sd)
	}
	return out, rows.Err()
}
