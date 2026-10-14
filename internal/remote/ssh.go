// Package remote runs commands on the remote box over ssh, sharing one
// multiplexed (ControlMaster) connection between invocations.
package remote

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ControlPath is the ssh multiplexing socket template.
const ControlPath = "/tmp/ssh-claude-%r@%h:%p"

// SSH talks to one host.
type SSH struct {
	// Bin is the ssh executable ($CLAUDE_REMOTE_SSH, else ssh from PATH).
	Bin  string
	Host string
}

// New returns an SSH client for host.
func New(host string) *SSH {
	bin := os.Getenv("CLAUDE_REMOTE_SSH")
	if bin == "" {
		bin = "/usr/bin/ssh"
		if p, err := exec.LookPath("ssh"); err == nil {
			bin = p
		}
	}
	return &SSH{Bin: bin, Host: host}
}

// MuxOptions are the options every regular command uses: reuse (or become)
// the control master, keep it alive for 10 minutes, and notice a dead
// network instead of hanging on it.
func MuxOptions() []string {
	return []string{
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + ControlPath,
		"-o", "ControlPersist=600",
		"-o", "ConnectTimeout=5",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
	}
}

// Args builds the full ssh argument list for running remoteCmd. extra goes
// between the options and the host (e.g. "-t").
func (s *SSH) Args(extra []string, remoteCmd string) []string {
	args := MuxOptions()
	args = append(args, extra...)
	return append(args, s.Host, remoteCmd)
}

// Command returns an *exec.Cmd running remoteCmd on the host. The caller
// wires up stdio.
func (s *SSH) Command(ctx context.Context, remoteCmd string, extra ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, s.Bin, s.Args(extra, remoteCmd)...)
	// If a forked control master ever holds our output pipe open, don't wait
	// on it forever once ssh itself has exited.
	cmd.WaitDelay = 2 * time.Second
	return cmd
}

// Run runs remoteCmd with no stdin; nil writers discard output.
func (s *SSH) Run(ctx context.Context, remoteCmd string, stdout, stderr io.Writer) error {
	cmd := s.Command(ctx, remoteCmd)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	return cmd.Run()
}

// Available reports whether the host answers within a few seconds.
//
// A live control master is probed through itself, which costs one round trip
// instead of a full handshake. A master that is alive but no longer answers
// (laptop changed networks, VPN dropped) is shut down so the next command
// dials fresh instead of hanging on it. Without a usable master the host is
// probed with a plain connection that bypasses multiplexing altogether.
func (s *SSH) Available() bool {
	if s.Host == "" || strings.HasPrefix(s.Host, "-") {
		return false
	}
	ctl := "ControlPath=" + ControlPath
	if s.quiet(time.Second, "-o", ctl, "-O", "check", s.Host) == nil {
		if s.quiet(3*time.Second, "-o", ctl, "-o", "ControlMaster=no", "-o", "BatchMode=yes", s.Host, "exit 0") == nil {
			return true
		}
		_ = s.quiet(time.Second, "-o", ctl, "-O", "exit", s.Host)
	}
	return s.quiet(6*time.Second,
		"-o", "ControlMaster=no", "-o", "ControlPath=none",
		"-o", "ConnectTimeout=5", "-o", "BatchMode=yes",
		s.Host, "exit 0") == nil
}

// MasterRunning reports whether a control master for the host is up.
func (s *SSH) MasterRunning() bool {
	return s.quiet(time.Second, "-o", "ControlPath="+ControlPath, "-O", "check", s.Host) == nil
}

// ControlSocket returns the expanded control socket path for the host.
func (s *SSH) ControlSocket() string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, s.Bin, "-G", "-o", "ControlPath="+ControlPath, s.Host).Output()
	if err != nil {
		return ""
	}
	cfg := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), " "); ok {
			cfg[strings.ToLower(k)] = v
		}
	}
	path := cfg["controlpath"]
	if strings.Contains(path, "%") {
		// Older OpenSSH prints the template unexpanded.
		path = strings.NewReplacer("%r", cfg["user"], "%h", cfg["hostname"], "%p", cfg["port"], "%%", "%").Replace(path)
	}
	return path
}

// quiet runs ssh with no stdio, killing it after timeout.
func (s *SSH) quiet(timeout time.Duration, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return exec.CommandContext(ctx, s.Bin, args...).Run()
}

// Quote single-quotes s for a POSIX shell.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
