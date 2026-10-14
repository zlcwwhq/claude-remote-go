package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/langwatch/claude-remote/internal/config"
	"github.com/langwatch/claude-remote/internal/remote"
)

// Setup implements `claude-remote setup`: write the config, install the shell
// shim and ~/bin links, check dependencies and test the ssh connection.
func Setup([]string) int {
	fmt.Println("Claude Remote Setup")
	fmt.Println("===================")
	fmt.Println()

	path := config.FilePath()
	cfg, err := config.Load()
	switch {
	case err == nil:
		fmt.Printf("✓ %s already exists\n", path)
	case errors.Is(err, config.ErrNotFound):
		fmt.Println("Creating configuration...")
		fmt.Println()
		in := bufio.NewReader(os.Stdin)
		cfg = &config.Config{
			RemoteHost: prompt(in, "Remote SSH host (e.g., ubuntu@your-server.com): ", ""),
			MirrorRoot: prompt(in, fmt.Sprintf("Remote mirror root [%s]: ", config.DefaultMirrorRoot), config.DefaultMirrorRoot),
		}
		answer := prompt(in, "PATH_IDENTITY mode — sync to identical absolute path on box? [y/N]: ", "")
		cfg.PathIdentity = strings.HasPrefix(strings.ToLower(answer), "y")
		if err := config.Write(path, cfg); err != nil {
			return fail(err)
		}
		fmt.Println()
		fmt.Printf("✓ Configuration saved to %s\n", path)
	default:
		return fail(err)
	}
	fmt.Println()

	stateDir := config.StateDir()
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return fail(err)
	}
	fmt.Printf("✓ State dir: %s\n", stateDir)
	shim, err := EnsureShim(stateDir)
	if err != nil {
		return fail(fmt.Errorf("installing shell shim: %w", err))
	}
	exe, _ := executable()
	fmt.Printf("✓ Shell shim: %s -> %s\n", shim, exe)
	fmt.Println()

	home, _ := os.UserHomeDir()
	binDir := filepath.Join(home, "bin")
	linkCommands(binDir, exe)
	fmt.Println()
	if !slices.Contains(filepath.SplitList(os.Getenv("PATH")), binDir) {
		fmt.Printf("⚠ Warning: %s is not in your PATH\n", binDir)
		fmt.Println("  Add this to your ~/.zshrc or ~/.bashrc:")
		fmt.Println(`  export PATH="$HOME/bin:$PATH"`)
		fmt.Println()
	}

	fmt.Println("Checking dependencies...")
	if has("mutagen") {
		fmt.Println("✓ mutagen installed")
	} else {
		fmt.Println("⚠ mutagen not found. Install it from https://mutagen.io/ (macOS: brew install mutagen-io/mutagen/mutagen)")
	}
	if has("claude") {
		fmt.Println("✓ claude installed")
	} else {
		fmt.Println("⚠ claude not found. Install Claude Code first.")
	}
	fmt.Println()

	testConnection(cfg, home)

	fmt.Println()
	fmt.Println("Setup complete!")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  claude-remote          # Launch Claude with remote execution (syncs CWD)")
	fmt.Println("  sync-start [path]      # Start Mutagen sync for a directory")
	fmt.Println("  sync-status            # Show Mutagen sync status")
	fmt.Println("  sync-stop [path]       # Stop Mutagen sync")
	fmt.Println("  sync-reap              # Remove zombie sync sessions and stale session files")
	fmt.Println("  remote-status          # Diagnostic status check")
	fmt.Println("  claude-remote mode     # Show how command routing resolves right now")
	return 0
}

func prompt(in *bufio.Reader, question, def string) string {
	fmt.Print(question)
	line, _ := in.ReadString('\n')
	if line = strings.TrimSpace(line); line != "" {
		return line
	}
	return def
}

// linkCommands points each command name in binDir at exe. Existing symlinks
// are replaced; anything else is left alone.
func linkCommands(binDir, exe string) {
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		fmt.Printf("⚠ could not create %s: %v\n", binDir, err)
		return
	}
	fmt.Printf("Creating symlinks in %s...\n", binDir)
	for _, name := range Commands {
		link := filepath.Join(binDir, name)
		if link == exe {
			continue // the binary itself lives here
		}
		if fi, err := os.Lstat(link); err == nil && fi.Mode()&os.ModeSymlink == 0 {
			fmt.Printf("  ⚠ %s exists and is not a symlink; leaving it alone\n", link)
			continue
		}
		_ = os.Remove(link)
		if err := os.Symlink(exe, link); err != nil {
			fmt.Printf("  ⚠ %s: %v\n", name, err)
			continue
		}
		fmt.Printf("  %s -> %s\n", name, exe)
	}
}

func testConnection(cfg *config.Config, home string) {
	fmt.Printf("Testing SSH connection to %s...\n", cfg.RemoteHost)
	ssh := remote.New(cfg.RemoteHost)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if exec.CommandContext(ctx, ssh.Bin, "-o", "ConnectTimeout=5", "-o", "BatchMode=yes", cfg.RemoteHost, "echo ok").Run() != nil {
		fmt.Println("Warning: SSH connection failed. Make sure:")
		fmt.Println("  1. The host is reachable")
		fmt.Printf("  2. SSH keys are set up (ssh-copy-id %s)\n", cfg.RemoteHost)
		return
	}
	fmt.Println("✓ SSH connection works")
	if !cfg.PathIdentity {
		return
	}

	// PATH_IDENTITY syncs to the identical absolute path, so the box needs
	// this machine's home directory, owned by the ssh user.
	fmt.Println()
	fmt.Println("PATH_IDENTITY=true: setting up identity path on box...")
	dirs := []string{home}
	if filepath.Dir(home) == "/Users" {
		dirs = []string{"/Users", home}
	}
	quoted := make([]string, len(dirs))
	for i, d := range dirs {
		quoted[i] = remote.Quote(d)
	}
	cmd := "sudo mkdir -p " + remote.Quote(home) + ` && sudo chown "$(id -un):$(id -gn)" ` + strings.Join(quoted, " ")
	fmt.Printf("  Creating %s on box owned by the ssh user...\n", home)
	if exec.CommandContext(ctx, ssh.Bin, "-o", "ConnectTimeout=5", cfg.RemoteHost, cmd).Run() == nil {
		fmt.Printf("  OK: %s exists and is owned by the ssh user\n", home)
	} else {
		fmt.Printf("  Warning: could not create %s on box (sudo may not be available).\n", home)
		fmt.Printf("  Run manually on the box: %s\n", cmd)
	}
}
