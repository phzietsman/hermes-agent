package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/phzietsman/hermes-agent/internal/agent/prompttext"
	"github.com/phzietsman/hermes-agent/internal/llm"
	"github.com/phzietsman/hermes-agent/internal/provider/anthropic"
	"github.com/phzietsman/hermes-agent/internal/session"
)

const maxCompressAttempts = 3

const contextOverflowText = "The conversation no longer fits the model's context window and could not be shortened further. " +
	"Start a new session, or ask for a summary of this one first."

// callModel makes one call, retrying what the classifier says is retryable. Context overflow compresses the
// transcript and tries again; an auth failure gets one credential refresh; a rejected thinking signature
// replays without preserved thinking. Anything else ends the turn as a failure with a plain explanation.
func (a *Agent) callModel(ctx context.Context, t *turn) (llm.Response, error) {
	for attempt := 1; ; attempt++ {
		msgs := a.withSystem(outbound(a.history, t.stripThinking))
		resp, err := a.cfg.Provider.Complete(ctx, CallInput{Messages: msgs, Tools: a.tools(), MaxTokens: a.maxTokens(t), StaticPrefix: a.static}, a.cfg.Events.Text)
		if err == nil {
			return resp, nil
		}
		if ctx.Err() != nil {
			return llm.Response{}, ctx.Err()
		}
		ce := anthropic.Classify(err, anthropic.ClassifyOptions{
			Model: a.cfg.Model, ApproxTokens: t.lastPromptTokens, ContextLength: a.cfg.ContextLength, NumMessages: len(a.history),
		})
		switch {
		case ce.ShouldCompress:
			if err := a.compressForRetry(ctx, t, ce); err != nil {
				return llm.Response{}, err
			}
			attempt = 0 // compression is progress; the retry budget restarts
		case ce.IsAuth() && !t.authRetried && a.cfg.OnAuthError != nil:
			t.authRetried = true
			if !a.cfg.OnAuthError(ctx, err) {
				return llm.Response{}, failure{authFailureText(ce)}
			}
		case ce.Reason == anthropic.ReasonThinkingSignature && !t.stripThinking:
			t.stripThinking = true
			a.status("The provider rejected a thinking signature; retrying without preserved thinking")
		case ce.Retryable && attempt <= a.cfg.MaxRetries:
			wait, announce := anthropic.RetryWait(err, attempt, a.cfg.Backoff)
			if announce {
				a.status("Rate limited, retrying in %ds", int(wait.Seconds()))
			} else {
				a.status("API call failed (%s), retrying (%d/%d)", ce.Reason, attempt, a.cfg.MaxRetries)
			}
			if err := a.cfg.Sleep(ctx, wait); err != nil {
				return llm.Response{}, err
			}
		default:
			return llm.Response{}, failure{apiFailureText(ce, attempt > 1)}
		}
	}
}

func (a *Agent) maxTokens(t *turn) int {
	if t.outputCap > 0 {
		return t.outputCap
	}
	return a.cfg.MaxTokens
}

func authFailureText(ce anthropic.ClassifiedError) string {
	return "The provider rejected the credentials (" + ce.Message + "). Log in again or check the API key."
}

func apiFailureText(ce anthropic.ClassifiedError, retried bool) string {
	prefix := "The model call failed"
	if retried {
		prefix = "The model call kept failing"
	}
	switch ce.Reason {
	case anthropic.ReasonAuth:
		return authFailureText(ce)
	case anthropic.ReasonBilling:
		return "The provider reports no remaining credit: " + ce.Message
	case anthropic.ReasonContextOverflow, anthropic.ReasonPayloadTooLarge:
		return contextOverflowText
	}
	return fmt.Sprintf("%s (%s): %s", prefix, ce.Reason, ce.Message)
}

