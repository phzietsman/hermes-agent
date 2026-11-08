// Package memory connects the agent to long-term memory (Agent Memory, "SAM"): a frozen core profile for the
// system prompt, per-turn recall that never blocks long, writes that always go through a durable outbox, and the
// agent_memory_* tools. A service outage degrades to "no recall, writes queued"; it never fails a turn.
package memory

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/phzietsman/hermes-agent/internal/llm"
	"github.com/phzietsman/hermes-agent/internal/memory/sam"
)

const (
	outboxFile  = "agent-memory-outbox.jsonl"
	profileFile = "core_profile_cache.json"
	// profileBudget bounds the blocking core-profile fetch at session start: a slow service must not stall the
	// first prompt; the cached profile is used instead.
	profileBudget = 5 * time.Second
)

// Origin labels who made a write. A background-review fork sets "background_review".
const (
	OriginTool   = "assistant_tool"
	OriginReview = "background_review"
)

var skillHit = regexp.MustCompile(`^Skill '([^']+)':`)

// Provider is the memory of one session.
type Provider struct {
	settings *sam.Settings
	client   *sam.Client
	breaker  *sam.Breaker
	outbox   *sam.Outbox
	cache    sam.ProfileCache

	sessionID string
	// Origin labels memory writes made through the tools.
	Origin        string
	writesEnabled bool
	fork          bool
	coreText      string

	jobs      chan func()
	wg        sync.WaitGroup
	closeOnce sync.Once

	mu         sync.Mutex
	turns      int
	doneQuery  string
	doneText   string
	inflight   *flight
	remembered map[string]bool
}

type flight struct {
	query string
	done  chan struct{}
}

// Options configure one session's provider.
type Options struct {
	Home      string
	SessionID string
	// UserID, when set and the scope is the default, scopes recall and writes to "user/<id>".
	UserID string
	// ReadOnly sessions (cron, subagents, flushes) never write: they are not the user's conversation.
	ReadOnly bool
	// Client overrides the HTTP client (tests).
	Client *sam.Client
	// Fork marks a background-review fork: its writes carry origin=background_review, and it neither recalls nor
	// syncs (its turns carry the review harness, not the user's conversation).
	Fork bool
}

// New builds the provider, loads the core profile (frozen for the session: a change mid-session would break the
// cached prompt prefix) and starts the writer, which replays anything the outbox still holds.
func New(ctx context.Context, s *sam.Settings, o Options) *Provider {
	settings := *s
	if o.UserID != "" && settings.DefaultScope == "user/default" {
		settings.DefaultScope = "user/" + o.UserID
	}
	p := &Provider{
		settings: &settings, client: o.Client, breaker: sam.NewBreaker(settings.BreakerThreshold),
		outbox: sam.NewOutbox(filepath.Join(o.Home, outboxFile)), cache: sam.ProfileCache{Path: filepath.Join(o.Home, profileFile)},
		sessionID: o.SessionID, Origin: OriginTool, writesEnabled: !o.ReadOnly, fork: o.Fork, jobs: make(chan func(), 256),
		remembered: map[string]bool{},
	}
	if p.client == nil {
		p.client = settings.Client()
	} else {
		p.client.Context, p.client.Endpoint = orStr(p.client.Context, settings.Context), orStr(p.client.Endpoint, settings.Endpoint)
	}
	if p.fork {
		p.Origin = OriginReview
	}
	p.coreText = p.loadCoreProfile(ctx)
	p.wg.Add(1)
	go p.work()
	p.enqueue(p.replay)
	return p
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (p *Provider) loadCoreProfile(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, profileBudget)
	defer cancel()
	prof, err := p.client.Profile(ctx)
	if err != nil {
		if k, ok := sam.KindOf(err); ok && k != sam.Rejected {
			p.breaker.RecordFailure(k == sam.AuthFailed)
		} else if !ok {
			p.breaker.RecordFailure(false) // the budget ran out: the service is too slow to depend on
		}
		return p.cache.Load()
	}
	p.breaker.RecordOK()
	text := sam.FormatProfile(prof, p.settings.CoreProfileMaxChars)
	if text != "" {
		if err := p.cache.Save(text); err != nil {
			slog.Warn("agent memory: core profile cache not saved", "err", err)
		}
	}
	return text
}

// Close drains the writer (up to five seconds) and stops it.
func (p *Provider) Close() { p.closeOnce.Do(p.close) }

func (p *Provider) close() {
	close(p.jobs)
	done := make(chan struct{})
	go func() { p.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
}

func (p *Provider) work() {
	defer p.wg.Done()
	for job := range p.jobs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("agent memory writer job panicked", "panic", r)
				}
			}()
			job()
		}()
	}
}

// enqueue hands a job to the writer; a full queue drops the job (everything durable is in the outbox, and the next
// replay picks it up).
func (p *Provider) enqueue(f func()) {
	defer func() { _ = recover() }() // a send after Close
	select {
	case p.jobs <- f:
	default:
	}
}

