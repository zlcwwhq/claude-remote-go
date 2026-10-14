package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// Parse reads shell-style assignments:
//
//	# comment
//	KEY="double quoted, $VAR and ${VAR} expand"
//	KEY='single quoted, literal'
//	export KEY=unquoted   # trailing comment
//
// Variables expand from earlier assignments, then from getenv. Anything that
// would need a real shell (command substitution, ${VAR:-x}, ...) is an error
// rather than being silently misread.
func Parse(data []byte, getenv func(string) string) (map[string]string, error) {
	vals := map[string]string{}
	lookup := func(k string) string {
		if v, ok := vals[k]; ok {
			return v
		}
		return getenv(k)
	}
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "export "); ok {
			line = strings.TrimLeft(rest, " \t")
		}
		key, raw, ok := strings.Cut(line, "=")
		if !ok || !validKey(key) {
			return nil, fmt.Errorf("line %d: expected KEY=value, got %q", i+1, line)
		}
		val, err := parseValue(raw, lookup)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		vals[key] = val
	}
	return vals, nil
}

func validKey(k string) bool {
	if k == "" {
		return false
	}
	for i, c := range k {
		alpha := c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		if !alpha && (i == 0 || c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func parseValue(s string, lookup func(string) string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); {
		switch c := s[i]; {
		case c == ' ' || c == '\t':
			if rest := strings.TrimLeft(s[i:], " \t"); rest != "" && rest[0] != '#' {
				return "", fmt.Errorf("unexpected text after value: %q", rest)
			}
			return b.String(), nil
		case c == '\'':
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				return "", errors.New("unterminated single quote")
			}
			b.WriteString(s[i+1 : i+1+end])
			i += end + 2
		case c == '"':
			n, err := parseDoubleQuoted(s[i:], lookup, &b)
			if err != nil {
				return "", err
			}
			i += n
		case c == '\\' && i+1 < len(s):
			b.WriteByte(s[i+1])
			i += 2
		case c == '$':
			n, v, err := expand(s[i:], lookup)
			if err != nil {
				return "", err
			}
			b.WriteString(v)
			i += n
		case c == '`':
			return "", errors.New("command substitution is not supported")
		case c == '~' && i == 0 && (len(s) == 1 || s[1] == '/'):
			home, _ := os.UserHomeDir()
			b.WriteString(home)
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), nil
}

// parseDoubleQuoted consumes a "..." segment at the start of s and returns how
// many bytes it used.
func parseDoubleQuoted(s string, lookup func(string) string, b *strings.Builder) (int, error) {
	for i := 1; i < len(s); {
		switch c := s[i]; {
		case c == '"':
			return i + 1, nil
		case c == '\\' && i+1 < len(s) && strings.IndexByte("\"\\$`", s[i+1]) >= 0:
			b.WriteByte(s[i+1])
			i += 2
		case c == '$':
			n, v, err := expand(s[i:], lookup)
			if err != nil {
				return 0, err
			}
			b.WriteString(v)
			i += n
		case c == '`':
			return 0, errors.New("command substitution is not supported")
		default:
			b.WriteByte(c)
			i++
		}
	}
	return 0, errors.New("unterminated double quote")
}

// expand resolves $NAME or ${NAME} at the start of s. A '$' that starts
// neither is kept literally, as the shell does.
func expand(s string, lookup func(string) string) (int, string, error) {
	if strings.HasPrefix(s, "$(") {
		return 0, "", errors.New("command substitution is not supported")
	}
	if strings.HasPrefix(s, "${") {
		end := strings.IndexByte(s, '}')
		if end < 0 {
			return 0, "", errors.New("unterminated ${")
		}
		name := s[2:end]
		if !validKey(name) {
			return 0, "", fmt.Errorf("unsupported expansion ${%s}", name)
		}
		return end + 1, lookup(name), nil
	}
	n := 1
	for n < len(s) && validKey(s[1:n+1]) {
		n++
	}
	if n == 1 {
		return 1, "$", nil
	}
	return n, lookup(s[1:n]), nil
}
