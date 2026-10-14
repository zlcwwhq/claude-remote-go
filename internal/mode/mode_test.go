package mode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	sidA = "a1b2c3d4-e5f6-7890-abcd-ef1234567890"
	sidB = "b2c3d4e5-f6a7-8901-bcde-f12345678901"
)

type fixture struct {
	state, project string
}

func newFixture(t *testing.T) fixture {
	dir := t.TempDir()
	f := fixture{state: filepath.Join(dir, "state"), project: filepath.Join(dir, "project")}
	for _, d := range []string{filepath.Join(f.state, "session"), f.project} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) global(t *testing.T, v string) { write(t, filepath.Join(f.state, "mode"), v) }
func (f fixture) projectMode(t *testing.T, v string) {
	write(t, filepath.Join(f.project, ProjectFile), v)
}
func (f fixture) session(t *testing.T, id, v string) {
	write(t, filepath.Join(f.state, "session", id), v)
}

func TestPrecedence(t *testing.T) {
	cases := []struct {
		name       string
		setup      func(*testing.T, fixture)
		in         func(fixture) Inputs
		wantMode   string
		wantSource string
	}{
		{
			name:  "project beats global",
			setup: func(t *testing.T, f fixture) { f.global(t, "on\n"); f.projectMode(t, "off\n") },
			in: func(f fixture) Inputs {
				return Inputs{StateDir: f.state, ProjectDir: f.project}
			},
			wantMode: "off", wantSource: SourceProject,
		},
		{
			name:     "global beats seed",
			setup:    func(t *testing.T, f fixture) { f.global(t, "off\n") },
			in:       func(f fixture) Inputs { return Inputs{StateDir: f.state, Seed: "on"} },
			wantMode: "off", wantSource: SourceGlobal,
		},
		{
			name:     "seed beats default",
			setup:    func(*testing.T, fixture) {},
			in:       func(f fixture) Inputs { return Inputs{StateDir: f.state, Seed: "off"} },
			wantMode: "off", wantSource: SourceSeed,
		},
		{
			name:     "default is on",
			setup:    func(*testing.T, fixture) {},
			in:       func(f fixture) Inputs { return Inputs{StateDir: f.state} },
			wantMode: "on", wantSource: SourceDefault,
		},
		{
			name: "session beats project",
			setup: func(t *testing.T, f fixture) {
				f.projectMode(t, "on\n")
				f.session(t, sidA, "off\n")
			},
			in: func(f fixture) Inputs {
				return Inputs{StateDir: f.state, ProjectDir: f.project, SessionID: sidA}
			},
			wantMode: "off", wantSource: SourceSession,
		},
		{
			name: "no session file falls through to project",
			setup: func(t *testing.T, f fixture) {
				f.global(t, "on\n")
				f.projectMode(t, "off\n")
			},
			in: func(f fixture) Inputs {
				return Inputs{StateDir: f.state, ProjectDir: f.project, SessionID: sidA}
			},
			wantMode: "off", wantSource: SourceProject,
		},
		{
			name:     "session file: only line 1 is the mode",
			setup:    func(t *testing.T, f fixture) { f.session(t, sidA, "off\npid:12345\n") },
			in:       func(f fixture) Inputs { return Inputs{StateDir: f.state, SessionID: sidA} },
			wantMode: "off", wantSource: SourceSession,
		},
		{
			name:     "session file without trailing newline",
			setup:    func(t *testing.T, f fixture) { f.session(t, sidA, "on") },
			in:       func(f fixture) Inputs { return Inputs{StateDir: f.state, SessionID: sidA} },
			wantMode: "on", wantSource: SourceSession,
		},
		{
			name:     "CRLF is trimmed",
			setup:    func(t *testing.T, f fixture) { f.global(t, "on\r\n") },
			in:       func(f fixture) Inputs { return Inputs{StateDir: f.state} },
			wantMode: "on", wantSource: SourceGlobal,
		},
		{
			name:     "tab is trimmed",
			setup:    func(t *testing.T, f fixture) { f.global(t, "on\t\n") },
			in:       func(f fixture) Inputs { return Inputs{StateDir: f.state} },
			wantMode: "on", wantSource: SourceGlobal,
		},
		{
			name:     "empty global file wins with empty mode",
			setup:    func(t *testing.T, f fixture) { f.global(t, "") },
			in:       func(f fixture) Inputs { return Inputs{StateDir: f.state, Seed: "on"} },
			wantMode: "", wantSource: SourceGlobal,
		},
		{
			name:     "project dir without the file is skipped",
			setup:    func(t *testing.T, f fixture) { f.global(t, "off") },
			in:       func(f fixture) Inputs { return Inputs{StateDir: f.state, ProjectDir: f.project} },
			wantMode: "off", wantSource: SourceGlobal,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			c.setup(t, f)
			got := Resolve(c.in(f))
			if got.Mode != c.wantMode || got.Source != c.wantSource {
				t.Errorf("Resolve = %q from %s, want %q from %s", got.Mode, got.Source, c.wantMode, c.wantSource)
			}
			if got.Warning != "" {
				t.Errorf("unexpected warning %q", got.Warning)
			}
		})
	}
}

