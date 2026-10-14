# Claude Remote (Go)

Run Claude Code locally with the UI on your machine, but execute all commands on a remote server.

This is a Go rewrite of [langwatch/claude-remote](https://github.com/langwatch/claude-remote): one static binary instead of a set of bash scripts, with the same configuration format, state files, mode precedence and mutagen session names, so it can replace the bash version in place.

**Why?** Claude Code can be CPU-intensive (TypeScript compilation, tests, file operations). This setup lets you:
- Keep your local machine fast and responsive
- Use a powerful remote server (EC2, etc.) for heavy lifting
- Maintain low-latency typing since the Claude UI runs locally

## Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                       Your machine (Local)                          │
│  ┌──────────────┐    ┌──────────────┐    ┌────────────────────┐    │
│  │ Claude Code  │───▶│ claude-remote│───▶│ SSH ControlMaster  │    │
│  │   (UI/TUI)   │    │ ($SHELL shim)│    │ (persistent conn)  │    │
│  └──────────────┘    └──────────────┘    └─────────┬──────────┘    │
│         │                                          │                │
│         ▼                                          │                │
│  ┌──────────────┐    ┌──────────────┐              │                │
│  │ Any local    │◀──▶│   Mutagen    │◀─────────────┼────────┐      │
│  │  directory   │    │ (bidirectional sync)        │        │      │
│  └──────────────┘    └──────────────┘              │        │      │
└────────────────────────────────────────────────────┼────────┼──────┘
                                                     │        │
                                                     ▼        ▼
┌────────────────────────────────────────────────────────────────────┐
│                      Remote Server (EC2)                           │
│  ┌──────────────┐    ┌──────────────────────────────────────┐     │
│  │   SSH        │    │  REMOTE_MIRROR_ROOT/<abs-path>/       │     │
│  │   Server     │───▶│  (your actual files & execution)     │     │
│  └──────────────┘    └──────────────────────────────────────┘     │
└────────────────────────────────────────────────────────────────────┘
```

### How it works

1. **Shell interception**: Claude Code runs every Bash tool call through `$SHELL`. `claude-remote` starts Claude Code with `$SHELL` pointing at itself (via a `zsh` symlink, since Claude Code only accepts shells it recognises by name). For each command it:
   - resolves whether routing is on or off (see [Toggling](#toggling-offload-onoff))
   - runs the command over SSH in the directory that mirrors your local one
   - streams output back as it is produced and returns the command's real exit code
   - reports the final remote directory back to Claude Code, mapped to the local path, so `cd` works as it would locally

2. **Bidirectional file sync**: [Mutagen](https://mutagen.io/) keeps files in sync between local and remote:
   - Files are **real local files**: instant access for Claude's Read/Write/Edit/Glob/Grep tools
   - Works with any local editor (VS Code, Cursor, etc.)
   - The sync session is flushed before and after each remote command
   - `node_modules`, `.venv`, `.cache`, build output etc. are not synced

3. **SSH multiplexing**: one ControlMaster connection is shared by all commands. While it is up, checking that the remote is reachable costs one round trip instead of a handshake. If the master stops answering (laptop changed networks, VPN dropped), it is shut down and replaced instead of hanging the command.

4. **Local fallback**: when the remote server is unreachable:
   - commands run locally, with a loud `[claude-remote] LOCAL FALLBACK` line on stderr so Claude notices
   - a desktop notification announces the change (macOS: `osascript`, Linux: `notify-send`)
   - you are reminded every 5 minutes while it stays offline
   - routing switches back automatically when the remote is available again

### Why Mutagen sync instead of SSHFS?

| Aspect | SSHFS (mounting) | Mutagen (sync) |
|--------|------------------|----------------|
| **File access speed** | Network latency on every operation | Instant (files are local) |
| **Glob/Grep/Search** | Slow: must traverse remote FS | Fast: operates on local files |
| **Editor support** | Works but can be laggy | Full native speed |
| **Offline work** | Broken when disconnected | Files available locally |
| **Large repos** | Sluggish with many files | Only syncs what changed |

The tradeoff is disk space (files exist in both places) and sync delay, but in practice Mutagen syncs are near-instant.

## Requirements

- macOS or Linux
- [Claude Code](https://claude.ai/code)
- SSH access to a remote server with key-based auth; the remote login shell must be bash
- [Mutagen](https://mutagen.io/) for file synchronization
- Go 1.24+ to build

## Installation

```bash
# 1. Mutagen
brew install mutagen-io/mutagen/mutagen      # macOS; see mutagen.io for Linux

# 2. Build and install the binary (into $(go env GOPATH)/bin)
git clone <this repo> claude-remote-go && cd claude-remote-go
go install ./cmd/claude-remote

# 3. Configure
claude-remote setup
```

`claude-remote setup`:
- asks for your remote server details and writes `~/.claude-remote/config.sh`
- creates the `$SHELL` shim `~/.claude-remote/bin/zsh`
- links `claude-remote`, `sync-start`, `sync-status`, `sync-stop`, `sync-reap`, `remote-status` and `paste-image-remote` into `~/bin`
- checks for mutagen and claude, and tests the SSH connection

If SSH key auth is not set up yet: `ssh-copy-id ubuntu@your-server.com`.

### Migrating from the bash version

Copy your config and keep everything else:

```bash
mkdir -p ~/.claude-remote
cp /path/to/claude-remote/config.sh ~/.claude-remote/config.sh
claude-remote setup      # replaces the ~/bin symlinks with links to the Go binary
```

The mode/url/session files in `~/.claude-remote` and the existing mutagen sessions are picked up as they are.

## Usage

```bash
# Launch Claude with remote execution (syncs the current directory)
claude-remote [claude args...]
```

All arguments go to `claude` (after `--dangerously-skip-permissions`, as in the bash version). If your first argument happens to be a subcommand name, use `claude-remote launch <args...>`.

Once running, Claude's commands execute on the remote server:

```
❯ uname -a
Linux ip-10-0-3-248 6.14.0-1018-aws ... aarch64 GNU/Linux
```

### Other commands

Each is available as `claude-remote <command>` and, after `setup`, under its own name in `~/bin`:

```bash
sync-start [path]        # Start (or reuse) the sync session for a directory (default: CWD)
sync-stop [path]         # Stop one sync session, or every claude-remote session
sync-status              # Show claude-remote sync sessions
sync-reap                # Remove zombie sync sessions and stale per-session mode files
remote-status            # Diagnostic overview: config, mode, shim, links, SSH, sync
paste-image-remote [tmux-session]   # Upload the clipboard image; paste its path

claude-remote mode       # Show how command routing resolves right now, and why
claude-remote session-reap
claude-remote shell -c <command>    # Run a command exactly as Claude Code's shell would
claude-remote help
```

## Configuration

`~/.claude-remote/config.sh` (override the location with `$CLAUDE_REMOTE_CONFIG`):

```bash
REMOTE_HOST="ubuntu@your-ec2-instance.amazonaws.com"
REMOTE_MIRROR_ROOT="/home/ubuntu/claude-remote-mirror"
PATH_IDENTITY="false"   # set to "true" for identity-path mode (see below)
```

The format is the same as the bash version's `config.sh`, but the file is parsed rather than executed: quoting, comments, `export` and `$VAR`/`${VAR}` expansion work; command substitution is rejected with an error. See [`config.example.sh`](config.example.sh) for all settings.

### PATH_IDENTITY mode

When `PATH_IDENTITY="true"`, files are synced to the **identical absolute path** on the remote box instead of being mirrored under `REMOTE_MIRROR_ROOT`. For example, `/Users/USER/workspace/myproject` locally syncs to `/Users/USER/workspace/myproject` on the box, with no path translation at all.

Benefits: git worktrees just work (no `.git` file rewriting needed), and any tool that embeds absolute paths in its output is correct on both sides without translation.

Requirements: the box must have your local home directory (e.g. `/Users/<localuser>`) created and owned by the ssh user. `claude-remote setup` does this when you answer `y` to the PATH_IDENTITY prompt (it needs passwordless `sudo` on the box). To do it manually:

```bash
# On the box (as a user with sudo):
sudo mkdir -p /Users/<localuser> && sudo chown ubuntu:ubuntu /Users /Users/<localuser>
```

**Note:** only the directory you run `claude-remote` from is synced. One mutagen session is created per launch directory. Paths outside that tree are not present on the box.

### Toggling offload on/off

Claude Code starts a fresh `$SHELL` for every Bash call, so routing is decided per command from files that are re-read every time. Changing a file takes effect on the **next command**, with no restart:

```
~/.claude-remote/session/<id>  — per-session on|off (highest precedence)
~/.claude-remote/mode          — global on|off
~/.claude-remote/url           — ssh host override (e.g. ubuntu@10.0.3.56)
```

```bash
printf 'on\n'  > ~/.claude-remote/mode     # offload every session without its own setting
printf 'off\n' > ~/.claude-remote/mode
claude-remote mode                          # what applies here, and which layer decided it
```

The companion **`/claude-remote` skill** (shipped separately, in `~/.claude/skills/claude-remote/`) writes these files from inside a session (`/claude-remote on|off|status`), defaulting to the per-session file.

**Mode precedence (highest first):**
1. Per-session file `~/.claude-remote/session/<CLAUDE_CODE_SESSION_ID>`: line 1 is the mode, an optional line 2 `pid:<owner>` is a liveness token for the reaper. Only consulted when the id is a well-formed UUID; anything else (e.g. `../mode`) is ignored with a warning.
2. Project-local file `$CLAUDE_PROJECT_DIR/.claude-remote-mode`
3. Global file `~/.claude-remote/mode`
4. `CLAUDE_REMOTE_MODE` from the config file, else the environment (launch-time seed; cannot change mid-session)
5. Built-in default: `on`

**Fail-safe:** any value other than exactly `on` (after trimming whitespace, including `\r`) routes **locally** and prints a warning to stderr. A corrupt toggle file can never silently SSH to a box.

**Wraps, doesn't activate:** when `claude-remote` launches and `~/.claude-remote/mode` does not exist yet, it is created with `off`. A fresh install runs locally until you turn offloading on.

**Stale session files** are removed by `sync-reap` (which `claude-remote` runs at every launch) once their owner process has exited and they are older than 60 seconds.

### Scoped sync sessions

Each launch directory gets exactly one mutagen session, named after its path (e.g. `claude-remote-Users-localuser-workspace-myproject`) and labelled `name=claude-remote`. Only that session is flushed around each remote command, including when Claude has `cd`'d into a subdirectory, so other projects running in parallel are unaffected.

`sync-reap` terminates any `claude-remote` session whose status is Halted/Disconnected/errored or whose local directory no longer exists; healthy sessions are left alone.

### Sync ignore patterns

The excluded patterns are listed in `IgnorePatterns` in [`internal/mutagen/mutagen.go`](internal/mutagen/mutagen.go): `node_modules`, `.venv`, `.cache`, `dist`, `.next*`, `__pycache__`, `.pytest_cache`, `.mypy_cache`, `.turbo`, `*.pyc`, `.DS_Store`, `coverage`, `.nyc_output`, `target`, `build`. They apply to sessions created after a change: `sync-stop <path> && sync-start <path>`.

## Tips

### Port forwarding

If your remote server runs services (dev servers, databases, etc.), forward ports to access them locally:

```bash
ssh -N -L 3000:localhost:3000 ubuntu@your-server.com                         # one port
ssh -N -L 3000:localhost:3000 -L 5432:localhost:5432 ubuntu@your-server.com  # several
ssh -fN -L 3000:localhost:3000 ubuntu@your-server.com                        # in the background
```

### Remote PATH setup

Remote commands source `~/.profile` and `~/.bashrc` (with Ubuntu's non-interactive early return disabled). For the fastest startup, put your tools on PATH in `~/.profile` instead of relying on slow init scripts like `nvm.sh`:

```bash
# On the remote machine, in ~/.profile:
export PATH=~/.nvm/versions/node/v24.13.0/bin:$PATH
export PATH=~/.local/bin:$PATH  # for uv, pipx, etc.
```

## Troubleshooting

Start with `remote-status`: it shows the config, the resolved mode and why, the shim and `~/bin` links, the SSH control socket and connection, and the sync sessions.

### Commands not finding binaries (pnpm, node, etc.)

Make sure the remote `~/.profile` sets PATH, then check: `ssh your-server "source ~/.profile && which pnpm"`.

### "remote directory ... does not exist"

The directory Claude is in has no copy on the remote: the sync session is missing, or Claude moved outside the synced tree. Run `sync-status`, and `sync-start` in the project directory if needed. Commands never fall back to running in some other remote directory.

### Sync issues

```bash
mutagen sync list                 # all sessions, with errors
sync-reap                         # clear halted/orphaned sessions
sync-stop . && sync-start .       # recreate the session for this directory
```

### SSH connection issues

```bash
ssh -v your-server "echo ok"
ssh -o ControlPath='/tmp/ssh-claude-%r@%h:%p' -O exit your-server   # drop the shared connection
```

## How the shell wrapper works

Claude Code invokes the shell as `$SHELL -c -l "<command> && pwd -P >| <cwd file>"`. For each call, `claude-remote`:

1. Resolves the mode; if it is not `on`, or the remote does not answer, replaces itself with local `bash -c "<command>"` (after saying so on stderr)
2. Flushes this directory's Mutagen sync session
3. Splits off Claude's `pwd -P >| <cwd file>` suffix
4. Runs the command on the remote over the shared SSH connection: sources `~/.profile`/`~/.bashrc`, enters the mirrored directory, runs the command in bash, then prints a trailer line with the final directory
5. Streams stdout/stderr back as they arrive, removing the trailer
6. Flushes the sync session again
7. If the command succeeded, writes the final directory, mapped back to the local path, to Claude's cwd file
8. Exits with the command's exit code

In mirror mode it also rewrites a git worktree's `.git` file in the current directory so its `gitdir:` points into the mirror.

## Development

```bash
go test ./...         # unit tests + end-to-end tests against the built binary
go vet ./...
```

The end-to-end tests in `cmd/claude-remote` stub ssh (it runs the "remote" command locally), mutagen, claude and the notification and clipboard tools, so they need no network. They include a port of the bash version's `tests/toggle.bats`.

Layout:

```
cmd/claude-remote/   multi-call entry point (dispatches on argv[0] and subcommand)
internal/shell/      the $SHELL wrapper and the output trailer filter
internal/mode/       mode / url resolution
internal/config/     config file parsing and path mapping
internal/remote/     ssh with ControlMaster, reachability checks
internal/mutagen/    mutagen CLI, session names, list parsing
internal/reap/       stale per-session file reaper
internal/notify/     desktop notifications with rate limiting
internal/cli/        launch, setup, sync-*, remote-status, paste-image-remote
```

## License

MIT
