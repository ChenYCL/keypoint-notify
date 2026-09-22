package store

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/light/keypoint-notify/internal/model"
)

// NextWork is one answer to "what should I do now".
//
// It exists because a collaborating agent otherwise has to reconstruct this
// from four calls and re-derive "is this mine" itself — polling events,
// filtering by role, fetching the task, then fetching the pack. That
// re-derivation is exactly the kind of logic that drifts between sessions and
// ends with two of them doing the same work.
type NextWork struct {
	// Reason is why this landed on you. The mobile-app convention: a work item
	// with no reason is a work item nobody trusts.
	//
	//	mention    someone @'d you and is waiting
	//	unblocked  a dependency of your side just finished
	//	assigned   a side addressed to your role is ready and unclaimed
	//	owned      a task you own has new activity
	Reason string `json:"reason"`
	// Explanation renders Reason as one sentence for the receiver.
	Explanation string `json:"explanation"`

	Task   model.Task    `json:"task"`
	Side   *model.Side   `json:"side,omitempty"`
	Report *model.Report `json:"report,omitempty"`
	// Dependents are sides waiting on the one you are being asked to advance.
	Dependents []string `json:"dependents,omitempty"`
}

// NextWorkQuery describes who is asking and how far back to look.
type NextWorkQuery struct {
	IdentityName string
	Roles        []string
	// Since is an event cursor. Zero means "no time constraint" — used on a
	// first call or by an agent that just wants the current best item.
	Since int64
	// SideKey and TaskCode narrow the search; empty means all.
	SideKey  string
	TaskCode string
}

// NextWork returns the single most urgent piece of work for this identity, or
// (nil, nil) when there is nothing to do.
//
// The ordering is deliberate: someone explicitly waiting on you beats an
// assignment beats unclaimed capacity.
func (s *Store) NextWork(q NextWorkQuery) (*NextWork, error) {
	if w, err := s.nextMention(q); err != nil || w != nil {
		return w, err
	}
	if w, err := s.nextReadySide(q); err != nil || w != nil {
		return w, err
	}
	return s.nextOwned(q)
}

// rolePredicate is the "this side is addressed to me" test: named to me, or
// addressed to a role I hold.
func rolePredicate(roles []string) (string, []any) {
	ors := []string{}
	args := []any{}
	if len(roles) > 0 {
		ph := placeholders(len(roles))
		ors = append(ors, "s.assignee_role IN ("+ph+")")
		for _, r := range roles {
			args = append(args, r)
		}
	}
	return strings.Join(ors, " OR "), args
}

func (s *Store) nextMention(q NextWorkQuery) (*NextWork, error) {
	people := append([]string{q.IdentityName}, q.Roles...)
	ph := placeholders(len(people))
	args := []any{}
	// Never hand someone their own report back to them.
	args = append(args, q.IdentityName)
	// `since` is an event cursor; translate it to the timestamp it sits at.
	sinceMS := int64(0)
	if q.Since > 0 {
		_ = s.db.QueryRow(`SELECT created_at FROM events WHERE id = ?`, q.Since).Scan(&sinceMS)
	}
	args = append(args, sinceMS)
	for _, x := range people {
		args = append(args, x)
	}
	where := []string{
		"r.identity_id <> (SELECT id FROM identities WHERE name = ?)",
		"r.created_at > ?",
		"EXISTS (SELECT 1 FROM json_each(r.mentions) m WHERE m.value IN (" + ph + "))",
	}
	if q.TaskCode != "" {
		where = append(where, "r.task_id = (SELECT id FROM tasks WHERE code = ?)")
		args = append(args, q.TaskCode)
	}
	if q.SideKey != "" {
		where = append(where, "r.side_id = (SELECT id FROM sides WHERE key = ? AND task_id = r.task_id)")
		args = append(args, q.SideKey)
	}
	rows, err := s.db.Query(
		`SELECT `+prefixCols(reportCols, "r")+` FROM reports r
		 WHERE `+strings.Join(where, " AND ")+`
		 ORDER BY r.created_at DESC
		 LIMIT 20`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		r, err := scanReport(rows)
		if err != nil {
			return nil, err
		}
		t, err := s.GetTask(r.TaskID)
		if err != nil {
			continue
		}
		r.TaskCode = t.Code
		w := &NextWork{
			Reason: "mention",
			Explanation: fmt.Sprintf("%s 在 %s 的上报里点到你：%s",
				displayName(s, r.IdentityID), t.Code, truncateRunes(r.Body, 80)),
			Task:   t,
			Report: &r,
		}
		if r.SideID != "" {
			if sd, err := s.Side(t.ID, r.SideID); err == nil {
				r.SideKey = sd.Key
				w.Side = &sd
				w.Dependents = s.dependentsOf(t, sd.Key)
			}
		}
		return w, nil
	}
	return nil, rows.Err()
}

