package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func noEnv(string) string { return "" }

func TestParse(t *testing.T) {
	env := func(k string) string {
		if k == "HOME" {
			return "/home/me"
		}
		return ""
	}
	data := `# Claude Remote Configuration
REMOTE_HOST="ubuntu@example.com"
REMOTE_MIRROR_ROOT='/srv/$literal'
export PATH_IDENTITY=true   # trailing comment
CLAUDE_REMOTE_MODE=
REMOTE_IMAGE_CACHE="$HOME/.cache/${REMOTE_HOST}"
ESCAPED="a \"b\" \$c"
CONCAT="a"'b'c
`
	got, err := Parse([]byte(data), env)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"REMOTE_HOST":        "ubuntu@example.com",
		"REMOTE_MIRROR_ROOT": "/srv/$literal",
		"PATH_IDENTITY":      "true",
		"CLAUDE_REMOTE_MODE": "",
		"REMOTE_IMAGE_CACHE": "/home/me/.cache/ubuntu@example.com",
		"ESCAPED":            `a "b" $c`,
		"CONCAT":             "abc",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d keys, want %d: %v", len(got), len(want), got)
	}
}

func TestParseRejectsWhatNeedsAShell(t *testing.T) {
	for _, line := range []string{
		`REMOTE_HOST=$(hostname)`,
		"REMOTE_HOST=`hostname`",
		`REMOTE_HOST="${HOST:-x}"`,
		`REMOTE_HOST="unterminated`,
		`REMOTE_HOST='unterminated`,
		`REMOTE_HOST=a b`,
		`not an assignment`,
		`1BAD=x`,
	} {
		if _, err := Parse([]byte(line+"\n"), noEnv); err == nil {
			t.Errorf("Parse(%q) succeeded, want error", line)
		} else if !strings.Contains(err.Error(), "line 1") {
			t.Errorf("Parse(%q) error %q does not name the line", line, err)
		}
	}
}

func TestFromValuesFileWinsOverEnv(t *testing.T) {
	env := func(k string) string { return "env-" + k }

	c := FromValues(map[string]string{"CLAUDE_REMOTE_MODE": "off"}, env)
	if c.ModeSeed != "off" {
		t.Errorf("ModeSeed = %q, want file value", c.ModeSeed)
	}
	if c.ImageCache != "env-REMOTE_IMAGE_CACHE" {
		t.Errorf("ImageCache = %q, want env fallback", c.ImageCache)
	}
	// An explicit empty value in the file still wins, as with `source`.
	if c := FromValues(map[string]string{"CLAUDE_REMOTE_MODE": ""}, env); c.ModeSeed != "" {
		t.Errorf("ModeSeed = %q, want empty", c.ModeSeed)
	}
	if c := FromValues(map[string]string{"PATH_IDENTITY": "yes"}, env); c.PathIdentity {
		t.Error("PATH_IDENTITY=yes enabled identity mode; only \"true\" should")
	}
}

func TestPathMapping(t *testing.T) {
	mirror := &Config{MirrorRoot: "/home/ubuntu/mirror/"}
	identity := &Config{MirrorRoot: "/home/ubuntu/mirror", PathIdentity: true}

	cases := []struct {
		cfg           *Config
		local, remote string
	}{
		{cfg: mirror, local: "/Users/a/proj", remote: "/home/ubuntu/mirror/Users/a/proj"},
		{cfg: identity, local: "/Users/a/proj", remote: "/Users/a/proj"},
	}
	for _, c := range cases {
		if got := c.cfg.LocalToRemote(c.local); got != c.remote {
			t.Errorf("LocalToRemote(%q) = %q, want %q", c.local, got, c.remote)
		}
		if got := c.cfg.RemoteToLocal(c.remote); got != c.local {
			t.Errorf("RemoteToLocal(%q) = %q, want %q", c.remote, got, c.local)
		}
	}

	for remote, want := range map[string]string{
		"/home/ubuntu/mirror":        "/",
		"/home/ubuntu/mirror2/x":     "/home/ubuntu/mirror2/x", // shares a prefix, not the root
		"/tmp":                       "/tmp",
		"/home/ubuntu/mirror/tmp/xy": "/tmp/xy",
	} {
		if got := mirror.RemoteToLocal(remote); got != want {
			t.Errorf("RemoteToLocal(%q) = %q, want %q", remote, got, want)
		}
	}
}

func TestValidate(t *testing.T) {
	if err := (&Config{Path: "c"}).Validate(); err == nil {
		t.Error("missing mirror root accepted")
	}
	if err := (&Config{PathIdentity: true}).Validate(); err != nil {
		t.Errorf("identity mode without mirror root rejected: %v", err)
	}
}

func TestWriteRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.sh")
	in := &Config{RemoteHost: `we"ird$host`, MirrorRoot: "/srv/m`x", PathIdentity: true}
	if err := Write(path, in); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	vals, err := Parse(data, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	out := FromValues(vals, noEnv)
	if out.RemoteHost != in.RemoteHost || out.MirrorRoot != in.MirrorRoot || !out.PathIdentity {
		t.Errorf("round trip = %+v, want %+v", out, in)
	}
}
