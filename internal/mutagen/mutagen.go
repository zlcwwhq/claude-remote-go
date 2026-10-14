// Package mutagen drives the mutagen CLI that keeps local directories and
// their remote copies in sync.
package mutagen

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// Label marks every sync session claude-remote creates.
const Label = "name=claude-remote"

// IgnorePatterns are excluded from sync.
var IgnorePatterns = []string{
	"node_modules",
	".venv",
	".cache",
	"dist",
	".next*",
	"__pycache__",
	".pytest_cache",
	".mypy_cache",
	".turbo",
	"*.pyc",
	".DS_Store",
	"coverage",
	".nyc_output",
	"target",
	"build",
}

// SessionName derives the sync session name for an absolute path: every
// character other than an ASCII letter, digit or hyphen becomes a hyphen and
// one leading and one trailing hyphen are dropped, so /Users/a/proj becomes
// claude-remote-Users-a-proj. It matches the names the bash implementation
// created, so existing sessions are picked up.
func SessionName(path string) string {
	var b strings.Builder
	for _, r := range path + "\n" { // the newline mirrors `echo "$path" | tr ...`
		if r == '-' || (r < 128 && isAlnum(byte(r))) {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	s := strings.TrimPrefix(b.String(), "-")
	return "claude-remote-" + strings.TrimSuffix(s, "-")
}

func isAlnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// Session is one entry of `mutagen sync list`.
type Session struct {
	Name   string
	Status string
	Alpha  string // local path
}

// ParseList extracts sessions from `mutagen sync list` text output: the
// "Name:" and "Status:" lines that start a line, and the first "URL:" line of
// each session (the alpha, i.e. local, endpoint).
func ParseList(out string) []Session {
	var sessions []Session
	var cur *Session
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "Name:"):
			sessions = append(sessions, Session{Name: strings.TrimSpace(strings.TrimPrefix(line, "Name:"))})
			cur = &sessions[len(sessions)-1]
		case cur == nil:
		case strings.Contains(line, "URL:"):
			if cur.Alpha == "" {
				_, url, _ := strings.Cut(line, "URL:")
				cur.Alpha = strings.TrimSpace(url)
			}
		case strings.HasPrefix(line, "Status:"):
			cur.Status = strings.TrimSpace(strings.TrimPrefix(line, "Status:"))
		}
	}
	return sessions
}

var deadStatus = regexp.MustCompile(`(?i)halted|disconnected|error`)

// ReapReason explains why s is a zombie, or returns "" if it is healthy.
// dirExists reports whether a local directory is present.
func (s Session) ReapReason(dirExists func(string) bool) string {
	var reasons []string
	if deadStatus.MatchString(s.Status) {
		reasons = append(reasons, "status: "+s.Status)
	}
	if s.Alpha != "" && !dirExists(s.Alpha) {
		reasons = append(reasons, "alpha path gone: "+s.Alpha)
	}
	return strings.Join(reasons, "; ")
}

// CLI runs the mutagen executable.
type CLI struct {
	Bin string
}

// New finds mutagen on PATH. Bin is empty when it is not installed.
func New() *CLI {
	bin, _ := exec.LookPath("mutagen")
	return &CLI{Bin: bin}
}

// Installed reports whether mutagen was found.
func (m *CLI) Installed() bool {
	return m.Bin != ""
}

func (m *CLI) command(args ...string) *exec.Cmd {
	bin := m.Bin
	if bin == "" {
		bin = "mutagen" // fails with a clear "not found" error
	}
	return exec.Command(bin, args...)
}

// DaemonStart starts the mutagen daemon if it is not running.
func (m *CLI) DaemonStart() error {
	return m.command("daemon", "start").Run()
}

// Exists reports whether a sync session with exactly this name exists.
func (m *CLI) Exists(name string) bool {
	return m.command("sync", "list", name).Run() == nil
}

// List returns the `mutagen sync list` text for claude-remote's sessions.
func (m *CLI) List() (string, error) {
	out, err := m.command("sync", "list", "--label-selector="+Label).Output()
	return string(out), err
}

// Create starts syncing alpha (local) to beta (host:path), streaming
// mutagen's own output.
func (m *CLI) Create(name, alpha, beta string, stdout, stderr io.Writer) error {
	args := []string{"sync", "create", alpha, beta, "--name=" + name, "--label=" + Label}
	for _, p := range IgnorePatterns {
		args = append(args, "--ignore="+p)
	}
	args = append(args,
		"--sync-mode=two-way-resolved",
		"--default-file-mode=0644",
		"--default-directory-mode=0755",
	)
	cmd := m.command(args...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	return cmd.Run()
}

// Flush waits for the named session to complete a sync cycle.
func (m *CLI) Flush(name string) error {
	return m.command("sync", "flush", name).Run()
}

// Terminate removes the named session.
func (m *CLI) Terminate(name string) error {
	return m.command("sync", "terminate", name).Run()
}

// TerminateAll removes every claude-remote session.
func (m *CLI) TerminateAll() error {
	cmd := m.command("sync", "terminate", "--label-selector="+Label)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}