// compressForRetry shortens the transcript after an overflow so the call can be repeated.
func (a *Agent) compressForRetry(ctx context.Context, t *turn, ce anthropic.ClassifiedError) error {
	if a.cfg.Compactor == nil || t.compressAttempts >= maxCompressAttempts {
		return failure{contextOverflowText}
	}
	t.compressAttempts++
	a.status("The context window is full, compressing the conversation (%d/%d)", t.compressAttempts, maxCompressAttempts)
	if err := a.compress(ctx); err != nil {
		if errors.Is(err, ctx.Err()) && ctx.Err() != nil {
			return err
		}
		return failure{contextOverflowText}
	}
	return nil
}

// compress replaces the transcript with its compacted form and keeps the store in step: the old live rows are
// archived (still searchable) and the new transcript written under the same session id. The system prompt is
// rebuilt here and only here, because the cached prefix is being rewritten anyway.
func (a *Agent) compress(ctx context.Context) error {
	before := append([]llm.Message(nil), a.history...)
	out, err := a.cfg.Compactor.Compress(ctx, before)
	if err != nil {
		return err
	}
	if sameTranscript(before, out) {
		return nil
	}
	names := toolNames(before)
	rows := make([]session.Message, 0, len(out))
	for _, m := range out {
		row, err := toRow(a.cfg.SessionID, m, names[m.ToolCallID], "")
		if err != nil {
			return err
		}
		row.CompressedSummary = IsSummaryMessage(m)
		rows = append(rows, row)
	}
	bg := context.WithoutCancel(ctx)
	if err := a.cfg.Store.ReplaceLive(bg, a.cfg.SessionID, rows); err != nil {
		return err
	}
	a.history = out
	if st, ok := a.cfg.Compactor.(interface{ Stats() CompressionStats }); ok {
		s := st.Stats()
		a.status("Compressed %d messages to %d (about %d tokens saved)", s.MessagesBefore, s.MessagesAfter, s.TokensBefore-s.TokensAfter)
	}
	return a.rebuildSystemPrompt(bg)
}

// rebuildSystemPrompt refreshes the stored system prompt after a compaction and appends the compaction note.
// The stable tier is a prefix of the new text as before, so the static breakpoint stays valid.
func (a *Agent) rebuildSystemPrompt(ctx context.Context) error {
	parts := a.cfg.Prompt()
	text := parts.String() + "\n\n" + prompttext.C.CompressionNote
	if text == a.system {
		return nil
	}
	if err := a.cfg.Store.RewriteSystemPrompt(ctx, a.cfg.SessionID, text); err != nil {
		return err
	}
	a.system, a.static = text, parts.Stable
	return nil
}

func toolNames(msgs []llm.Message) map[string]string {
	names := map[string]string{}
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			names[tc.ID] = tc.Function.Name
		}
	}
	return names
}

func sameTranscript(a, b []llm.Message) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Role != b[i].Role || a[i].ToolCallID != b[i].ToolCallID || contentText(a[i].Content) != contentText(b[i].Content) ||
			len(a[i].ToolCalls) != len(b[i].ToolCalls) {
			return false
		}
	}
	return true
}

// ProviderSummarizer adapts a Provider to the compressor's summarizer: one tool-less user message, no output
// cap (a cap would truncate the summary mid-section). A refusal or a malformed request is reported as
// ErrSummaryRejected so the compressor can fall back; any other failure aborts the compression.
func ProviderSummarizer(p Provider) func(ctx context.Context, prompt string) (string, error) {
	return func(ctx context.Context, prompt string) (string, error) {
		resp, err := p.Complete(ctx, CallInput{Messages: []llm.Message{{Role: llm.RoleUser, Content: llm.Text(prompt)}}}, nil)
		if err != nil {
			switch anthropic.Classify(err, anthropic.ClassifyOptions{}).Reason {
			case anthropic.ReasonFormatError, anthropic.ReasonContentPolicyBlocked, anthropic.ReasonProviderPolicyBlocked:
				return "", fmt.Errorf("%w: %v", ErrSummaryRejected, err)
			}
			return "", err
		}
		if resp.FinishReason == llm.FinishContentFilter {
			return "", fmt.Errorf("%w: content filter", ErrSummaryRejected)
		}
		return deref(resp.Content), nil
	}
}
