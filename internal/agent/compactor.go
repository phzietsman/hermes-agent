package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/phzietsman/hermes-agent/internal/agent/prompttext"
	"github.com/phzietsman/hermes-agent/internal/llm"
	"github.com/phzietsman/hermes-agent/internal/redact"
)

// ErrSummaryRejected marks a summarizer failure that is about this request (a refusal, a format error), not
// about reaching the model. The compressor then falls back to a deterministic summary instead of aborting.
var ErrSummaryRejected = errors.New("the summarizer rejected the request")

// ErrCompressionAborted means compression left the transcript unchanged: the summarizer could not be reached
// or returned nothing, and dropping the middle without a summary would lose the conversation. A cooldown is armed.
var ErrCompressionAborted = errors.New("compression aborted, transcript unchanged")

// failure cooldown ladder after consecutive summarizer failures.
var cooldownLadder = []time.Duration{60 * time.Second, 300 * time.Second, 900 * time.Second}

// CompressorConfig configures a Compressor.
type CompressorConfig struct {
	// ContextLength is the model's window. MaxTokens is the output reservation carved out of it.
	ContextLength int
	MaxTokens     int
	// ThresholdPercent is the share of the input budget at which compaction triggers (default 0.50, raised to
	// 0.75 for windows under 512K).
	ThresholdPercent float64
	// ProtectFirstN head messages are kept verbatim until the first compaction (default 3);
	// ProtectLastN is the tail floor in messages (default 20, capped at 8 when the tail budget decides).
	ProtectFirstN int
	ProtectLastN  int
	// SessionID names the session in the recovery footer.
	SessionID string
	// Summarize sends the summarizer prompt to a model and returns its text.
	Summarize func(ctx context.Context, prompt string) (string, error)
	// Now is the wall clock (the date in the prompt); Mono is the clock for cooldowns. Nil means time.Now.
	Now  func() time.Time
	Mono func() time.Time
}

// CompressionStats describes the last compression.
type CompressionStats struct {
	MessagesBefore, MessagesAfter int
	TokensBefore, TokensAfter     int
	Summarized                    int  // messages replaced by the summary
	Fallback                      bool // the summary is the deterministic fallback
}

// Compressor shrinks a transcript that has outgrown the window: old tool results become one-line stubs, the
// middle is replaced by a structured summary, and the head and a token-budget tail stay verbatim. It
// implements Compactor. It works on the transcript only; the system prompt is the caller's.
type Compressor struct {
	cfg CompressorConfig

	mu               sync.Mutex
	previousSummary  string
	compressionCount int
	cooldownUntil    time.Time
	failures         int
	ineffective      int
	stats            CompressionStats
}

// NewCompressor returns a Compressor with defaults filled in.
func NewCompressor(cfg CompressorConfig) *Compressor {
	if cfg.ThresholdPercent == 0 {
		cfg.ThresholdPercent = 0.50
	}
	if cfg.ProtectFirstN == 0 {
		cfg.ProtectFirstN = 3
	}
	if cfg.ProtectLastN == 0 {
		cfg.ProtectLastN = 20
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Mono == nil {
		cfg.Mono = time.Now
	}
	return &Compressor{cfg: cfg}
}

// Threshold is the prompt size, in tokens, at which compaction triggers.
func (c *Compressor) Threshold() int {
	return computeThresholdTokens(c.cfg.ContextLength, effectiveThresholdPercent(c.cfg.ContextLength, c.cfg.ThresholdPercent), c.cfg.MaxTokens)
}

// Stats is the outcome of the most recent compression.
func (c *Compressor) Stats() CompressionStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats
}

// ShouldCompress reports whether a prompt of promptTokens warrants compaction now. It is false during the
// cooldown after a failed summary and after two compactions in a row that reclaimed almost nothing: more
// passes would only break the cache prefix again.
func (c *Compressor) ShouldCompress(promptTokens int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if promptTokens < c.Threshold() {
		return false
	}
	return !c.cooldownActive() && c.ineffective < 2
}

