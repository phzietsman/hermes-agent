package agent

import (
	"context"
	"strings"
	"unicode"

	"github.com/phzietsman/hermes-agent/internal/llm"
	"github.com/phzietsman/hermes-agent/internal/provider/anthropic"
)

// Texts the model and the user see when a reply is cut off. The nudges are verbatim from Python.
const (
	continueNudge = "[System: Your previous response was truncated by the output length limit. Continue exactly " +
		"where you left off. Do not restart or repeat prior text. Finish the answer directly.]"
	truncatedText = "The model's reply was cut off before it finished (it hit its output length limit), so " +
		"Hermes didn't run the incomplete action. Nothing was changed. Send `continue`, ask for " +
		"the work in smaller steps, or raise max_tokens for this model."
	noVisibleAnswerText = "⚠️ **No visible answer was produced.** The model hit its output-token limit on every " +
		"continuation attempt — its reasoning consumed the entire budget each time.\n\nTo fix this:\n" +
		"→ Lower reasoning effort: `/reasoning low` or `/reasoning none`\n→ Or raise max_tokens for this model"
	windowFilledText = "⚠️ The prompt used %d of %d context tokens, leaving no room for an answer. " +
		"Start a new session or compress the conversation."
)

const (
	maxContinuations    = 4
	maxTruncToolTries   = 4
	minContinueHeadroom = 512
)

// boostedOutputCap is the output budget for truncation retry n (1-based): base·2ⁿ, never below the failed
// request's cap, at most max(32768, 2×cap), and never above the model's output limit.
func boostedOutputCap(model string, requested, configured, n int) int {
	limit := anthropic.MaxOutputTokens(model)
	base := configured
	if base == 0 {
		base = requested
	}
	if base == 0 {
		base = 4096
	}
	anchor := requested
	if anchor == 0 {
		anchor = base
	}
	if limit > 0 && anchor >= limit {
		return anchor
	}
	boost := min(max(base<<n, requested), max(32768, anchor*2))
	if limit > 0 {
		boost = min(boost, limit)
	}
	return boost
}

// handleLength recovers from a reply cut off by the output cap. Text is continued with a nudge up to four
// times and stitched into one answer; a cut-off tool call is retried with a larger cap and never executed
// incomplete; a reply that is all thinking is retried with a larger cap, since a nudge would put two user
// messages in a row.
func (a *Agent) handleLength(ctx context.Context, t *turn, resp llm.Response) (*TurnResult, error) {
	content := deref(resp.Content)
	if len(resp.ToolCalls) > 0 || strings.TrimSpace(content) == "" {
		if t.truncToolTries < maxTruncToolTries {
			t.truncToolTries++
			requested := a.maxTokens(t)
			if requested == 0 {
				requested = anthropic.MaxOutputTokens(a.cfg.Model)
			}
			t.outputCap = boostedOutputCap(a.cfg.Model, requested, a.cfg.MaxTokens, t.truncToolTries)
			a.status("Response truncated, retrying with a larger output limit (%d/%d)", t.truncToolTries, maxTruncToolTries)
			return nil, nil
		}
		if len(resp.ToolCalls) == 0 {
			return a.endTruncated(ctx, t, "", noVisibleAnswerText)
		}
		return a.failTurn(ctx, t, truncatedText)
	}
	if ceiling := a.cfg.ContextLength; ceiling > 0 && t.lastPromptTokens > ceiling-minContinueHeadroom {
		return a.endTruncated(ctx, t, content, sprintf(windowFilledText, t.lastPromptTokens, ceiling))
	}
	t.lengthContinues++
	if t.lengthContinues > maxContinuations {
		a.status("Response still truncated after %d continuation attempts", maxContinuations)
		return a.endTruncated(ctx, t, content, "")
	}
	t.fragments = append(t.fragments, content)
	a.status("Requesting continuation (%d/%d)", t.lengthContinues, maxContinuations)
	a.history = append(a.history, assistantMessage(resp, content, nil))
	a.history = append(a.history, llm.Message{Role: llm.RoleUser, Content: llm.Text(continueNudge)})
	t.trail += 2
	return nil, nil
}

// endTruncated finishes the turn with whatever text was received, stitched with earlier fragments into one
// stored assistant message, followed by a notice when there is one.
func (a *Agent) endTruncated(ctx context.Context, t *turn, last, notice string) (*TurnResult, error) {
	text := a.collapseTrail(t, last)
	a.dropTrail(t)
	switch {
	case notice != "" && strings.TrimSpace(text) != "":
		text += "\n\n" + notice
	case notice != "":
		text = notice
	}
	if err := a.add(ctx, llm.Message{Role: llm.RoleAssistant, Content: llm.Text(text)}, "", llm.FinishLength, true); err != nil {
		return nil, err
	}
	r := t.result
	r.Partial, r.Error, r.FinalResponse = true, "response truncated", text
	return &r, nil
}

func (a *Agent) joinWithFragments(t *turn, last string) string {
	return joinParts(append(append([]string(nil), t.fragments...), last))
}

// joinParts concatenates continuation fragments, adding a newline only where neither side has whitespace.
func joinParts(parts []string) string {
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		if s := b.String(); s != "" && !unicode.IsSpace(rune(s[len(s)-1])) && !unicode.IsSpace([]rune(p)[0]) {
			b.WriteByte('\n')
		}
		b.WriteString(p)
	}
	return b.String()
}

// dropTrail removes this turn's transient continuation messages from the transcript tail.
func (a *Agent) dropTrail(t *turn) {
	if t.trail > 0 {
		a.history = a.history[:len(a.history)-t.trail]
	}
	t.trail, t.fragments = 0, nil
}

// collapseTrail removes the continuation fragments and nudges and returns the stitched text including
// last. Nothing in the trail was stored, so the transcript only ever holds the one stitched answer.
func (a *Agent) collapseTrail(t *turn, last string) string {
	if len(t.fragments) == 0 {
		return last
	}
	text := a.joinWithFragments(t, last)
	a.dropTrail(t)
	return text
}
