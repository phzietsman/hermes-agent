package agent

import (
	"context"

	"github.com/phzietsman/hermes-agent/internal/llm"
	"github.com/phzietsman/hermes-agent/internal/provider/anthropic"
)

// CallInput is one model call. Messages lead with the system message.
type CallInput struct {
	Messages []llm.Message
	Tools    []llm.ToolDef
	// MaxTokens caps the response; 0 means the model's ceiling.
	MaxTokens int
	// StaticPrefix is the stable tier of the system prompt, so the cache planner can give it its own breakpoint.
	StaticPrefix string
}

// Provider makes one model call and returns the normalised response. onText, when non-nil, sees text as it
// streams. Errors are returned as the transport produced them; the loop classifies them.
type Provider interface {
	Complete(ctx context.Context, in CallInput, onText func(string)) (llm.Response, error)
}

// AnthropicProvider calls the native Messages API.
type AnthropicProvider struct {
	Client        *anthropic.Client
	Model         string
	Reasoning     *anthropic.Reasoning
	OAuth         bool
	ContextLength int
	// CacheTTL is "5m" or "1h". Direct is true on api.anthropic.com, whose tools array accepts cache markers.
	CacheTTL string
	Direct   bool
	FastMode bool
}

// Complete plans the cache breakpoints (request-local; the transcript is not decorated), builds the request,
// streams it and normalises the reply.
func (p *AnthropicProvider) Complete(ctx context.Context, in CallInput, onText func(string)) (llm.Response, error) {
	plan := BuildPromptCachePlan(in.Messages, in.Tools, CacheOptions{TTL: p.CacheTTL, StaticSystemPrefix: in.StaticPrefix, DirectNativeToolCache: p.Direct})
	req, err := anthropic.BuildRequest(anthropic.RequestOptions{
		Model: p.Model, Messages: plan.Messages, Tools: plan.Tools, MaxTokens: in.MaxTokens, Reasoning: p.Reasoning,
		OAuth: p.OAuth, ContextLength: p.ContextLength, FastMode: p.FastMode,
	})
	if err != nil {
		return llm.Response{}, err
	}
	opts := anthropic.CallOptions{}
	if onText != nil {
		opts.OnEvent = func(ev anthropic.StreamEvent) error {
			if ev.Type != "content_block_delta" {
				return nil
			}
			if d, ok := ev.Data["delta"].(map[string]any); ok && d["type"] == "text_delta" {
				if t, ok := d["text"].(string); ok && t != "" {
					onText(t)
				}
			}
			return nil
		}
	}
	msg, err := p.Client.Create(ctx, req, opts)
	if err != nil {
		return llm.Response{}, err
	}
	known := map[string]bool{}
	for _, t := range in.Tools {
		known[t.Name] = true
	}
	return anthropic.Normalize(msg, anthropic.NormalizeOptions{StripToolPrefix: p.OAuth, ToolExists: func(n string) bool { return known[n] }})
}
