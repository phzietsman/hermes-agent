package skills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/phzietsman/hermes-agent/internal/config"
)

// essentialSkills can never be disabled: hermes-agent is the agent's own operating manual and the
// system prompt points at it unconditionally.
var essentialSkills = map[string]bool{"hermes-agent": true}

// Settings is what config.yaml says about skills: where to look and what is switched off.
type Settings struct {
	Roots    []Root
	Disabled map[string]bool
}

// LoadSettings reads skills.external_dirs, skills.disabled and skills.platform_disabled. platform
// is the session platform (telegram, discord, ...) or "" for none. The local <home>/skills root
// comes first and is kept even when missing; external dirs must exist.
func LoadSettings(home string, cfg *config.Config, platform string) Settings {
	local := filepath.Join(home, "skills")
	st := Settings{Roots: []Root{{Tier: TierLocal, Dir: local}}, Disabled: map[string]bool{}}

	localReal := resolvePath(local)
	seen := map[string]bool{local: true}
	raw, _ := cfg.Get("skills.external_dirs")
	for _, entry := range configStrList(raw) {
		p := entry
		p = os.ExpandEnv(p)
		if p == "~" || strings.HasPrefix(p, "~/") {
			if h, err := os.UserHomeDir(); err == nil {
				p = filepath.Join(h, strings.TrimPrefix(p, "~"))
			}
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(home, p)
		}
		p = resolvePath(p)
		if p == localReal || seen[p] || !isDir(p) {
			continue
		}
		seen[p] = true
		st.Roots = append(st.Roots, Root{Tier: TierExternal, Dir: p})
	}

	dis, _ := cfg.Get("skills.disabled")
	addAll(st.Disabled, ParseConfigStringList(dis))
	if platform != "" {
		if pd, ok := cfg.Get("skills.platform_disabled"); ok {
			if m, ok := pd.(map[string]any); ok {
				if list, ok := m[platform]; ok {
					addAll(st.Disabled, ParseConfigStringList(list))
				}
			}
		}
	}
	for name := range essentialSkills {
		delete(st.Disabled, name)
	}
	return st
}

func resolvePath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

func addAll(set map[string]bool, names []string) {
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			set[n] = true
		}
	}
}

// configStrList reads a scalar-or-list config entry as stripped, non-empty strings.
func configStrList(raw any) []string {
	var items []string
	switch t := raw.(type) {
	case string:
		items = []string{t}
	case []any:
		for _, x := range t {
			items = append(items, toString(x))
		}
	}
	var out []string
	for _, s := range items {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ParseConfigStringList normalises a config value that may hold a list written as a string. `hermes
// config set` stored lists as quoted JSON or Python-literal strings, and treating one as a single
// name would silently filter nothing. A scalar string still means one name.
func ParseConfigStringList(v any) []string {
	switch t := v.(type) {
	case string:
		if s := strings.TrimSpace(t); strings.HasPrefix(s, "[") {
			if list, ok := parseListLiteral(s); ok {
				return list
			}
		}
		return []string{t}
	case []any:
		out := make([]string, len(t))
		for i, x := range t {
			out[i] = toString(x)
		}
		return out
	}
	return nil
}

// parseListLiteral reads ["a", "b"] (JSON) or ['a', 'b'] (Python literal) of scalars.
func parseListLiteral(s string) ([]string, bool) {
	var j []any
	if err := json.Unmarshal([]byte(s), &j); err == nil {
		out := make([]string, len(j))
		for i, x := range j {
			out[i] = toString(x)
		}
		return out, true
	}
	if !strings.HasSuffix(s, "]") {
		return nil, false
	}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	if inner == "" {
		return []string{}, true
	}
	var out []string
	for _, part := range strings.Split(inner, ",") {
		part = strings.TrimSpace(part)
		if len(part) < 2 || part[0] != part[len(part)-1] || (part[0] != '\'' && part[0] != '"') {
			return nil, false
		}
		out = append(out, part[1:len(part)-1])
	}
	return out, true
}
