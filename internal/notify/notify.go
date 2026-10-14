// Package notify shows desktop notifications when the remote goes offline or
// comes back.
package notify

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// States passed to Send.
const (
	Online  = "online"
	Offline = "offline"
)

// Cooldown is how often a still-offline remote is re-announced.
const Cooldown = 5 * time.Minute

// Send notifies when state differs from the last notified state, or when the
// remote is still offline and Cooldown has passed. stateFile remembers the
// last state and when it was announced.
func Send(stateFile, message, state string) {
	now := time.Now()
	lastState, lastAt := read(stateFile)
	if state == lastState && !(state == Offline && now.Sub(lastAt) >= Cooldown) {
		return
	}
	desktop("Claude Remote", message)
	_ = os.WriteFile(stateFile, fmt.Appendf(nil, "%s\n%d\n", state, now.Unix()), 0o644)
}

func read(stateFile string) (string, time.Time) {
	data, err := os.ReadFile(stateFile)
	if err != nil {
		return "", time.Time{}
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	secs, _ := strconv.ParseInt(strings.TrimSpace(lines[len(lines)-1]), 10, 64)
	return strings.TrimSpace(lines[0]), time.Unix(secs, 0)
}

// desktop shows a notification with whatever the platform offers, silently
// doing nothing when it offers nothing.
func desktop(title, message string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		script := fmt.Sprintf("display notification %s with title %s", appleString(message), appleString(title))
		cmd = exec.Command("osascript", "-e", script)
	default:
		if _, err := exec.LookPath("notify-send"); err != nil {
			return
		}
		cmd = exec.Command("notify-send", title, message)
	}
	_ = cmd.Run()
}

func appleString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
