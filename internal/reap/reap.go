// Package reap removes stale per-session mode files from <state>/session.
//
// Liveness is a PID, not the session UUID: the reaper cannot observe a
// session's UUID (it is in neither the launcher's argv nor its environment as
// seen from outside), so the toggle records the PID of the long-lived
// claude/node process that owns the session:
//
//	line 1: mode (on|off)
//	line 2: pid:<owner>
//
// A file is live while its owner process exists. Files without a usable
// owner are reaped, but only once they are older than Grace, which guards
// against reaping a file that is being written right now.
package reap

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/langwatch/claude-remote/internal/mode"
)

// Grace is the minimum age of a file whose owner is dead or unknown before it
// is removed.
const Grace = 60 * time.Second

var pidLine = regexp.MustCompile(`^pid:([0-9]+)$`)

// Sessions reaps <stateDir>/session and returns how many files it removed.
// Progress goes to stdout, skipped entries to stderr.
func Sessions(stateDir string, now time.Time, stdout, stderr io.Writer) int {
	dir := filepath.Join(stateDir, "session")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	reaped := 0
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		fi, err := os.Stat(path)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if !mode.ValidSessionID(e.Name()) {
			fmt.Fprintf(stderr, "session-reap: skipping non-UUID file '%s'\n", e.Name())
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if pid := OwnerPID(string(data)); pid > 0 && Alive(pid) {
			continue
		}
		if now.Sub(fi.ModTime()) < Grace {
			continue
		}
		if err := os.Remove(path); err != nil {
			continue
		}
		fmt.Fprintf(stdout, "session-reap: removed stale session file %s\n", e.Name())
		reaped++
	}
	return reaped
}

// OwnerPID returns the PID from the first "pid:<n>" line, or 0.
func OwnerPID(content string) int {
	for _, line := range strings.Split(content, "\n") {
		if m := pidLine.FindStringSubmatch(line); m != nil {
			// pid_t is 32 bits; a larger value would be truncated by kill(2).
			pid, err := strconv.ParseInt(m[1], 10, 32)
			if err != nil {
				return 0
			}
			return int(pid)
		}
	}
	return 0
}

// Alive reports whether a process with this PID exists. A process owned by
// another user still counts.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
