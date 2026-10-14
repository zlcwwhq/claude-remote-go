// Package shell is the program Claude Code runs as $SHELL.
//
// Claude Code starts a fresh shell for every Bash tool call, as
// `$SHELL -c [-l] "<command> && pwd -P >| <cwd file>"`, and reads the cwd file
// afterwards to follow `cd`s. Depending on the resolved mode the command
// either runs on the remote box over ssh, in the directory that mirrors the
// local one, or locally through bash.
package shell

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/langwatch/claude-remote/internal/config"
	"github.com/langwatch/claude-remote/internal/mode"
	"github.com/langwatch/claude-remote/internal/mutagen"
	"github.com/langwatch/claude-remote/internal/notify"
	"github.com/langwatch/claude-remote/internal/remote"
)

// SyncRootEnv carries the directory the launcher synced, so commands run from
// a subdirectory still flush the right sync session.
const SyncRootEnv = "CLAUDE_REMOTE_SYNC_ROOT"

type shell struct {
	cfg        *config.Config
	stateDir   string
	ssh        *remote.SSH
	forceLocal bool
}

// Run is the $SHELL entry point; args excludes argv[0]. Local execution
// replaces the process with bash, so Run only returns for remote commands
// and errors.
func Run(args []string) int {
	cfg, err := config.Load()
	if err == nil {
		err = cfg.Validate()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	stateDir := config.StateDir()
	res := mode.Resolve(mode.InputsFromEnv(stateDir, cfg.ModeSeed))
	if res.Warning != "" {
		fmt.Fprintln(os.Stderr, res.Warning)
	}
	if !res.Remote() && res.Mode != mode.Off {
		fmt.Fprintf(os.Stderr, "[claude-remote] WARNING: unrecognized mode '%s' — defaulting to local (off)\n", res.Mode)
	}

	sh := &shell{
		cfg:        cfg,
		stateDir:   stateDir,
		ssh:        remote.New(mode.ResolveURL(stateDir, cfg.RemoteHost)),
		forceLocal: !res.Remote(),
	}
	cmd, ok := parseArgs(args)
	if !ok {
		return sh.interactive()
	}
	if !sh.forceLocal && sh.ssh.Available() {
		return sh.runRemote(cmd)
	}
	sh.announceLocal("Remote unavailable - using local execution")
	return execLocal("-c", cmd)
}

// parseArgs skips the -c/-l/-i flags Claude Code passes and returns the
// command, if any.
func parseArgs(args []string) (string, bool) {
	for _, a := range args {
		if a == "--login" || (len(a) > 1 && a[0] == '-' && strings.Trim(a[1:], "cli") == "") {
			continue
		}
		return a, true
	}
	return "", false
}

var pwdSuffix = regexp.MustCompile(`(?s)^(.*)&& pwd -P >\| ('[^']*'|"[^"]*"|[^\s'"]+)\s*$`)

// splitPwdSuffix separates Claude Code's trailing `&& pwd -P >| <file>` from
// the command.
func splitPwdSuffix(cmd string) (string, string) {
	m := pwdSuffix.FindStringSubmatch(cmd)
	if m == nil {
		return cmd, ""
	}
	file := m[2]
	if file[0] == '\'' || file[0] == '"' {
		file = file[1 : len(file)-1]
	}
	return m[1], file
}

func (sh *shell) runRemote(cmdline string) int {
	cmdline, pwdFile := splitPwdSuffix(cmdline)
	localCwd, err := PhysicalCwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[claude-remote] %v\n", err)
		return 1
	}
	sh.notify("Remote instance available", notify.Online)

	m := mutagen.New()
	session := mutagen.SessionName(syncRoot(localCwd))
	flush := func() {
		if m.Installed() {
			_ = m.Flush(session)
		}
	}

	gitRoot := ""
	if !sh.cfg.PathIdentity {
		gitRoot = strings.TrimRight(sh.cfg.MirrorRoot, "/")
	}
	marker := newMarker()
	script := remoteScript(cmdline, sh.cfg.LocalToRemote(localCwd), marker, gitRoot)

	flush()
	out := newTrailerFilter(os.Stdout, marker)
	c := sh.ssh.Command(context.Background(), script)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, out, os.Stderr
	code := runForwardingSignals(c)
	_ = out.Close()
	flush()

	// Like `cmd && pwd -P >| file`: only a successful command moves the cwd.
	if pwdFile != "" && code == 0 && out.found {
		_ = os.WriteFile(pwdFile, []byte(sh.cfg.RemoteToLocal(out.pwd)+"\n"), 0o644)
	}
	return code
}

