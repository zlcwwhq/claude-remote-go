package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/langwatch/claude-remote/internal/config"
	"github.com/langwatch/claude-remote/internal/mode"
	"github.com/langwatch/claude-remote/internal/mutagen"
	"github.com/langwatch/claude-remote/internal/remote"
)

// Commands is what setup links into ~/bin and what main dispatches on when
// invoked under one of these names.
var Commands = []string{
	"claude-remote",
	"sync-start",
	"sync-status",
	"sync-stop",
	"sync-reap",
	"remote-status",
	"paste-image-remote",
}

// Mode implements `claude-remote mode`: print how routing resolves for the
// current environment.
func Mode([]string) int {
	stateDir := config.StateDir()
	seed, host := os.Getenv("CLAUDE_REMOTE_MODE"), ""
	if cfg, err := config.Load(); err == nil {
		seed, host = cfg.ModeSeed, cfg.RemoteHost
	}
	printMode(stateDir, seed, host, "")
	return 0
}

func printMode(stateDir, seed, host, indent string) {
	res := mode.Resolve(mode.InputsFromEnv(stateDir, seed))
	if res.Warning != "" {
		fmt.Fprintln(os.Stderr, res.Warning)
	}
	route := "remote"
	if !res.Remote() {
		route = "local"
	}
	url := mode.ResolveURL(stateDir, host)
	if url == "" {
		url = "<not set>"
	}
	fmt.Printf("%smode:   %q (source: %s)\n", indent, res.Mode, res.Source)
	fmt.Printf("%sroute:  %s\n", indent, route)
	fmt.Printf("%surl:    %s\n", indent, url)
}

// RemoteStatus implements `remote-status`: a diagnostic overview.
func RemoteStatus([]string) int {
	fmt.Println("Claude Remote Status")
	fmt.Println("====================")
	fmt.Println()

	stateDir := config.StateDir()
	cfg, cfgErr := config.Load()
	fmt.Printf("Config (%s):\n", config.FilePath())
	if cfgErr != nil {
		fmt.Printf("  ERROR: %v\n", cfgErr)
		cfg = &config.Config{ModeSeed: os.Getenv("CLAUDE_REMOTE_MODE")}
	} else {
		fmt.Printf("  REMOTE_HOST:        %s\n", orNotSet(cfg.RemoteHost))
		fmt.Printf("  REMOTE_MIRROR_ROOT: %s\n", orNotSet(cfg.MirrorRoot))
		fmt.Printf("  PATH_IDENTITY:      %t\n", cfg.PathIdentity)
	}
	fmt.Printf("  State dir:          %s\n", stateDir)
	fmt.Println()

	fmt.Println("Mode:")
	printMode(stateDir, cfg.ModeSeed, cfg.RemoteHost, "  ")
	fmt.Println()

	exe, _ := executable()
	fmt.Println("Shell shim:")
	checkLink(filepath.Join(stateDir, "bin", ShimName), exe, "  run claude-remote once (or 'claude-remote setup') to create it")
	fmt.Println()

	fmt.Println("~/bin symlinks:")
	home, _ := os.UserHomeDir()
	for _, name := range Commands {
		checkLink(filepath.Join(home, "bin", name), exe, "")
	}
	fmt.Println()

	fmt.Println("SSH:")
	host := mode.ResolveURL(stateDir, cfg.RemoteHost)
	if host == "" {
		fmt.Println("  SKIP: REMOTE_HOST not configured")
	} else {
		ssh := remote.New(host)
		socket, state := ssh.ControlSocket(), "none"
		if ssh.MasterRunning() {
			state = "master running"
		} else if _, err := os.Lstat(socket); err == nil {
			state = "stale"
		}
		fmt.Printf("  Control socket: %s (%s)\n", socket, state)
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		out, err := exec.CommandContext(ctx, ssh.Bin, "-o", "ConnectTimeout=3", "-o", "BatchMode=yes", host, "hostname && uname -s").Output()
		cancel()
		if err != nil {
			fmt.Println("  Connection: FAILED")
		} else {
			fmt.Println("  Connection: OK")
			fmt.Printf("  Remote: %s\n", strings.Join(strings.Fields(string(out)), " "))
		}
	}
	fmt.Println()

	fmt.Println("Mutagen sync:")
	m := mutagen.New()
	switch out, err := m.List(); {
	case !m.Installed():
		fmt.Println("  SKIP: mutagen not installed")
	case err != nil || strings.TrimSpace(out) == "":
		fmt.Println("  No active claude-remote sync sessions")
	default:
		for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
			fmt.Println("  " + line)
		}
	}
	return 0
}

// checkLink reports whether link is a symlink resolving to want.
func checkLink(link, want, hint string) {
	target, err := os.Readlink(link)
	switch {
	case err != nil:
		if _, statErr := os.Lstat(link); statErr == nil {
			fmt.Printf("  WARN: %s is not a symlink\n", link)
			return
		}
		fmt.Printf("  MISSING: %s\n", link)
		if hint != "" {
			fmt.Println(hint)
		}
	case !exists(link):
		fmt.Printf("  BROKEN: %s -> %s\n", link, target)
	default:
		resolved, _ := filepath.EvalSymlinks(link)
		if resolved == want {
			fmt.Printf("  OK: %s -> %s\n", link, target)
		} else {
			fmt.Printf("  WARN: %s -> %s (points elsewhere)\n", link, target)
		}
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func orNotSet(s string) string {
	if s == "" {
		return "<not set>"
	}
	return s
}
