package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phzietsman/hermes-agent/internal/agent"
	"github.com/phzietsman/hermes-agent/internal/app"
	"github.com/phzietsman/hermes-agent/internal/config"
	"github.com/phzietsman/hermes-agent/internal/gateway"
	"github.com/phzietsman/hermes-agent/internal/llm"
)

const token = "123456:ABC-secret-token"

// fakeAPI is a Bot API server: it queues updates for getUpdates and records every other call.
type fakeAPI struct {
	*httptest.Server
	mu       sync.Mutex
	updates  []map[string]any
	calls    []call
	nextID   int64
	failNext map[string][]map[string]any // method -> responses to give before behaving
	polls    int
	offsets  []int64
}

type call struct {
	Method string
	Params map[string]any
}

func newAPI(t *testing.T) *fakeAPI {
	f := &fakeAPI{failNext: map[string][]map[string]any{}, nextID: 100}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prefix := "/bot" + token + "/"
		if !strings.HasPrefix(r.URL.Path, prefix) {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"ok":false,"error_code":404,"description":"Not Found"}`)
			return
		}
		method := strings.TrimPrefix(r.URL.Path, prefix)
		raw, _ := io.ReadAll(r.Body)
		var params map[string]any
		json.Unmarshal(raw, &params)
		f.mu.Lock()
		if q := f.failNext[method]; len(q) > 0 {
			resp := q[0]
			f.failNext[method] = q[1:]
			f.mu.Unlock()
			w.WriteHeader(int(num(resp["error_code"])))
			json.NewEncoder(w).Encode(resp)
			return
		}
		if method == "getUpdates" {
			f.polls++
			f.offsets = append(f.offsets, int64(num(params["offset"])))
			var out []map[string]any
			off := int64(num(params["offset"]))
			for _, u := range f.updates {
				if int64(num(u["update_id"])) >= off {
					out = append(out, u)
				}
			}
			f.mu.Unlock()
			if len(out) == 0 {
				time.Sleep(30 * time.Millisecond) // a short "long poll"
			}
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": append([]map[string]any{}, out...)})
			return
		}
		f.calls = append(f.calls, call{method, params})
		f.nextID++
		id := f.nextID
		f.mu.Unlock()
		switch method {
		case "getMe":
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"id": 999, "is_bot": true, "first_name": "Hermes", "username": "HermesBot"}})
		case "sendMessage":
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": id}})
		default:
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
		}
	}))
	t.Cleanup(f.Close)
	return f
}

// num reads a JSON-ish number whether it is an int (built in a test) or a float64 (decoded from a request).
func num(v any) float64 {
	switch n := v.(type) {
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case float64:
		return n
	}
	return 0
}

func (f *fakeAPI) push(u map[string]any) {
	f.mu.Lock()
	f.updates = append(f.updates, u)
	f.mu.Unlock()
}

func (f *fakeAPI) called(method string) []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []call
	for _, c := range f.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

func msg(updateID int, chat int64, chatType string, from int64, text string, extra map[string]any) map[string]any {
	m := map[string]any{"message_id": updateID * 10, "from": map[string]any{"id": from, "is_bot": false, "first_name": "Ann", "username": "ann"},
		"chat": map[string]any{"id": chat, "type": chatType}, "text": text}
	for k, v := range extra {
		m[k] = v
	}
	return map[string]any{"update_id": updateID, "message": m}
}

func newAdapter(t *testing.T, f *fakeAPI) *Adapter {
	t.Helper()
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, ".env"), []byte(TokenEnv+"="+token+"\n"), 0o600)
	os.WriteFile(filepath.Join(home, "config.yaml"), []byte("gateway:\n  platforms:\n    telegram:\n      base_url: "+f.URL+"\n      poll_timeout: 1\n"), 0o644)
	secrets := config.LoadSecrets(home)
	cfg, err := config.Load(home, secrets)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(TokenEnv, "")
	a, err := New(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	ad := a.(*Adapter)
	ad.Backoff, ad.MaxBackoff = 10*time.Millisecond, 40*time.Millisecond
	return ad
}

type inbox struct {
	mu sync.Mutex
	ev []gateway.Event
}

func (i *inbox) add(e gateway.Event) { i.mu.Lock(); i.ev = append(i.ev, e); i.mu.Unlock() }
func (i *inbox) all() []gateway.Event {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]gateway.Event(nil), i.ev...)
}
func (i *inbox) wait(t *testing.T, n int) []gateway.Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if e := i.all(); len(e) >= n {
			return e
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("got %d events, want %d", len(i.all()), n)
	return nil
}

func start(t *testing.T, f *fakeAPI) (*Adapter, *inbox) {
	ad := newAdapter(t, f)
	in := &inbox{}
	if err := ad.Start(context.Background(), in.add); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ad.Stop(context.Background()) })
	return ad, in
}

func TestNewNeedsATokenAndNamesWhereItGoes(t *testing.T) {
	t.Setenv(TokenEnv, "")
	home := t.TempDir()
	secrets := config.LoadSecrets(home)
	cfg, _ := config.Load(home, secrets)
	if _, err := New(cfg, secrets); err == nil || !strings.Contains(err.Error(), TokenEnv) || !strings.Contains(err.Error(), "BotFather") {
		t.Errorf("%v", err)
	}
}

func TestStartRejectsABadTokenWithoutLeakingIt(t *testing.T) {
	f := newAPI(t)
	ad := newAdapter(t, f)
	ad.token = "999:wrong-token"
	err := ad.Start(context.Background(), func(gateway.Event) {})
	if err == nil || strings.Contains(err.Error(), "wrong-token") {
		t.Errorf("a bad token must fail, and never be echoed: %v", err)
	}
	// A network failure carries the URL, which holds the token.
	f.Close()
	ad2 := newAdapter(t, newAPI(t))
	ad2.baseURL = f.URL
	err = ad2.Start(context.Background(), func(gateway.Event) {})
	if err == nil || strings.Contains(err.Error(), token) {
		t.Errorf("a transport error leaked the token: %v", err)
	}
}

func TestInboundDMBecomesAnEventAndIsAcknowledgedOnce(t *testing.T) {
	f := newAPI(t)
	f.push(msg(1, 555, "private", 42, "hello bot", nil))
	_, in := start(t, f)
	ev := in.wait(t, 1)[0]
	if ev.Platform != "telegram" || ev.ChatID != "555" || ev.UserID != "42" || ev.UserName != "ann" || ev.Text != "hello bot" || ev.MessageID != "10" || !ev.DM || !ev.Addressed || ev.ThreadID != "" {
		t.Errorf("%+v", ev)
	}
	time.Sleep(200 * time.Millisecond)
	if len(in.all()) != 1 {
		t.Errorf("an update was delivered %d times", len(in.all()))
	}
	f.mu.Lock()
	last := f.offsets[len(f.offsets)-1]
	f.mu.Unlock()
	if last != 2 {
		t.Errorf("the offset must acknowledge update 1: %d", last)
	}
}

func TestGroupMessagesAreAddressedByMentionOrReplyOnly(t *testing.T) {
	f := newAPI(t)
	f.push(msg(1, -100, "supergroup", 42, "just chatting", nil))
	f.push(msg(2, -100, "supergroup", 42, "@HermesBot what time is it", nil))
	f.push(msg(3, -100, "supergroup", 42, "hey @hermesbot", nil))
	f.push(msg(4, -100, "supergroup", 42, "and you?", map[string]any{"reply_to_message": map[string]any{"from": map[string]any{"id": 999, "is_bot": true}}}))
	f.push(msg(5, -100, "supergroup", 42, "reply to someone else", map[string]any{"reply_to_message": map[string]any{"from": map[string]any{"id": 7}}}))
	f.push(msg(6, -100, "supergroup", 42, "/stop@HermesBot", nil))
	f.push(msg(7, -100, "supergroup", 42, "@HermesBot /status", nil))
	_, in := start(t, f)
	evs := in.wait(t, 7)
	want := []struct {
		text      string
		addressed bool
	}{{"just chatting", false}, {"what time is it", true}, {"hey", true}, {"and you?", true}, {"reply to someone else", false}, {"/stop@HermesBot", false}, {"/status", true}}
	for i, w := range want {
		if i == 5 { // a command addressed to the bot by name counts as a mention
			w.addressed, w.text = true, "/stop"
		}
		if evs[i].Text != w.text || evs[i].Addressed != w.addressed || evs[i].DM {
			t.Errorf("event %d: %+v, want text %q addressed %v", i, evs[i], w.text, w.addressed)
		}
	}
}

func TestOtherBotsEditsAndNonTextAreIgnoredAndTopicsCarryThreads(t *testing.T) {
	f := newAPI(t)
	bot := msg(1, 5, "private", 8, "I am a bot", nil)
	bot["message"].(map[string]any)["from"].(map[string]any)["is_bot"] = true
	f.push(bot)
	f.push(map[string]any{"update_id": 2, "edited_message": map[string]any{"message_id": 1, "chat": map[string]any{"id": 5, "type": "private"}, "text": "edited"}})
	f.push(map[string]any{"update_id": 3, "message": map[string]any{"message_id": 3, "from": map[string]any{"id": 42}, "chat": map[string]any{"id": 5, "type": "private"}, "photo": []any{}}})
	f.push(msg(4, 5, "private", 42, "", map[string]any{"caption": "a photo caption"}))
	f.push(msg(5, -100, "supergroup", 42, "in a topic @HermesBot", map[string]any{"is_topic_message": true, "message_thread_id": 77}))
	_, in := start(t, f)
	evs := in.wait(t, 2)
	time.Sleep(150 * time.Millisecond)
	if len(in.all()) != 2 {
		t.Fatalf("%+v", in.all())
	}
	if evs[0].Text != "a photo caption" || evs[1].ThreadID != "77" || evs[1].Text != "in a topic" {
		t.Errorf("%+v", evs)
	}
}

func TestSendEditTypingWireShape(t *testing.T) {
	f := newAPI(t)
	ad, _ := start(t, f)
	id, err := ad.Send(context.Background(), gateway.Target{ChatID: "555", ThreadID: "9"}, "héllo")
	if err != nil || id == "" {
		t.Fatalf("%q %v", id, err)
	}
	s := f.called("sendMessage")[0].Params
	if s["chat_id"] != float64(555) || s["text"] != "héllo" || s["message_thread_id"] != float64(9) || s["parse_mode"] != nil {
		t.Errorf("%v", s)
	}
	if err := ad.Edit(context.Background(), gateway.Target{ChatID: "555"}, id, "edited"); err != nil {
		t.Fatal(err)
	}
	e := f.called("editMessageText")[0].Params
	if e["chat_id"] != float64(555) || e["text"] != "edited" || fmt.Sprint(e["message_id"]) != id {
		t.Errorf("%v", e)
	}
	if err := ad.Typing(context.Background(), gateway.Target{ChatID: "555", ThreadID: "9"}); err != nil {
		t.Fatal(err)
	}
	if c := f.called("sendChatAction")[0].Params; c["action"] != "typing" || c["message_thread_id"] != float64(9) {
		t.Errorf("%v", c)
	}
	// A channel is addressed by name.
	ad.Send(context.Background(), gateway.Target{ChatID: "@mychannel"}, "x")
	if c := f.called("sendMessage")[1].Params; c["chat_id"] != "@mychannel" {
		t.Errorf("%v", c)
	}
	if ad.Edit(context.Background(), gateway.Target{ChatID: "1"}, "not-a-number", "x") == nil {
		t.Error("a bad message id")
	}
	if ad.Name() != "telegram" || ad.MaxLen() != maxLen {
		t.Error("identity")
	}
}

func TestErrorsAreReportedAndNotModifiedIsFine(t *testing.T) {
	f := newAPI(t)
	ad, _ := start(t, f)
	f.mu.Lock()
	f.failNext["editMessageText"] = []map[string]any{{"ok": false, "error_code": 400, "description": "Bad Request: message is not modified: specified new message content is exactly the same"}}
	f.failNext["sendMessage"] = []map[string]any{{"ok": false, "error_code": 400, "description": "Bad Request: chat not found"}}
	f.mu.Unlock()
	if err := ad.Edit(context.Background(), gateway.Target{ChatID: "1"}, "5", "same"); err != nil {
		t.Errorf("an unchanged edit is not a failure: %v", err)
	}
	if _, err := ad.Send(context.Background(), gateway.Target{ChatID: "1"}, "x"); err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Errorf("%v", err)
	}
	f.mu.Lock()
	f.failNext["editMessageText"] = []map[string]any{{"ok": false, "error_code": 400, "description": "Bad Request: message to edit not found"}}
	f.mu.Unlock()
	if err := ad.Edit(context.Background(), gateway.Target{ChatID: "1"}, "5", "x"); err == nil {
		t.Error("a real edit failure must surface so the gateway can resend")
	}
}

func TestRateLimitIsWaitedOutOnce(t *testing.T) {
	f := newAPI(t)
	ad, _ := start(t, f)
	f.mu.Lock()
	f.failNext["sendMessage"] = []map[string]any{{"ok": false, "error_code": 429, "description": "Too Many Requests: retry after 1", "parameters": map[string]any{"retry_after": 1}}}
	f.mu.Unlock()
	start := time.Now()
	if _, err := ad.Send(context.Background(), gateway.Target{ChatID: "1"}, "x"); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 900*time.Millisecond || len(f.called("sendMessage")) != 1 {
		t.Errorf("retried after %v with %d sends", time.Since(start), len(f.called("sendMessage")))
	}
	// A cancelled context ends the wait.
	f.mu.Lock()
	f.failNext["sendMessage"] = []map[string]any{{"ok": false, "error_code": 429, "description": "slow", "parameters": map[string]any{"retry_after": 30}}}
	f.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := ad.Send(ctx, gateway.Target{ChatID: "1"}, "y"); err == nil {
		t.Error("expected the cancelled wait to fail")
	}
	// A wait longer than a minute is reported, not slept.
	f.mu.Lock()
	f.failNext["sendMessage"] = []map[string]any{{"ok": false, "error_code": 429, "description": "flood", "parameters": map[string]any{"retry_after": 3000}}}
	f.mu.Unlock()
	if _, err := ad.Send(context.Background(), gateway.Target{ChatID: "1"}, "z"); err == nil || !strings.Contains(err.Error(), "flood") {
		t.Errorf("%v", err)
	}
}

func TestPollingSurvivesFailuresAndStops(t *testing.T) {
	f := newAPI(t)
	f.mu.Lock()
	f.failNext["getUpdates"] = []map[string]any{
		{"ok": false, "error_code": 500, "description": "Internal Server Error"},
		{"ok": false, "error_code": 409, "description": "Conflict: terminated by other getUpdates request"},
	}
	f.mu.Unlock()
	f.push(msg(1, 5, "private", 42, "after the outage", nil))
	ad, in := start(t, f)
	if evs := in.wait(t, 1); evs[0].Text != "after the outage" {
		t.Errorf("%+v", evs)
	}
	done := make(chan struct{})
	go func() { ad.Stop(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not end the poll loop")
	}
	f.mu.Lock()
	n := f.polls
	f.mu.Unlock()
	time.Sleep(150 * time.Millisecond)
	f.mu.Lock()
	after := f.polls
	f.mu.Unlock()
	if after != n {
		t.Errorf("still polling after Stop: %d -> %d", n, after)
	}
}

func TestStripMention(t *testing.T) {
	for in, want := range map[string]string{
		"@Bot hi": " hi", "hi @bot": "hi ", "a @BOT b @bot c": "a  b  c", "@botty": "ty", "no mention": "no mention",
	} {
		if got := stripMention(in, "bot"); got != want {
			t.Errorf("stripMention(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---- end to end: a Telegram chat through the gateway, the runtime and a scripted model ----

type echoModel struct {
	mu    sync.Mutex
	texts []string
}

func (m *echoModel) Complete(_ context.Context, in agent.CallInput, onText func(string)) (llm.Response, error) {
	last := in.Messages[len(in.Messages)-1].Content.AsText()
	m.mu.Lock()
	m.texts = append(m.texts, last)
	m.mu.Unlock()
	reply := "You said: " + last
	for _, part := range []string{reply[:8], reply[8:]} {
		if onText != nil {
			onText(part)
			time.Sleep(40 * time.Millisecond)
		}
	}
	return llm.Response{Content: &reply, FinishReason: "stop", StopReason: "end_turn"}, nil
}

func TestATelegramChatEndToEnd(t *testing.T) {
	f := newAPI(t)
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, ".env"), []byte(TokenEnv+"="+token+"\n"), 0o600)
	os.WriteFile(filepath.Join(home, "config.yaml"), []byte("auxiliary:\n  background_review:\n    enabled: false\ngateway:\n  platforms:\n    telegram:\n      enabled: true\n      base_url: "+f.URL+"\n      poll_timeout: 1\n      allowed_users: [42]\n"), 0o644)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv(TokenEnv, "")
	ctx, cancel := context.WithCancel(config.WithHome(context.Background(), home))
	defer cancel()
	rt, err := app.Open(ctx, home, app.Options{Provider: &echoModel{}, LongRunning: true})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	gateway.Register("telegram", New)
	adapters, err := gateway.Build(rt.Config, rt.Secrets)
	if err != nil || len(adapters) != 1 || adapters[0].Name() != "telegram" {
		t.Fatalf("%v %v", adapters, err)
	}
	gw := gateway.New(gateway.Options{RT: rt, Adapters: adapters, Pairing: gateway.NewPairing(home), EditInterval: 10 * time.Millisecond, TypingInterval: time.Hour})
	done := make(chan struct{})
	go func() { gw.Run(ctx); close(done) }()

	f.push(msg(1, 555, "private", 42, "ping", nil))
	deadline := time.Now().Add(10 * time.Second)
	for len(f.called("sendMessage")) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	for len(f.called("editMessageText")) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	sends, edits := f.called("sendMessage"), f.called("editMessageText")
	if len(sends) != 1 || len(edits) == 0 {
		t.Fatalf("a streamed reply is one message edited in place: %d sends %d edits", len(sends), len(edits))
	}
	if final := edits[len(edits)-1].Params["text"]; final != "You said: ping" {
		t.Errorf("final text %q", final)
	}
	if sends[0].Params["chat_id"] != float64(555) {
		t.Errorf("%v", sends[0].Params)
	}
	// A stranger is paired, not served.
	f.push(msg(2, 777, "private", 99, "let me in", nil))
	for len(f.called("sendMessage")) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if s := f.called("sendMessage"); len(s) != 2 || !strings.Contains(s[1].Params["text"].(string), "pairing code") {
		t.Errorf("%v", s)
	}
	cancel()
	<-done
}
