package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phzietsman/hermes-agent/internal/agent"
	"github.com/phzietsman/hermes-agent/internal/app"
	"github.com/phzietsman/hermes-agent/internal/config"
	"github.com/phzietsman/hermes-agent/internal/cron"
	"github.com/phzietsman/hermes-agent/internal/llm"
)

// ---- a fake platform ----

type sent struct {
	Op   string // send or edit
	To   Target
	ID   string
	Text string
}

type fakeAdapter struct {
	mu       sync.Mutex
	name     string
	maxLen   int
	log      []sent
	typing   int
	handle   func(Event)
	started  bool
	stopped  bool
	next     int
	failEdit bool
}

func newAdapter() *fakeAdapter { return &fakeAdapter{name: "fake", maxLen: 4096} }

func (a *fakeAdapter) Name() string { return a.name }
func (a *fakeAdapter) MaxLen() int  { return a.maxLen }
func (a *fakeAdapter) Start(_ context.Context, h func(Event)) error {
	a.mu.Lock()
	a.handle, a.started = h, true
	a.mu.Unlock()
	return nil
}
func (a *fakeAdapter) Stop(context.Context) error {
	a.mu.Lock()
	a.stopped = true
	a.mu.Unlock()
	return nil
}
func (a *fakeAdapter) Send(_ context.Context, to Target, text string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.next++
	id := fmt.Sprintf("m%d", a.next)
	a.log = append(a.log, sent{"send", to, id, text})
	return id, nil
}
func (a *fakeAdapter) Edit(_ context.Context, to Target, id, text string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.failEdit {
		return fmt.Errorf("message to edit not found")
	}
	a.log = append(a.log, sent{"edit", to, id, text})
	return nil
}
func (a *fakeAdapter) Typing(context.Context, Target) error {
	a.mu.Lock()
	a.typing++
	a.mu.Unlock()
	return nil
}

func (a *fakeAdapter) sends() []sent {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]sent(nil), a.log...)
}

// texts is what the chat finally shows: each message id's last text, in order of first appearance.
func (a *fakeAdapter) shown() []string {
	var order []string
	last := map[string]string{}
	for _, s := range a.sends() {
		if _, ok := last[s.ID]; !ok {
			order = append(order, s.ID)
		}
		last[s.ID] = s.Text
	}
	out := make([]string, len(order))
	for i, id := range order {
		out[i] = last[id]
	}
	return out
}

