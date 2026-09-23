# Keypoint Notify

**English** · [中文](README.md)

Turns "work happening in a Claude Code session" into structured, queryable,
handoff-able tasks. One Go binary: server, kanban board, and CLI.

```bash
# Start the hub (on your machine)
kp serve --data ~/.keypoint/data

# In another terminal: connect and claim your identity
kp init

# File a task with segments and work faces
kp task new --title "Login countdown drifts after backgrounding" --kind bug --priority P1 \
  --goal "Countdown stays accurate across app switches" \
  --acceptance "Same behaviour on iOS and Android; unit test covers visibilitychange"

kp task side add KP-1 ui --role frontend --deps api
kp task side assign KP-1 ui --role frontend

# Hand it off: one command produces the full context
kp task pack KP-1 --side ui | pbcopy     # paste into any Claude Code session
```

---

## The problem it solves

**The break in collaboration is not "work wasn't assigned." It is that the
person who receives it has no context.**

Traditional trackers give you a title and a few lines. The receiver still has to
go ask, go dig, go guess. This system makes "everything you need to start" the
output of a single call:

- Tasks are split into **fixed-skeleton segments** (background / goal /
  deliverables / constraints / acceptance / interface / files) plus free-form
  ones you invent.
- Tasks are split into parallel **work faces (sides)**, each with its own
  owner role, status, dependencies, and details.
- `kp task pack` emits a **self-contained markdown document**: header, segment
  index, work-face table, all the prose, recent reports, attachment links, and
  a **delivery contract** at the end telling the receiver how to report back.

The receiver doesn't get "a title". They get a prompt they can start from.

---

## Three core concepts

| Concept | What it is | Why |
|---|---|---|
| **Segment** | A slice of a task's detail with a stable key, fetchable on its own | The unit of copy. `kp task seg KP-1 acceptance` goes straight to your clipboard |
| **Side** | A parallel work face of one task (api / ui / review), with its own owner and dependencies | Lets "multiple people pushing on one task" be handed off separately |
| **Report** | An append-only timeline: progress / blocker / decision / handoff / result / question | The loop. Finishing means filing a report, not going silent |

---

## Roles and identities

An API key binds to an **identity**. An identity holds **roles**, and roles can be
re-bound without ever changing the key:

```bash
kp identity create alice --roles frontend          # issue a key (shown once)
kp identity set-roles alice backend,review         # same key, new capabilities
kp identity rotate alice                           # only when you actually need a new key
```

Work is addressed to **roles** ("who can take frontend work"), not to people.
Notifications go to everyone holding that role. Someone going on leave, changing
teams, or picking up a new skill never requires re-dispatching anything.

Roles are extensible: `kp role add pm --name "Product Manager"` and it is
immediately assignable.

---

## For agents

**Skill** (Claude Code):

```bash
make skill        # links into ~/.claude/skills/keypoint-notify
```

After that, saying "file a task", "report progress", "hand this to frontend", or
"what's on my plate" triggers it. The skill confirms your role first, then pulls
segments out of the current session, decides whether to split work faces, and
assigns to roles that actually exist.

**Any CLI, any machine**:

```bash
kp install --target claude,codex,gemini    # writes each CLI's native form
curl -fsSL "http://<server>/install.sh?key=kp_xxx" | sh   # fresh machine
```

**HTTP API** — the server ships its own manual; feed it to any model:

```bash
curl -H "Authorization: Bearer $KP_KEY" $KP/api/v1/llms.txt       # API reference
curl -H "Authorization: Bearer $KP_KEY" $KP/api/v1/agent-prompt   # how to operate
```

Design notes: errors carry `hint` and `did_you_mean`; time parameters accept
`7d`-style relative values; `pack` returns markdown by default; size limits are
announced rather than silent.

---

## The collaboration loop

An agent session doesn't poll and reason about "is this mine". One call answers
*whether there is work for me*, *why it's mine*, and *everything I need to start*:

```bash
kp next --wait 30 --claim        # blocks server-side; returns the moment it's your turn
```

| `reason` | Meaning | What to do |
|---|---|---|
| `mention` | Someone @'d you in a report | **Respond.** This is not a request to take their work face |
| `unblocked` | A dependency of your face just finished | Take it, start work |
| `assigned` | A face addressed to your role, unclaimed | Take it, start work |
| `owned` | You own the task and it has new activity | Look, decide whether to dispatch |

Finishing a work face **releases its dependents automatically** and emits
`side.unblocked` — the waiting session's long poll wakes up. Handing off is a
side effect, not an extra step. When the last face lands, the task closes
itself.

---

## Board

Open `http://127.0.0.1:8787/`:

**The home page is onboarding**, not the board: what the system is, three steps
to put an agent to work (install skill → paste the operating prompt → wait for
work), the four subscription modes side by side, a standard operating procedure
for a task from creation to close, and a copyable agent prompt. The board is at
`/board`.