// displayName resolves a report author for an explanation line.
func displayName(s *Store, id string) string {
	if id == "" {
		return "有人"
	}
	if idn, err := s.IdentityByID(id); err == nil {
		return idn.Name
	}
	return "有人"
}

// nextReadySide finds a work face addressed to me that nothing is blocking.
func (s *Store) nextReadySide(q NextWorkQuery) (*NextWork, error) {
	roleOr, args := rolePredicate(q.Roles)
	if roleOr == "" {
		return nil, nil
	}
	// A side is ready when it is not started, addressed to me, and every
	// dependency is done. status='blocked' ranks above 'todo' because someone
	// was actively stuck on it.
	// The filters have to be in the SQL, not applied to the row LIMIT 1
	// returned: with a task filter applied afterwards, a work face from another
	// task would win the ordering and the real answer would be discarded.
	where := []string{
		"s.status IN ('blocked','todo')",
		"(" + roleOr + " OR s.assignee_identity = ?)",
		`NOT EXISTS (
		   SELECT 1 FROM sides d
		   WHERE d.task_id = s.task_id AND d.status <> ?
		     AND d.key IN (SELECT value FROM json_each(s.deps))
		 )`,
	}
	args = append(args, q.IdentityName, model.SideDone)
	if q.TaskCode != "" {
		where = append(where, "t.code = ?")
		args = append(args, q.TaskCode)
	}
	if q.SideKey != "" {
		where = append(where, "s.key = ?")
		args = append(args, q.SideKey)
	}

	rows, err := s.db.Query(
		`SELECT `+prefixCols(sideCols, "s")+`
		 FROM sides s JOIN tasks t ON t.id = s.task_id
		 WHERE `+strings.Join(where, " AND ")+`
		 ORDER BY CASE s.status WHEN 'blocked' THEN 0 ELSE 1 END, s.updated_at ASC
		 LIMIT 1`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	sd, err := scanSide(rows)
	if err != nil {
		return nil, err
	}
	rows.Close()
	t, err := s.GetTask(sd.TaskID)
	if err != nil {
		return nil, err
	}
	wasBlocked := sd.Status == model.SideBlocked
	reason, expl := "assigned", fmt.Sprintf("%s 的 %s 工作面指派给了 @%s，且依赖已就绪", t.Code, sd.Key, sd.AssigneeRole)
	if wasBlocked {
		reason = "unblocked"
		expl = fmt.Sprintf("%s 的 %s 工作面原本被依赖挡住，现在依赖已完成", t.Code, sd.Key)
	}
	return &NextWork{
		Reason: reason, Explanation: expl, Task: t, Side: &sd,
		Dependents: s.dependentsOf(t, sd.Key),
	}, nil
}

func (s *Store) nextOwned(q NextWorkQuery) (*NextWork, error) {
	where := []string{"t.owner_identity = ?", "t.status NOT IN ('done','archived')"}
	args := []any{q.IdentityName}
	if q.TaskCode != "" {
		where = append(where, "t.code = ?")
		args = append(args, q.TaskCode)
	}
	if q.SideKey != "" {
		where = append(where, "EXISTS (SELECT 1 FROM sides s WHERE s.task_id = t.id AND s.key = ?)")
		args = append(args, q.SideKey)
	}
	rows, err := s.db.Query(
		`SELECT `+taskCols+` FROM tasks t
		 WHERE `+strings.Join(where, " AND ")+`
		 ORDER BY t.updated_at DESC LIMIT 1`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	t, err := scanTask(rows)
	if err != nil {
		return nil, err
	}
	rows.Close()
	if err := s.hydrate(&t); err != nil {
		return nil, err
	}
	return &NextWork{
		Reason:      "owned",
		Explanation: fmt.Sprintf("你是 %s 的负责人，它还在 %s", t.Code, t.Status),
		Task:        t,
	}, nil
}

// dependentsOf lists the sides that will be unblocked once key finishes.
func (s *Store) dependentsOf(t model.Task, key string) []string {
	out := []string{}
	for _, sd := range t.Sides {
		for _, d := range sd.Deps {
			if d == key {
				out = append(out, sd.Key)
			}
		}
	}
	return out
}

// truncateRunes shortens a string for a one-line explanation.
func truncateRunes(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// ---------------------------------------------------------------------------
// Release: unblock whatever depended on a side that just finished
// ---------------------------------------------------------------------------

// releaseDependentsTx is called inside the same transaction that marks a side
// done. It walks the dependents of that side; any whose dependencies are now
// all satisfied is moved out of "blocked" and reported back to the caller so
// an event can be emitted for it.
//
// Doing this at the write is what makes the collaboration self-driving: the
// person waiting on a dependency does not have to poll, because the system
// tells them the moment they are unblocked.
func releaseDependentsTx(tx *sql.Tx, taskID, finishedKey string, now int64) ([]model.Side, error) {
	rows, err := tx.Query(
		`SELECT `+sideCols+` FROM sides
		 WHERE task_id = ? AND status IN (?, ?)
		   AND deps LIKE ?`, taskID, model.SideBlocked, model.SideTodo, "%\""+finishedKey+"\"%")
	if err != nil {
		return nil, err
	}
	var candidates []model.Side
	for rows.Next() {
		sd, err := scanSide(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, sd)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	released := []model.Side{}
	for _, sd := range candidates {
		var pending int
		if err := tx.QueryRow(
			`SELECT COUNT(*) FROM sides
			 WHERE task_id = ? AND status <> ?
			   AND key IN (SELECT value FROM json_each(?))`,
			taskID, model.SideDone, jsonStrings(sd.Deps)).Scan(&pending); err != nil {
			return nil, err
		}
		if pending > 0 {
			continue
		}
		if sd.Status == model.SideBlocked {
			if _, err := tx.Exec(`UPDATE sides SET status=?, updated_at=? WHERE id=?`,
				model.SideTodo, now, sd.ID); err != nil {
				return nil, err
			}
			sd.Status = model.SideTodo
		}
		released = append(released, sd)
	}
	return released, nil
}

// taskTx loads a task inside an open transaction. Store.GetTask would borrow a
// second pooled connection, which deadlocks when the pool is saturated by
// concurrent writers — the same hazard identityByNameTx exists to avoid.
func taskTx(tx *sql.Tx, id string) (model.Task, error) {
	row := tx.QueryRow(`SELECT `+taskCols+` FROM tasks WHERE id = ?`, id)
	return scanTask(row)
}

// ClaimSide records that this identity is taking the work face.
//
// The conditional UPDATE is the whole point: two sessions polling the same
// role will both be offered the same side, and exactly one must win. A
// read-then-write would let both see "unclaimed" and both proceed.
func (s *Store) ClaimSide(taskCode, sideKey string, who model.Identity) (bool, error) {
	t, err := s.GetTask(taskCode)
	if err != nil {
		return false, err
	}
	role := who.ActiveRole
	if role == "" && len(who.Roles) > 0 {
		role = who.Roles[0]
	}
	// Claiming is only legal on work addressed to me. Being mentioned in a
	// report that happens to carry someone else's side does not make that side
	// mine — the mention asks for an answer, not a takeover.
	roles := who.Roles
	if len(roles) == 0 && role != "" {
		roles = []string{role}
	}
	roleGuard := "1=0"
	args := []any{}
	if len(roles) > 0 {
		ph := placeholders(len(roles))
		roleGuard = "s.assignee_role = '' OR s.assignee_role IN (" + ph + ")"
		for _, r := range roles {
			args = append(args, r)
		}
	}
	res, err := s.db.Exec(
		`UPDATE sides AS s
		    SET assignee_identity = ?, assignee_role = CASE WHEN s.assignee_role = '' THEN ? ELSE s.assignee_role END,
		        updated_at = ?
		  WHERE s.task_id = ? AND s.key = ? AND s.assignee_identity = ''
		    AND (`+roleGuard+`)`,
		append([]any{who.Name, role, nowMS(), t.ID, sideKey}, args...)...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 1 {
		if sd, err := s.Side(t.ID, sideKey); err == nil {
			_, _ = s.Emit(EmitInput{
				Type: EvSideAssigned, Actor: who, Task: &t, SideID: sd.ID,
				Kind: "assigned", Title: t.Code + " · " + sd.Key + " 被 " + who.Name + " 认领",
				Payload: map[string]any{"side": sd.Key, "claimed_by": who.Name},
			})
		}
	}
	return n == 1, nil
}
