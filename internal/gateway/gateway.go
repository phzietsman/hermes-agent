package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/phzietsman/hermes-agent/internal/agent"
	"github.com/phzietsman/hermes-agent/internal/app"
	"github.com/phzietsman/hermes-agent/internal/approval"
	"github.com/phzietsman/hermes-agent/internal/cron"
	"github.com/phzietsman/hermes-agent/internal/llm"
)

// Options configure a Gateway.
type Options struct {
	RT       *app.Runtime
	Adapters []Adapter
	Pairing  *Pairing
	// EditInterval throttles edits of a streaming reply (default 1.5 s); TypingInterval repeats the typing
	// indicator (default 4 s); IdleEnd ends a session after this long without messages (default 60 min, 0 never);
	// ReapEvery is how often idle sessions are looked for (default 1 min).
	EditInterval, TypingInterval, IdleEnd, ReapEvery time.Duration
	// MaxQueue is how many messages may wait behind a running turn (default 20).
	MaxQueue int
	// ToolProgress shows a line per tool call while the agent works.
	ToolProgress bool
	// RunCron starts the cron ticker in this process.
	RunCron bool
	Now     func() time.Time
}

// Gateway serves chat platforms.
type Gateway struct {
	o        Options
	adapters map[string]Adapter

	ctx    context.Context
	cancel context.CancelFunc

	mu    sync.Mutex
	chats map[string]*chat
	wg    sync.WaitGroup
}

// New builds a Gateway. It registers itself as the runtime's cron deliverer.
func New(o Options) *Gateway {
	if o.EditInterval <= 0 {
		o.EditInterval = 1500 * time.Millisecond
	}
	if o.TypingInterval <= 0 {
		o.TypingInterval = 4 * time.Second
	}
	if o.ReapEvery <= 0 {
		o.ReapEvery = time.Minute
	}
	if o.MaxQueue <= 0 {
		o.MaxQueue = 20
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	g := &Gateway{o: o, adapters: map[string]Adapter{}, chats: map[string]*chat{}}
	for _, a := range o.Adapters {
		g.adapters[a.Name()] = a
	}
	g.ctx, g.cancel = context.WithCancel(context.Background())
	if o.RT != nil {
		o.RT.CronDeliverer = g
	}
	return g
}

// Run starts every adapter and blocks until ctx ends, then ends the sessions (so memory and reviews see them) and
// stops the adapters.
func (g *Gateway) Run(ctx context.Context) error {
	g.ctx, g.cancel = context.WithCancel(ctx)
	if len(g.adapters) == 0 {
		return errors.New("no chat platform is enabled: set gateway.platforms.<name>.enabled in config.yaml")
	}
	started := 0
	var firstErr error
	for _, a := range g.o.Adapters {
		if err := a.Start(g.ctx, g.Handle); err != nil {
			slog.Error("platform failed to start", "platform", a.Name(), "err", err)
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", a.Name(), err)
			}
			delete(g.adapters, a.Name())
			continue
		}
		started++
		slog.Info("platform connected", "platform", a.Name())
	}
	if started == 0 {
		return firstErr
	}
	if g.o.RunCron && g.o.RT != nil {
		g.wg.Add(1)
		go func() { defer g.wg.Done(); g.o.RT.Cron().Scheduler().Run(g.ctx) }()
	}
	if g.o.IdleEnd > 0 {
		g.wg.Add(1)
		go func() { defer g.wg.Done(); g.reap() }()
	}
	<-g.ctx.Done()
	g.shutdown()
	return nil
}

func (g *Gateway) shutdown() {
	g.mu.Lock()
	chats := make([]*chat, 0, len(g.chats))
	for _, c := range g.chats {
		chats = append(chats, c)
	}
	g.mu.Unlock()
	for _, c := range chats {
		c.stop()
	}
	g.wg.Wait()
	for _, c := range chats {
		c.endSession(context.Background())
	}
	for _, a := range g.adapters {
		_ = a.Stop(context.Background())
	}
}