func (p *Provider) active() bool { return !p.breaker.IsOpen() }

// Active reports whether memory is reachable (the breaker is closed).
func (p *Provider) Active() bool { return p.active() }

// Settings returns the effective settings.
func (p *Provider) Settings() *sam.Settings { return p.settings }

const guide = "You have persistent long-term memory (SurrealDB Agent Memory). Call agent_memory_remember for " +
	"durable facts, corrections, preferences and lessons; recalled memory appears under " +
	"\"## Recalled memory\". If the user refers to something you may have been told before and it is " +
	"not in context, call agent_memory_recall. Procedures belong in skills (skill_manage), not memory."

// SystemPromptBlock is the memory guide plus the core profile, frozen for the session.
func (p *Provider) SystemPromptBlock() string {
	if p.coreText != "" {
		return guide + "\n\n" + p.coreText
	}
	return guide
}

// ---- recall ----

func (p *Provider) recallText(ctx context.Context, query string) (string, error) {
	s := p.settings
	lens := []string{s.DefaultScope}
	if s.RecallMode == sam.RecallContext {
		body, err := p.client.QueryContext(ctx, query, s.TopK, lens)
		if err != nil {
			return "", err
		}
		body = strings.TrimSpace(body)
		if body == "" {
			return "", nil
		}
		return truncRunes("## Recalled memory\n"+body, s.MaxPrefetchChars), nil
	}
	hits, err := p.client.Recall(ctx, query, s.TopK, lens)
	if err != nil {
		return "", err
	}
	return FormatHits(hits, s.MaxPrefetchChars), nil
}

// FormatHits renders recalled hits, with a "Relevant skills" hint for hits that are skill pointers.
func FormatHits(hits []sam.Hit, limit int) string {
	var lines, skills []string
	seen := map[string]bool{}
	for _, h := range hits {
		if m := skillHit.FindStringSubmatch(h.Text); m != nil && !seen[m[1]] {
			seen[m[1]] = true
			skills = append(skills, m[1])
		}
		lines = append(lines, fmt.Sprintf("- %s (%.2f)", strings.TrimSpace(h.Text), h.Score))
	}
	if len(lines) == 0 {
		return ""
	}
	out := "## Recalled memory\n" + strings.Join(lines, "\n")
	if len(skills) > 0 {
		out += "\nRelevant skills: " + strings.Join(skills, ", ") + " (load with skill_view)"
	}
	return truncRunes(out, limit)
}

func truncRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// ensureRecall starts (or joins) a background recall for query; the channel closes when it has finished. The
// recall outlives the call that asked for it, so a slow answer still warms the next turn.
func (p *Provider) ensureRecall(query string) <-chan struct{} {
	p.mu.Lock()
	if f := p.inflight; f != nil && f.query == query {
		select {
		case <-f.done:
		default:
			p.mu.Unlock()
			return f.done
		}
	}
	f := &flight{query: query, done: make(chan struct{})}
	p.inflight = f
	p.mu.Unlock()
	go func() {
		defer close(f.done)
		ctx, cancel := context.WithTimeout(context.Background(), p.settings.Timeout)
		defer cancel()
		text, err := p.recallText(ctx, query)
		switch {
		case err == nil:
			p.mu.Lock()
			p.doneQuery, p.doneText = query, text
			p.mu.Unlock()
			p.breaker.RecordOK()
		case sam.IsTimeout(err):
			// slow is not down: serve the last completed recall
		default:
			if k, ok := sam.KindOf(err); ok && k != sam.Rejected {
				p.breaker.RecordFailure(k == sam.AuthFailed)
			}
		}
	}()
	return f.done
}

