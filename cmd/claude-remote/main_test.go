package main

// End-to-end tests against the built binary. Nothing touches the network:
// ssh is a stub that answers control/probe calls from environment variables
// and runs every other command locally, as if the temp directory were the
// remote box. mutagen, claude and the notification/clipboard tools are stubs
// that log their arguments.
//
// The mode-routing cases are a port of the original bash suite
// (tests/toggle.bats); their names keep its AC/PS-AC numbering.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/langwatch/claude-remote/internal/mutagen"
)

const (
	sentinel = "__REMOTE_STUB_CALLED__"
	sidA     = "a1b2c3d4-e5f6-7890-abcd-ef1234567890"
	sidB     = "b2c3d4e5-f6a7-8901-bcde-f12345678901"
)

var binary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "claude-remote-bin")
	if err != nil {
		panic(err)
	}
	binary = filepath.Join(dir, "claude-remote")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "go build: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

var stubs = map[string]string{
	"ssh": `#!/usr/bin/env bash
printf '[%s] ' "$@" >> "$STUB_LOG_DIR/ssh.log"; echo >> "$STUB_LOG_DIR/ssh.log"
for a; do [[ "$a" == "-O" ]] && { [[ "$*" == *"-O exit"* ]] && exit 0; exit "${STUB_MASTER_EXIT:-0}"; }; done
last="${!#}"
if [[ "$last" == "exit 0" ]]; then
    [[ "$*" == *"ControlPath=none"* ]] && exit "${STUB_PLAIN_PROBE_EXIT:-0}"
    exit "${STUB_MUX_PROBE_EXIT:-0}"
fi
echo "` + sentinel + `"
exec bash -c "$last"
`,
	"mutagen": `#!/usr/bin/env bash
printf '[%s] ' "$@" >> "$STUB_LOG_DIR/mutagen.log"; echo >> "$STUB_LOG_DIR/mutagen.log"
if [[ "$1 $2" == "sync list" ]]; then
    [[ -n "$3" && "$3" != --* ]] && exit "${MUTAGEN_EXISTS_EXIT:-0}"
    [[ -n "$MUTAGEN_LIST_FILE" ]] && cat "$MUTAGEN_LIST_FILE"
fi
exit 0
`,
	"claude": `#!/usr/bin/env bash
echo "CLAUDE ARGS: $*"
echo "CLAUDE SHELL: $SHELL"
echo "CLAUDE SYNC ROOT: $CLAUDE_REMOTE_SYNC_ROOT"
`,
	"xclip": `#!/usr/bin/env bash
if [[ "$*" == *-out* ]]; then printf 'PNGDATA'; else cat > "$STUB_LOG_DIR/clipboard"; fi
`,
	"notify-send": `#!/usr/bin/env bash
echo "$*" >> "$STUB_LOG_DIR/notify.log"
`,
	"osascript": `#!/usr/bin/env bash
echo "$*" >> "$STUB_LOG_DIR/notify.log"
`,
}