func (c *Compressor) cooldownActive() bool { return c.cfg.Mono().Before(c.cooldownUntil) }

func isSummaryMessage(m llm.Message) bool {
	if m.Role != llm.RoleUser && m.Role != llm.RoleAssistant {
		return false
	}
	t := contentText(m.Content)
	return strings.HasPrefix(t, prompttext.C.SummaryPrefix) || strings.HasPrefix(t, prompttext.C.LegacySummaryPrefix) ||
		strings.Contains(t, prompttext.C.MergedSummaryDelimiter)
}

// IsSummaryMessage reports whether m is a compaction handoff (standalone or merged into another message).
func IsSummaryMessage(m llm.Message) bool { return isSummaryMessage(m) }

// stripSummaryPrefix returns the summary body without the prefix, the end marker and any merged prior context.
func stripSummaryPrefix(s string) string {
	t := pyStrip(s)
	c := prompttext.C
	if i := strings.Index(t, c.MergedSummaryDelimiter); i >= 0 {
		t = pyStrip(t[i+len(c.MergedSummaryDelimiter):])
	}
	for _, p := range []string{c.SummaryPrefix, c.LegacySummaryPrefix} {
		if strings.HasPrefix(t, p) {
			t = strings.TrimLeft(t[len(p):], " \t\r\n\v\f")
			break
		}
	}
	if i := strings.Index(t, c.EndMarker); i >= 0 {
		t = strings.TrimRight(t[:i], " \t\r\n\v\f")
	}
	return t
}

func withSummaryPrefix(body string) string {
	if body = stripSummaryPrefix(body); body != "" {
		return prompttext.C.SummaryPrefix + "\n" + body
	}
	return prompttext.C.SummaryPrefix
}

func hasUserText(msgs []llm.Message) bool {
	for _, m := range msgs {
		if m.Role == llm.RoleUser && strings.TrimSpace(contentText(m.Content)) != "" && !isSummaryMessage(m) {
			return true
		}
	}
	return false
}

func summaryBudget(middle []llm.Message, contextLength int) int {
	budget := int(float64(estimateMessages(middle)) * summaryRatio)
	ceiling := min(int(float64(contextLength)*0.05), summaryTokensCeiling)
	return max(minSummaryTokens, min(budget, ceiling))
}

// Compress returns the transcript with its middle replaced by a summary. The transcript is returned unchanged
// (and the error nil) when there is nothing worth compressing; ErrCompressionAborted when the summary could
// not be produced.
func (c *Compressor) Compress(ctx context.Context, history []llm.Message) ([]llm.Message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(history)
	head := c.headSize(history)
	if n <= head+3+1 {
		return history, nil
	}
	tailBudget := tailTokenBudget(c.cfg.ContextLength)
	pruned := pruneOldToolResults(history, c.cfg.ProtectLastN, tailBudget)
	start := alignForward(pruned, head)
	cut := findTailCut(pruned, start, tailBudget, c.cfg.ProtectLastN)
	if cut <= start {
		return history, nil
	}
	middle, folded := c.middleWithoutHandoffs(pruned[start:cut])
	if folded != "" {
		c.previousSummary = folded
	}
	if len(middle) == 0 {
		return history, nil
	}

	body, fallback, err := c.summarize(ctx, middle)
	if err != nil {
		return history, err
	}
	summary := withSummaryPrefix(body)
	out := assemble(pruned, start, cut, summary)
	out = sanitizeToolPairs(out)

	c.previousSummary = body
	c.compressionCount++
	before, after := estimateMessages(history), estimateMessages(out)
	if before > 0 && float64(before-after)/float64(before) < 0.10 {
		c.ineffective++
	} else {
		c.ineffective = 0
	}
	c.stats = CompressionStats{MessagesBefore: n, MessagesAfter: len(out), TokensBefore: before, TokensAfter: after,
		Summarized: len(middle), Fallback: fallback}
	return out, nil
}

