package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/langwatch/claude-remote/internal/config"
	"github.com/langwatch/claude-remote/internal/remote"
)

// PasteImage implements `paste-image-remote [tmux-session]`: upload the
// clipboard image to the remote box, then type its path into the given tmux
// session or put the path on the local clipboard.
func PasteImage(args []string) int {
	cfg, err := config.Load()
	if err != nil {
		return fail(err)
	}

	tmp, err := os.CreateTemp("", "clipboard_*.png")
	if err != nil {
		return fail(err)
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	if err := clipboardImage(tmp.Name()); err != nil {
		fmt.Println("No image in clipboard")
		return 1
	}
	if fi, err := os.Stat(tmp.Name()); err != nil || fi.Size() == 0 {
		fmt.Println("No image in clipboard")
		return 1
	}

	// Without REMOTE_IMAGE_CACHE the directory is resolved on the remote, so
	// it lands in the remote user's own home.
	dir := `"$HOME/.cache/tmux-paste-image"`
	if cfg.ImageCache != "" {
		dir = remote.Quote(cfg.ImageCache)
	}
	file := remote.Quote("clipboard_" + time.Now().Format("20060102_150405") + ".png")
	upload := "d=" + dir + `; mkdir -p "$d" && cat > "$d"/` + file + ` && printf '%s\n' "$d"/` + file

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	img, err := os.Open(tmp.Name())
	if err != nil {
		return fail(err)
	}
	defer img.Close()
	ssh := remote.New(cfg.RemoteHost)
	cmd := ssh.Command(ctx, upload)
	cmd.Stdin, cmd.Stderr = img, os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return fail(fmt.Errorf("upload to %s failed: %w", cfg.RemoteHost, err))
	}
	// The path is the last line, whatever the remote's login files printed before it.
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	remotePath := lines[len(lines)-1]

	if len(args) > 0 {
		send := "tmux send-keys -t " + remote.Quote(args[0]) + " " + remote.Quote(remotePath) + " Enter"
		if err := ssh.Run(ctx, send, os.Stdout, os.Stderr); err != nil {
			return fail(fmt.Errorf("tmux send-keys failed: %w", err))
		}
		fmt.Printf("Sent to tmux session: %s\n", args[0])
		return 0
	}
	if err := copyText(remotePath); err != nil {
		fmt.Printf("Image uploaded: %s (could not copy path to clipboard: %v)\n", remotePath, err)
		return 0
	}
	fmt.Printf("Image uploaded: %s (path copied to clipboard)\n", remotePath)
	return 0
}

// clipboardImage writes the clipboard's PNG image to path.
func clipboardImage(path string) error {
	if runtime.GOOS == "darwin" {
		if _, err := exec.LookPath("pngpaste"); err == nil {
			return exec.Command("pngpaste", path).Run()
		}
		script := fmt.Sprintf(`set png to (the clipboard as «class PNGf»)
set f to open for access (POSIX file %q) with write permission
write png to f
close access f`, path)
		return exec.Command("osascript", "-e", script).Run()
	}
	var cmd *exec.Cmd
	switch {
	case os.Getenv("WAYLAND_DISPLAY") != "" && has("wl-paste"):
		cmd = exec.Command("wl-paste", "--no-newline", "--type", "image/png")
	case has("xclip"):
		cmd = exec.Command("xclip", "-selection", "clipboard", "-target", "image/png", "-out")
	default:
		return errors.New("no clipboard tool found (install wl-clipboard or xclip)")
	}
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()
	cmd.Stdout = out
	return cmd.Run()
}

// copyText puts s on the local clipboard.
func copyText(s string) error {
	var cmd *exec.Cmd
	switch {
	case runtime.GOOS == "darwin":
		cmd = exec.Command("pbcopy")
	case os.Getenv("WAYLAND_DISPLAY") != "" && has("wl-copy"):
		cmd = exec.Command("wl-copy")
	case has("xclip"):
		cmd = exec.Command("xclip", "-selection", "clipboard")
	case has("xsel"):
		cmd = exec.Command("xsel", "--clipboard", "--input")
	default:
		return errors.New("no clipboard tool found")
	}
	cmd.Stdin = strings.NewReader(s)
	return cmd.Run()
}

func has(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}
