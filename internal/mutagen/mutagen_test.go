package mutagen

import (
	"os/exec"
	"strings"
	"testing"
)

var namePaths = []string{
	"/Users/alice/workspace/myproject",
	"/Users/alice/.worktrees/feat.x",
	"/home/bob/my project/v1.2",
	"/srv/a--b/",
	"/tmp/x_y+z",
	"/",
}

func TestSessionName(t *testing.T) {
	want := map[string]string{
		"/Users/alice/workspace/myproject": "claude-remote-Users-alice-workspace-myproject",
		"/Users/alice/.worktrees/feat.x":   "claude-remote-Users-alice--worktrees-feat-x",
		"/":                                "claude-remote-",
	}
	for path, w := range want {
		if got := SessionName(path); got != w {
			t.Errorf("SessionName(%q) = %q, want %q", path, got, w)
		}
	}
}

// The names must match what the bash implementation created so existing
// sessions keep being recognised.
func TestSessionNameMatchesBash(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	for _, path := range namePaths {
		cmd := exec.Command("bash", "-c", `echo "claude-remote-$(echo "$1" | tr -c '[:alnum:]-' '-' | sed 's/^-//;s/-$//')"`, "_", path)
		cmd.Env = append(cmd.Environ(), "LC_ALL=C")
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		if want, got := strings.TrimSpace(string(out)), SessionName(path); got != want {
			t.Errorf("SessionName(%q) = %q, bash gives %q", path, got, want)
		}
	}
}

const sampleList = `--------------------------------------------------------------------------------
Name: claude-remote-Users-alice-proj
Identifier: sync_abc
Labels:
	name: claude-remote
Alpha:
	URL: /Users/alice/proj
	Connected: Yes
Beta:
	URL: ubuntu@box:/home/ubuntu/claude-remote-mirror/Users/alice/proj
	Connected: Yes
Status: Watching for changes
--------------------------------------------------------------------------------
Name: claude-remote-Users-alice-gone
Identifier: sync_def
Alpha:
	URL: /Users/alice/gone
Beta:
	URL: ubuntu@box:/x
Status: Halted on root emptied
--------------------------------------------------------------------------------
Name: claude-remote-Users-alice-net
Alpha:
	URL: /Users/alice/net
Beta:
	URL: ubuntu@box:/y
Last error: connection reset
Status: [Disconnected] Connecting to beta
--------------------------------------------------------------------------------
`

func TestParseList(t *testing.T) {
	got := ParseList(sampleList)
	want := []Session{
		{Name: "claude-remote-Users-alice-proj", Status: "Watching for changes", Alpha: "/Users/alice/proj"},
		{Name: "claude-remote-Users-alice-gone", Status: "Halted on root emptied", Alpha: "/Users/alice/gone"},
		{Name: "claude-remote-Users-alice-net", Status: "[Disconnected] Connecting to beta", Alpha: "/Users/alice/net"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d sessions: %+v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("session %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if s := ParseList(""); len(s) != 0 {
		t.Errorf("empty output parsed to %+v", s)
	}
}

func TestReapReason(t *testing.T) {
	exists := func(p string) bool { return p != "/gone" }
	cases := map[Session]string{
		{Status: "Watching for changes", Alpha: "/ok"}:   "",
		{Status: "Halted on root emptied", Alpha: "/ok"}: "status: Halted on root emptied",
		{Status: "Errored", Alpha: "/ok"}:                "status: Errored",
		{Status: "Watching for changes", Alpha: "/gone"}: "alpha path gone: /gone",
		{Status: "[Disconnected]", Alpha: "/gone"}:       "status: [Disconnected]; alpha path gone: /gone",
	}
	for s, want := range cases {
		if got := s.ReapReason(exists); got != want {
			t.Errorf("ReapReason(%+v) = %q, want %q", s, got, want)
		}
	}
}
