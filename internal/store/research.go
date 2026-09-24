package store

import (
	"strings"

	"github.com/ChenYCL/keypoint-notify/internal/model"
)

// ResearchItem is one research task seen as a discussion: the question it is
// meant to answer, the options on the table, how much has been said for and
// against, what is still unresolved, and the latest conclusion.
type ResearchItem struct {
	Code          string        `json:"code"`
	Title         string        `json:"title"`
	Status        string        `json:"status"`
	OwnerRole     string        `json:"owner_role,omitempty"`
	Question      string        `json:"question"`
	Options       []string      `json:"options"`
	Findings      int           `json:"findings"`
	OpenQuestions int           `json:"open_questions"`
	Decisions     int           `json:"decisions"`
	LastDecision  *model.Report `json:"last_decision,omitempty"`
	// State is "decided" once a decision has been recorded after every
	// question (or the task is done), and "open" otherwise.
	State     string `json:"state"`
	UpdatedAt string `json:"updated_at"`
}

// OpenQuestion is a question report that nothing has settled yet.
type OpenQuestion struct {
	model.Report
	TaskCode  string `json:"task_code"`
	TaskTitle string `json:"task_title"`
	TaskKind  string `json:"task_kind"`
}

// openQuestionSQL defines "still needs a decision": a question on a live task
// (not done, not archived) with no decision recorded on the same task after
// it. It is a deliberately simple rule — easy to predict, and the fix for a
// question that is settled but still listed is to write down the decision,
// which is the thing worth having anyway.
const openQuestionSQL = `r.type = 'question'
	AND t.status NOT IN ('done', 'archived')
	AND NOT EXISTS (
	  SELECT 1 FROM reports d
	  WHERE d.task_id = r.task_id AND d.type = 'decision' AND d.id <> r.id
	    AND d.created_at >= r.created_at)`

// OpenQuestions lists every unsettled question across live tasks, newest first.
func (s *Store) OpenQuestions(limit int) ([]OpenQuestion, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(
		`SELECT `+prefixCols(reportCols, "r")+`, t.code, t.title, t.kind
		 FROM reports r JOIN tasks t ON t.id = r.task_id
		 WHERE `+openQuestionSQL+`
		 ORDER BY r.created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OpenQuestion{}
	for rows.Next() {
		var (
			q                 OpenQuestion
			code, title, kind string
		)
		r, err := scanReport(scanTail{rows, []any{&code, &title, &kind}})
		if err != nil {
			return nil, err
		}
		q.Report, q.TaskCode, q.TaskTitle, q.TaskKind = r, code, title, kind
		out = append(out, q)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if idn, err := s.IdentityByID(out[i].IdentityID); err == nil {
			out[i].IdentityName = idn.Name
		}
	}
	return out, nil
}

// Research lists research tasks (kind=research, not archived) as discussions.
func (s *Store) Research() ([]ResearchItem, error) {
	tasks, err := s.ListTasks(TaskFilter{Kind: []string{model.TaskResearch}})
	if err != nil {
		return nil, err
	}
	out := make([]ResearchItem, 0, len(tasks))
	for _, lt := range tasks {
		t, err := s.GetTask(lt.ID)
		if err != nil {
			return nil, err
		}
		it := ResearchItem{
			Code: t.Code, Title: t.Title, Status: t.Status, OwnerRole: t.OwnerRole,
			Options: []string{}, UpdatedAt: t.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		}
		for _, sg := range t.Segments {
			switch {
			case sg.Key == "goal":
				it.Question = strings.TrimSpace(sg.Body)
			case sg.Kind == "free" && strings.TrimSpace(sg.Body) != "":
				it.Options = append(it.Options, sg.Title)
			}
		}
		if it.Question == "" {
			it.Question = t.Title
		}
		rows, err := s.db.Query(`SELECT type, COUNT(*) FROM reports WHERE task_id = ? GROUP BY type`, t.ID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var typ string
			var n int
			if err := rows.Scan(&typ, &n); err != nil {
				rows.Close()
				return nil, err
			}
			switch typ {
			case model.ReportFinding:
				it.Findings = n
			case model.ReportDecision:
				it.Decisions = n
			}
		}
		rows.Close()
		if err := s.db.QueryRow(
			`SELECT COUNT(*) FROM reports r JOIN tasks t ON t.id = r.task_id
			 WHERE r.task_id = ? AND `+openQuestionSQL, t.ID).Scan(&it.OpenQuestions); err != nil {
			return nil, err
		}
		if last, err := s.ListReports(ReportFilter{TaskCode: t.Code, Type: []string{model.ReportDecision}, Limit: 1}); err == nil && len(last) > 0 {
			it.LastDecision = &last[0]
		}
		it.State = "open"
		if t.Status == model.StatusDone || (it.LastDecision != nil && it.OpenQuestions == 0) {
			it.State = "decided"
		}
		out = append(out, it)
	}
	return out, nil
}

// scanTail lets scanReport read the report columns of a row that carries a few
// extra columns after them.
type scanTail struct {
	row  interface{ Scan(...any) error }
	tail []any
}

func (s scanTail) Scan(dest ...any) error { return s.row.Scan(append(dest, s.tail...)...) }
