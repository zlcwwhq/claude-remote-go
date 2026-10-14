// Package mode resolves whether shell commands are routed to the remote box
// ("on") or run locally ("off"), and which host to use.
//
// Precedence (highest first):
//
//  1. session file  <state>/session/<CLAUDE_CODE_SESSION_ID>
//     — only consulted when the id is a well-formed UUID; a malformed,
//     non-empty id produces a warning and the layer is skipped
//     — line 1 is the mode; an optional "pid:<owner>" line 2 is a liveness
//     token for the reaper
//  2. project file  $CLAUDE_PROJECT_DIR/.claude-remote-mode
//  3. global file   <state>/mode
//  4. env seed      CLAUDE_REMOTE_MODE (launch-time only)
//  5. default       on
//
// Callers must treat anything other than exactly "on" as local: a corrupt
// toggle file must never silently route commands to a box.
package mode

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	On  = "on"
	Off = "off"
)

// Layer names reported as Result.Source.
const (
	SourceSession = "session"
	SourceProject = "project"
	SourceGlobal  = "global"
	SourceSeed    = "seed"
	SourceDefault = "default"
)

// ProjectFile is the per-project override file name.
const ProjectFile = ".claude-remote-mode"

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ValidSessionID reports whether id is a well-formed UUID. Anything else
// (path separators, "..", ...) must never be used to build a file path.
func ValidSessionID(id string) bool {
	return uuidRE.MatchString(id)
}

// Inputs are everything the resolution reads besides the files themselves.
type Inputs struct {
	StateDir   string
	SessionID  string // CLAUDE_CODE_SESSION_ID
	ProjectDir string // CLAUDE_PROJECT_DIR
	Seed       string // CLAUDE_REMOTE_MODE
}

// InputsFromEnv fills Inputs from the process environment.
func InputsFromEnv(stateDir, seed string) Inputs {
	return Inputs{
		StateDir:   stateDir,
		SessionID:  os.Getenv("CLAUDE_CODE_SESSION_ID"),
		ProjectDir: os.Getenv("CLAUDE_PROJECT_DIR"),
		Seed:       seed,
	}
}

// Result is the outcome of Resolve.
type Result struct {
	// Mode is the winning layer's value, whitespace-trimmed but otherwise
	// unvalidated.
	Mode string
	// Source names the winning layer.
	Source string
	// Warning is non-empty when CLAUDE_CODE_SESSION_ID was malformed.
	Warning string
}

// Remote reports whether commands should go to the remote box.
func (r Result) Remote() bool {
	return r.Mode == On
}

// Resolve walks the precedence chain.
func Resolve(in Inputs) Result {
	var res Result
	if id := in.SessionID; id != "" {
		if ValidSessionID(id) {
			path := filepath.Join(in.StateDir, "session", id)
			if isRegular(path) {
				first, _, _ := strings.Cut(readOrEmpty(path), "\n")
				return Result{Mode: Trim(first), Source: SourceSession}
			}
		} else {
			res.Warning = fmt.Sprintf("[claude-remote] WARNING: CLAUDE_CODE_SESSION_ID '%s' is not a well-formed UUID — session layer skipped", id)
		}
	}
	if in.ProjectDir != "" {
		if path := filepath.Join(in.ProjectDir, ProjectFile); isRegular(path) {
			res.Mode, res.Source = Trim(readOrEmpty(path)), SourceProject
			return res
		}
	}
	if path := filepath.Join(in.StateDir, "mode"); isRegular(path) {
		res.Mode, res.Source = Trim(readOrEmpty(path)), SourceGlobal
		return res
	}
	if in.Seed != "" {
		res.Mode, res.Source = Trim(in.Seed), SourceSeed
		return res
	}
	res.Mode, res.Source = On, SourceDefault
	return res
}

// ResolveURL returns the host from <state>/url when it holds a non-blank
// value, otherwise fallback (the configured REMOTE_HOST).
func ResolveURL(stateDir, fallback string) string {
	if v := Trim(readOrEmpty(filepath.Join(stateDir, "url"))); v != "" {
		return v
	}
	return fallback
}

// Trim strips the characters bash's [[:space:]] class matches, so "on\r\n"
// and "on\t" both read as "on".
func Trim(s string) string {
	return strings.Trim(s, " \t\n\v\f\r")
}

func isRegular(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}

func readOrEmpty(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}
