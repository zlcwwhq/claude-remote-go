// Package cli implements claude-remote's commands.
package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/langwatch/claude-remote/internal/config"
	"github.com/langwatch/claude-remote/internal/shell"
)

// ShimName is the file name Claude Code sees as $SHELL. Claude Code only
// accepts shells it recognises by name, so the wrapper poses as zsh.
const ShimName = "zsh"

// Launch implements `claude-remote [claude args...]`: sync the current
// directory, then start Claude Code with the wrapper as its shell.
func Launch(args []string) int {
	cfg, err := loadConfig()
	if err != nil {
		return fail(err)
	}
	stateDir := config.StateDir()

	// Wraps, doesn't activate: a fresh install stays local until someone
	// turns offloading on.
	modeFile := filepath.Join(stateDir, "mode")
	if _, err := os.Stat(modeFile); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(stateDir, 0o755); err == nil {
			_ = os.WriteFile(modeFile, []byte("off\n"), 0o644)
		}
	}

	work, err := shell.PhysicalCwd()
	if err != nil {
		return fail(err)
	}
	_ = startSync(cfg, work, os.Stdout, os.Stderr)
	reapSync(os.Stdout)
	SessionReap(nil)

	shim, err := EnsureShim(stateDir)
	if err != nil {
		return fail(fmt.Errorf("installing shell shim: %w", err))
	}
	fmt.Println("Remote shell connection:")
	check := exec.Command(shim, "-c", "uname -a")
	check.Stdout, check.Stderr = os.Stdout, os.Stderr
	_ = check.Run()

	claude, err := exec.LookPath("claude")
	if err != nil {
		return fail(errors.New("claude not found in PATH. Install Claude Code first"))
	}
	env := setEnv(os.Environ(), "SHELL", shim)
	env = setEnv(env, shell.SyncRootEnv, work)
	argv := append([]string{"claude", "--dangerously-skip-permissions"}, args...)
	err = syscall.Exec(claude, argv, env)
	return fail(fmt.Errorf("exec %s: %w", claude, err))
}

// EnsureShim points <state>/bin/zsh at this executable and returns its path.
func EnsureShim(stateDir string) (string, error) {
	exe, err := executable()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(stateDir, "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	link := filepath.Join(dir, ShimName)
	if cur, err := os.Readlink(link); err == nil && cur == exe {
		return link, nil
	}
	// Swap atomically so a Claude session using the old link never sees it missing.
	tmp := fmt.Sprintf("%s.tmp%d", link, os.Getpid())
	_ = os.Remove(tmp)
	if err := os.Symlink(exe, tmp); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, link); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return link, nil
}

// executable is the resolved path of the running binary.
func executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

func setEnv(env []string, key, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, key+"=") {
			out = append(out, kv)
		}
	}
	return append(out, key+"="+value)
}