func (g *Gateway) reap() {
	t := time.NewTicker(g.o.ReapEvery)
	defer t.Stop()
	for {
		select {
		case <-g.ctx.Done():
			return
		case <-t.C:
		}
		g.mu.Lock()
		var idle []*chat
		for _, c := range g.chats {
			if c.idleFor(g.o.Now()) > g.o.IdleEnd {
				idle = append(idle, c)
			}
		}
		g.mu.Unlock()
		for _, c := range idle {
			c.endSession(g.ctx)
		}
	}
}

func sessionKey(ev Event) string {
	k := ev.Platform + ":" + ev.ChatID
	if ev.ThreadID != "" {
		k += ":" + ev.ThreadID
	}
	return k
}

func (g *Gateway) chatFor(ev Event) *chat {
	key := sessionKey(ev)
	g.mu.Lock()
	defer g.mu.Unlock()
	c := g.chats[key]
	if c == nil {
		c = &chat{g: g, key: key, platform: ev.Platform, to: ev.Target, last: g.o.Now()}
		g.chats[key] = c
	}
	return c
}

// authorized applies gateway.platforms.<p>.allow_all, allowed_users and the paired list.
func (g *Gateway) authorized(ev Event) bool {
	cfg := g.o.RT.Config
	base := "gateway.platforms." + ev.Platform
	if cfg.Bool(base+".allow_all", false) {
		return true
	}
	if raw, ok := cfg.Get(base + ".allowed_users"); ok {
		if list, ok := raw.([]any); ok {
			for _, u := range list {
				if fmt.Sprint(u) == ev.UserID {
					return true
				}
			}
		}
	}
	return g.o.Pairing != nil && g.o.Pairing.Approved(ev.Platform, ev.UserID)
}

// Handle takes one inbound event. It never blocks on the agent: a turn runs on its own goroutine.
func (g *Gateway) Handle(ev Event) {
	if ev.DM {
		ev.Addressed = true
	}
	if !ev.Addressed || strings.TrimSpace(ev.Text) == "" {
		return
	}
	ad := g.adapters[ev.Platform]
	if ad == nil {
		return
	}
	if !g.authorized(ev) {
		g.unauthorized(ad, ev)
		return
	}
	g.chatFor(ev).accept(ev)
}

// unauthorized answers a stranger. In a private chat they get a pairing code to hand to the owner; in a group the bot
// stays silent (it does not announce itself to people it does not serve).
func (g *Gateway) unauthorized(ad Adapter, ev Event) {
	if !ev.DM || g.o.Pairing == nil {
		return
	}
	code, fresh, err := g.o.Pairing.Request(ev.Platform, ev.UserID, ev.UserName)
	msg := ""
	switch {
	case errors.Is(err, ErrPairingFull):
		msg = "I can't take new pairing requests right now. Ask the owner to approve or clear the waiting ones."
	case err != nil:
		slog.Warn("pairing request failed", "err", err)
		return
	case fresh:
		msg = fmt.Sprintf("I don't know you yet. Your pairing code is %s — ask the owner to run:\n\nhermes pairing approve %s %s", code, ev.Platform, code)
	default:
		return // already asked: stay quiet instead of repeating
	}
	if _, err := ad.Send(g.ctx, ev.Target, msg); err != nil {
		slog.Warn("pairing reply failed", "err", err)
	}
}

// ---- one conversation ----

type chat struct {
	g        *Gateway
	key      string
	platform string
	to       Target

	mu       sync.Mutex
	running  bool
	queue    []Event
	sess     *app.Session
	cancel   context.CancelFunc
	approval chan approval.Choice
	fresh    bool // /new: the next message starts a new session
	yolo     bool
	last     time.Time
	cur      *stream
	progress *progress
}

