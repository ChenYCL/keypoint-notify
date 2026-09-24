// Package skill embeds the Keypoint agent skills so the server can hand them out.
//
// A hub that can describe itself but cannot hand over the thing that teaches an
// agent to use it is only half self-describing. `GET /skill/SKILL.md` closes
// that: an operator pastes one URL into a fresh session and the session has the
// same instructions a locally-installed skill would have given it.
//
// There is one playbook skill (keypoint-notify) plus a handful of small command
// skills (kp-new, kp-next, kp-report, kp-done, kp-cancel, kp-loop, kp-watch) that
// show up as slash commands in Claude Code (/kp-next) and Kimi Code
// (/skill:kp-next). Each is a directory with a SKILL.md, laid out exactly as the
// CLIs expect, so installing is a plain copy.
//
// The canonical source lives in skills/ at the repo root — that is what
// `make skill` links into ~/.claude/skills. The copy here exists only because
// go:embed cannot reach outside a package directory. A test fails if the two
// ever drift, and `make sync-skill` rewrites the copy.
package skill

import (
	"embed"
	"io/fs"
	"sort"
	"strings"
)

//go:embed assets
var assets embed.FS

// Main is the playbook skill. Paths without a skill directory in front
// (`SKILL.md`, `reference/api.md`) resolve inside it, which is what every
// client before the command skills existed asks for.
const Main = "keypoint-notify"

// FS is the skills tree: one directory per skill.
var FS fs.FS = mustSub()

func mustSub() fs.FS {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		panic("skill: " + err.Error())
	}
	return sub
}

// Index lists every skill and the files it is made of, relative to the skill's
// own directory. The installer walks this instead of a hard-coded file list, so
// adding a skill does not need a client release.
func Index(fsys fs.FS) (map[string][]string, error) {
	out := map[string][]string{}
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		name, rest, ok := strings.Cut(path, "/")
		if !ok {
			return nil // stray file at the root; not part of any skill
		}
		out[name] = append(out[name], rest)
		return nil
	})
	for _, files := range out {
		sort.Strings(files)
	}
	return out, err
}

// Resolve maps a request path to a file in the tree: `kp-next/SKILL.md` is read
// as is, while a path whose first segment is not a skill directory is taken to
// mean the playbook (`SKILL.md` → `keypoint-notify/SKILL.md`).
func Resolve(fsys fs.FS, path string) string {
	path = strings.TrimPrefix(path, "/")
	if first, _, ok := strings.Cut(path, "/"); ok {
		if st, err := fs.Stat(fsys, first); err == nil && st.IsDir() {
			return path
		}
	}
	return Main + "/" + path
}
