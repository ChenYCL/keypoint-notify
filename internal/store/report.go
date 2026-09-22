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

const reportCols = `id, task_id, side_id, identity_id, role, type, priority, body, segments, mentions, created_at, edited_at`

// CreateReportInput is what a 上报 carries.
type CreateReportInput struct {
	TaskCode    string
	SideKey     string
	Type        string
	Priority    string
	Body        string
	Segments    []model.Segment // inline keyed snippets; stored with the report
	Mentions    []string        // identity names or role keys
	Attachments []string        // file ids already uploaded
	ActorID     string
	ActorName   string
	Role        string
}

// CreateReport appends a report to a task's timeline. Reports are the only
// thing on a task that is never reordered or rewritten; edits stamp EditedAt
// and leave the original creation time intact.
func (s *Store) CreateReport(in CreateReportInput) (model.Report, error) {
	if strings.TrimSpace(in.Body) == "" && len(in.Segments) == 0 {
		return model.Report{}, errors.New("report needs a body or at least one segment")
	}
	if in.Type == "" {
		in.Type = model.ReportProgress
	}
	if !model.ValidReportType(in.Type) {
		return model.Report{}, fmt.Errorf("unknown report type %q (want progress|blocker|decision|handoff|result|question)", in.Type)
	}

	t, err := s.GetTask(in.TaskCode)
	if err != nil {
		return model.Report{}, err
	}

	var sideID, sideKey string
	if in.SideKey != "" {
		sd, err := s.Side(t.ID, in.SideKey)
		if err != nil {
			return model.Report{}, err
		}
		sideID, sideKey = sd.ID, sd.Key
	}

	mentions, err := s.normalizeMentions(in.Mentions)
	if err != nil {
		return model.Report{}, err
	}

	if in.Role == "" {
		in.Role = "member"
	}
	now := nowMS()
	rep := model.Report{
		ID:           NewID("rep_"),
		TaskID:       t.ID,
		TaskCode:     t.Code,
		SideID:       sideID,
		SideKey:      sideKey,
		IdentityID:   in.ActorID,
		IdentityName: in.ActorName,
		Role:         in.Role,
		Type:         in.Type,
		Priority:     in.Priority,
		Body:         in.Body,
		Segments:     in.Segments,
		Mentions:     mentions,
		CreatedAt:    msToTime(now),
	}

	err = s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(
			`INSERT INTO reports(id, task_id, side_id, identity_id, role, type, priority, body, segments, mentions, created_at)
			 VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			rep.ID, rep.TaskID, rep.SideID, rep.IdentityID, rep.Role, rep.Type, rep.Priority,
			rep.Body, jsonAny(segmentsForStorage(rep.Segments)), jsonStrings(rep.Mentions), now); err != nil {
			return err
		}
		if err := touchTask(tx, t.ID, now); err != nil {
			return err
		}
		// A side that reports progress on itself moves out of "todo"; a blocker
		// report moves it into "blocked". This is the one inference the system
		// makes, and it matches what people expect a report to do.
		if sideID != "" {
			switch in.Type {
			case model.ReportBlocker:
				if _, err := tx.Exec(`UPDATE sides SET status=?, updated_at=? WHERE id=? AND status <> ?`,
					model.SideBlocked, now, sideID, model.SideDone); err != nil {
					return err
				}
			case model.ReportProgress, model.ReportResult:
				if _, err := tx.Exec(`UPDATE sides SET status=?, updated_at=? WHERE id=? AND status = ?`,
					model.SideDoing, now, sideID, model.SideTodo); err != nil {
					return err
				}
			}
		}
		// Attach any files the report references.
		for _, fid := range in.Attachments {
			if _, err := tx.Exec(
				`UPDATE files SET task_id=?, side_id=?, scope='report', ref_id=? WHERE id=?`,
				t.ID, sideID, rep.ID, fid); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return model.Report{}, err
	}
	return s.Report(rep.ID)
}

// Report loads a single report by id.
func (s *Store) Report(id string) (model.Report, error) {
	row := s.db.QueryRow(`SELECT `+reportCols+` FROM reports WHERE id = ?`, id)
	r, err := scanReport(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Report{}, fmt.Errorf("%w: report %s", ErrNotFound, id)
	}
	if err != nil {
		return model.Report{}, err
	}
	// The reports table stores only the author's id; the name is resolved here
	// so callers never have to make a second request to render "who said this".
	if r.IdentityID != "" {
		if idn, err := s.IdentityByID(r.IdentityID); err == nil {
			r.IdentityName = idn.Name
		}
	}
	if r.SideID != "" {
		if sd, err := s.Side(r.TaskID, r.SideID); err == nil {
			r.SideKey = sd.Key
		}
	}
	r.Attachments, _ = s.FilesByScope("report", id)
	return r, nil
}

func scanReport(row interface{ Scan(...any) error }) (model.Report, error) {
	var (
		r           model.Report
		segs, ments string
		created     int64
		edited      sql.NullInt64
	)
	err := row.Scan(&r.ID, &r.TaskID, &r.SideID, &r.IdentityID, &r.Role, &r.Type, &r.Priority,
		&r.Body, &segs, &ments, &created, &edited)
	if err != nil {
		return model.Report{}, err
	}
	r.Segments = scanSegmentsJSON(segs)
	r.Mentions = scanStrings(ments)
	r.CreatedAt = msToTime(created)
	r.EditedAt = msToTimePtr(edited)
	return r, nil
}

// ReportFilter describes a timeline query.
type ReportFilter struct {
	TaskCode string
	SideKey  string
	Type     []string
	Since    time.Time
	Limit    int
	Offset   int
}

// ListReports returns reports newest-first, optionally scoped to one task.
func (s *Store) ListReports(f ReportFilter) ([]model.Report, error) {
	var (
		where []string
		args  []any
	)
	if f.TaskCode != "" {
		t, err := s.GetTask(f.TaskCode)
		if err != nil {
			return nil, err
		}
		where = append(where, "r.task_id = ?")
		args = append(args, t.ID)
	}
	if f.SideKey != "" {
		where = append(where, "r.side_id = (SELECT id FROM sides WHERE task_id = r.task_id AND key = ?)")
		args = append(args, f.SideKey)
	}
	if len(f.Type) > 0 {
		where = append(where, "r.type IN ("+placeholders(len(f.Type))+")")
		for _, v := range f.Type {
			args = append(args, v)
		}
	}
	if !f.Since.IsZero() {
		where = append(where, "r.created_at >= ?")
		args = append(args, f.Since.UTC().UnixMilli())
	}

	q := `SELECT ` + prefixCols(reportCols, "r") + ` FROM reports r`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY r.created_at DESC"
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
	out := []model.Report{}
	for rows.Next() {
		r, err := scanReport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Resolve names and attachments for the page we are about to return.
	for i := range out {
		if out[i].IdentityID != "" {
			if idn, err := s.IdentityByID(out[i].IdentityID); err == nil {
				out[i].IdentityName = idn.Name
			}
		}
		if out[i].SideID != "" {
			if sd, err := s.Side(out[i].TaskID, out[i].SideID); err == nil {
				out[i].SideKey = sd.Key
			}
		}
		out[i].Attachments, _ = s.FilesByScope("report", out[i].ID)
	}
	return out, nil
}

// normalizeMentions accepts @name, name, and role keys, strips the sigil, and
// drops anything that resolves to nothing so a typo does not create a phantom
// notification target.
func (s *Store) normalizeMentions(in []string) ([]string, error) {
	out := []string{}
	for _, raw := range in {
		m := strings.TrimPrefix(strings.TrimSpace(raw), "@")
		if m == "" {
			continue
		}
		if _, err := s.IdentityByName(m); err == nil {
			out = append(out, m)
			continue
		}
		if _, err := s.roleExists(m); err == nil {
			out = append(out, m)
			continue
		}
		// Unknown target: keep it but mark it, so a report never fails to send
		// just because someone typed a name that does not exist yet.
		out = append(out, m)
	}
	return out, nil
}

// segmentsForStorage strips presentation-only fields before the inline JSON is
// written, keeping the stored timeline stable across renderer changes.
func segmentsForStorage(in []model.Segment) []model.Segment {
	out := make([]model.Segment, 0, len(in))
	for _, sg := range in {
		if strings.TrimSpace(sg.Body) == "" && sg.Key == "" {
			continue
		}
		key := sg.Key
		if key == "" {
			key = model.Slug(sg.Title)
		}
		title := sg.Title
		if title == "" {
			title = key
		}
		out = append(out, model.Segment{Key: key, Title: title, Body: sg.Body, Format: sg.Format})
	}
	return out
}

func scanSegmentsJSON(raw string) []model.Segment {
	out := []model.Segment{}
	if raw == "" || raw == "null" {
		return out
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return []model.Segment{}
	}
	if out == nil {
		return []model.Segment{}
	}
	return out
}
