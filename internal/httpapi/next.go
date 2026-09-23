package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ChenYCL/keypoint-notify/internal/model"
	"github.com/ChenYCL/keypoint-notify/internal/pack"
	"github.com/ChenYCL/keypoint-notify/internal/store"
)

// ---------------------------------------------------------------------------
// GET /api/v1/me/next   — the consume endpoint
// ---------------------------------------------------------------------------

// handleNext is what a collaborating session polls.
//
// The shape is deliberately "one call, one decision": it answers *whether*
// there is work for me, *why* it is mine, and *what I need to start* — in a
// single response. The alternative (poll events, filter by role, fetch the
// task, fetch the pack) pushes four steps of judgement into every client,
// where it will drift.
//
// Long-polling rather than SSE is intentional: an agent loop wants a blocking
// call it can put in a `while`, not a stream it has to hold open and parse
// frame by frame. `wait` blocks server-side until work appears or the deadline
// passes, so the caller's loop costs one request per wake-up instead of a
// request per second.
func (s *Server) handleNext(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	actor := identity(r)

	wait := atoiOr(q.Get("wait"), 0)
	if wait > 60 {
		wait = 60
	}

	// The cursor is remembered per identity, not threaded by the caller.
	//
	// A session that polls in a loop should not have to carry a cursor around
	// for the loop to make progress, and — more importantly — should not be
	// handed the same mention forever because it forgot to advance one. An
	// explicit ?since= still wins, so a caller that wants to replay can.
	since := parseInt64(q.Get("since"))
	if !q.Has("since") {
		if raw, err := s.St.GetMeta("next_cursor:" + actor.ID); err == nil && raw != "" {
			since = parseInt64(raw)
		}
	}
	query := store.NextWorkQuery{
		IdentityName: actor.Name,
		Roles:        actor.Roles,
		Since:        since,
		SideKey:      strings.TrimSpace(q.Get("side")),
		TaskCode:     strings.TrimSpace(q.Get("task")),
		// A session that has decided a particular item is not its to take needs
		// a way to say so. Without this it is offered the same item forever and
		// the only escape is to stop asking.
		Exclude: parseCSV(q.Get("exclude")),
	}

	claim := q.Get("claim") == "1" || q.Get("claim") == "true"

	start := time.Now()
	deadline := start.Add(time.Duration(wait) * time.Second)
	var (
		work       *store.NextWork
		dispatched bool
		err        error
		waited     int
	)
	for {
		// claim=1 must dispatch atomically: find-and-take in one pass, else a
		// burst of same-role sessions all see the same head of the queue and
		// four of six come back empty-handed.
		if claim {
			work, dispatched, err = s.St.ClaimNext(query, actor)
		} else {
			work, err = s.St.NextWork(query)
		}
		if err != nil {
			respondError(w, err)
			return
		}
		if work != nil || wait == 0 || time.Now().After(deadline) {
			break
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(1200 * time.Millisecond):
		}
		waited = int(time.Since(start).Seconds())
	}

	cursor, _ := s.St.LatestEventID()
	// Advance the stored cursor to now, whether or not work was found: the
	// point of the cursor is "I have seen everything up to here", not "I acted
	// on it". Work that is still outstanding stays outstanding for a different
	// reason — an unclaimed side remains assigned, a mention remains in the
	// inbox — so nothing is lost by moving the pointer forward.
	_ = s.St.SetMeta("next_cursor:"+actor.ID, strconv.FormatInt(cursor, 10))

	base := map[string]any{
		"identity": actor.Name,
		"role":     actor.ActiveRole,
		"cursor":   cursor,
		"waited_s": maxInt(waited, 0),
	}

	if work == nil {
		base["work"] = nil
		base["hint"] = "没有属于你的活。游标已前进到 " + strconv.FormatInt(cursor, 10) + "，下一轮只看这之后的新指派/提及"
		if q.Get("format") == "json" {
			writeOK(w, base)
			return
		}
		writeText(w, http.StatusOK, "text/plain",
			"（没有属于你的活）\ncursor="+strconv.FormatInt(cursor, 10)+"\n")
		return
	}

	// `claim=1` means "dispatch me work". ClaimNext already took the face if
	// there was one to take. A mention is not dispatch — it asks for an answer,
	// and answering does not transfer ownership of the face it was asked on —
	// so the response has to say plainly which of the two happened instead of
	// leaving the caller to infer it from a false-looking boolean.
	if claim {
		switch {
		case dispatched:
			base["claimed"] = true
		case work.Side != nil && work.Side.AssigneeIdentity == actor.Name:
			// Already mine from an earlier round. Nothing was *newly* claimed,
			// which is different from "someone else has it" — say which.
			base["claimed"] = false
			base["note"] = "这个工作面已经在你名下了，这次没有新的认领动作"
		default:
			base["claimed"] = false
			base["note"] = "这次不是派活，是 " + work.Reason +
				"；没有认领任何工作面。要接活用 kp claim，或等它被指派给你"
		}
	}

	opt := pack.Options{
		MaxChars: atoiOr(q.Get("max_chars"), pack.DefaultMaxChars),
		Reports:  atoiOr(q.Get("reports"), 3),
	}
	if work.Side != nil {
		opt.SideKey = work.Side.Key
	}
	var reports []model.Report
	if opt.Reports > 0 {
		reports, err = s.St.ListReports(store.ReportFilter{TaskCode: work.Task.Code, Limit: opt.Reports})
		if err != nil {
			respondError(w, err)
			return
		}
	}
	role := actor.ActiveRole
	if work.Side != nil && work.Side.AssigneeRole != "" {
		role = work.Side.AssigneeRole
	}
	bundle := pack.Build(work.Task, reports, role, opt)

	base["work"] = work
	base["self"] = map[string]any{"name": actor.Name, "role": role}

	if q.Get("format") == "json" {
		bundle.Reports = reports
		base["pack"] = bundle
		writeOK(w, base)
		return
	}

	// Markdown is the default because the consumer is a prompt: the "why you
	// were woken" preamble is what tells the model it is not being asked to
	// re-read a task it already finished.
	var sb strings.Builder
	sb.WriteString("<!-- keypoint next · reason=" + work.Reason +
		" · cursor=" + strconv.FormatInt(cursor, 10) + " -->\n")
	sb.WriteString("> **为什么是你**：" + work.Explanation + "\n")
	if work.Side != nil {
		sb.WriteString("> **你的工作面**：`" + work.Side.Key + "`")
		if work.Side.AssigneeRole != "" {
			sb.WriteString("（角色 @" + work.Side.AssigneeRole + "）")
		}
		sb.WriteString("，状态 " + work.Side.Status + "\n")
		if len(work.Dependents) > 0 {
			sb.WriteString("> **别人在等你**：" + strings.Join(work.Dependents, ", ") + "\n")
		}
	}
	if note, ok := base["note"].(string); ok {
		sb.WriteString("> ⚠️ " + note + "\n")
	} else if claimed, ok := base["claimed"].(bool); ok && claimed {
		sb.WriteString("> 已认领：这个工作面已记在你名下，别的同角色会话不会重复捡走\n")
	}
	sb.WriteString("\n---\n\n")
	sb.WriteString(pack.Markdown(bundle, opt))
	writeText(w, http.StatusOK, "text/markdown", sb.String())
}

// ---------------------------------------------------------------------------
// POST /api/v1/tasks/{code}/sides/{key}/claim
// ---------------------------------------------------------------------------

// handleSideClaim is the atomic "this is mine" handshake. It only succeeds on
// an unclaimed work face, so two same-role sessions cannot both take it.
func (s *Server) handleSideClaim(w http.ResponseWriter, r *http.Request) {
	code, key := r.PathValue("code"), r.PathValue("key")
	t, err := s.St.GetTask(code)
	if err != nil {
		respondError(w, s.taskNotFound(code, err))
		return
	}
	actor := identity(r)
	claimed, err := s.St.ClaimSide(t.Code, key, actor)
	if err != nil {
		respondError(w, s.sideNotFound(t, key))
		return
	}
	sd, err := s.St.Side(t.ID, key)
	if err != nil {
		respondError(w, err)
		return
	}
	resp := map[string]any{"ok": true, "claimed": claimed, "side": sd}
	if !claimed {
		resp["reason"] = "已被认领"
		resp["note"] = "这个工作面已经记在别人名下；要做的话先上报交接，而不是直接改"
	} else {
		resp["hint"] = "kp task pack " + t.Code + " --side " + sd.Key
	}
	writeOK(w, resp)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
