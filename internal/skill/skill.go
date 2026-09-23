// Package skill embeds the Keypoint agent skill so the server can hand it out.
//
// A hub that can describe itself but cannot hand over the thing that teaches an
// agent to use it is only half self-describing. `GET /skill/SKILL.md` closes
// that: an operator pastes one URL into a fresh session and the session has the
// same instructions a locally-installed skill would have given it.
//
// The canonical source lives in skills/keypoint-notify/ at the repo root —
// that is what `make skill` installs into ~/.claude/skills. The copy here
// exists only because go:embed cannot reach outside a package directory. A test
// fails if the two ever drift, and `make sync-skill` rewrites the copy.
package skill

import (
	"embed"
	"io/fs"
)

//go:embed assets
var assets embed.FS

// FS is the skill tree rooted at the skill directory, so callers open
// "SKILL.md" rather than "assets/SKILL.md".
var FS fs.FS = mustSub()

func mustSub() fs.FS {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		panic("skill: " + err.Error())
	}
	return sub
}
