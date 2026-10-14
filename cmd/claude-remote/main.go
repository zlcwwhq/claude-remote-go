// Command claude-remote runs Claude Code locally while executing its shell
// commands on a remote machine.
//
// It is a multi-call binary. Invoked as zsh/bash (the shell shim Claude Code
// gets as $SHELL) it is the remote shell wrapper; invoked through a ~/bin
// link such as sync-start it runs that command; as claude-remote it takes an
// optional subcommand and otherwise launches Claude Code with all arguments.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/langwatch/claude-remote/internal/cli"
	"github.com/langwatch/claude-remote/internal/shell"
)

var version = "dev"

var commands = map[string]func([]string) int{
	"sync-start":         cli.SyncStart,
	"sync-stop":          cli.SyncStop,
	"sync-status":        cli.SyncStatus,
	"sync-reap":          cli.SyncReap,
	"session-reap":       cli.SessionReap,
	"remote-status":      cli.RemoteStatus,
	"paste-image-remote": cli.PasteImage,
	"setup":              cli.Setup,
	"mode":               cli.Mode,
	"shell":              shell.Run,
	"launch":             cli.Launch,
	"help":               help,
	"version": func([]string) int {
		fmt.Println("claude-remote", version)
		return 0
	},
}

func main() {
	os.Exit(run(filepath.Base(os.Args[0]), os.Args[1:]))
}

func run(name string, args []string) int {
	switch name {
	case "zsh", "bash", "remote-shell":
		return shell.Run(args)
	}
	if cmd, ok := commands[name]; ok {
		return cmd(args)
	}
	if len(args) > 0 {
		if cmd, ok := commands[args[0]]; ok {
			return cmd(args[1:])
		}
	}
	return cli.Launch(args)
}

func help([]string) int {
	fmt.Println(strings.TrimSpace(`
Usage:
  claude-remote [claude args...]       Sync the current directory and launch Claude Code
                                       with its shell commands running on the remote
  claude-remote launch [claude args]   Same, for arguments that collide with a subcommand
  claude-remote setup                  Create the config, shell shim and ~/bin links
  claude-remote sync-start [path]      Start (or reuse) the sync session for a directory
  claude-remote sync-stop [path]       Stop one sync session, or all of them
  claude-remote sync-status            Show claude-remote sync sessions
  claude-remote sync-reap              Remove zombie sync sessions and stale session files
  claude-remote session-reap           Remove stale per-session mode files only
  claude-remote remote-status          Diagnostic overview
  claude-remote mode                   Show how command routing resolves right now
  claude-remote paste-image-remote [tmux-session]
                                       Upload the clipboard image to the remote
  claude-remote shell -c <command>     Run a command the way Claude Code's shell would
  claude-remote version

After 'claude-remote setup', the sync-*, remote-status and paste-image-remote
commands are also available under their own names in ~/bin.`))
	return 0
}
