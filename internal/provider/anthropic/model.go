// Package anthropic adapts the neutral conversation types to the Anthropic Messages API. It ports
// agent/anthropic_message_convert.py, agent/anthropic_adapter.py, agent/anthropic_thinking_policy.py,
// the Claude Code credential handling and the error classifier, for the native Anthropic endpoint
// only. Not ported: Kimi, DeepSeek, Bedrock, DashScope, Nous Portal and other third-party routes.
package anthropic

import (
	"regexp"
	"strconv"
	"strings"
)

// NormalizeModelName strips the anthropic/ prefix (case-insensitively) and converts version dots to
// hyphens for Claude ids (claude-opus-4.6 becomes claude-opus-4-6). Other names keep their dots.
func NormalizeModelName(model string) string {
	if strings.HasPrefix(strings.ToLower(model), "anthropic/") {
		model = model[len("anthropic/"):]
	}
	if strings.HasPrefix(strings.ToLower(model), "claude-") {
		model = strings.ReplaceAll(model, ".", "-")
	}
	return model
}

var (
	claudeVersionRe = regexp.MustCompile(`(?i)claude[-_.](opus|sonnet|fable|mythos)[-_.](\d+)`)
	lastTurnOnlyRe  = regexp.MustCompile(`(?i)haiku|claude[-_.]?3(\D|$)`)
)

type version struct{ major, minor int }

func (v version) atLeast(o version) bool {
	return v.major > o.major || (v.major == o.major && v.minor >= o.minor)
}

// claudeFamilyVersion reads family and version from ids such as claude-opus-4-5. A minor is one or
// two digits followed by the end or a separator, so a snapshot date (claude-opus-4-20250514) stays
// 4.0 rather than becoming 4.20250514.
func claudeFamilyVersion(model string) (family string, v version, ok bool) {
	s := strings.TrimSpace(model)
	loc := claudeVersionRe.FindStringSubmatchIndex(s)
	if loc == nil {
		return "", version{}, false
	}
	family = strings.ToLower(s[loc[2]:loc[3]])
	v.major, _ = strconv.Atoi(s[loc[4]:loc[5]])
	rest := s[loc[1]:]
	if len(rest) >= 2 && strings.ContainsRune("-_.", rune(rest[0])) {
		digits := 0
		for digits < len(rest)-1 && rest[1+digits] >= '0' && rest[1+digits] <= '9' {
			digits++
		}
		end := 1 + digits
		if digits >= 1 && digits <= 2 && (end == len(rest) || strings.ContainsRune("-_.", rune(rest[end]))) {
			v.minor, _ = strconv.Atoi(rest[1:end])
		}
	}
	return family, v, true
}

// ModelPreservesPriorThinking reports whether Anthropic keeps prior assistant thinking in the
// model-visible context. It does on Opus 4.5+, Sonnet 4.6+ and the newer Fable/Mythos families.
// Unknown Claude ids default to keep: replaying a block the API strips costs nothing, while
// stripping on a model that keeps it rewrites the cached prefix on every call. Haiku, Claude 3,
// Opus below 4.5 and Sonnet below 4.6 keep only the latest turn. Non-Claude ids never keep.
func ModelPreservesPriorThinking(model string) bool {
	if !strings.Contains(strings.ToLower(model), "claude") || lastTurnOnlyRe.MatchString(model) {
		return false
	}
	family, v, ok := claudeFamilyVersion(model)
	if !ok {
		return true
	}
	min := version{5, 0}
	switch family {
	case "opus":
		min = version{4, 5}
	case "sonnet":
		min = version{4, 6}
	}
	return v.atLeast(min)
}