// headSize is how many leading messages stay verbatim. Protection decays to zero after the first compaction so
// early turns do not fossilise.
func (c *Compressor) headSize(history []llm.Message) int {
	if c.compressionCount >= 1 || c.previousSummary != "" {
		return 0
	}
	for i := 0; i < min(len(history), c.cfg.ProtectFirstN+4); i++ {
		if isSummaryMessage(history[i]) {
			return 0
		}
	}
	return c.cfg.ProtectFirstN
}

// middleWithoutHandoffs removes earlier compaction handoffs from the window: their content already lives in
// the previous summary. It returns the newest one's body so the next summary updates it.
func (c *Compressor) middleWithoutHandoffs(window []llm.Message) ([]llm.Message, string) {
	out := make([]llm.Message, 0, len(window))
	newest := ""
	for _, m := range window {
		if isSummaryMessage(m) {
			newest = stripSummaryPrefix(contentText(m.Content))
			continue
		}
		out = append(out, m)
	}
	return out, newest
}

// summarize returns the summary body. On a summarizer failure it arms the cooldown and either aborts (the model
// could not be reached, or returned nothing) or, for a rejected request, falls back to a deterministic summary.
func (c *Compressor) summarize(ctx context.Context, middle []llm.Message) (body string, fallback bool, err error) {
	prompt := buildSummaryPrompt(boundSummaryInput(serializeForSummary(middle)), summaryBudget(middle, c.cfg.ContextLength), "",
		c.previousSummary, hasUserText(middle), today(c.cfg.Now()))
	text, err := c.cfg.Summarize(ctx, prompt)
	switch {
	case err == nil && strings.TrimSpace(text) != "":
		c.failures = 0
		c.cooldownUntil = time.Time{}
		return c.augment(stripSummaryPrefix(text), middle), false, nil
	case ctx.Err() != nil:
		return "", false, ctx.Err()
	case err != nil && errors.Is(err, ErrSummaryRejected):
		c.armCooldown()
		return c.augment(fmt.Sprintf("## Summary unavailable\nSummary generation was rejected, so %d earlier message(s) were dropped "+
			"without a summary.", len(middle)), middle), true, nil
	}
	c.armCooldown()
	if err == nil {
		err = errors.New("the summarizer returned no text")
	}
	return "", false, fmt.Errorf("%w: %v", ErrCompressionAborted, err)
}

func (c *Compressor) armCooldown() {
	d := cooldownLadder[min(c.failures, len(cooldownLadder)-1)]
	c.failures++
	c.cooldownUntil = c.cfg.Mono().Add(d)
}

// augment appends the deterministic sections: the user's own words verbatim, and how to recover the compacted
// region from session history.
func (c *Compressor) augment(body string, middle []llm.Message) string {
	t := prompttext.C
	if !strings.Contains(body, t.LeanUserMessagesHeading) {
		body += redact.Text(verbatimUserSection(middle))
	}
	if !strings.Contains(body, t.LeanRecoveryHeading) {
		body += recoveryFooter(c.cfg.SessionID, len(middle))
	}
	return body
}

// pruneOldToolResults returns a copy where tool results older than the protected tail are replaced by
// one-line stubs. The newest tool round, which the model has not read yet, is never touched.
func pruneOldToolResults(msgs []llm.Message, protectLast, tailBudget int) []llm.Message {
	out := append([]llm.Message(nil), msgs...)
	calls := map[string][2]string{}
	for _, m := range out {
		for _, tc := range m.ToolCalls {
			calls[tc.ID] = [2]string{tc.Function.Name, tc.Function.Arguments}
		}
	}
	boundary := pruneBoundary(out, protectLast, tailBudget)
	if spared := trailingToolStart(out); spared < boundary {
		boundary = spared
	}
	for i := 0; i < boundary; i++ {
		m := out[i]
		if m.Role != llm.RoleTool {
			continue
		}
		text := contentText(m.Content)
		if text == "" || text == prompttext.C.PrunedToolPlaceholder || strings.HasPrefix(text, "[Duplicate tool output") ||
			isSummaryStub(text) || len([]rune(text)) <= pruneMinChars {
			continue
		}
		call := calls[m.ToolCallID]
		name := call[0]
		if name == "" {
			name = "unknown"
		}
		m.Content = llm.Text(summarizeToolResult(name, call[1], text))
		out[i] = m
	}
	return out
}