// remoteScript is what the remote login shell runs: load the user's
// environment (with .bashrc's non-interactive early return disabled), enter
// the mirrored directory, then run the command in bash and print the trailer
// carrying its final directory. The command's exit code is preserved.
// gitMirrorRoot, when set, rewrites a worktree's absolute `gitdir:` pointer to
// its mirrored location.
func remoteScript(cmd, remoteCwd, marker, gitMirrorRoot string) string {
	q := remote.Quote
	inner := cmd + "\n__cr_ec=$?\nprintf '%s%s\\n' " + q(marker) + " \"$(pwd -P)\"\nexit \"$__cr_ec\""
	parts := []string{
		"source ~/.profile 2>/dev/null",
		"source <(sed 's/return;;/;;/' ~/.bashrc 2>/dev/null) 2>/dev/null",
		"cd -- " + q(remoteCwd) + " || { echo " +
			q("[claude-remote] remote directory "+remoteCwd+" does not exist — is sync running?") +
			" >&2; exit 1; }",
	}
	if gitMirrorRoot != "" {
		parts = append(parts, "{ if [ -f .git ] && grep -q 'gitdir:' .git && ! grep -qF "+
			q("gitdir: "+gitMirrorRoot)+" .git; then sed -i "+
			q("s|gitdir: /|gitdir: "+gitMirrorRoot+"/|")+" .git; fi; } 2>/dev/null")
	}
	parts = append(parts, "/bin/bash -c "+q(inner))
	return strings.Join(parts, "; ")
}

func newMarker() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "__CLAUDE_REMOTE_PWD_" + hex.EncodeToString(b) + "__"
}

// runForwardingSignals runs c, relaying interrupts to it, and returns its
// exit code (128+N when killed by signal N).
func runForwardingSignals(c *exec.Cmd) int {
	if err := c.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "[claude-remote] %v\n", err)
		return 255
	}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-sigs:
				_ = c.Process.Signal(s)
			case <-done:
				return
			}
		}
	}()
	_ = c.Wait()
	signal.Stop(sigs)
	close(done)

	ps := c.ProcessState
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ps.ExitCode()
}

func (sh *shell) interactive() int {
	if !sh.forceLocal && sh.ssh.Available() {
		sh.notify("Remote instance available", notify.Online)
		cwd, err := PhysicalCwd()
		if err != nil {
			cwd = "/"
		}
		cmd := "cd -- " + remote.Quote(sh.cfg.LocalToRemote(cwd)) + "; exec /bin/bash -l"
		return execve(sh.ssh.Bin, append([]string{"ssh"}, sh.ssh.Args([]string{"-t"}, cmd)...))
	}
	sh.announceLocal("Remote unavailable - using local shell")
	return execLocal("-l")
}

// announceLocal says loudly, on stderr, that a command runs locally, so the
// fallback is never silent.
func (sh *shell) announceLocal(offlineMessage string) {
	if sh.forceLocal {
		fmt.Fprintln(os.Stderr, "[claude-remote] LOCAL EXECUTION — MODE=off, running locally")
		return
	}
	sh.notify(offlineMessage, notify.Offline)
	host := sh.ssh.Host
	if host == "" {
		host = "<not set>"
	}
	fmt.Fprintf(os.Stderr, "[claude-remote] LOCAL FALLBACK — remote %s unreachable, running locally\n", host)
}

func (sh *shell) notify(message, state string) {
	notify.Send(filepath.Join(sh.stateDir, "notify-state"), message, state)
}

// execLocal replaces the process with bash.
func execLocal(args ...string) int {
	bash := "/bin/bash"
	if _, err := os.Stat(bash); err != nil {
		if p, err := exec.LookPath("bash"); err == nil {
			bash = p
		}
	}
	return execve(bash, append([]string{bash}, args...))
}

func execve(path string, argv []string) int {
	err := syscall.Exec(path, argv, os.Environ())
	fmt.Fprintf(os.Stderr, "[claude-remote] exec %s: %v\n", path, err)
	return 127
}

// syncRoot is the root of the sync session covering cwd: the launcher's
// directory when cwd is inside it, else cwd itself.
func syncRoot(cwd string) string {
	root := strings.TrimRight(os.Getenv(SyncRootEnv), "/")
	if root != "" && (cwd == root || strings.HasPrefix(cwd, root+"/")) {
		return root
	}
	return cwd
}

// PhysicalCwd is the working directory with symlinks resolved, like `pwd -P`.
func PhysicalCwd() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(wd)
}
