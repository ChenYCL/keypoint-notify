package skill

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// The embedded copy must match skills/keypoint-notify/ exactly.
//
// Two copies of one document is a smell, but go:embed cannot reach outside its
// package, and shipping the skill inside the binary is worth more than the
// tidiness of a single directory. So: one source of truth, one generated copy,
// and a test that fails the moment they disagree.
func TestEmbeddedSkillMatchesSource(t *testing.T) {
	root := filepath.Join("..", "..", "skills", "keypoint-notify")
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
		seen[rel] = true

		want, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		got, err := fs.ReadFile(FS, filepath.ToSlash(rel))
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

// SKILL.md must be present and non-trivial: it is the one file the endpoint
// promises, and an empty embed would serve a 200 with nothing useful in it.
func TestSkillIsLoadable(t *testing.T) {
	data, err := fs.ReadFile(FS, "SKILL.md")
	if err != nil {
		t.Fatalf("SKILL.md missing from the embed: %v", err)
	}
	if len(data) < 2000 {
		t.Errorf("SKILL.md looks truncated (%d bytes)", len(data))
	}
}
