package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/light/keypoint-notify/internal/model"
	"github.com/light/keypoint-notify/internal/pack"
	"github.com/light/keypoint-notify/internal/store"
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

	start := time.Now()
	deadline := start.Add(time.Duration(wait) * time.Second)
	var (
		work   *store.NextWork
		err    error
		waited int
	)
	for {
		work, err = s.St.NextWork(query)
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

	// Claim before rendering: if two sessions of the same role race for one
	// work face, exactly one should walk away with it.
	//
	// Only work actually addressed to me is claimable. A mention can point at
	// a side that belongs to another role — that asks for my input, not for me
	// to take over their work face.
	claimable := work.Side != nil &&
		(work.Side.AssigneeIdentity == actor.Name ||
			(work.Side.AssigneeIdentity == "" &&
				(work.Side.AssigneeRole == "" || actor.HasRole(work.Side.AssigneeRole))))
	if (q.Get("claim") == "1" || q.Get("claim") == "true") && claimable {
		if work.Side != nil {
			claimed, err := s.St.ClaimSide(work.Task.Code, work.Side.Key, actor)
			if err != nil {
				respondError(w, err)
				return
			}
			if claimed {
				if sd, err := s.St.Side(work.Task.ID, work.Side.Key); err == nil {
					work.Side = &sd
				}
			}
			base["claimed"] = claimed
		}
	} else if work.Side != nil && (q.Get("claim") == "1" || q.Get("claim") == "true") {
		base["claimed"] = false
		base["claim_skipped"] = "这个工作面属于 @" + work.Side.AssigneeRole + "，不是你的；这次是回应提及，不是接活"
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
	if skip, ok := base["claim_skipped"].(string); ok {
		sb.WriteString("> ⚠️ " + skip + "\n")
	} else if claimed, ok := base["claimed"].(bool); ok {
		if claimed {
			sb.WriteString("> 已认领：这个工作面已记在你名下，别的同角色会话不会重复捡走\n")
		} else {
			sb.WriteString("> 未认领成功：已被同角色的另一个会话拿走，做之前先确认\n")
		}
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