// Prefetch never blocks long: it waits up to prefetch_wait_s for this query's recall, else returns the last
// completed one (the usual case once QueueNext warmed it during the previous turn).
func (p *Provider) Prefetch(ctx context.Context, query string) string {
	if p.fork || !p.active() || strings.TrimSpace(query) == "" || p.settings.RecallMode == sam.RecallTools {
		return ""
	}
	p.mu.Lock()
	cached := p.doneQuery == query
	p.mu.Unlock()
	if !cached {
		select {
		case <-p.ensureRecall(query):
		case <-time.After(p.settings.PrefetchWait):
		case <-ctx.Done():
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.doneText
}

// ---- writes (all through the outbox) ----

func (p *Provider) write(op sam.Op, payload map[string]any) {
	if !p.writesEnabled {
		return
	}
	fresh, err := p.outbox.Append(op, payload)
	if err != nil {
		slog.Error("agent memory: write could not be queued", "op", op, "err", err)
		return
	}
	if fresh && p.active() {
		p.enqueue(p.replay)
	}
}

func (p *Provider) replay() {
	if !p.active() {
		return
	}
	ctx := context.Background()
	_, _ = p.outbox.Replay(ctx, func(ctx context.Context, e sam.Entry) error {
		err := sam.Apply(ctx, p.client, e)
		switch {
		case err == nil:
			p.breaker.RecordOK()
		case sam.IsRejected(err):
			slog.Warn("agent memory rejected a queued write; dropped", "op", e.Op, "err", err)
		default:
			if k, ok := sam.KindOf(err); ok {
				if p.breaker.RecordFailure(k == sam.AuthFailed) {
					slog.Warn("agent memory unavailable: degraded mode, writes are queued for replay", "err", err)
				}
			}
		}
		return err
	})
}

// sessionLabel: sessions are labels, not the service's session_id (it only accepts ids it issued itself).
func (p *Provider) sessionLabel() string {
	return "session=" + strings.ReplaceAll(p.sessionID, " ", "_")
}

// direct keeps user and assistant prose only: tool rows and system prompts never leave the machine.
func direct(msgs []sam.Message) []sam.Message {
	var out []sam.Message
	for _, m := range msgs {
		if (m.Role == "user" || m.Role == "assistant") && strings.TrimSpace(m.Content) != "" {
			out = append(out, m)
		}
	}
	return out
}

func itemsPayload(items []sam.Message) []any {
	out := make([]any, len(items))
	for i, m := range items {
		out[i] = map[string]any{"role": m.Role, "content": m.Content}
	}
	return out
}

// SyncTurn queues the finished turn and, every replay_every_n_turns turns, a replay of anything still pending.
func (p *Provider) SyncTurn(_ context.Context, user, assistant string) {
	if p.fork {
		return
	}
	items := direct([]sam.Message{{Role: "user", Content: user}, {Role: "assistant", Content: assistant}})
	if len(items) > 0 {
		p.write(sam.OpRememberMany, map[string]any{"items": itemsPayload(items), "scopes": p.settings.DefaultScope,
			"labels": []any{"origin=turn", p.sessionLabel()}})
	}
	p.mu.Lock()
	p.turns++
	replay := p.turns%p.settings.ReplayEveryNTurns == 0
	p.mu.Unlock()
	if replay {
		p.enqueue(p.replay)
	}
}

// messagesOf flattens a transcript to user/assistant text; a message with non-text content is skipped.
func messagesOf(history []llm.Message) []sam.Message {
	var out []sam.Message
	for _, m := range history {
		if (m.Role == llm.RoleUser || m.Role == llm.RoleAssistant) && !m.Content.IsParts() {
			out = append(out, sam.Message{Role: string(m.Role), Content: m.Content.AsText()})
		}
	}
	return out
}

// transcriptDigest hashes every message (Python's transcript_digest: a message without text content counts as
// "None"), so the same transcript is recognised at pre-compress and at session end.
func transcriptDigest(history []llm.Message) string {
	h := newDigest()
	for _, m := range history {
		content := "None"
		if !m.Content.IsEmpty() && !m.Content.IsParts() {
			content = m.Content.AsText()
		}
		h.add(string(m.Role), content)
	}
	return h.sum()
}

func (p *Provider) rememberTranscript(history []llm.Message) {
	if p.fork {
		return
	}
	msgs := messagesOf(history)
	items := direct(msgs)
	// pre-compress and session end often see the same transcript; the outbox only dedupes while an entry is
	// pending, so remember what was already queued once the writer has sent it.
	digest := transcriptDigest(history)
	p.mu.Lock()
	seen := p.remembered[digest]
	if len(items) > 0 {
		p.remembered[digest] = true
	}
	p.mu.Unlock()
	if len(items) > 0 && !seen {
		p.write(sam.OpRememberMany, map[string]any{"items": itemsPayload(items), "scopes": p.settings.DefaultScope,
			"labels": []any{"digest=" + digest, "origin=session", p.sessionLabel()}})
	}
}

// PreCompress queues the transcript the compaction is about to replace with a summary.
func (p *Provider) PreCompress(_ context.Context, history []llm.Message) {
	p.rememberTranscript(history)
}

// EndSession queues the transcript and, when configured, asks the service to consolidate.
func (p *Provider) EndSession(_ context.Context, history []llm.Message) {
	p.rememberTranscript(history)
	if !p.fork && p.settings.ConsolidateOnEnd && p.writesEnabled {
		p.enqueue(p.consolidate)
	}
}

func (p *Provider) consolidate() {
	if !p.active() {
		return
	}
	p.replay() // land pending writes first so consolidation sees them
	if _, err := p.client.Consolidate(context.Background()); err != nil {
		if k, ok := sam.KindOf(err); ok && k != sam.Rejected {
			p.breaker.RecordFailure(k == sam.AuthFailed)
		}
	}
}

// Flush blocks until the writer has handled everything queued so far (tests, and process exit).
func (p *Provider) Flush() {
	done := make(chan struct{})
	p.enqueueBlocking(func() { close(done) })
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}
}

func (p *Provider) enqueueBlocking(f func()) {
	defer func() { _ = recover() }()
	p.jobs <- f
}