On a task page, every segment card has its own **copy button** ("copy" / "copy
as prompt").

Single HTML + vanilla JS, embedded in the binary, no build step.

---

## Notifications

| Direction | How |
|---|---|
| In-app | Unread badge in the board + `/inbox`; `kp inbox --unread` |
| Webhook | `kp hook add https://... --secret S --events report.created`, HMAC-signed |
| Pull | `kp events --since <cursor>` / `GET /api/v1/stream` (SSE) |

---

## Install

### Server: one Linux VPS, one command

```bash
# With a domain (recommended): automatic HTTPS via Caddy
curl -fsSL https://raw.githubusercontent.com/ChenYCL/keypoint-notify/main/deploy/install-server.sh | sudo sh -s -- --domain kp.example.com

# No domain: IP + port over plain HTTP (the script warns about it)
curl -fsSL https://raw.githubusercontent.com/ChenYCL/keypoint-notify/main/deploy/install-server.sh | sudo sh
```

[`deploy/install-server.sh`](deploy/install-server.sh) downloads `kp` from the latest
GitHub Release (SHA256-verified) plus client builds for all four platforms, creates a
system user and a systemd service (starts on boot, restarts on crash, runs as non-root),
adds a Caddy service for automatic certificates with `--domain`, claims the admin
identity and prints its key. Re-running it upgrades in place; data and admin are kept.
Then, on the VPS: `sudo kp-admin identity create alice --kind human --roles frontend`
prints an invite whose one-liner installs `kp` and the skill on the colleague's machine.

Other flags: `--local`, `--port`, `--admin`, `--version v0.2.0`, `--uninstall`.
Releases: push a `v*` tag and [`release.yml`](.github/workflows/release.yml) publishes
the binaries and `SHA256SUMS`.

### A machine with nothing on it

```bash
curl -fsSL "http://<server>/install.sh?key=kp_xxx" | sh
```

`install.sh` is generated by the server itself: downloads the right `kp` binary,
chmods it, connects with your key, and installs the skill into whatever CLIs it
finds. No repo, no package manager, no Go.

### A machine that already has `kp`

```bash
kp install                                  # detect installed CLIs, install for each
kp install --target claude                  # Claude Code only
kp install --target codex,gemini            # several
kp install --target agents                  # ./AGENTS.md, travels with the repo
kp install --from URL --key kp_xxx          # first connection
```

Each CLI has a different convention, so each gets a different shape:

| CLI | Location | Form |
|---|---|---|
| Claude Code | `~/.claude/skills/keypoint-notify/` | Skill directory (SKILL.md + reference/) |
| Codex CLI | `~/.codex/AGENTS.md` | Appended section, delimited |
| Gemini CLI | `~/.gemini/GEMINI.md` | Same |
| opencode | `~/.config/opencode/AGENTS.md` | Same |
| Kimi Code | `~/.kimi/AGENTS.md` | Same |
| Anything | `./AGENTS.md` | Same, travels with the repo |

`auto` (the default) only installs into CLIs whose config directory actually
exists; if it finds none it says so and offers options rather than silently
doing nothing. Re-installing replaces the delimited section — it never stacks a
second copy and never touches your own content.

The skill content is pulled **from the server**, so a new machine gets the
current version's behaviour manual, not a stale copy shipped with the binary.

### From source

```bash
make build          # → ./kp (CGO_ENABLED=0, a genuinely single binary)
make install        # → ~/.local/bin/kp
make skill          # → ~/.claude/skills/ (dev symlink)
```

Requires Go 1.25+ (the floor comes from `modernc.org/sqlite`, not from this
project's own code).

For public access, prefer **Cloudflare Tunnel** over opening a port — see
[`docs/deploy-tunnel.md`](docs/deploy-tunnel.md). Docker is supported too
(`Dockerfile` at the repo root: two-stage build, no Go and no C library in the
runtime image).

---

## Docs

| File | Contents |
|---|---|
| [`docs/design.md`](docs/design.md) | **Why it's built this way**: trade-offs, rejected approaches, known limits |
| [`docs/stability.md`](docs/stability.md) | **What's a contract and what isn't** — read before building on the API |
| [`docs/deploy-tunnel.md`](docs/deploy-tunnel.md) | Deployment: launchd, Cloudflare tunnel, Docker, backups, security checklist |
| [`SECURITY.md`](SECURITY.md) | Reporting, **and the design trade-offs you must evaluate before deploying** |
| [`CONTRIBUTING.md`](CONTRIBUTING.md) | How to build, what review looks for |
| [`skills/keypoint-notify/SKILL.md`](skills/keypoint-notify/SKILL.md) | The skill itself |
| [`skills/keypoint-notify/reference/`](skills/keypoint-notify/reference/) | Command reference, HTTP API, recipes |

At runtime, the server also serves:

| Endpoint | For | What |
|---|---|---|
| `GET /api/v1/llms.txt` | Models | Full API manual (no auth) |
| `GET /api/v1/schema` | Programs | Same content as JSON (enums / error codes / endpoints) |
| `GET /skill/SKILL.md` | Models | Behaviour manual (no auth) |
| `GET /api/v1/agent-prompt` | Models | **Operating instructions** (auth): who you are + rules + subscription modes + loop, rendered for the caller's real identity |
| `GET /install.sh` · `GET /kp` | Machines | Bootstrap script and the binary itself |

---

## Development

```bash
make check          # vet + test
make test           # end-to-end tests (real server, full flows)
make sync-skill     # after editing skill content — see below
```

```
cmd/keypoint            main
internal/cli            command dispatch
internal/client         HTTP client
internal/httpapi        routes / handlers / error model
internal/pack           ★ context-bundle rendering
internal/store          ★ the only package that knows SQL
internal/skill          embedded skill (with a drift test against skills/)
internal/webhook        outbound delivery
internal/webui          embedded board
internal/model          domain types
```

---

## Limits

Honest list of what isn't done: no schema migration framework, no cursor
pagination, no full-text index (search is `LIKE`), no MCP server, single writer
(SQLite + WAL). The reasoning and the conditions under which each should change
are in [`docs/design.md`](docs/design.md) under "known limits".

---

## License

[Apache-2.0](LICENSE).
