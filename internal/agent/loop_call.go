package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/phzietsman/hermes-agent/internal/llm"
	"github.com/phzietsman/hermes-agent/internal/provider/anthropic"
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

func (a *Agent) compress(ctx context.Context) error {
	out, err := a.cfg.Compactor.Compress(ctx, append([]llm.Message(nil), a.history...))
	if err != nil {
		return err
	}
	a.history = out
	return nil
}