func isSummaryStub(s string) bool {
	return strings.HasPrefix(s, "[") && strings.Contains(s, " chars)") && len(s) < 400
}

// maxTailMessageFloor caps the message-count floor so a bulky recent run stays prunable.
const maxTailMessageFloor = 8

func pruneBoundary(msgs []llm.Message, protectLast, tailBudget int) int {
	n := len(msgs)
	minProtect := min(protectLast, n, maxTailMessageFloor)
	acc := 0
	boundary := n
	for i := n - 1; i >= 0; i-- {
		cost := estimateMessage(msgs[i])
		if acc+cost > tailBudget && n-i >= minProtect {
			boundary = i
			break
		}
		acc += cost
		boundary = i
	}
	return min(boundary, n-minProtect)
}

// trailingToolStart is the index where the transcript's trailing run of tool results begins, or len(msgs).
func trailingToolStart(msgs []llm.Message) int {
	i := len(msgs)
	for i > 0 && msgs[i-1].Role == llm.RoleTool {
		i--
	}
	if i == len(msgs) {
		return i
	}
	// Include the assistant message that asked for them.
	if i > 0 && msgs[i-1].Role == llm.RoleAssistant && len(msgs[i-1].ToolCalls) > 0 {
		return i - 1
	}
	return i
}

func alignForward(msgs []llm.Message, idx int) int {
	for idx < len(msgs) && msgs[idx].Role == llm.RoleTool {
		idx++
	}
	return idx
}

// alignBackward pulls a cut back so a tool group is not split: when the cut lands among tool results the
// assistant message that asked for them moves into the tail too.
func alignBackward(msgs []llm.Message, idx int) int {
	if idx <= 0 || idx >= len(msgs) {
		return idx
	}
	check := idx - 1
	for check >= 0 && msgs[check].Role == llm.RoleTool {
		check--
	}
	if check >= 0 && msgs[check].Role == llm.RoleAssistant && len(msgs[check].ToolCalls) > 0 && check < idx {
		return check
	}
	return idx
}

// findTailCut is the first index of the verbatim tail: the newest messages that fit the token budget (with a
// small count floor), never splitting a tool group, and including the latest user message when that costs no
// more than the soft ceiling so the active request stays in view.
func findTailCut(msgs []llm.Message, headEnd, budget, protectLast int) int {
	n := len(msgs)
	available := max(0, n-headEnd-1)
	floor := max(3, min(protectLast, maxTailMessageFloor))
	minTail := 0
	if available > 1 {
		minTail = min(floor, max(3, available-2), available)
	}
	soft := budget * 3 / 2
	acc := 0
	cut := n
	for i := n - 1; i >= headEnd; i-- {
		cost := estimateMessage(msgs[i])
		if acc+cost > soft && n-i >= minTail {
			break
		}
		acc += cost
		cut = i
	}
	cut = min(cut, n-max(minTail, 1))
	if cut <= headEnd {
		cut = max(n-minTail, headEnd+1)
	}
	cut = alignBackward(msgs, cut)
	if lu := lastUserIndex(msgs, headEnd); lu >= headEnd && lu < cut && estimateMessages(msgs[lu:]) <= soft {
		cut = lu
	}
	return cut
}

func lastUserIndex(msgs []llm.Message, from int) int {
	for i := len(msgs) - 1; i >= from; i-- {
		if msgs[i].Role == llm.RoleUser && strings.TrimSpace(contentText(msgs[i].Content)) != "" && !isSummaryMessage(msgs[i]) {
			return i
		}
	}
	return -1
}

