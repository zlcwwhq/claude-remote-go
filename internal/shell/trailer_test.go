package shell

import (
	"bytes"
	"testing"
)

const testMarker = "__CLAUDE_REMOTE_PWD_0123456789abcdef__"

// feed writes input to a fresh filter split into chunks at every possible
// boundary pair, and checks each split gives the same result.
func feed(t *testing.T, input string) (string, string, bool) {
	t.Helper()
	var out0, pwd0 string
	var found0 bool
	for i := 0; i <= len(input); i++ {
		for j := i; j <= len(input); j += max(1, len(input)/7) {
			var out bytes.Buffer
			f := newTrailerFilter(&out, testMarker)
			for _, chunk := range []string{input[:i], input[i:j], input[j:]} {
				_, _ = f.Write([]byte(chunk))
			}
			_ = f.Close()
			if i == 0 && j == 0 {
				out0, pwd0, found0 = out.String(), f.pwd, f.found
				continue
			}
			if out.String() != out0 || f.pwd != pwd0 || f.found != found0 {
				t.Fatalf("split %d/%d of %q: got (%q, %q, %v), unsplit gives (%q, %q, %v)",
					i, j, input, out.String(), f.pwd, f.found, out0, pwd0, found0)
			}
		}
	}
	return out0, pwd0, found0
}

func TestTrailerFilter(t *testing.T) {
	cases := []struct {
		name, in, out, pwd string
		found              bool
	}{
		{"plain trailer", "hello\n" + testMarker + "/home/u/x\n", "hello\n", "/home/u/x", true},
		{"no trailing newline in output", "abc" + testMarker + "/p\n", "abc", "/p", true},
		{"empty output", testMarker + "/p\n", "", "/p", true},
		{"no trailer", "just output\n", "just output\n", "", false},
		{"partial marker at end", "ends with __CLAUDE_REM", "ends with __CLAUDE_REM", "", false},
		{"trailer without newline", "x" + testMarker + "/p", "x" + testMarker + "/p", "", false},
		{
			"marker in ordinary output",
			"ps: printf '%s%s' '" + testMarker + "' \"$(pwd -P)\"\n" + testMarker + "/real\n",
			"ps: printf '%s%s' '" + testMarker + "' \"$(pwd -P)\"\n",
			"/real", true,
		},
		{"output after trailer", testMarker + "/p\nlate\n", "late\n", "/p", true},
		{"empty pwd is not a trailer", testMarker + "\nrest", testMarker + "\nrest", "", false},
		{"utf-8 passes through", "héllo ✓\n" + testMarker + "/p\n", "héllo ✓\n", "/p", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, pwd, found := feed(t, c.in)
			if out != c.out || pwd != c.pwd || found != c.found {
				t.Errorf("got (%q, %q, %v), want (%q, %q, %v)", out, pwd, found, c.out, c.pwd, c.found)
			}
		})
	}
}

func TestTrailerFilterStreams(t *testing.T) {
	var out bytes.Buffer
	f := newTrailerFilter(&out, testMarker)
	_, _ = f.Write([]byte("first line\n"))
	if out.String() != "first line\n" {
		t.Errorf("output held back: %q", out.String())
	}
	_, _ = f.Write([]byte("tail __CLAUDE"))
	if out.String() != "first line\ntail " {
		t.Errorf("possible marker start not held back: %q", out.String())
	}
}

func TestTrailerFilterBoundsHeldOutput(t *testing.T) {
	var out bytes.Buffer
	f := newTrailerFilter(&out, testMarker)
	_, _ = f.Write([]byte(testMarker))
	_, _ = f.Write(bytes.Repeat([]byte("x"), maxTrailer+1))
	if out.Len() == 0 {
		t.Error("held more than maxTrailer bytes waiting for a newline")
	}
}