func (a *fakeAdapter) waitShown(t *testing.T, want string) {
	t.Helper()
	waitFor(t, func() bool {
		for _, s := range a.shown() {
			if strings.Contains(s, want) {
				return true
			}
		}
		return false
	}, "the chat to show "+want)
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// ---- a scripted model ----

type step struct {
	pieces []string      // streamed text
	gap    time.Duration // between pieces
	calls  []llm.ToolCall
	block  bool // wait for cancellation
}

type model struct {
	mu    sync.Mutex
	steps []step
	seen  [][]llm.Message
}

func (m *model) Complete(ctx context.Context, in agent.CallInput, onText func(string)) (llm.Response, error) {
	m.mu.Lock()
	n := len(m.seen)
	m.seen = append(m.seen, in.Messages)
	var st step
	if n < len(m.steps) {
		st = m.steps[n]
	} else {
		st = step{pieces: []string{"(out of script)"}}
	}
	m.mu.Unlock()
	if st.block {
		<-ctx.Done()
		return llm.Response{}, ctx.Err()
	}
	var all strings.Builder
	for i, p := range st.pieces {
		if i > 0 && st.gap > 0 {
			time.Sleep(st.gap)
		}
		all.WriteString(p)
		if onText != nil {
			onText(p)
		}
	}
	if len(st.calls) > 0 {
		txt := all.String()
		return llm.Response{FinishReason: "tool_calls", StopReason: "tool_use", ToolCalls: st.calls, Content: &txt}, nil
	}
	txt := all.String()
	return llm.Response{Content: &txt, FinishReason: "stop", StopReason: "end_turn"}, nil
}

func (m *model) calls() int { m.mu.Lock(); defer m.mu.Unlock(); return len(m.seen) }

type rig struct {
	t      *testing.T
	home   string
	rt     *app.Runtime
	ad     *fakeAdapter
	gw     *Gateway
	model  *model
	pair   *Pairing
	cancel context.CancelFunc
	done   chan struct{}
}

func newRig(t *testing.T, yaml string, steps ...step) *rig {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", "")
	home := t.TempDir()
	if yaml != "" {
		os.WriteFile(filepath.Join(home, "config.yaml"), []byte(yaml), 0o644)
	}
	r := &rig{t: t, home: home, ad: newAdapter(), model: &model{steps: steps}}
	r.boot()
	return r
}

func (r *rig) boot() {
	ctx := context.Background()
	rt, err := app.Open(config.WithHome(ctx, r.home), r.home, app.Options{Provider: r.model, LongRunning: true})
	if err != nil {
		r.t.Fatal(err)
	}
	r.rt = rt
	r.pair = NewPairing(r.home)
	r.ad.mu.Lock()
	r.ad.stopped, r.ad.started = false, false
	r.ad.mu.Unlock()
	r.gw = New(Options{RT: rt, Adapters: []Adapter{r.ad}, Pairing: r.pair, EditInterval: 20 * time.Millisecond, TypingInterval: 50 * time.Millisecond, MaxQueue: 3})
	rctx, cancel := context.WithCancel(ctx)
	r.cancel, r.done = cancel, make(chan struct{})
	go func() { r.gw.Run(rctx); close(r.done) }()
	waitFor(r.t, func() bool { r.ad.mu.Lock(); defer r.ad.mu.Unlock(); return r.ad.started }, "the adapter to start")
}

// restart stops the gateway and brings up a new one over the same home, as a process restart does.
func (r *rig) restart() {
	r.cancel()
	<-r.done
	r.rt.Close()
	r.boot()
}

func (r *rig) close() {
	r.cancel()
	<-r.done
	r.rt.Close()
}

func (r *rig) say(user, text string) {
	r.ad.handle(Event{Platform: "fake", Target: Target{ChatID: "chat1"}, UserID: user, UserName: "U" + user, Text: text, DM: true})
}

// Reviews are off: a review fork would consume scripted model steps meant for the conversation.
const allow = "auxiliary:\n  background_review:\n    enabled: false\ngateway:\n  platforms:\n    fake:\n      enabled: true\n      allowed_users: [42]\n"

// ---- tests ----

func TestStreamsAReplyByEditingOneMessage(t *testing.T) {
	r := newRig(t, allow, step{pieces: []string{"Hel", "lo ", "wor", "ld"}, gap: 60 * time.Millisecond})
	defer r.close()
	r.say("42", "hi")
	r.ad.waitShown(t, "Hello world")
	var sends, edits int
	for _, s := range r.ad.sends() {
		if s.Op == "send" {
			sends++
		} else {
			edits++
		}
	}
	if sends != 1 || edits < 1 {
		t.Errorf("a streamed reply is one message edited in place: %d sends, %d edits (%v)", sends, edits, r.ad.sends())
	}
	if got := r.ad.shown(); len(got) != 1 || got[0] != "Hello world" {
		t.Errorf("%v", got)
	}
	if r.ad.typing == 0 {
		t.Error("no typing indicator while working")
	}
}

func TestEditFailureFallsBackToANewMessage(t *testing.T) {
	r := newRig(t, allow, step{pieces: []string{"one", " two"}, gap: 60 * time.Millisecond})
	defer r.close()
	r.ad.failEdit = true
	r.say("42", "hi")
	r.ad.waitShown(t, "one two")
}

func TestLongRepliesContinueInANewMessage(t *testing.T) {
	long := strings.Repeat("word ", 50)
	r := newRig(t, allow, step{pieces: []string{long}})
	defer r.close()
	r.ad.maxLen = 100
	r.say("42", "hi")
	waitFor(t, func() bool { return len(r.ad.shown()) >= 3 }, "the reply to be split")
	var total string
	for _, s := range r.ad.shown() {
		if len([]rune(s)) > 100 {
			t.Errorf("a message exceeds the platform limit: %d", len([]rune(s)))
		}
		total += s
	}
	if strings.TrimSpace(total) != strings.TrimSpace(long) {
		t.Errorf("text lost or duplicated in the split:\n%q\n%q", total, long)
	}
}

func TestSplitTextPrefersBreaks(t *testing.T) {
	got := splitText("aaaa bbbb\n\ncccc dddd eeee", 12)
	if len(got) < 2 || got[0] != "aaaa bbbb\n\n" || strings.Join(got, "") != "aaaa bbbb\n\ncccc dddd eeee" {
		t.Errorf("%q", got)
	}
	if got := splitText("short", 100); len(got) != 1 {
		t.Errorf("%q", got)
	}
	if got := splitText("xxxxxxxxxxxx", 5); len(got) != 3 || strings.Join(got, "") != "xxxxxxxxxxxx" {
		t.Errorf("a word longer than the limit is cut: %q", got)
	}
	if got := splitText("日本語日本語日本語", 4); strings.Join(got, "") != "日本語日本語日本語" || len([]rune(got[0])) > 4 {
		t.Errorf("limits count characters, not bytes: %q", got)
	}
}

func TestStrangersGetAPairingCodeAndTheOwnerApproves(t *testing.T) {
	r := newRig(t, allow, step{pieces: []string{"welcome"}})
	defer r.close()
	r.say("77", "hello?")
	waitFor(t, func() bool { return len(r.ad.shown()) == 1 }, "a pairing reply")
	msg := r.ad.shown()[0]
	code := regexpCode(t, msg)
	if !strings.Contains(msg, "hermes pairing approve fake "+code) {
		t.Errorf("%q", msg)
	}
	r.say("77", "hello again?") // a repeat does not mint or repeat anything
	time.Sleep(100 * time.Millisecond)
	if len(r.ad.shown()) != 1 || r.model.calls() != 0 {
		t.Errorf("a stranger reached the model or got a second reply: %v calls=%d", r.ad.shown(), r.model.calls())
	}
	if uid, err := r.pair.Approve("fake", code); err != nil || uid != "77" {
		t.Fatalf("%q %v", uid, err)
	}
	r.say("77", "now?")
	r.ad.waitShown(t, "welcome")
}

func regexpCode(t *testing.T, s string) string {
	i := strings.Index(s, "code is ")
	if i < 0 {
		t.Fatalf("no code in %q", s)
	}
	return strings.TrimSpace(strings.Fields(s[i+len("code is "):])[0])
}

func TestGroupsStaySilentForStrangersAndUnaddressedMessages(t *testing.T) {
	r := newRig(t, allow, step{pieces: []string{"ok"}})
	defer r.close()
	grp := Target{ChatID: "group1"}
	r.ad.handle(Event{Platform: "fake", Target: grp, UserID: "99", Text: "hey bot", Addressed: true}) // stranger in a group
	r.ad.handle(Event{Platform: "fake", Target: grp, UserID: "42", Text: "chatting among ourselves"}) // allowed, not addressed
	time.Sleep(150 * time.Millisecond)
	if len(r.ad.sends()) != 0 || r.model.calls() != 0 {
		t.Errorf("the bot spoke or ran: %v", r.ad.sends())
	}
	r.ad.handle(Event{Platform: "fake", Target: grp, UserID: "42", Text: "@bot hello", Addressed: true})
	r.ad.waitShown(t, "ok")
}

func TestPairingCapAndExpiry(t *testing.T) {
	p := NewPairing(t.TempDir())
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	p.Now = func() time.Time { return now }
	for i := range 3 {
		if _, fresh, err := p.Request("fake", fmt.Sprint(i), ""); err != nil || !fresh {
			t.Fatal(err)
		}
	}
	if _, _, err := p.Request("fake", "9", ""); err != ErrPairingFull {
		t.Errorf("%v", err)
	}
	code, fresh, _ := p.Request("fake", "1", "")
	if fresh || code == "" {
		t.Error("the same person asking again gets the same code")
	}
	now = now.Add(2 * time.Hour)
	if _, fresh, err := p.Request("fake", "9", ""); err != nil || !fresh {
		t.Errorf("expired requests must free their slots: %v", err)
	}
	if _, err := p.Approve("fake", code); err == nil {
		t.Error("an expired code was approved")
	}
	c, _, _ := p.Request("fake", "5", "Eve")
	if _, err := p.Approve("fake", " "+strings.ToLower(c)+" "); err != nil {
		t.Errorf("codes are case-insensitive: %v", err)
	}
	if !p.Approved("fake", "5") {
		t.Error("not approved")
	}
	appr, pend, _ := p.List("fake")
	if appr["5"] != "Eve" || len(pend) != 1 {
		t.Errorf("%v %v", appr, pend)
	}
	if err := p.Revoke("fake", "5"); err != nil || p.Approved("fake", "5") {
		t.Errorf("revoke: %v", err)
	}
	if err := p.Revoke("fake", "5"); err == nil {
		t.Error("revoking twice")
	}
	if p.Approved("other", "5") {
		t.Error("approvals are per platform")
	}
}

func TestBusyChatQueuesInOrderAndStopInterrupts(t *testing.T) {
	r := newRig(t, allow,
		step{pieces: []string{"first"}, gap: 0},
		step{pieces: []string{"second"}},
	)
	defer r.close()
	r.model.mu.Lock()
	r.model.steps = []step{{block: true}, {pieces: []string{"after stop"}}}
	r.model.mu.Unlock()
	r.say("42", "long task")
	waitFor(t, func() bool { return r.model.calls() == 1 }, "the first turn to start")
	r.say("42", "next thing")
	r.ad.waitShown(t, "Queued (1 waiting)")
	r.say("42", "/status")
	r.ad.waitShown(t, "1 queued")
	r.say("42", "/stop")
	r.ad.waitShown(t, "Stopping")
	// Stopping interrupts the running turn and clears the queue: nothing else runs.
	waitFor(t, func() bool { return strings.Contains(strings.Join(r.ad.shown(), "|"), "Stopped") }, "the stop notice")
	time.Sleep(100 * time.Millisecond)
	if r.model.calls() != 1 {
		t.Errorf("a queued message ran after /stop: %d calls", r.model.calls())
	}
	r.say("42", "fresh start")
	r.ad.waitShown(t, "after stop")
}

func TestQueueFullIsSaidOutLoud(t *testing.T) {
	r := newRig(t, allow, step{block: true})
	defer r.close()
	r.say("42", "go")
	waitFor(t, func() bool { return r.model.calls() == 1 }, "start")
	for i := range 4 {
		r.say("42", fmt.Sprint("m", i))
	}
	r.ad.waitShown(t, "queue is full")
}

func TestNewStartsAFreshSessionAndRestartResumesTheOldOne(t *testing.T) {
	r := newRig(t, allow, step{pieces: []string{"one"}}, step{pieces: []string{"two"}}, step{pieces: []string{"three"}}, step{pieces: []string{"four"}})
	defer r.close()
	r.say("42", "first message")
	r.ad.waitShown(t, "one")
	key := "fake:chat1"
	id1, _ := r.rt.Store.LatestBySessionKey(context.Background(), key)
	if id1 == "" {
		t.Fatal("the session is not recorded under the chat's key")
	}
	r.say("42", "/new")
	r.ad.waitShown(t, "Started a new conversation")
	r.say("42", "second message")
	r.ad.waitShown(t, "two")
	id2, _ := r.rt.Store.LatestBySessionKey(context.Background(), key)
	if id2 == id1 || id2 == "" {
		t.Errorf("/new must start a new session: %s %s", id1, id2)
	}
	// A restarted gateway picks the chat's latest session up again, history and all.
	r.restart()
	r.say("42", "third message")
	r.ad.waitShown(t, "three")
	if id3, _ := r.rt.Store.LatestBySessionKey(context.Background(), key); id3 != id2 {
		t.Errorf("a restart must resume the conversation: %s vs %s", id3, id2)
	}
	seen := r.model.seen[len(r.model.seen)-1]
	var texts []string
	for _, m := range seen {
		texts = append(texts, m.Content.AsText())
	}
	if joined := strings.Join(texts, "|"); !strings.Contains(joined, "second message") || strings.Contains(joined, "first message") {
		t.Errorf("history after resume: %q", joined)
	}
}

func TestDangerousCommandsAreApprovedInTheChat(t *testing.T) {
	victim := filepath.Join(t.TempDir(), "victim")
	os.WriteFile(victim, []byte("x"), 0o644)
	args, _ := json.Marshal(map[string]any{"command": "rm -rf " + victim})
	call := llm.ToolCall{ID: "c1", Type: "function", Function: llm.ToolCallFunction{Name: "terminal", Arguments: string(args)}}
	r := newRig(t, allow, step{calls: []llm.ToolCall{call}}, step{pieces: []string{"done"}})
	defer r.close()
	r.say("42", "clean up")
	r.ad.waitShown(t, "Dangerous command")
	// The reply reaches the blocked turn even though the chat is busy: it is not queued behind it.
	r.say("42", "deny")
	r.ad.waitShown(t, "done")
	if _, err := os.Stat(victim); err != nil {
		t.Error("a denied command ran")
	}
	if strings.Contains(strings.Join(r.ad.shown(), "|"), "Queued") {
		t.Error("the approval answer was queued instead of delivered")
	}

	// And an approval lets it run.
	os.WriteFile(victim, []byte("x"), 0o644)
	r.model.mu.Lock()
	r.model.steps = append(r.model.steps, step{calls: []llm.ToolCall{{ID: "c2", Type: "function", Function: llm.ToolCallFunction{Name: "terminal", Arguments: string(args)}}}}, step{pieces: []string{"removed"}})
	r.model.mu.Unlock()
	r.say("42", "again")
	waitFor(t, func() bool { return strings.Count(strings.Join(r.ad.shown(), "|"), "Dangerous command") == 2 }, "second prompt")
	r.say("42", "once")
	r.ad.waitShown(t, "removed")
	if _, err := os.Stat(victim); err == nil {
		t.Error("an approved command did not run")
	}
}

func TestToolProgressLinesAreShownWhenEnabled(t *testing.T) {
	call := llm.ToolCall{ID: "c1", Type: "function", Function: llm.ToolCallFunction{Name: "skills_list", Arguments: "{}"}}
	r := newRig(t, allow+"  tool_progress: true\n", step{calls: []llm.ToolCall{call}}, step{pieces: []string{"listed"}})
	defer r.close()
	r.gw.o.ToolProgress = true
	r.say("42", "what skills?")
	r.ad.waitShown(t, "listed")
	r.ad.waitShown(t, "⚙ skills_list")
}

func TestIdleSessionsEndAndResume(t *testing.T) {
	r := newRig(t, allow, step{pieces: []string{"hello"}}, step{pieces: []string{"again"}})
	defer r.close()
	r.cancel()
	<-r.done
	r.rt.Close()
	// Rebuild with a short idle limit.
	ctx := context.Background()
	rt, err := app.Open(config.WithHome(ctx, r.home), r.home, app.Options{Provider: r.model, LongRunning: true})
	if err != nil {
		t.Fatal(err)
	}
	r.rt = rt
	r.ad.mu.Lock()
	r.ad.started = false
	r.ad.mu.Unlock()
	r.gw = New(Options{RT: rt, Adapters: []Adapter{r.ad}, Pairing: r.pair, EditInterval: 10 * time.Millisecond, TypingInterval: time.Hour, IdleEnd: 150 * time.Millisecond, ReapEvery: 30 * time.Millisecond})
	rctx, cancel := context.WithCancel(ctx)
	r.cancel, r.done = cancel, make(chan struct{})
	go func() { r.gw.Run(rctx); close(r.done) }()
	waitFor(t, func() bool { r.ad.mu.Lock(); defer r.ad.mu.Unlock(); return r.ad.started }, "start")
	r.say("42", "hi")
	r.ad.waitShown(t, "hello")
	r.gw.mu.Lock()
	c := r.gw.chats["fake:chat1"]
	r.gw.mu.Unlock()
	waitFor(t, func() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.sess == nil }, "the idle session to end")
	r.say("42", "hi again")
	r.ad.waitShown(t, "again")
}