func TestSessionsAreIndependent(t *testing.T) {
	f := newFixture(t)
	f.global(t, "off\n")
	f.session(t, sidA, "on\n")
	f.session(t, sidB, "off\n")
	if r := Resolve(Inputs{StateDir: f.state, SessionID: sidA}); !r.Remote() {
		t.Errorf("session A = %+v, want remote", r)
	}
	if r := Resolve(Inputs{StateDir: f.state, SessionID: sidB}); r.Remote() {
		t.Errorf("session B = %+v, want local", r)
	}
}

func TestMalformedSessionIDIsBlocked(t *testing.T) {
	f := newFixture(t)
	f.global(t, "off\n")
	// If the id were used as a path, session/../mode would be read instead.
	for _, id := range []string{"../mode", "a/b", "not-a-uuid", sidA + "x"} {
		r := Resolve(Inputs{StateDir: f.state, SessionID: id})
		if r.Source != SourceGlobal {
			t.Errorf("id %q: source %s, want global", id, r.Source)
		}
		if !strings.Contains(r.Warning, "WARNING") || !strings.Contains(r.Warning, id) {
			t.Errorf("id %q: warning %q should mention it", id, r.Warning)
		}
	}
	if r := Resolve(Inputs{StateDir: f.state, SessionID: ""}); r.Warning != "" {
		t.Errorf("empty id warned: %q", r.Warning)
	}
}

func TestValidSessionID(t *testing.T) {
	for id, want := range map[string]bool{
		sidA:                                   true,
		strings.ToUpper(sidA):                  true,
		"":                                     false,
		"../mode":                              false,
		"a1b2c3d4e5f67890abcdef1234567890":     false,
		"g1b2c3d4-e5f6-7890-abcd-ef1234567890": false,
		sidA + "\n":                            false,
	} {
		if got := ValidSessionID(id); got != want {
			t.Errorf("ValidSessionID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestResolveURL(t *testing.T) {
	f := newFixture(t)
	url := filepath.Join(f.state, "url")
	if got := ResolveURL(f.state, "cfg-host"); got != "cfg-host" {
		t.Errorf("no url file: %q", got)
	}
	for _, blank := range []string{"", "   \n", "\t\r\n"} {
		write(t, url, blank)
		if got := ResolveURL(f.state, "cfg-host"); got != "cfg-host" {
			t.Errorf("blank url file %q: got %q", blank, got)
		}
	}
	write(t, url, " ubuntu@10.0.3.56\r\n")
	if got := ResolveURL(f.state, "cfg-host"); got != "ubuntu@10.0.3.56" {
		t.Errorf("url file: got %q", got)
	}
}

func TestRemoteRequiresExactlyOn(t *testing.T) {
	for m, want := range map[string]bool{"on": true, "off": false, "onn": false, "yes": false, "": false, "ON": false} {
		if got := (Result{Mode: m}).Remote(); got != want {
			t.Errorf("Remote() for %q = %v, want %v", m, got, want)
		}
	}
}
