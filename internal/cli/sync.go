package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/langwatch/claude-remote/internal/config"
	"github.com/langwatch/claude-remote/internal/mutagen"
	"github.com/langwatch/claude-remote/internal/reap"
	"github.com/langwatch/claude-remote/internal/remote"
	"github.com/langwatch/claude-remote/internal/shell"
)

// SyncStart implements `sync-start [path]`.
func SyncStart(args []string) int {
	cfg, err := loadConfig()
	if err != nil {
		return fail(err)
	}
	target, err := resolveDir(args)
	if err != nil {
		return fail(err)
	}
	if err := startSync(cfg, target, os.Stdout, os.Stderr); err != nil {
		return 1
	}
	return 0
}

func resolveDir(args []string) (string, error) {
	if len(args) == 0 {
		return shell.PhysicalCwd()
	}
	abs, err := filepath.Abs(args[0])
	if err == nil {
		abs, err = filepath.EvalSymlinks(abs)
	}
	if err == nil {
		var fi os.FileInfo
		if fi, err = os.Stat(abs); err == nil && !fi.IsDir() {
			err = errors.New("not a directory")
		}
	}
	if err != nil {
		return "", fmt.Errorf("could not resolve directory: %s", args[0])
	}
	return abs, nil
}

// startSync makes sure a sync session for target exists and is flushed.
func startSync(cfg *config.Config, target string, stdout, stderr io.Writer) error {
	m := mutagen.New()
	if !m.Installed() {
		fmt.Fprintln(stderr, "✗ mutagen not found. Install it from https://mutagen.io/")
		return errors.New("mutagen not installed")
	}
	_ = m.DaemonStart()

	name := mutagen.SessionName(target)
	remotePath := cfg.LocalToRemote(target)
	if m.Exists(name) {
		fmt.Fprintf(stdout, "✓ Sync '%s' already running\n", name)
	} else if err := createSync(cfg, m, name, target, remotePath, stdout, stderr); err != nil {
		return err
	}

	fmt.Fprintln(stdout, "Waiting for sync...")
	if err := m.Flush(name); err != nil {
		fmt.Fprintf(stderr, "✗ Sync flush failed: %v\n", err)
		return err
	}
	fmt.Fprintln(stdout, "✓ Sync ready")
	return nil
}

func createSync(cfg *config.Config, m *mutagen.CLI, name, local, remotePath string, stdout, stderr io.Writer) error {
	fmt.Fprintf(stdout, "Creating sync: %s (%s -> %s)...\n", name, local, remotePath)
	ssh := remote.New(cfg.RemoteHost)
	q := remote.Quote(remotePath)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	mkdir := "mkdir -p " + q
	if cfg.PathIdentity {
		// The identity path (e.g. /Users/...) is usually not writable by the
		// ssh user, so create it with sudo and hand it over.
		mkdir = "sudo mkdir -p " + q + " && sudo chown \"$(id -un):$(id -gn)\" " + q
	}
	_ = ssh.Run(ctx, mkdir, nil, os.Stderr)

	// Fail loudly: a session whose remote side is missing would "succeed"
	// while syncing nothing.
	if ssh.Run(ctx, "test -d "+q, nil, nil) != nil {
		fmt.Fprintf(stderr, "✗ remote path '%s' could not be created on %s\n", remotePath, cfg.RemoteHost)
		fmt.Fprintln(stderr, "  (PATH_IDENTITY mode needs the identity root to exist and be writable; re-run 'claude-remote setup' or:")
		fmt.Fprintf(stderr, "   ssh %s 'sudo mkdir -p %s && sudo chown <user> %s')\n", cfg.RemoteHost, remotePath, remotePath)
		return errors.New("remote path missing")
	}

	if err := m.Create(name, local, cfg.RemoteHost+":"+remotePath, stdout, stderr); err != nil {
		fmt.Fprintf(stdout, "✗ Failed to create %s\n", name)
		return err
	}
	fmt.Fprintf(stdout, "✓ %s created\n", name)
	return nil
}

// SyncStop implements `sync-stop [path]`: stop the session for path, or every
// claude-remote session when no path is given.
func SyncStop(args []string) int {
	m := mutagen.New()
	if len(args) > 0 {
		dir, err := resolveDir(args)
		if err != nil {
			return fail(err)
		}
		name := mutagen.SessionName(dir)
		if !m.Exists(name) {
			fmt.Printf("○ No sync session for %s\n", dir)
			return 0
		}
		if err := m.Terminate(name); err != nil {
			return fail(fmt.Errorf("terminating %s: %w", name, err))
		}
		fmt.Printf("✓ Sync session %s stopped\n", name)
		return 0
	}
	out, err := m.List()
	if err != nil || len(mutagen.ParseList(out)) == 0 {
		fmt.Println("○ No sync session running")
		return 0
	}
	fmt.Println("Stopping sync session...")
	if err := m.TerminateAll(); err != nil {
		return fail(err)
	}
	fmt.Println("✓ Sync session stopped")
	return 0
}

// SyncStatus implements `sync-status`.
func SyncStatus([]string) int {
	out, err := mutagen.New().List()
	if err != nil {
		fmt.Println("No sync session running")
		return 0
	}
	fmt.Print(out)
	return 0
}

// SyncReap implements `sync-reap`: terminate zombie sync sessions (halted,
// disconnected or errored, or whose local directory is gone), then reap stale
// per-session mode files.
func SyncReap([]string) int {
	reapSync(os.Stdout)
	return SessionReap(nil)
}

func reapSync(stdout io.Writer) {
	m := mutagen.New()
	if !m.Installed() {
		return
	}
	out, err := m.List()
	if err != nil {
		return
	}
	sessions := mutagen.ParseList(out)
	if len(sessions) == 0 {
		fmt.Fprintln(stdout, "sync-reap: no claude-remote sessions found")
		return
	}
	reaped := 0
	for _, s := range sessions {
		reason := s.ReapReason(isDir)
		if reason == "" {
			continue
		}
		fmt.Fprintf(stdout, "sync-reap: terminating '%s' (%s)\n", s.Name, reason)
		if m.Terminate(s.Name) == nil {
			reaped++
		}
	}
	fmt.Fprintf(stdout, "sync-reap: done (reaped %d session(s))\n", reaped)
}

// SessionReap implements `session-reap`.
func SessionReap([]string) int {
	n := reap.Sessions(config.StateDir(), time.Now(), os.Stdout, os.Stderr)
	fmt.Printf("session-reap: done (reaped %d session file(s))\n", n)
	return 0
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

func loadConfig() (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return cfg, cfg.Validate()
}

func fail(err error) int {
	msg := err.Error()
	if !strings.HasPrefix(msg, "Error") {
		msg = "Error: " + msg
	}
	fmt.Fprintln(os.Stderr, msg)
	return 1
}
