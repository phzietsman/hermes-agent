package agent

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/phzietsman/hermes-agent/internal/agent/prompttext"
	"github.com/phzietsman/hermes-agent/internal/llm"
)

// Compaction thresholds, ported from agent/context_compressor.py.
const (
	minimumContextLength   = 64_000  // MINIMUM_CONTEXT_LENGTH: the trigger never sits below this
	smallCtxWindowLimit    = 512_000 // windows under this raise the trigger to at least smallCtxThresholdPct
	smallCtxThresholdPct   = 0.75
	minCtxTriggerRatio     = 0.85
	leanTailFloorTokens    = 10_000
	leanTailCapTokens      = 25_000
	tailMaxContextFraction = 0.20
)

// effectiveThresholdPercent raises (never lowers) the trigger for windows under 512K to at least 75%.
func effectiveThresholdPercent(contextLength int, pct float64) float64 {
	if contextLength > 0 && contextLength < smallCtxWindowLimit {
		return max(pct, smallCtxThresholdPct)
	}
	return pct
}

// computeThresholdTokens is the compaction trigger in tokens: the input budget (window minus the output
// reservation) times the percentage, floored at 64K, capped at 85% of the budget when the floor binds, and
// never at or above the budget itself.
func computeThresholdTokens(contextLength int, pct float64, maxTokens int) int {
	window := contextLength - maxTokens
	if window <= 0 {
		window = contextLength
	}
	pctValue := int(float64(window) * pct)
	floored := max(pctValue, minimumContextLength)
	triggerCap := int(float64(window) * minCtxTriggerRatio)
	if window > 0 && floored > pctValue && floored > triggerCap {
		floored = max(pctValue, triggerCap)
	}
	if window > 0 && floored >= window {
		return max(1, min(triggerCap, window-1))
	}
	return floored
}

// tailTokenBudget is how much recent conversation compaction keeps verbatim: a small clamped window, since
// the summary carries continuity.
func tailTokenBudget(contextLength int) int {
	budget := max(leanTailFloorTokens, min(leanTailCapTokens, int(float64(contextLength)*0.025)))
	if contextLength > 0 {
		budget = min(budget, int(float64(contextLength)*tailMaxContextFraction))
	}
	return max(1, budget)
}

// isDenseRune reports CJK, Hangul and Kana, which cost about one token per character.
func isDenseRune(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x11ff, r >= 0x2e80 && r <= 0x9fff, r >= 0xa960 && r <= 0xa97f,
		r >= 0xac00 && r <= 0xd7af, r >= 0xf900 && r <= 0xfaff, r >= 0xff00 && r <= 0xffef:
		return true
	}
	return false
}

// EstimateTokens is the rough token count of text: dense scripts one per character, everything else a quarter
// of the UTF-8 bytes rounded up.
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	ascii := true
	for i := 0; i < len(text); i++ {
		if text[i] >= utf8.RuneSelf {
			ascii = false
			break
		}
	}
	if ascii {
		return (len(text) + 3) / charsPerToken
	}
	dense, rest := 0, 0
	for _, r := range text {
		if isDenseRune(r) {
			dense++
		} else {
			rest += utf8.RuneLen(r)
		}
	}
	return dense + (rest+3)/charsPerToken
}

// estimateMessage prices one message: its text, the full tool-call envelope, an overhead for the role, and the
// reasoning that rides along.
func estimateMessage(m llm.Message) int {
	tokens := EstimateTokens(contentText(m.Content)) + 10
	for _, tc := range m.ToolCalls {
		b, _ := json.Marshal(tc)
		tokens += EstimateTokens(string(b))
	}
	if m.ReasoningContent != nil && strings.TrimSpace(*m.ReasoningContent) != "" {
		tokens += EstimateTokens(*m.ReasoningContent)
	} else {
		for _, d := range m.ReasoningDetails {
			if t, ok := d["thinking"].(string); ok {
				tokens += len(t) / charsPerToken
			}
		}
	}
	return tokens
}

func estimateMessages(msgs []llm.Message) int {
	n := 0
	for _, m := range msgs {
		n += estimateMessage(m)
	}
	return n
}

// withCommas renders n as Python's {:,} does.
func withCommas(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// elisionMarker is the non-imitable marker left where text was cut: it states it is not original content, so
// a model that copies it into new output is visibly wrong.
func elisionMarker(omitted, total int) string {
	r := strings.NewReplacer("{omitted:,}", withCommas(omitted), "{total:,}", withCommas(total))
	return r.Replace(prompttext.C.ElisionMarkerTemplate)
}

// elide caps text at limit characters with a marker, never exceeding limit unless the limit cannot hold the
// marker at all.
func elide(text string, limit int) string {
	r := []rune(text)
	if len(r) <= limit {
		return text
	}
	total := len(r)
	headLen := limit - utf8.RuneCountInString(elisionMarker(total, total))
	if headLen <= 0 {
		return elisionMarker(total, total)
	}
	kept := strings.TrimRight(string(r[:headLen]), " \t\r\n\v\f")
	return kept + elisionMarker(total-utf8.RuneCountInString(kept), total)
}

// elideMiddle keeps head characters from the start and tail from the end.
func elideMiddle(text string, head, tail int) string {
	r := []rune(text)
	if len(r) <= head+tail {
		return text
	}
	return string(r[:head]) + elisionMarker(len(r)-head-tail, len(r)) + string(r[len(r)-tail:])
}

var elisionMarkerMaxLen = utf8.RuneCountInString(elisionMarker(1e12-1, 1e12-1))