type harness struct {
	t                               *testing.T
	root, state, home, work, mirror string
	stubBin, logs, configPath, cwd  string
	shim                            string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{
		t:          t,
		root:       root,
		state:      filepath.Join(root, "state"),
		home:       filepath.Join(root, "home"),
		work:       filepath.Join(root, "work"),
		mirror:     filepath.Join(root, "mirror"),
		stubBin:    filepath.Join(root, "stubs"),
		logs:       filepath.Join(root, "logs"),
		configPath: filepath.Join(root, "config.sh"),
	}
	h.cwd = h.work
	for _, d := range []string{h.state, h.home, h.work, h.stubBin, h.logs, h.mirror + h.work} {
		h.mkdir(d)
	}
	for name, body := range stubs {
		h.write(filepath.Join(h.stubBin, name), body)
		if err := os.Chmod(filepath.Join(h.stubBin, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	h.config(fmt.Sprintf("REMOTE_HOST=\"stubhost\"\nREMOTE_MIRROR_ROOT=%q\nPATH_IDENTITY=\"false\"\n", h.mirror))
	h.shim = filepath.Join(root, "shim", "zsh")
	h.mkdir(filepath.Dir(h.shim))
	if err := os.Symlink(binary, h.shim); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) mkdir(dir string) {
	h.t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) write(path, content string) {
	h.t.Helper()
	h.mkdir(filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) config(content string) { h.write(h.configPath, content) }
func (h *harness) global(mode string)    { h.write(filepath.Join(h.state, "mode"), mode) }
func (h *harness) log(name string) string {
	data, _ := os.ReadFile(filepath.Join(h.logs, name+".log"))
	return string(data)
}

type result struct {
	stdout, stderr string
	code           int
}

func (r result) remote() bool { return strings.Contains(r.stdout, sentinel) }

// exec runs path with a clean environment (no CLAUDE_* leaking in from the
// developer's own session) plus env.
func (h *harness) exec(stdin string, env []string, path string, args ...string) result {
	h.t.Helper()
	cmd := exec.Command(path, args...)
	cmd.Dir = h.cwd
	cmd.Env = append([]string{
		"PATH=" + h.stubBin + ":" + os.Getenv("PATH"),
		"HOME=" + h.home,
		"CLAUDE_REMOTE_STATE_DIR=" + h.state,
		"CLAUDE_REMOTE_CONFIG=" + h.configPath,
		"CLAUDE_REMOTE_SSH=" + filepath.Join(h.stubBin, "ssh"),
		"STUB_LOG_DIR=" + h.logs,
	}, env...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	r := result{stdout: out.String(), stderr: errb.String()}
	if ee, ok := err.(*exec.ExitError); ok {
		r.code = ee.ExitCode()
	} else if err != nil {
		h.t.Fatal(err)
	}
	return r
}

// shell runs a command the way Claude Code does: `$SHELL -c -l <cmd>`.
func (h *harness) shell(cmd string, env ...string) result {
	h.t.Helper()
	return h.exec("", env, h.shim, "-c", "-l", cmd)
}

func (h *harness) cli(args ...string) result {
	h.t.Helper()
	return h.exec("", nil, binary, args...)
}

func TestModeRouting(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(h *harness)
		env    []string
		remote bool
		warn   []string // nil: stderr must not mention WARNING
	}{
		{name: "AC1a global off routes locally", setup: func(h *harness) { h.global("off\n") }},
		{name: "AC1b global on routes remotely", setup: func(h *harness) { h.global("on\n") }, remote: true},
		{
			name: "AC2a project file beats global",
			setup: func(h *harness) {
				h.global("on\n")
				h.write(filepath.Join(h.root, "project", ".claude-remote-mode"), "off\n")
			},
			env: []string{"CLAUDE_PROJECT_DIR=$ROOT/project"},
		},
		{name: "AC2b global beats env seed", setup: func(h *harness) { h.global("off\n") }, env: []string{"CLAUDE_REMOTE_MODE=on"}},
		{name: "AC2c env seed beats default", setup: func(*harness) {}, env: []string{"CLAUDE_REMOTE_MODE=off"}},
		{name: "AC2d default is on", setup: func(*harness) {}, remote: true},
		{
			name: "config file seed beats env seed",
			setup: func(h *harness) {
				h.config(fmt.Sprintf("REMOTE_HOST=stubhost\nREMOTE_MIRROR_ROOT=%q\nCLAUDE_REMOTE_MODE=off\n", h.mirror))
			},
			env: []string{"CLAUDE_REMOTE_MODE=on"},
		},
		{name: "AC9a 'onn' is local with a warning", setup: func(h *harness) { h.global("onn\n") }, warn: []string{"WARNING", "unrecognized mode"}},
		{name: "AC9b whitespace-only is local with a warning", setup: func(h *harness) { h.global("   \n") }, warn: []string{"WARNING"}},
		{name: "AC9c 'yes' is local with a warning", setup: func(h *harness) { h.global("yes\n") }, warn: []string{"WARNING"}},
		{name: "AC9d empty is local with a warning", setup: func(h *harness) { h.global("") }, warn: []string{"WARNING"}},
		{name: "Trim CRLF", setup: func(h *harness) { h.global("on\r\n") }, remote: true},
		{name: "Trim TAB", setup: func(h *harness) { h.global("on\t\n") }, remote: true},
		{
			name:   "AC11a empty url file keeps REMOTE_HOST",
			setup:  func(h *harness) { h.global("on\n"); h.write(filepath.Join(h.state, "url"), "") },
			remote: true,
		},
		{
			name:   "AC11b whitespace url file keeps REMOTE_HOST",
			setup:  func(h *harness) { h.global("on\n"); h.write(filepath.Join(h.state, "url"), "   \n") },
			remote: true,
		},
		{
			name: "PS-AC1 session beats project",
			setup: func(h *harness) {
				h.write(filepath.Join(h.root, "project", ".claude-remote-mode"), "on\n")
				h.write(filepath.Join(h.state, "session", sidA), "off\n")
			},
			env: []string{"CLAUDE_CODE_SESSION_ID=" + sidA, "CLAUDE_PROJECT_DIR=$ROOT/project"},
		},
		{
			name: "PS-AC3 no session file keeps old precedence",
			setup: func(h *harness) {
				h.global("on\n")
				h.write(filepath.Join(h.root, "project", ".claude-remote-mode"), "off\n")
				h.mkdir(filepath.Join(h.state, "session"))
			},
			env: []string{"CLAUDE_CODE_SESSION_ID=" + sidA, "CLAUDE_PROJECT_DIR=$ROOT/project"},
		},
		{name: "PS-AC7a unset session id is silent", setup: func(h *harness) { h.global("on\n") }, remote: true},
		{
			name:   "PS-AC7b empty session id is silent",
			setup:  func(h *harness) { h.global("on\n") },
			env:    []string{"CLAUDE_CODE_SESSION_ID="},
			remote: true,
		},
		{
			name:  "PS-AC7c traversal id is blocked",
			setup: func(h *harness) { h.global("off\n"); h.mkdir(filepath.Join(h.state, "session")) },
			env:   []string{"CLAUDE_CODE_SESSION_ID=../mode"},
			warn:  []string{"WARNING", "../mode"},
		},
		{
			name:   "PS-AC7d slash id is blocked",
			setup:  func(h *harness) { h.global("on\n") },
			env:    []string{"CLAUDE_CODE_SESSION_ID=a/b"},
			remote: true,
			warn:   []string{"WARNING", "a/b"},
		},
		{
			name:   "PS-AC7e valid id without a file is silent",
			setup:  func(h *harness) { h.global("on\n"); h.mkdir(filepath.Join(h.state, "session")) },
			env:    []string{"CLAUDE_CODE_SESSION_ID=" + sidA},
			remote: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			c.setup(h)
			env := make([]string, len(c.env))
			for i, e := range c.env {
				env[i] = strings.ReplaceAll(e, "$ROOT", h.root)
			}
			r := h.shell("echo HELLO", env...)
			if r.code != 0 || !strings.Contains(r.stdout, "HELLO") {
				t.Fatalf("code %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
			}
			if r.remote() != c.remote {
				t.Errorf("remote = %v, want %v (stderr %q)", r.remote(), c.remote, r.stderr)
			}
			if !c.remote && !strings.Contains(r.stderr, "[claude-remote] LOCAL EXECUTION") {
				t.Errorf("local run not announced on stderr: %q", r.stderr)
			}
			if c.warn == nil && strings.Contains(r.stderr, "WARNING") {
				t.Errorf("unexpected warning: %q", r.stderr)
			}
			for _, w := range c.warn {
				if !strings.Contains(r.stderr, w) {
					t.Errorf("stderr %q does not contain %q", r.stderr, w)
				}
			}
		})
	}
}

func TestPSAC2SessionsAreIndependent(t *testing.T) {
	h := newHarness(t)
	h.global("off\n")
	h.write(filepath.Join(h.state, "session", sidA), "on\n")
	h.write(filepath.Join(h.state, "session", sidB), "off\n")
	if r := h.shell("echo A", "CLAUDE_CODE_SESSION_ID="+sidA); !r.remote() {
		t.Errorf("session A ran locally: %+v", r)
	}
	if r := h.shell("echo B", "CLAUDE_CODE_SESSION_ID="+sidB); r.remote() {
		t.Errorf("session B ran remotely: %+v", r)
	}
}

func TestURLFileOverridesHost(t *testing.T) {
	h := newHarness(t)
	h.global("on\n")
	h.write(filepath.Join(h.state, "url"), " ubuntu@10.0.3.56\n")
	if r := h.shell("true"); !r.remote() {
		t.Fatalf("ran locally: %+v", r)
	}
	if log := h.log("ssh"); !strings.Contains(log, "[ubuntu@10.0.3.56]") || strings.Contains(log, "[stubhost]") {
		t.Errorf("ssh calls did not use the url file host:\n%s", log)
	}
}

func TestAC10LocalWritesPwdFile(t *testing.T) {
	h := newHarness(t)
	h.global("off\n")
	pwdFile := filepath.Join(h.root, "pwd_out")
	r := h.shell("echo HELLO && pwd -P >| " + pwdFile)
	got, err := os.ReadFile(pwdFile)
	if err != nil || string(got) != h.work+"\n" {
		t.Errorf("pwd file = %q, %v; want %q (run: %+v)", got, err, h.work+"\n", r)
	}
}

func TestRemoteExecution(t *testing.T) {
	h := newHarness(t)
	h.global("on\n")
	h.mkdir(filepath.Join(h.mirror+h.work, "sub"))

	t.Run("output is passed through exactly", func(t *testing.T) {
		r := h.shell("printf 'no newline'; echo to-stderr >&2")
		if r.stdout != sentinel+"\nno newline" {
			t.Errorf("stdout = %q", r.stdout)
		}
		if !strings.Contains(r.stderr, "to-stderr") {
			t.Errorf("stderr = %q", r.stderr)
		}
	})

	t.Run("runs in the mirrored directory", func(t *testing.T) {
		r := h.shell("pwd -P")
		if want := sentinel + "\n" + h.mirror + h.work + "\n"; r.stdout != want {
			t.Errorf("stdout = %q, want %q", r.stdout, want)
		}
	})

	t.Run("exit code is preserved", func(t *testing.T) {
		if r := h.shell("exit 3"); r.code != 3 {
			t.Errorf("code = %d, want 3", r.code)
		}
	})

	t.Run("cd is mapped back into the pwd file", func(t *testing.T) {
		pwdFile := filepath.Join(h.root, "cwd-ok")
		r := h.shell("cd sub && pwd -P >| " + pwdFile)
		got, err := os.ReadFile(pwdFile)
		if err != nil || string(got) != filepath.Join(h.work, "sub")+"\n" {
			t.Errorf("pwd file = %q, %v (run: %+v)", got, err, r)
		}
	})

	t.Run("failed command leaves no pwd file", func(t *testing.T) {
		pwdFile := filepath.Join(h.root, "cwd-fail")
		if r := h.shell("false && pwd -P >| " + pwdFile); r.code != 1 {
			t.Errorf("code = %d, want 1", r.code)
		}
		if _, err := os.Stat(pwdFile); err == nil {
			t.Error("pwd file written for a failed command")
		}
	})

	t.Run("sync is flushed before and after", func(t *testing.T) {
		before := strings.Count(h.log("mutagen"), "[flush]")
		h.shell("true")
		want := "[sync] [flush] [" + mutagen.SessionName(h.work) + "]"
		if n := strings.Count(h.log("mutagen"), want) - before; n < 2 {
			t.Errorf("expected 2 flushes of %s, log:\n%s", want, h.log("mutagen"))
		}
	})
}

func TestFlushUsesLauncherSyncRoot(t *testing.T) {
	h := newHarness(t)
	h.global("on\n")
	h.cwd = filepath.Join(h.work, "sub")
	h.mkdir(h.cwd)
	h.mkdir(h.mirror + h.cwd)
	if r := h.shell("true", "CLAUDE_REMOTE_SYNC_ROOT="+h.work); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if want := "[flush] [" + mutagen.SessionName(h.work) + "]"; !strings.Contains(h.log("mutagen"), want) {
		t.Errorf("no %s in mutagen log:\n%s", want, h.log("mutagen"))
	}
}

func TestPathIdentity(t *testing.T) {
	h := newHarness(t)
	h.global("on\n")
	h.config("REMOTE_HOST=stubhost\nPATH_IDENTITY=true\n")
	r := h.shell("pwd -P")
	if want := sentinel + "\n" + h.work + "\n"; r.stdout != want {
		t.Errorf("stdout = %q, want %q", r.stdout, want)
	}
}

func TestMissingRemoteDirectoryFails(t *testing.T) {
	h := newHarness(t)
	h.global("on\n")
	h.cwd = filepath.Join(h.work, "local-only")
	h.mkdir(h.cwd)
	r := h.shell("echo SHOULD-NOT-RUN")
	if r.code != 1 || strings.Contains(r.stdout, "SHOULD-NOT-RUN") || !strings.Contains(r.stderr, "does not exist") {
		t.Errorf("%+v", r)
	}
}

func TestUnreachableRemoteFallsBack(t *testing.T) {
	h := newHarness(t)
	h.global("on\n")
	r := h.shell("echo HELLO", "STUB_MASTER_EXIT=255", "STUB_PLAIN_PROBE_EXIT=255")
	if r.remote() || !strings.Contains(r.stdout, "HELLO") {
		t.Errorf("did not run locally: %+v", r)
	}
	if !strings.Contains(r.stderr, "LOCAL FALLBACK — remote stubhost unreachable") {
		t.Errorf("stderr = %q", r.stderr)
	}
	if !strings.Contains(h.log("notify"), "Remote unavailable") {
		t.Errorf("no offline notification, log: %q", h.log("notify"))
	}
	// Still offline right after: no second notification within the cooldown.
	h.shell("true", "STUB_MASTER_EXIT=255", "STUB_PLAIN_PROBE_EXIT=255")
	if n := strings.Count(h.log("notify"), "Remote unavailable"); n != 1 {
		t.Errorf("%d offline notifications, want 1", n)
	}
	// Back online: announced once.
	h.shell("true")
	if !strings.Contains(h.log("notify"), "Remote instance available") {
		t.Errorf("no online notification, log: %q", h.log("notify"))
	}
}

func TestWedgedMasterIsReplaced(t *testing.T) {
	h := newHarness(t)
	h.global("on\n")
	r := h.shell("true", "STUB_MASTER_EXIT=0", "STUB_MUX_PROBE_EXIT=255")
	if !r.remote() {
		t.Fatalf("fell back to local: %+v", r)
	}
	if !strings.Contains(h.log("ssh"), "[-O] [exit]") {
		t.Errorf("wedged master not shut down:\n%s", h.log("ssh"))
	}
}

func TestLocalExitCode(t *testing.T) {
	h := newHarness(t)
	h.global("off\n")
	if r := h.shell("exit 3"); r.code != 3 {
		t.Errorf("code = %d, want 3", r.code)
	}
}

func TestInteractiveShell(t *testing.T) {
	h := newHarness(t)
	h.global("off\n")
	r := h.exec("echo INTERACTIVE; exit 4\n", nil, h.shim)
	if r.code != 4 || !strings.Contains(r.stdout, "INTERACTIVE") || r.remote() {
		t.Errorf("local: %+v", r)
	}
	h.global("on\n")
	r = h.exec("pwd -P; exit 5\n", nil, h.shim)
	if r.code != 5 || !r.remote() || !strings.Contains(r.stdout, h.mirror+h.work) {
		t.Errorf("remote: %+v", r)
	}
}

func TestConfigErrors(t *testing.T) {
	h := newHarness(t)
	h.configPath = filepath.Join(h.root, "missing.sh")
	if r := h.shell("true"); r.code != 1 || !strings.Contains(r.stderr, "claude-remote setup") {
		t.Errorf("missing config: %+v", r)
	}
	h.configPath = filepath.Join(h.root, "nomirror.sh")
	h.config("REMOTE_HOST=stubhost\n")
	if r := h.shell("true"); r.code != 1 || !strings.Contains(r.stderr, "REMOTE_MIRROR_ROOT") {
		t.Errorf("missing mirror root: %+v", r)
	}
}

func TestSyncStart(t *testing.T) {
	h := newHarness(t)
	target := filepath.Join(h.work, "newproj")
	h.mkdir(target)

	r := h.exec("", []string{"MUTAGEN_EXISTS_EXIT=1"}, binary, "sync-start", target)
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if fi, err := os.Stat(h.mirror + target); err != nil || !fi.IsDir() {
		t.Errorf("remote directory %s not created", h.mirror+target)
	}
	name := mutagen.SessionName(target)
	create := fmt.Sprintf("[sync] [create] [%s] [stubhost:%s] [--name=%s] [--label=name=claude-remote]", target, h.mirror+target, name)
	log := h.log("mutagen")
	for _, want := range []string{create, "[--ignore=node_modules]", "[--sync-mode=two-way-resolved]", "[sync] [flush] [" + name + "]"} {
		if !strings.Contains(log, want) {
			t.Errorf("mutagen log lacks %s:\n%s", want, log)
		}
	}
	if !strings.Contains(r.stdout, "✓ "+name+" created") || !strings.Contains(r.stdout, "✓ Sync ready") {
		t.Errorf("stdout = %q", r.stdout)
	}

	if r := h.cli("sync-start", target); !strings.Contains(r.stdout, "already running") {
		t.Errorf("existing session: %+v", r)
	}
}

func TestSyncReap(t *testing.T) {
	h := newHarness(t)
	list := fmt.Sprintf(`Name: claude-remote-healthy
Alpha:
	URL: %[1]s
Beta:
	URL: stubhost:/m
Status: Watching for changes
Name: claude-remote-halted
Alpha:
	URL: %[1]s
Status: Halted on root emptied
Name: claude-remote-gone
Alpha:
	URL: %[1]s/deleted
Status: Watching for changes
`, h.work)
	listFile := filepath.Join(h.root, "list.txt")
	h.write(listFile, list)
	// A stale session file that the second pass removes.
	stale := filepath.Join(h.state, "session", sidA)
	h.write(stale, "on\n")
	if err := os.Chtimes(stale, timeAgo(), timeAgo()); err != nil {
		t.Fatal(err)
	}

	r := h.exec("", []string{"MUTAGEN_LIST_FILE=" + listFile}, binary, "sync-reap")
	log := h.log("mutagen")
	for _, want := range []string{"[terminate] [claude-remote-halted]", "[terminate] [claude-remote-gone]"} {
		if !strings.Contains(log, want) {
			t.Errorf("mutagen log lacks %s:\n%s", want, log)
		}
	}
	if strings.Contains(log, "[terminate] [claude-remote-healthy]") {
		t.Error("healthy session terminated")
	}
	if !strings.Contains(r.stdout, "reaped 2 session(s)") || !strings.Contains(r.stdout, "reaped 1 session file(s)") {
		t.Errorf("stdout = %q", r.stdout)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("stale session file survived")
	}
}

func TestLaunch(t *testing.T) {
	h := newHarness(t)
	r := h.exec("", []string{"MUTAGEN_EXISTS_EXIT=0"}, binary, "--resume", "abc")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	shim := filepath.Join(h.state, "bin", "zsh")
	for _, want := range []string{
		"Remote shell connection:",
		"CLAUDE ARGS: --dangerously-skip-permissions --resume abc",
		"CLAUDE SHELL: " + shim,
		"CLAUDE SYNC ROOT: " + h.work,
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, r.stdout)
		}
	}
	// Wraps, doesn't activate: the first launch seeds mode=off.
	if got, _ := os.ReadFile(filepath.Join(h.state, "mode")); string(got) != "off\n" {
		t.Errorf("mode file = %q", got)
	}
	target, _ := filepath.EvalSymlinks(shim)
	if want, _ := filepath.EvalSymlinks(binary); target != want {
		t.Errorf("shim -> %q, want %q", target, want)
	}

	// An argument that names a subcommand reaches claude through `launch`.
	if r := h.cli("launch", "setup"); !strings.Contains(r.stdout, "CLAUDE ARGS: --dangerously-skip-permissions setup") {
		t.Errorf("launch setup: %+v", r)
	}
}

func TestSetup(t *testing.T) {
	h := newHarness(t)
	h.configPath = filepath.Join(h.state, "config.sh")
	r := h.exec("myhost\n\nn\n", []string{"CLAUDE_REMOTE_CONFIG="}, binary, "setup")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	got, _ := os.ReadFile(h.configPath)
	for _, want := range []string{`REMOTE_HOST="myhost"`, `REMOTE_MIRROR_ROOT="/home/ubuntu/claude-remote-mirror"`, `PATH_IDENTITY="false"`} {
		if !strings.Contains(string(got), want) {
			t.Errorf("config lacks %s:\n%s", want, got)
		}
	}
	for _, link := range []string{
		filepath.Join(h.home, "bin", "sync-start"),
		filepath.Join(h.home, "bin", "claude-remote"),
		filepath.Join(h.state, "bin", "zsh"),
	} {
		if _, err := os.Readlink(link); err != nil {
			t.Errorf("%s: %v", link, err)
		}
	}
	if !strings.Contains(r.stdout, "✓ SSH connection works") {
		t.Errorf("stdout = %s", r.stdout)
	}

	// Invoked through a ~/bin link, the binary runs that command.
	if r := h.exec("", nil, filepath.Join(h.home, "bin", "sync-status")); r.code != 0 {
		t.Errorf("sync-status via link: %+v", r)
	}

	// Re-running keeps the existing config.
	if r := h.exec("", []string{"CLAUDE_REMOTE_CONFIG="}, binary, "setup"); !strings.Contains(r.stdout, "already exists") {
		t.Errorf("second setup: %s", r.stdout)
	}
}

func TestModeCommand(t *testing.T) {
	h := newHarness(t)
	h.global("on\n")
	r := h.cli("mode")
	if !strings.Contains(r.stdout, `mode:   "on" (source: global)`) || !strings.Contains(r.stdout, "route:  remote") ||
		!strings.Contains(r.stdout, "url:    stubhost") {
		t.Errorf("stdout = %q", r.stdout)
	}
}

func TestRemoteStatus(t *testing.T) {
	h := newHarness(t)
	r := h.cli("remote-status")
	if r.code != 0 || !strings.Contains(r.stdout, "Connection: OK") || !strings.Contains(r.stdout, "REMOTE_HOST:        stubhost") {
		t.Errorf("%+v", r)
	}
}

func TestPasteImage(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("uses the Linux clipboard tools")
	}
	h := newHarness(t)
	r := h.cli("paste-image-remote")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	clip, err := os.ReadFile(filepath.Join(h.logs, "clipboard"))
	if err != nil {
		t.Fatal(err)
	}
	remotePath := string(clip)
	if !strings.HasPrefix(remotePath, filepath.Join(h.home, ".cache", "tmux-paste-image", "clipboard_")) {
		t.Errorf("remote path %q not in the remote home's cache", remotePath)
	}
	if img, err := os.ReadFile(remotePath); err != nil || string(img) != "PNGDATA" {
		t.Errorf("uploaded image = %q, %v", img, err)
	}
}

func timeAgo() time.Time {
	return time.Now().Add(-2 * time.Minute)
}