func (c *chat) idleFor(now time.Time) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running || c.sess == nil {
		return 0
	}
	return now.Sub(c.last)
}

func (c *chat) adapter() Adapter { return c.g.adapters[c.platform] }

func (c *chat) say(text string) {
	for _, piece := range splitText(text, c.adapter().MaxLen()) {
		if _, err := c.adapter().Send(c.g.ctx, c.to, piece); err != nil {
			slog.Warn("gateway send failed", "platform", c.platform, "err", err)
			return
		}
	}
}

const helpText = `I answer here like in the terminal. Commands:
/new — start a fresh conversation
/stop — interrupt what I'm doing
/status — what I'm doing and what's queued
/yolo — toggle: run dangerous commands without asking
/help — this list
While I work, new messages are queued; if I ask for approval, reply once, session, always or deny.`

var approvalWords = map[string]approval.Choice{
	"once": approval.Once, "o": approval.Once, "y": approval.Once, "yes": approval.Once, "approve": approval.Once, "/approve": approval.Once,
	"session": approval.Session, "s": approval.Session, "always": approval.Always, "a": approval.Always,
	"deny": approval.Deny, "d": approval.Deny, "n": approval.Deny, "no": approval.Deny, "/deny": approval.Deny,
}

// accept routes one authorised message. Control commands and approval answers are handled inline, even while a turn
// is running: queueing them behind the turn they control would deadlock it.
func (c *chat) accept(ev Event) {
	text := strings.TrimSpace(ev.Text)
	c.mu.Lock()
	c.last = c.g.o.Now()
	if c.approval != nil {
		if ch, ok := approvalWords[strings.ToLower(text)]; ok {
			select {
			case c.approval <- ch:
			default:
			}
			c.mu.Unlock()
			return
		}
	}
	if strings.HasPrefix(text, "/") {
		cmd, _, _ := strings.Cut(strings.ToLower(text), " ")
		cmd, _, _ = strings.Cut(cmd, "@") // /stop@botname in group chats
		switch cmd {
		case "/stop":
			running := c.running
			if running && c.cancel != nil {
				c.cancel()
			}
			c.queue = nil
			c.mu.Unlock()
			if running {
				c.say("⏹ Stopping.")
			} else {
				c.say("Nothing is running.")
			}
			return
		case "/new":
			if c.cancel != nil && c.running {
				c.cancel()
			}
			c.queue, c.fresh = nil, true
			c.mu.Unlock()
			go func() { c.endSession(c.g.ctx); c.say("Started a new conversation.") }()
			return
		case "/status":
			s := c.statusLocked()
			c.mu.Unlock()
			c.say(s)
			return
		case "/help", "/start":
			c.mu.Unlock()
			c.say(helpText)
			return
		case "/yolo":
			c.yolo = !c.yolo
			on, sess := c.yolo, c.sess
			c.mu.Unlock()
			if sess != nil {
				c.g.o.RT.Gate.SetSessionYolo(sess.ID, on)
			}
			c.say("yolo " + map[bool]string{true: "on (hardline blocks still apply)", false: "off"}[on])
			return
		}
	}
	if c.running {
		if len(c.queue) >= c.g.o.MaxQueue {
			c.mu.Unlock()
			c.say("The queue is full. /stop interrupts me.")
			return
		}
		c.queue = append(c.queue, ev)
		n := len(c.queue)
		c.mu.Unlock()
		c.say(fmt.Sprintf("Queued (%d waiting). /stop interrupts.", n))
		return
	}
	c.running = true
	c.mu.Unlock()
	c.g.wg.Add(1)
	go func() {
		defer c.g.wg.Done()
		c.loop(ev)
	}()
}

func (c *chat) statusLocked() string {
	id := "none yet"
	if c.sess != nil {
		id = c.sess.ID
	}
	return fmt.Sprintf("session %s\n%s\n%d queued", id, map[bool]string{true: "working", false: "idle"}[c.running], len(c.queue))
}

