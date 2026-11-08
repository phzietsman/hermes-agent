package config

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// ParseEnv tokenizes .env text. It is the single .env parser: BOM stripped, `export ` prefix,
// `#` comment lines, quoted values with \" and \\ escapes, dotenv-style inline comments.
// Invalid UTF-8 is decoded as latin-1. It never touches the process environment.
func ParseEnv(raw []byte) map[string]string {
	text := decodeEnv(raw)
	out := map[string]string{}
	for _, line := range splitLines(text) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimLeft(line[len("export "):], " \t")
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if ok && key != "" {
			out[key] = parseEnvValue(stripInlineComment(value))
		}
	}
	return out
}

func decodeEnv(raw []byte) string {
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	if utf8.Valid(raw) {
		return string(raw)
	}
	runes := make([]rune, len(raw))
	for i, b := range raw {
		runes[i] = rune(b)
	}
	return string(runes)
}

// splitLines splits on \n, \r\n and \r.
func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Split(s, "\n")
}

var inlineCommentRe = regexp.MustCompile(`\s+#`)

// stripInlineComment follows python-dotenv: a quoted value is scanned to its closing quote
// (backslash-aware inside double quotes) and a trailing comment dropped; an unquoted value is
// cut only at a # preceded by whitespace, so foo#bar survives and `value # c` becomes `value`.
func stripInlineComment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return value
	}
	if q := value[0]; q == '\'' || q == '"' {
		for i := 1; i < len(value); i++ {
			switch {
			case q == '"' && value[i] == '\\':
				i++
			case value[i] == q:
				if strings.HasPrefix(strings.TrimLeft(value[i+1:], " \t\r\n\f\v"), "#") {
					return value[:i+1]
				}
				return value
			}
		}
		return value // unterminated quote: leave as is
	}
	loc := inlineCommentRe.FindStringIndex(value)
	if loc == nil {
		return value
	}
	return strings.TrimSpace(value[:loc[0]])
}

// parseEnvValue unquotes the subset Hermes writes: bare, 'single', or "double" with \" and \\.
func parseEnvValue(raw string) string {
	v := strings.TrimSpace(raw)
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		q := v[1 : len(v)-1]
		var b strings.Builder
		for i := 0; i < len(q); i++ {
			if q[i] == '\\' && i+1 < len(q) && (q[i+1] == '"' || q[i+1] == '\\') {
				i++
			}
			b.WriteByte(q[i])
		}
		return b.String()
	}
	if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
		return v[1 : len(v)-1]
	}
	return v
}

// LoadEnvFile parses path. A missing or unreadable file yields an empty map.
func LoadEnvFile(path string) map[string]string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return map[string]string{}
	}
	return ParseEnv(raw)
}

// Secrets resolves credentials. A value defined in <home>/.env wins over the process
// environment (the user's .env overrides stale shell exports); anything else falls back
// to the process environment.
type Secrets struct {
	file map[string]string
}

// LoadSecrets reads <home>/.env.
func LoadSecrets(home string) *Secrets {
	return &Secrets{file: LoadEnvFile(filepath.Join(home, ".env"))}
}

// Lookup returns the value and whether it is set anywhere. An empty assignment in .env is a value.
func (s *Secrets) Lookup(name string) (string, bool) {
	if s != nil {
		if v, ok := s.file[name]; ok {
			return v, true
		}
	}
	return os.LookupEnv(name)
}

// Get returns the value or def when unset.
func (s *Secrets) Get(name, def string) string {
	if v, ok := s.Lookup(name); ok {
		return v
	}
	return def
}
