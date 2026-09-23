package cli

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// These run the real binary against a real `kp serve`, because every bug here
// lived between the argument parser and the wire — found by driving a sandbox
// of role sessions, not by unit tests of either half.

type live struct {
	t    *testing.T
	bin  string
	home string
	url  string
}

func startLive(t *testing.T) *live {
	t.Helper()
	bin := buildCLI(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	srv := exec.Command(bin, "serve", "--addr", addr, "--data", t.TempDir(), "--quiet")
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Process.Kill(); _ = srv.Wait() })

	url := "http://" + addr
	for i := 0; ; i++ {
		if resp, err := http.Get(url + "/api/v1/health"); err == nil {
			resp.Body.Close()
			break
		}
		if i > 100 {
			t.Fatal("server did not come up")
		}
		time.Sleep(50 * time.Millisecond)
	}
	l := &live{t: t, bin: bin, home: t.TempDir(), url: url}
	l.ok("init", "--server", url, "--name", "boss", "--kind", "human", "--roles", "admin,member,backend", "--yes")
	return l
}

func (l *live) run(args ...string) (string, int) {
	l.t.Helper()
	cmd := exec.Command(l.bin, args...)
	cmd.Env = append(os.Environ(), "KEYPOINT_HOME="+l.home, "KEYPOINT_SERVER=", "KEYPOINT_API_KEY=", "KEYPOINT_ROLE=")
	out, _ := cmd.Output()
	return string(out), cmd.ProcessState.ExitCode()
}

func (l *live) ok(args ...string) string {
	l.t.Helper()
	out, code := l.run(args...)
	if code != 0 {
		l.t.Fatalf("kp %s exited %d\n%s", strings.Join(args, " "), code, clipOut([]byte(out)))
	}
	return out
}

// Global flags go anywhere — the reference says so, and scripts rely on it.
// Commands without flags of their own ignored a trailing --json (whoami,
// board…) or took a leading one as a positional (identity rotate --json bob).
func TestGlobalFlagsWorkInAnyPosition(t *testing.T) {
	l := startLive(t)
	l.ok("identity", "create", "bob", "--roles", "member")
	l.ok("role", "add", "tmp")
	l.ok("task", "new", "--title", "flags", "--goal", "g", "--acceptance", "a")
	l.ok("task", "side", "add", "KP-1", "api", "--role", "backend")
	l.ok("task", "side", "add", "KP-1", "gone", "--role", "backend")

	cases := [][]string{
		{"whoami", "--json"},
		{"board", "--json"},
		{"hook", "ls", "--json"},
		{"claim", "KP-1", "api", "--json"},
		{"task", "status", "KP-1", "doing", "--json"},
		{"task", "side", "rm", "KP-1", "gone", "--json"},
		{"role", "rm", "tmp", "--json"},
		{"identity", "disable", "--json", "bob"},
		{"identity", "enable", "bob", "--json"},
		{"identity", "rotate", "--json", "bob"},
		{"identity", "use", "boss", "--json"},
		{"task", "rm", "KP-1", "--json"},
	}
	for _, args := range cases {
		out, code := l.run(args...)
		if code != 0 {
			t.Errorf("kp %s exited %d\n%s", strings.Join(args, " "), code, clipOut([]byte(out)))
			continue
		}
		if !json.Valid([]byte(out)) {
			t.Errorf("kp %s: --json should print JSON, got\n%s", strings.Join(args, " "), clipOut([]byte(out)))
		}
	}
}

// Rotating your own key with --json used to return before saving the new key,
// leaving this machine holding a key the server had just revoked.
func TestRotateSelfWithJSONKeepsWorking(t *testing.T) {
	l := startLive(t)
	for _, args := range [][]string{
		{"--json", "identity", "rotate", "boss"},
		{"identity", "rotate", "boss", "--json"},
	} {
		l.ok(args...)
		if out, code := l.run("whoami"); code != 0 {
			t.Fatalf("after `kp %s` the local config should hold the new key; whoami exited %d\n%s",
				strings.Join(args, " "), code, out)
		}
	}
}

// `kp loop` asked the server twice per item — and with claim on, both asks
// dispatched. With two faces waiting, `--max 1` took both and handed only one
// to the command.
func TestLoopClaimsOnlyWhatItHandles(t *testing.T) {
	l := startLive(t)
	l.ok("task", "new", "--title", "两个面", "--goal", "g", "--acceptance", "a")
	l.ok("task", "side", "add", "KP-1", "s1", "--role", "backend")
	l.ok("task", "side", "add", "KP-1", "s2", "--role", "backend")

	l.ok("loop", "--max", "1", "--wait", "1", "--task", "KP-1")

	var show struct {
		Task struct {
			Sides []struct {
				Key              string `json:"key"`
				AssigneeIdentity string `json:"assignee_identity"`
			} `json:"sides"`
		} `json:"task"`
	}
	if err := json.Unmarshal([]byte(l.ok("--json", "task", "show", "KP-1")), &show); err != nil {
		t.Fatal(err)
	}
	claimed := 0
	for _, s := range show.Task.Sides {
		if s.AssigneeIdentity != "" {
			claimed++
		}
	}
	if claimed != 1 {
		t.Errorf("loop --max 1 handled one item, so it should have claimed exactly one face; claimed %d", claimed)
	}
}

// The generic `kp task` help says "see kp task new --help"; that request has
// to reach the page with the --from-json shape instead of looping back.
func TestTaskNewHelpShowsTheJSONShape(t *testing.T) {
	bin := buildCLI(t)
	cmd := exec.Command(bin, "task", "new", "--help")
	cmd.Env = append(os.Environ(), "KEYPOINT_HOME="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("exit: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), `"segments"`) || !strings.Contains(string(out), "assignee_role") {
		t.Errorf("kp task new --help should show the JSON body, got\n%s", clipOut(out))
	}
}

// Most people an invite goes to have no kp yet; the invite has to carry the
// one-liner that installs it.
func TestInviteCarriesTheInstallOneLiner(t *testing.T) {
	l := startLive(t)
	out := l.ok("identity", "create", "carol", "--roles", "frontend")
	if !strings.Contains(out, fmt.Sprintf("curl -fsSL \"%s/install.sh?key=kp_", l.url)) {
		t.Errorf("the invite should include the install.sh one-liner, got\n%s", out)
	}

	// An admin on the server box talks to 127.0.0.1; the invite must carry the
	// address the recipient can reach.
	l.ok("config", "set", "public_url", "https://kp.example.com/")
	out = l.ok("identity", "create", "dave", "--roles", "frontend")
	if !strings.Contains(out, `curl -fsSL "https://kp.example.com/install.sh?key=kp_`) ||
		!strings.Contains(out, "kp init --server https://kp.example.com --key") {
		t.Errorf("with public_url set the invite should use it, got\n%s", out)
	}
	if _, code := l.run("whoami"); code != 0 {
		t.Error("public_url must not change where this CLI itself connects")
	}
}