// stop interrupts a running turn (shutdown).
func (c *chat) stop() {
	c.mu.Lock()
	if c.cancel != nil {
		c.cancel()
	}
	c.mu.Unlock()
}

func (c *chat) loop(ev Event) {
	for {
		c.turn(ev)
		c.mu.Lock()
		if len(c.queue) == 0 || c.g.ctx.Err() != nil {
			c.running, c.cancel = false, nil
			c.mu.Unlock()
			return
		}
		ev = c.queue[0]
		c.queue = c.queue[1:]
		c.mu.Unlock()
	}
}

// ensureSession resumes the chat's latest stored session (a gateway restart keeps the conversation) or starts one.
func (c *chat) ensureSession(ctx context.Context, ev Event) (*app.Session, error) {
	c.mu.Lock()
	s, fresh := c.sess, c.fresh
	c.mu.Unlock()
	if s != nil {
		return s, nil
	}
	rt := c.g.o.RT
	id := ""
	if !fresh {
		id, _ = rt.Store.LatestBySessionKey(ctx, c.key)
	}
	s, err := rt.NewSession(ctx, app.SessionOptions{
		ID: id, Source: c.platform, Platform: c.platform, UserID: ev.UserID, ChatID: ev.ChatID, ThreadID: ev.ThreadID, SessionKey: c.key,
		Prompter: c, Events: agent.Events{
			Text:      func(t string) { c.withStream(func(s *stream) { s.Append(t) }) },
			ToolStart: c.toolStart,
		},
	})
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.sess, c.fresh = s, false
	yolo := c.yolo
	c.mu.Unlock()
	if yolo {
		rt.Gate.SetSessionYolo(s.ID, true)
	}
	return s, nil
}

func (c *chat) withStream(f func(*stream)) {
	c.mu.Lock()
	s := c.cur
	c.mu.Unlock()
	if s != nil {
		f(s)
	}
}

func (c *chat) toolStart(call llm.ToolCall) {
	c.withStream(func(s *stream) { s.Break() })
	if !c.g.o.ToolProgress {
		return
	}
	c.mu.Lock()
	p := c.progress
	c.mu.Unlock()
	if p != nil {
		p.add(call.Function.Name)
	}
}

// turn runs one message through the agent and shows the reply.
func (c *chat) turn(ev Event) {
	ctx, cancel := context.WithCancel(c.g.ctx)
	defer cancel()
	c.mu.Lock()
	c.cancel = cancel
	c.mu.Unlock()

	done := make(chan struct{})
	defer close(done)
	go c.typing(ctx, done)

	sess, err := c.ensureSession(ctx, ev)
	if err != nil {
		c.say("⚠️ I couldn't start a session: " + err.Error())
		return
	}
	st := newStream(c.g.ctx, c.adapter(), c.to, c.g.o.EditInterval, c.g.o.Now)
	c.mu.Lock()
	c.cur = st
	c.progress = nil
	if c.g.o.ToolProgress {
		c.progress = &progress{ad: c.adapter(), to: c.to, ctx: c.g.ctx}
	}
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.cur = nil; c.mu.Unlock() }()

	res, err := sess.Agent.RunTurn(ctx, ev.Text)
	c.mu.Lock()
	c.last = c.g.o.Now()
	c.mu.Unlock()
	switch {
	case err != nil:
		st.Finish("⚠️ " + err.Error())
	case res.Interrupted:
		if !st.Sent() {
			st.Finish("⏹ Stopped.")
		} else {
			st.Finish("")
		}
	case res.FinalResponse != "":
		st.Finish(res.FinalResponse)
	default:
		st.Finish("")
	}
}

