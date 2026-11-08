// Package skills discovers SKILL.md files and resolves which skills are offered. It ports the
// scanning, filtering and catalog logic of agent/skill_utils.py, agent/skill_commands.py and the
// catalog half of tools/skills_tool.py.
//
// SKILL.md files are data and load unchanged from the Python tree. Not ported in Stage 1:
// project-local skills (trust-gated), the skill create dir, plugin-namespaced skills, bundles,
// inline shell expansion and message templating. Message building lives with the turn loop.
package skills

import (
	"regexp"
	"strings"

	"github.com/goccy/go-yaml"
)

var frontmatterEndRe = regexp.MustCompile(`\n---\s*\n`)

// ParseFrontmatter splits a SKILL.md into its YAML frontmatter and body. Content that does not
// open with a --- fence and close it on its own line has no frontmatter. A leading UTF-8 BOM is
// stripped first, or it would defeat the fence check. Malformed YAML falls back to splitting each
// line at its first colon, so one bad value does not hide the name and description. YAML that
// parses to something other than a mapping yields no frontmatter.
func ParseFrontmatter(content string) (map[string]any, string) {
	content = strings.TrimPrefix(content, "\ufeff")
	if !strings.HasPrefix(content, "---") {
		return map[string]any{}, content
	}
	loc := frontmatterEndRe.FindStringIndex(content[3:])
	if loc == nil {
		return map[string]any{}, content
	}
	front := content[3 : loc[0]+3]
	body := content[loc[1]+3:]

	var parsed any
	if err := yaml.Unmarshal([]byte(front), &parsed); err != nil {
		out := map[string]any{}
		for _, line := range strings.Split(strings.TrimSpace(front), "\n") {
			if k, v, ok := strings.Cut(line, ":"); ok {
				out[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
		return out, body
	}
	if m, ok := parsed.(map[string]any); ok {
		return m, body
	}
	return map[string]any{}, body
}

// hermesMetadata is the metadata.hermes mapping, or nil when absent or malformed.
func hermesMetadata(fm map[string]any) map[string]any {
	meta, _ := fm["metadata"].(map[string]any)
	h, _ := meta["hermes"].(map[string]any)
	return h
}

// Conditions are the conditional-activation lists from metadata.hermes. session_platforms is the
// gateway-channel gate (platforms the skill is for), unlike platforms: which is the host OS.
type Conditions struct {
	FallbackForToolsets []string
	RequiresToolsets    []string
	FallbackForTools    []string
	RequiresTools       []string
	SessionPlatforms    []string
}

// ExtractConditions reads the activation conditions; absent keys are empty.
func ExtractConditions(fm map[string]any) Conditions {
	h := hermesMetadata(fm)
	return Conditions{
		FallbackForToolsets: stringList(h["fallback_for_toolsets"]),
		RequiresToolsets:    stringList(h["requires_toolsets"]),
		FallbackForTools:    stringList(h["fallback_for_tools"]),
		RequiresTools:       stringList(h["requires_tools"]),
		SessionPlatforms:    stringList(h["session_platforms"]),
	}
}

// ConfigVar is a metadata.hermes.config declaration: a config.yaml key a skill reads.
type ConfigVar struct {
	Key         string
	Description string
	Default     any
	Prompt      string
}

// ExtractConfigVars reads metadata.hermes.config (one mapping or a list). Entries without a key or
// a description are skipped, the first declaration of a key wins, and Prompt defaults to the
// description.
func ExtractConfigVars(fm map[string]any) []ConfigVar {
	raw := hermesMetadata(fm)["config"]
	var items []any
	switch t := raw.(type) {
	case map[string]any:
		items = []any{t}
	case []any:
		items = t
	}
	var out []ConfigVar
	seen := map[string]bool{}
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		key, desc := strings.TrimSpace(toString(m["key"])), strings.TrimSpace(toString(m["description"]))
		if key == "" || desc == "" || seen[key] {
			continue
		}
		seen[key] = true
		v := ConfigVar{Key: key, Description: desc, Default: m["default"], Prompt: desc}
		if p, ok := m["prompt"].(string); ok && strings.TrimSpace(p) != "" {
			v.Prompt = strings.TrimSpace(p)
		}
		out = append(out, v)
	}
	return out
}

// stringList reads a YAML list of scalars as strings; a lone scalar is a one-item list.
func stringList(v any) []string {
	switch t := v.(type) {
	case nil:
		return nil
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			out = append(out, toString(x))
		}
		return out
	default:
		return []string{toString(t)}
	}
}
