package skill

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The embedded copy must match skills/ exactly.
//
// Two copies of one document is a smell, but go:embed cannot reach outside its
// package, and shipping the skills inside the binary is worth more than the
// tidiness of a single directory. So: one source of truth, one generated copy,
// and a test that fails the moment they disagree.
func TestEmbeddedSkillMatchesSource(t *testing.T) {
	root := filepath.Join("..", "..", "skills")
	if _, err := os.Stat(root); err != nil {
		t.Skipf("skill source not present (%v)", err)
	}

	seen := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		seen[rel] = true

		want, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		got, err := fs.ReadFile(FS, rel)
		if err != nil {
			t.Errorf("%s exists at the source but not in the embedded copy — run `make sync-skill`", rel)
			return nil
		}
		if string(want) != string(got) {
			t.Errorf("%s differs from the embedded copy — run `make sync-skill`", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// And nothing extra hiding in the copy.
	_ = fs.WalkDir(FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !seen[path] {
			t.Errorf("%s is in the embedded copy but not in the source — run `make sync-skill`", path)
		}
		return nil
	})
}

// The playbook must be present and non-trivial, and the old unprefixed paths
// (`SKILL.md`, `reference/…`) must still land on it: clients released before
// the command skills existed ask for exactly those.
func TestSkillIsLoadable(t *testing.T) {
	data, err := fs.ReadFile(FS, Resolve(FS, "SKILL.md"))
	if err != nil {
		t.Fatalf("SKILL.md missing from the embed: %v", err)
	}
	if len(data) < 2000 {
		t.Errorf("SKILL.md looks truncated (%d bytes)", len(data))
	}
	if _, err := fs.ReadFile(FS, Resolve(FS, "reference/commands.md")); err != nil {
		t.Errorf("reference/commands.md should resolve into the playbook: %v", err)
	}
}

// Every command skill needs the two fields both Claude Code and Kimi Code
// require, and a name that matches its directory — Kimi fails to parse a
// directory-form SKILL.md without name and description, and a mismatched name
// makes /kp-x and /skill:kp-x disagree.
func TestEverySkillHasNameAndDescription(t *testing.T) {
	idx, err := Index(FS)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx) < 2 {
		t.Fatalf("expected the playbook plus command skills, got %v", idx)
	}
	for name := range idx {
		data, err := fs.ReadFile(FS, name+"/SKILL.md")
		if err != nil {
			t.Errorf("%s has no SKILL.md", name)
			continue
		}
		head, _, ok := strings.Cut(strings.TrimPrefix(string(data), "---\n"), "\n---")
		if !ok {
			t.Errorf("%s: no frontmatter", name)
			continue
		}
		if !strings.Contains(head, "name: "+name) && !strings.Contains(head, `name: "`+name+`"`) {
			t.Errorf("%s: frontmatter name does not match the directory", name)
		}
		if !strings.Contains(head, "description:") {
			t.Errorf("%s: frontmatter has no description", name)
		}
	}
}
