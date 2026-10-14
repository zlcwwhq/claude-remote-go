# Claude Remote Configuration
#
# Location: ~/.claude-remote/config.sh (override with $CLAUDE_REMOTE_CONFIG).
# 'claude-remote setup' writes this file for you.
#
# Same KEY="value" format as the bash version's config.sh, so an existing
# config.sh can be copied here unchanged. The file is parsed, not executed:
# quoting, comments, `export` and $VAR / ${VAR} expansion work; command
# substitution does not.

# SSH connection to remote machine (user@host or an alias from ~/.ssh/config)
REMOTE_HOST="ubuntu@your-ec2-instance.amazonaws.com"

# Root on remote where CWD mirrors are synced (local /abs/path → REMOTE_MIRROR_ROOT/abs/path)
# Only used when PATH_IDENTITY=false (legacy/default mode).
REMOTE_MIRROR_ROOT="/home/ubuntu/claude-remote-mirror"

# PATH_IDENTITY: when true, sync to the identical absolute path on the remote box
# (e.g. /Users/USER/workspace/... exists on the box at exactly that path).
# Requires the box to have your local home directory created and owned by the
# ssh user; 'claude-remote setup' can do this for you when PATH_IDENTITY=true.
# When false (default), paths are mirrored under REMOTE_MIRROR_ROOT.
PATH_IDENTITY="false"

# ---------------------------------------------------------------------------
# CLAUDE_REMOTE_MODE — LAUNCH-TIME SEED ONLY
#
# The lowest-priority input to the mode toggle. It cannot change routing
# mid-session: Claude Code starts a new $SHELL for every Bash call, so an
# "export CLAUDE_REMOTE_MODE=off" inside one call is gone by the next.
#
# To toggle mid-session, write the mode files instead (see README):
#   ~/.claude-remote/session/<CLAUDE_CODE_SESSION_ID>   per session
#   ~/.claude-remote/mode                               global
#
# Precedence: session file > project file > global file > this seed > "on".
# A value set here wins over the environment variable of the same name.
# ---------------------------------------------------------------------------
# CLAUDE_REMOTE_MODE="on"   # uncomment to seed a session default

# ---------------------------------------------------------------------------
# REMOTE_IMAGE_CACHE — where paste-image-remote uploads clipboard images.
# Default: ~/.cache/tmux-paste-image in the remote user's home.
# ---------------------------------------------------------------------------
# REMOTE_IMAGE_CACHE="/home/ubuntu/.cache/tmux-paste-image"

# The state directory itself (default ~/.claude-remote) can only be moved with
# the CLAUDE_REMOTE_STATE_DIR environment variable, since this file lives in it.
