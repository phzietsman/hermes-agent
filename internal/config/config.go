package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
)

// Config is config.yaml deep-merged over Defaults, with ${VAR} references expanded.
// It is read-only after Load; later tasks add typed accessors next to the keys they own.
type Config struct {
	data map[string]any
}

// Load reads <home>/config.yaml. A missing or empty file yields the defaults. The root must be
// a mapping. References resolve through secrets (nil means the process environment only).
func Load(home string, secrets *Secrets) (*Config, error) {
	raw, err := os.ReadFile(filepath.Join(home, "config.yaml"))
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read config.yaml: %w", err)
	}
	user := map[string]any{}
	if len(strings.TrimSpace(string(raw))) > 0 {
		var doc any
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("parse config.yaml: %w", err)
		}
		switch m := doc.(type) {
		case nil:
		case map[string]any:
			user = m
		default:
			return nil, fmt.Errorf("config.yaml must be a mapping, got %T", doc)
		}
	}
	merged := deepMerge(Defaults(), user)
	return &Config{data: expandRefs(merged, secrets).(map[string]any)}, nil
}

// deepMerge returns base overlaid with over; nested maps merge, anything else is replaced.
func deepMerge(base, over map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(over))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		if om, ok := v.(map[string]any); ok {
			if bm, ok := out[k].(map[string]any); ok {
				out[k] = deepMerge(bm, om)
				continue
			}
		}
		out[k] = v
	}
	return out
}

var envRefRe = regexp.MustCompile(`\$\{([^}]+)\}`)
var sourcedRefRe = regexp.MustCompile(`^[a-z][a-z0-9_-]*:`)

// refName is the variable behind a ${...} body, or "" for a non-env source (vault:X) or an empty env:.
func refName(inner string) string {
	inner = strings.TrimSpace(inner)
	if rest, ok := strings.CutPrefix(inner, "env:"); ok {
		return strings.TrimSpace(rest)
	}
	if strings.Contains(inner, ":") && sourcedRefRe.MatchString(inner) {
		return ""
	}
	return inner
}

// expandRefs expands ${VAR} and ${env:VAR} in string values. Keys and non-strings are untouched,
// and unresolved references stay verbatim so callers can detect them.
func expandRefs(v any, secrets *Secrets) any {
	switch t := v.(type) {
	case string:
		return envRefRe.ReplaceAllStringFunc(t, func(m string) string {
			name := refName(m[2 : len(m)-1])
			if name == "" {
				return m
			}
			if val, ok := secrets.Lookup(name); ok {
				return val
			}
			return m
		})
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = expandRefs(x, secrets)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = expandRefs(x, secrets)
		}
		return out
	}
	return v
}

// Get looks up a dotted path such as "agent.max_turns".
func (c *Config) Get(path string) (any, bool) {
	var cur any = c.data
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[part]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// String returns the value at path, or def when unset, null or not a string.
func (c *Config) String(path, def string) string {
	if s, ok := c.value(path).(string); ok {
		return s
	}
	return def
}

// Int returns the integer at path (YAML ints and whole floats), or def.
func (c *Config) Int(path string, def int) int {
	switch n := c.value(path).(type) {
	case int:
		return n
	case int64:
		return int(n)
	case uint64:
		return int(n)
	case float64:
		if n == float64(int(n)) {
			return int(n)
		}
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
			return i
		}
	}
	return def
}

// Float returns the number at path, or def.
func (c *Config) Float(path string, def float64) float64 {
	switch n := c.value(path).(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case uint64:
		return float64(n)
	}
	return def
}

// Bool returns the boolean at path, or def. Strings true/false/yes/no/on/off/1/0 are accepted.
func (c *Config) Bool(path string, def bool) bool {
	switch b := c.value(path).(type) {
	case bool:
		return b
	case string:
		switch strings.ToLower(strings.TrimSpace(b)) {
		case "true", "yes", "on", "1":
			return true
		case "false", "no", "off", "0":
			return false
		}
	}
	return def
}

// Strings returns the list of strings at path (non-strings are skipped), or def.
func (c *Config) Strings(path string, def []string) []string {
	list, ok := c.value(path).([]any)
	if !ok {
		return def
	}
	out := make([]string, 0, len(list))
	for _, x := range list {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func (c *Config) value(path string) any {
	v, _ := c.Get(path)
	return v
}