func (c *chat) typing(ctx context.Context, done <-chan struct{}) {
	t := time.NewTicker(c.g.o.TypingInterval)
	defer t.Stop()
	for {
		_ = c.adapter().Typing(ctx, c.to)
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// endSession ends the conversation's session: memory and the learning loop see it end. The chat resumes it (or, after
// /new, starts another) on its next message.
func (c *chat) endSession(ctx context.Context) {
	c.mu.Lock()
	s := c.sess
	c.sess = nil
	c.mu.Unlock()
	if s == nil {
		return
	}
	s.End(context.WithoutCancel(ctx))
	s.Close()
}

// Ask is the approval prompt: it goes to the chat and waits for the person's reply. A reply is taken inline (see
// accept), so the turn that is blocked on this prompt never has to be unblocked by a queued message.
func (c *chat) Ask(ctx context.Context, r approval.Request) (approval.Choice, error) {
	ch := make(chan approval.Choice, 1)
	c.mu.Lock()
	c.approval = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.approval = nil; c.mu.Unlock() }()
	c.say(fmt.Sprintf("⚠️ Dangerous command (%s):\n\n%s\n\nReply: once, session, always or deny.", r.Description, r.Command))
	select {
	case choice := <-ch:
		return choice, nil
	case <-ctx.Done():
		return approval.Deny, ctx.Err()
	}
}

// ---- tool progress ----

// progress is one message that lists the tools the agent has called this turn, edited as it grows.
type progress struct {
	ad    Adapter
	to    Target
	ctx   context.Context
	mu    sync.Mutex
	lines []string
	id    string
}

func (p *progress) add(tool string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lines = append(p.lines, "⚙ "+tool)
	if len(p.lines) > 12 {
		p.lines = p.lines[len(p.lines)-12:]
	}
	text := strings.Join(p.lines, "\n")
	if p.id == "" {
		id, err := p.ad.Send(p.ctx, p.to, text)
		if err == nil {
			p.id = id
		}
		return
	}
	_ = p.ad.Edit(p.ctx, p.to, p.id, text)
}

// ---- cron delivery ----

// Deliver implements cron.Deliverer. A target is "origin" (the chat the job was created from), "local" (nothing),
// "<platform>" (that platform's home_channel), or "<platform>:<chat>[:<thread>]"; several may be comma-separated.
func (g *Gateway) Deliver(ctx context.Context, job cron.Job, text string) error {
	var errs []error
	for _, raw := range strings.Split(job.Deliver, ",") {
		tok := strings.TrimSpace(raw)
		if tok == "" || tok == "local" {
			continue
		}
		platform, to, err := g.resolve(tok, job)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		ad := g.adapters[platform]
		if ad == nil {
			errs = append(errs, fmt.Errorf("platform %q is not connected", platform))
			continue
		}
		for _, piece := range splitText(text, ad.MaxLen()) {
			if _, err := ad.Send(ctx, to, piece); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", tok, err))
				break
			}
		}
	}
	return errors.Join(errs...)
}

func (g *Gateway) resolve(tok string, job cron.Job) (string, Target, error) {
	if tok == "origin" {
		if job.Origin == nil {
			return "", Target{}, errors.New("the job has no origin chat")
		}
		return job.Origin.Platform, Target{ChatID: job.Origin.ChatID, ThreadID: job.Origin.ThreadID}, nil
	}
	parts := strings.SplitN(tok, ":", 3)
	if len(parts) == 1 {
		home := g.o.RT.Config.String("gateway.platforms."+parts[0]+".home_channel", "")
		if home == "" {
			return "", Target{}, fmt.Errorf("%s has no home_channel configured", parts[0])
		}
		parts = append([]string{parts[0]}, strings.SplitN(home, ":", 2)...)
	}
	t := Target{ChatID: parts[1]}
	if len(parts) > 2 {
		t.ThreadID = parts[2]
	}
	if t.ChatID == "" {
		return "", Target{}, fmt.Errorf("delivery target %q has no chat id", tok)
	}
	return parts[0], t, nil
}

var _ cron.Deliverer = (*Gateway)(nil)
var _ approval.Prompter = (*chat)(nil)