func TestShutdownEndsSessionsAndStopsAdapters(t *testing.T) {
	r := newRig(t, allow, step{pieces: []string{"hello"}})
	r.say("42", "hi")
	r.ad.waitShown(t, "hello")
	r.cancel()
	<-r.done
	r.ad.mu.Lock()
	stopped := r.ad.stopped
	r.ad.mu.Unlock()
	if !stopped {
		t.Error("the adapter was not stopped")
	}
	r.gw.mu.Lock()
	c := r.gw.chats["fake:chat1"]
	r.gw.mu.Unlock()
	if c.sess != nil {
		t.Error("the session was not ended at shutdown")
	}
	r.rt.Close()
}

func TestNoPlatformIsAnError(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	home := t.TempDir()
	rt, err := app.Open(config.WithHome(context.Background(), home), home, app.Options{Provider: &model{}})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if err := New(Options{RT: rt}).Run(context.Background()); err == nil || !strings.Contains(err.Error(), "enabled") {
		t.Errorf("%v", err)
	}
}

func TestCronDeliveryTargets(t *testing.T) {
	r := newRig(t, allow+"      home_channel: \"home9:55\"\n")
	defer r.close()
	ctx := context.Background()
	job := func(deliver string, o *cron.Origin) cron.Job { return cron.Job{ID: "j", Deliver: deliver, Origin: o} }
	if err := r.gw.Deliver(ctx, job("origin", &cron.Origin{Platform: "fake", ChatID: "c7", ThreadID: "t3"}), "report"); err != nil {
		t.Fatal(err)
	}
	if err := r.gw.Deliver(ctx, job("fake:c8,local", nil), "report2"); err != nil {
		t.Fatal(err)
	}
	if err := r.gw.Deliver(ctx, job("fake", nil), "report3"); err != nil {
		t.Fatal(err)
	}
	if err := r.gw.Deliver(ctx, job("local", nil), "never"); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range r.ad.sends() {
		got = append(got, s.To.ChatID+"/"+s.To.ThreadID+":"+s.Text)
	}
	want := []string{"c7/t3:report", "c8/:report2", "home9/55:report3"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("deliveries %v, want %v", got, want)
	}
	for name, j := range map[string]cron.Job{"no origin": job("origin", nil), "unknown platform": job("nowhere:1", nil), "no home": job("telegram", nil), "no chat": job("fake:", nil)} {
		if err := r.gw.Deliver(ctx, j, "x"); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestAgentCreatedCronJobsAddressTheirChat(t *testing.T) {
	args, _ := json.Marshal(map[string]any{"action": "create", "prompt": "remind me", "schedule": "in 30m"})
	call := llm.ToolCall{ID: "c1", Type: "function", Function: llm.ToolCallFunction{Name: "cronjob_manage", Arguments: string(args)}}
	r := newRig(t, allow, step{calls: []llm.ToolCall{call}}, step{pieces: []string{"scheduled"}})
	defer r.close()
	r.ad.handle(Event{Platform: "fake", Target: Target{ChatID: "chat1", ThreadID: "th1"}, UserID: "42", Text: "remind me in 30 minutes", DM: true})
	r.ad.waitShown(t, "scheduled")
	jobs, _ := r.rt.Cron().Store().List()
	if len(jobs) != 1 || jobs[0].Deliver != "origin" || jobs[0].Origin == nil || jobs[0].Origin.Platform != "fake" || jobs[0].Origin.ChatID != "chat1" || jobs[0].Origin.ThreadID != "th1" {
		t.Fatalf("%+v", jobs)
	}
}

func TestStopAndHelpWhenIdleAndUnknownSlashGoToTheAgent(t *testing.T) {
	r := newRig(t, allow, step{pieces: []string{"saw it"}})
	defer r.close()
	r.say("42", "/stop")
	r.ad.waitShown(t, "Nothing is running")
	r.say("42", "/help")
	r.ad.waitShown(t, "/new")
	r.say("42", "/yolo")
	r.ad.waitShown(t, "yolo on")
	r.say("42", "/yolo")
	r.ad.waitShown(t, "yolo off")
	r.say("42", "/etc/hosts is odd")
	r.ad.waitShown(t, "saw it")
}
