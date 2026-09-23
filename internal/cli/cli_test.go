package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Asking for help must exit 0.
//
// flag.Parse reports -h/--help as an error, so a naive wrapper turns the most
// benign input a user can type into a red message and a non-zero exit. Found
// by running `go install` on the freshly published module and typing --help.
//
// The test builds the real binary and runs it, because the bug lived in the
// dispatch layer — a unit test of the parser would have missed it.
func TestHelpExitsZero(t *testing.T) {
	bin := buildCLI(t)

	cases := [][]string{
		{"--help"}, {"-h"},
		{"next", "--help"}, {"loop", "--help"}, {"claim", "--help"},
		{"inbox", "--help"}, {"events", "--help"}, {"attach", "--help"},
		{"whoami", "--help"}, {"board", "--help"}, {"report", "--help"},
		{"install", "--help"}, {"docs", "--help"},
		{"task", "--help"}, {"task", "list", "--help"}, {"task", "side", "--help"},
		{"task", "new", "--help"}, {"role", "ls", "--help"},
		{"identity", "ls", "--help"}, {"hook", "ls", "--help"},
	}
	for _, args := range cases {
		cmd := exec.Command(bin, args...)
		// A config that does not exist, so nothing can accidentally reach the
		// network; help must not need one.
		cmd.Env = append(os.Environ(), "KEYPOINT_HOME="+t.TempDir())
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("kp %s exited non-zero (%v)\n%s",
				strings.Join(args, " "), err, clipOut(out))
		}
		if strings.Contains(string(out), "✗") {
			t.Errorf("kp %s printed an error marker\n%s",
				strings.Join(args, " "), clipOut(out))
		}
	}
}

// Errors must still be errors. The fix above could easily have gone too far
// and made everything exit 0.
func TestFailuresStillExitNonZero(t *testing.T) {
	bin := buildCLI(t)

	cases := [][]string{
		{},                     // no command: nothing accomplished
		{"nosuchcommand"},      // unknown
		{"claim"},              // missing arguments
		{"task", "side", "ls"}, // missing task code
	}
	for _, args := range cases {
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), "KEYPOINT_HOME="+t.TempDir())
		out, _ := cmd.CombinedOutput()
		if cmd.ProcessState.ExitCode() == 0 {
			t.Errorf("kp %s should exit non-zero, got 0\n%s",
				strings.Join(args, " "), clipOut(out))
		}
	}
}

func clipOut(b []byte) string {
	s := string(b)
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}

// buildCLI compiles the binary once per test process.
func buildCLI(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "kp")
	cmd := exec.Command("go", "build", "-o", bin, "../../cmd/keypoint")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}
