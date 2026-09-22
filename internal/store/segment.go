package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/light/keypoint-notify/internal/model"
)

const segmentCols = `id, task_id, side_id, key, title, body, kind, format, ord, updated_by, created_at, updated_at`

func scanSegment(row interface{ Scan(...any) error }) (model.Segment, error) {
	var (
		sg               model.Segment
		created, updated int64
	)
	err := row.Scan(&sg.ID, &sg.TaskID, &sg.SideID, &sg.Key, &sg.Title, &sg.Body, &sg.Kind,
		&sg.Format, &sg.Order, &sg.UpdatedBy, &created, &updated)
	if err != nil {
		return model.Segment{}, err
	}
	sg.UpdatedAt = msToTime(updated)
	sg.Empty = strings.TrimSpace(sg.Body) == ""
	return sg, nil
}

// Segments returns a task's own (task-level) segments in declaration order.
func (s *Store) Segments(taskID string) ([]model.Segment, error) {
	rows, err := s.db.Query(
		`SELECT `+segmentCols+` FROM segments WHERE task_id=? AND side_id='' ORDER BY ord, created_at`,
		taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Segment{}
	for rows.Next() {
		sg, err := scanSegment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sg)
	}
	return out, rows.Err()
}

func (s *Store) sideSegments(sideID string) ([]model.Segment, error) {
	rows, err := s.db.Query(
		`SELECT `+segmentCols+` FROM segments WHERE side_id=? ORDER BY ord, created_at`, sideID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Segment{}
	for rows.Next() {
		sg, err := scanSegment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sg)
	}
	return out, rows.Err()
}

// Segment resolves one segment by key. It searches task-level segments first,
// then every side, so `kp task seg KP-12 acceptance` works without the caller
// knowing where the segment lives.
func (s *Store) Segment(taskID, key string) (model.Segment, error) {
	row := s.db.QueryRow(
		`SELECT `+segmentCols+` FROM segments WHERE task_id=? AND side_id='' AND (key=? OR id=?)`,
		taskID, key, key)
	sg, err := scanSegment(row)
	if err == nil {
		return sg, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return model.Segment{}, err
	}
	row = s.db.QueryRow(
		`SELECT `+segmentCols+` FROM segments WHERE task_id=? AND (key=? OR id=?) ORDER BY ord LIMIT 1`,
		taskID, key, key)
	sg, err = scanSegment(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Segment{}, fmt.Errorf("%w: segment %q", ErrNotFound, key)
	}
	if err != nil {
		return model.Segment{}, err
	}
	if sg.SideID != "" {
		if sd, err := s.Side(taskID, sg.SideID); err == nil {
			sg.SideKey = sd.Key
		}
	}
	return sg, nil
}

// SegmentKeys lists every addressable segment key on a task, task-level first.
// Used to build "did you mean" hints on a miss.
func (s *Store) SegmentKeys(taskID string) ([]string, error) {
	rows, err := s.db.Query(`SELECT key FROM segments WHERE task_id=? ORDER BY side_id, ord`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// UpsertSegmentInput is the write shape for a single segment.
type UpsertSegmentInput struct {
	Key     string
	SideKey string // empty = task-level
	Title   string
	Body    string
	Format  string // md | text | code
	Append  bool   // append to the existing body instead of replacing
}

// UpsertSegment creates or replaces a segment. Skeleton keys on the task level
// keep their canonical title; free segments take the title given.
func (s *Store) UpsertSegment(taskID string, in UpsertSegmentInput, actor string) (model.Segment, error) {
	key := model.Slug(in.Key)
	if key == "" {
		return model.Segment{}, errors.New("segment key is required")
	}
	format := in.Format
	if format != "md" && format != "text" && format != "code" {
		format = "md"
	}

	var sideID, sideKey string
	if in.SideKey != "" {
		sd, err := s.Side(taskID, in.SideKey)
		if err != nil {
			return model.Segment{}, err
		}
		sideID, sideKey = sd.ID, sd.Key
		if model.IsSkeletonKey(key) {
			return model.Segment{}, fmt.Errorf("%q is a reserved task-level segment key", key)
		}
	}

	kind := "free"
	title := strings.TrimSpace(in.Title)
	if sideID == "" && model.IsSkeletonKey(key) {
		kind = "skeleton"
		title = model.SkeletonTitles[key]
	}
	if title == "" {
		title = key
	}

	var out model.Segment
	err := s.tx(func(tx *sql.Tx) error {
		now := nowMS()
		var existingID, existingBody string
		err := tx.QueryRow(
			`SELECT id, body FROM segments WHERE task_id=? AND side_id=? AND key=?`,
			taskID, sideID, key).Scan(&existingID, &existingBody)
		body := in.Body
		switch {
		case errors.Is(err, sql.ErrNoRows):
			ord := 0
			if err := tx.QueryRow(
				`SELECT COALESCE(MAX(ord)+1, 0) FROM segments WHERE task_id=? AND side_id=?`,
				taskID, sideID).Scan(&ord); err != nil {
				return err
			}
			id := NewID("seg_")
			if _, err := tx.Exec(
				`INSERT INTO segments(id, task_id, side_id, key, title, body, kind, format, ord, updated_by, created_at, updated_at)
				 VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
				id, taskID, sideID, key, title, body, kind, format, ord, actor, now, now); err != nil {
				return err
			}
			out = model.Segment{ID: id, TaskID: taskID, SideID: sideID, SideKey: sideKey,
				Key: key, Title: title, Body: body, Kind: kind, Format: format, Order: ord,
				UpdatedBy: actor, UpdatedAt: msToTime(now)}
		case err != nil:
			return err
		default:
			if in.Append && strings.TrimSpace(existingBody) != "" {
				body = strings.TrimRight(existingBody, "\n") + "\n\n" + in.Body
			}
			// A skeleton segment keeps its canonical title even if a caller
			// supplies one, so the board never shows "目标" as "goal xyz".
			dbTitle := title
			if kind == "skeleton" {
				dbTitle = model.SkeletonTitles[key]
			}
			if _, err := tx.Exec(
				`UPDATE segments SET title=?, body=?, format=?, updated_by=?, updated_at=? WHERE id=?`,
				dbTitle, body, format, actor, now, existingID); err != nil {
				return err
			}
			out = model.Segment{ID: existingID, TaskID: taskID, SideID: sideID, SideKey: sideKey,
				Key: key, Title: dbTitle, Body: body, Kind: kind, Format: format,
				UpdatedBy: actor, UpdatedAt: msToTime(now)}
		}
		return touchTask(tx, taskID, now)
	})
	if err != nil {
		return model.Segment{}, err
	}
	out.Empty = strings.TrimSpace(out.Body) == ""
	return out, nil
}

// DeleteSegment removes a segment. Skeleton segments are cleared, not dropped,
// so their key stays addressable.
func (s *Store) DeleteSegment(taskID, key string) error {
	sg, err := s.Segment(taskID, key)
	if err != nil {
		return err
	}
	if sg.Kind == "skeleton" {
		_, err := s.db.Exec(
			`UPDATE segments SET body='', updated_by='', updated_at=? WHERE id=?`, nowMS(), sg.ID)
		return err
	}
	_, err = s.db.Exec(`DELETE FROM segments WHERE id=?`, sg.ID)
	return err
}