// assemble joins the head, the summary and the tail. The summary takes the role that keeps user and assistant
// alternating; when neither fits it is merged into the first tail message.
func assemble(msgs []llm.Message, start, cut int, summary string) []llm.Message {
	t := prompttext.C
	var head, tail []llm.Message
	for _, m := range msgs[:start] {
		if !isSummaryMessage(m) {
			head = append(head, m)
		}
	}
	for _, m := range msgs[cut:] {
		if !isSummaryMessage(m) {
			tail = append(tail, m)
		}
	}
	lastHead := llm.Role("")
	if len(head) > 0 {
		lastHead = head[len(head)-1].Role
	}
	firstTail, firstTailIdx := llm.Role(""), -1
	for i, m := range tail {
		if m.Role != llm.RoleTool {
			firstTail, firstTailIdx = m.Role, i
			break
		}
	}
	forceUser := len(head) == 0 || !hasUserText(append(append([]llm.Message(nil), head...), tail...))
	// Tool results stand on the user's side of the exchange: after them the next message must be the assistant's.
	if lastHead == llm.RoleTool {
		lastHead = llm.RoleUser
	}
	role := llm.RoleAssistant
	if lastHead == "" || lastHead == llm.RoleAssistant || forceUser {
		role = llm.RoleUser
	}
	merge := false
	if firstTail != "" && role == firstTail {
		flipped := llm.RoleUser
		if role == llm.RoleUser {
			flipped = llm.RoleAssistant
		}
		if flipped != lastHead && lastHead != "" && !forceUser {
			role = flipped
		} else {
			merge = len(tail) > 0
		}
	}
	out := append([]llm.Message(nil), head...)
	if !merge {
		out = append(out, llm.Message{Role: role, Content: llm.Text(summary + "\n\n" + t.EndMarker)})
		return append(out, tail...)
	}
	target := 0
	if forceUser && firstTailIdx >= 0 {
		target = firstTailIdx
	}
	for i, m := range tail {
		if i == target {
			old := contentText(m.Content)
			if forceUser && role == llm.RoleUser {
				m.Content = llm.Text(summary + "\n\n" + t.EndMarker + "\n\n" + old)
			} else {
				m.Content = llm.Text(t.MergedPriorContextHeader + "\n" + old + "\n\n" + t.MergedSummaryDelimiter + "\n\n" + summary + "\n\n" + t.EndMarker)
			}
		}
		out = append(out, m)
	}
	return out
}

// sanitizeToolPairs removes tool results whose call is gone and strips tool calls whose result is gone, so no
// pair is left half-open at the compaction boundary. The trailing assistant message's calls are in flight (the
// executor has not appended results yet) and are kept as they are.
func sanitizeToolPairs(msgs []llm.Message) []llm.Message {
	calls := map[string]bool{}
	results := map[string]bool{}
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			calls[tc.ID] = true
		}
		if m.Role == llm.RoleTool {
			results[m.ToolCallID] = true
		}
	}
	inflight := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != llm.RoleTool {
			if msgs[i].Role == llm.RoleAssistant {
				inflight = i
			}
			break
		}
	}
	out := make([]llm.Message, 0, len(msgs))
	for i, m := range msgs {
		if m.Role == llm.RoleTool && !calls[m.ToolCallID] {
			continue
		}
		if m.Role == llm.RoleAssistant && i != inflight && len(m.ToolCalls) > 0 {
			var kept []llm.ToolCall
			for _, tc := range m.ToolCalls {
				if results[tc.ID] {
					kept = append(kept, tc)
				}
			}
			if len(kept) != len(m.ToolCalls) {
				m = m.Clone()
				m.ToolCalls = kept
				// The ordered blocks and signed thinking describe the original turn; replaying them with calls
				// removed would be rejected.
				m.ContentBlocks, m.StashedBlocks, m.ReasoningDetails = nil, nil, nil
				if len(kept) == 0 {
					if strings.TrimSpace(contentText(m.Content)) == "" {
						m.Content = llm.Text("(tool call removed)")
					}
				}
			}
		}
		out = append(out, m)
	}
	return out
}
