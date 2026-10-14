package reap

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	liveID = "a1b2c3d4-e5f6-7890-abcd-ef1234567890"
	deadID = "dead1234-dead-dead-dead-deaddeaddead"
)

// Liveness comes from real processes: a running `sleep` is a live owner, one
// that has been killed and waited for is a dead one.
func liveProcess(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "300")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd.Process.Pid
}

func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "300")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	return cmd.Process.Pid
}

func sessionFile(t *testing.T, state, id, content string, age time.Duration) string {
	t.Helper()
	dir := filepath.Join(state, "session")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	mtime := time.Now().Add(-age)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return path
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestLiveOwnerKeptDeadOwnerReaped(t *testing.T) {
	state := t.TempDir()
	// Both are past the grace window: liveness must come from the pid, not age.
	live := sessionFile(t, state, liveID, fmt.Sprintf("on\npid:%d\n", liveProcess(t)), 2*time.Minute)
	dead := sessionFile(t, state, deadID, fmt.Sprintf("on\npid:%d\n", deadPID(t)), 2*time.Minute)

	var out bytes.Buffer
	if n := Sessions(state, time.Now(), &out, &out); n != 1 {
		t.Errorf("reaped %d, want 1\n%s", n, out.String())
	}
	if !exists(live) {
		t.Error("live-owner file was removed")
	}
	if exists(dead) {
		t.Error("dead-owner file survived")
	}
	if !strings.Contains(out.String(), "removed stale session file "+deadID) {
		t.Errorf("output %q does not report the removal", out.String())
	}

	// A second run has nothing left to do.
	if n := Sessions(state, time.Now(), &out, &out); n != 0 || !exists(live) {
		t.Errorf("second run reaped %d (live file present: %v)", n, exists(live))
	}
}

func TestLegacyFileWithoutPIDIsReapedAfterGrace(t *testing.T) {
	state := t.TempDir()
	path := sessionFile(t, state, liveID, "on\n", 2*time.Minute)
	Sessions(state, time.Now(), new(bytes.Buffer), new(bytes.Buffer))
	if exists(path) {
		t.Error("legacy file survived")
	}
}

func TestYoungFileSurvivesEvenWithDeadOwner(t *testing.T) {
	state := t.TempDir()
	path := sessionFile(t, state, liveID, fmt.Sprintf("on\npid:%d\n", deadPID(t)), 0)
	if n := Sessions(state, time.Now(), new(bytes.Buffer), new(bytes.Buffer)); n != 0 || !exists(path) {
		t.Errorf("young file reaped (n=%d)", n)
	}
}

func TestNonUUIDNamesAreSkipped(t *testing.T) {
	state := t.TempDir()
	path := sessionFile(t, state, "notes.txt", "on\n", time.Hour)
	var stderr bytes.Buffer
	Sessions(state, time.Now(), new(bytes.Buffer), &stderr)
	if !exists(path) {
		t.Error("non-UUID file was removed")
	}
	if !strings.Contains(stderr.String(), "skipping non-UUID file 'notes.txt'") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestMissingSessionDir(t *testing.T) {
	if n := Sessions(filepath.Join(t.TempDir(), "nope"), time.Now(), new(bytes.Buffer), new(bytes.Buffer)); n != 0 {
		t.Errorf("reaped %d from a missing dir", n)
	}
}

func TestOwnerPID(t *testing.T) {
	for content, want := range map[string]int{
		"on\npid:42\n":            42,
		"on\npid:42":              42,
		"on\n":                    0,
		"on\npid:abc\n":           0,
		"on\npid:42\r\n":          0,
		"on\n pid:42\n":           0,
		"on\npid:7\npid:8\n":      7,
		"pid:99999999999999999\n": 0,
	} {
		if got := OwnerPID(content); got != want {
			t.Errorf("OwnerPID(%q) = %d, want %d", content, got, want)
		}
	}
}

func TestAlive(t *testing.T) {
	if !Alive(os.Getpid()) {
		t.Error("own pid not alive")
	}
	if Alive(deadPID(t)) {
		t.Error("dead pid alive")
	}
	if Alive(0) || Alive(-1) {
		t.Error("non-positive pid alive")
	}
}
