package shell

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseArgs(t *testing.T) {
	cases := []struct {
		args []string
		cmd  string
		ok   bool
	}{
		{[]string{"-c", "-l", "echo hi"}, "echo hi", true},
		{[]string{"-c", "echo hi"}, "echo hi", true},
		{[]string{"-lc", "echo hi"}, "echo hi", true},
		{[]string{"-i", "--login", "-c", "x"}, "x", true},
		{[]string{"-l"}, "", false},
		{nil, "", false},
		{[]string{"-c", "-x"}, "-x", true},
	}
	for _, c := range cases {
		cmd, ok := parseArgs(c.args)
		if cmd != c.cmd || ok != c.ok {
			t.Errorf("parseArgs(%q) = %q, %v; want %q, %v", c.args, cmd, ok, c.cmd, c.ok)
		}
	}
}

func TestSplitPwdSuffix(t *testing.T) {
	cases := []struct{ in, cmd, file string }{
		{
			"source /s.sh 2>/dev/null || true && eval 'ls' < /dev/null && pwd -P >| /tmp/claude-ab12-cwd",
			"source /s.sh 2>/dev/null || true && eval 'ls' < /dev/null ",
			"/tmp/claude-ab12-cwd",
		},
		{"echo HELLO && pwd -P >| '/tmp/with space/cwd'", "echo HELLO ", "/tmp/with space/cwd"},
		{"eval 'a\nb' && pwd -P >| /tmp/x-cwd\n", "eval 'a\nb' ", "/tmp/x-cwd"},
		{"echo no suffix", "echo no suffix", ""},
		{"pwd -P >| /tmp/x && echo later", "pwd -P >| /tmp/x && echo later", ""},
	}
	for _, c := range cases {
		cmd, file := splitPwdSuffix(c.in)
		if cmd != c.cmd || file != c.file {
			t.Errorf("splitPwdSuffix(%q) = %q, %q; want %q, %q", c.in, cmd, file, c.cmd, c.file)
		}
	}
}

func TestSyncRoot(t *testing.T) {
	t.Setenv(SyncRootEnv, "/work/proj/")
	for cwd, want := range map[string]string{
		"/work/proj":       "/work/proj",
		"/work/proj/sub/x": "/work/proj",
		"/work/project2":   "/work/project2",
		"/tmp":             "/tmp",
	} {
		if got := syncRoot(cwd); got != want {
			t.Errorf("syncRoot(%q) = %q, want %q", cwd, got, want)
		}
	}
	t.Setenv(SyncRootEnv, "")
	if got := syncRoot("/a"); got != "/a" {
		t.Errorf("without env: %q", got)
	}
}

// runScript executes remoteScript with a local bash standing in for the
// remote login shell.
func runScript(t *testing.T, cmd, cwd, gitRoot string) (stdout, stderr string, code int, f *trailerFilter) {
	t.Helper()
	marker := newMarker()
	var out, errb bytes.Buffer
	f = newTrailerFilter(&out, marker)
	c := exec.Command("bash", "-c", remoteScript(cmd, cwd, marker, gitRoot))
	c.Env = []string{"HOME=" + t.TempDir(), "PATH=" + os.Getenv("PATH")}
	c.Stdout, c.Stderr = f, &errb
	err := c.Run()
	_ = f.Close()
	code = 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), errb.String(), code, f
}

func TestRemoteScript(t *testing.T) {
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("/bin/bash not available")
	}
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("output, exit code and final directory", func(t *testing.T) {
		out, _, code, f := runScript(t, "printf 'it'\\''s here'; cd sub; exit 7", dir, "")
		if out != "it's here" {
			t.Errorf("stdout = %q", out)
		}
		if code != 7 {
			t.Errorf("exit code = %d, want 7", code)
		}
		// `exit` skips the trailer, as it skips `&& pwd -P` in a real shell.
		if f.found {
			t.Errorf("trailer found after explicit exit: %q", f.pwd)
		}
	})

	t.Run("cd is tracked", func(t *testing.T) {
		out, _, code, f := runScript(t, "cd sub && echo ok", dir, "")
		if out != "ok\n" || code != 0 {
			t.Errorf("stdout = %q, code = %d", out, code)
		}
		if !f.found || f.pwd != filepath.Join(dir, "sub") {
			t.Errorf("pwd = %q (found %v), want %s/sub", f.pwd, f.found, dir)
		}
	})

	t.Run("failing command keeps its exit code", func(t *testing.T) {
		_, _, code, f := runScript(t, "false", dir, "")
		if code != 1 || !f.found {
			t.Errorf("code = %d, found = %v", code, f.found)
		}
	})

	t.Run("missing remote directory fails instead of running elsewhere", func(t *testing.T) {
		out, stderr, code, _ := runScript(t, "echo SHOULD-NOT-RUN", filepath.Join(dir, "missing"), "")
		if code != 1 || strings.Contains(out, "SHOULD-NOT-RUN") {
			t.Errorf("code = %d, stdout = %q", code, out)
		}
		if !strings.Contains(stderr, "does not exist") {
			t.Errorf("stderr = %q", stderr)
		}
	})

	t.Run("worktree gitdir is rewritten once", func(t *testing.T) {
		if runtime.GOOS == "darwin" {
			t.Skip("the remote side relies on GNU sed -i")
		}
		gitFile := filepath.Join(dir, ".git")
		if err := os.WriteFile(gitFile, []byte("gitdir: /Users/a/repo/.git/worktrees/x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if _, _, code, _ := runScript(t, "true", dir, "/srv/mirror"); code != 0 {
				t.Fatalf("code = %d", code)
			}
		}
		got, _ := os.ReadFile(gitFile)
		if want := "gitdir: /srv/mirror/Users/a/repo/.git/worktrees/x\n"; string(got) != want {
			t.Errorf(".git = %q, want %q", got, want)
		}
	})
}
