package discord

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
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gorilla/websocket"

	"github.com/phzietsman/hermes-agent/internal/agent"
	"github.com/phzietsman/hermes-agent/internal/app"
	"github.com/phzietsman/hermes-agent/internal/config"
	"github.com/phzietsman/hermes-agent/internal/gateway"
	"github.com/phzietsman/hermes-agent/internal/llm"
)

const token = "MTIz.fake.discord-secret-token"

// fakeDiscord is the REST API and the websocket gateway of a Discord server.
type fakeDiscord struct {
	*httptest.Server
	mux           *http.ServeMux
	mu            sync.Mutex
	conn          *websocket.Conn
	writeMu       sync.Mutex
	identify      map[string]any
	rest          []restCall
	seq           int
	nextID        int
	ready         bool
	withholdReady bool
	failSend      int // number of sends to fail with 403
}

type restCall struct {
	Method, Path string
	Body         map[string]any
	Auth         string
}

// discordgo reads its endpoints from package variables, so they are pointed at one shared server for the whole test
// binary (set once in TestMain, never rewritten: goroutines of a closed session may still read them). Each test
// installs its own fakeDiscord behind that server.
var (
	shared    *httptest.Server
	active    atomic.Pointer[fakeDiscord]
	savedBase [4]string
)

func TestMain(m *testing.M) {
	shared = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f := active.Load(); f != nil {
			f.mux.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	savedBase = [4]string{discordgo.EndpointAPI, discordgo.EndpointGateway, discordgo.EndpointGatewayBot, discordgo.EndpointChannels}
	discordgo.EndpointAPI = shared.URL + "/api/v9/"
	discordgo.EndpointGateway = discordgo.EndpointAPI + "gateway"
	discordgo.EndpointGatewayBot = discordgo.EndpointGateway + "/bot"
	discordgo.EndpointChannels = discordgo.EndpointAPI + "channels/"
	code := m.Run()
	shared.Close()
	os.Exit(code)
}

func newDiscord(t *testing.T) *fakeDiscord {
	f := &fakeDiscord{nextID: 5000}
	up := websocket.Upgrader{}
	mux := http.NewServeMux()
	f.mux = mux
	gatewayURL := func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `{"url":"ws://%s/ws"}`, r.Host) }
	mux.HandleFunc("/api/v9/gateway", gatewayURL)
	mux.HandleFunc("/api/v9/gateway/bot", gatewayURL)
	wsHandler := func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conn = c
		f.mu.Unlock()
		f.send(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 30000}})
		for {
			var m map[string]any
			if err := c.ReadJSON(&m); err != nil {
				return
			}
			switch m["op"].(float64) {
			case 1:
				f.send(map[string]any{"op": 11})
			case 2:
				f.mu.Lock()
				f.identify = m["d"].(map[string]any)
				hold := f.withholdReady
				f.mu.Unlock()
				if !hold {
					f.dispatch("READY", map[string]any{"v": 9, "user": map[string]any{"id": "900", "username": "hermes", "bot": true}, "session_id": "sess1",
						"guilds": []any{}, "application": map[string]any{"id": "900"}, "resume_gateway_url": "ws://" + r.Host + "/ws"})
					f.mu.Lock()
					f.ready = true
					f.mu.Unlock()
				}
			}
		}
	}
	mux.HandleFunc("/ws", wsHandler)
	mux.HandleFunc("/ws/", wsHandler)
	mux.HandleFunc("/api/v9/channels/", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.rest = append(f.rest, restCall{r.Method, r.URL.Path, body, r.Header.Get("Authorization")})
		f.nextID++
		id := f.nextID
		failing := f.failSend > 0 && r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/messages")
		if failing {
			f.failSend--
		}
		f.mu.Unlock()
		switch {
		case failing:
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, `{"code":50013,"message":"Missing Permissions"}`)
		case strings.HasSuffix(r.URL.Path, "/typing"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "PATCH":
			fmt.Fprintf(w, `{"id":%q,"channel_id":"c","content":%q}`, r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:], body["content"])
		default:
			fmt.Fprintf(w, `{"id":"%d","channel_id":"c","content":%q}`, id, body["content"])
		}
	})
	f.Server = shared
	active.Store(f)
	t.Cleanup(func() {
		f.mu.Lock()
		c := f.conn
		f.mu.Unlock()
		if c != nil {
			c.Close()
		}
	})
	return f
}

func (f *fakeDiscord) send(m map[string]any) {
	f.mu.Lock()
	c := f.conn
	f.mu.Unlock()
	if c == nil {
		return
	}
	f.writeMu.Lock()
	defer f.writeMu.Unlock()
	c.WriteJSON(m)
}

func (f *fakeDiscord) dispatch(event string, d map[string]any) {
	f.mu.Lock()
	f.seq++
	s := f.seq
	f.mu.Unlock()
	f.send(map[string]any{"op": 0, "t": event, "s": s, "d": d})
}

func (f *fakeDiscord) message(id, channel, guild, authorID, content string, extra map[string]any) {
	d := map[string]any{"id": id, "channel_id": channel, "content": content, "timestamp": "2026-06-01T12:00:00.000000+00:00", "type": 0,
		"author": map[string]any{"id": authorID, "username": "ann", "bot": false}, "mentions": []any{}, "attachments": []any{}, "embeds": []any{}}
	if guild != "" {
		d["guild_id"] = guild
	}
	for k, v := range extra {
		d[k] = v
	}
	f.dispatch("MESSAGE_CREATE", d)
}

func (f *fakeDiscord) calls(method, suffix string) []restCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []restCall
	for _, c := range f.rest {
		if c.Method == method && strings.HasSuffix(c.Path, suffix) {
			out = append(out, c)
		}
	}
	return out
}

func adapterFor(t *testing.T) *Adapter {
	t.Helper()
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, ".env"), []byte(TokenEnv+"="+token+"\n"), 0o600)
	t.Setenv(TokenEnv, "")
	secrets := config.LoadSecrets(home)
	cfg, _ := config.Load(home, secrets)
	a, err := New(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	return a.(*Adapter)
}

type inbox struct {
	mu sync.Mutex
	ev []gateway.Event
}

func (i *inbox) add(e gateway.Event) { i.mu.Lock(); i.ev = append(i.ev, e); i.mu.Unlock() }
func (i *inbox) wait(t *testing.T, n int) []gateway.Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		i.mu.Lock()
		if len(i.ev) >= n {
			out := append([]gateway.Event(nil), i.ev...)
			i.mu.Unlock()
			return out
		}
		i.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("got %d events, want %d", len(i.ev), n)
	return nil
}
func (i *inbox) count() int { i.mu.Lock(); defer i.mu.Unlock(); return len(i.ev) }

func connect(t *testing.T, f *fakeDiscord) (*Adapter, *inbox) {
	a := adapterFor(t)
	in := &inbox{}
	if err := a.Start(context.Background(), in.add); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Stop(context.Background()) })
	return a, in
}

func TestNewNeedsATokenAndNamesWhereItGoes(t *testing.T) {
	t.Setenv(TokenEnv, "")
	home := t.TempDir()
	secrets := config.LoadSecrets(home)
	cfg, _ := config.Load(home, secrets)
	if _, err := New(cfg, secrets); err == nil || !strings.Contains(err.Error(), TokenEnv) || !strings.Contains(err.Error(), "Message Content") {
		t.Errorf("%v", err)
	}
}

func TestStartIdentifiesWithTheRightTokenAndIntents(t *testing.T) {
	f := newDiscord(t)
	a, _ := connect(t, f)
	f.mu.Lock()
	id := f.identify
	f.mu.Unlock()
	if id["token"] != "Bot "+token {
		t.Errorf("identify token %v", id["token"])
	}
	intents := int(id["intents"].(float64))
	for name, bit := range map[string]int{"guilds": 1 << 0, "guild messages": 1 << 9, "direct messages": 1 << 12, "message content": 1 << 15} {
		if intents&bit == 0 {
			t.Errorf("intent %s missing: %d", name, intents)
		}
	}
	if a.botID != "900" || a.Name() != "discord" || a.MaxLen() != maxLen {
		t.Errorf("%s", a.botID)
	}
}

func TestStartGivesUpWhenReadyNeverComes(t *testing.T) {
	f := newDiscord(t)
	f.withholdReady = true
	a := adapterFor(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := a.Start(ctx, func(gateway.Event) {})
	if err == nil || strings.Contains(fmt.Sprint(err), token) {
		t.Errorf("%v", err)
	}
}

func TestDMsAndMentionsAreAddressedAndEverythingElseIsNot(t *testing.T) {
	f := newDiscord(t)
	_, in := connect(t, f)
	f.message("1", "dm1", "", "42", "hello in private", nil)
	f.message("2", "ch1", "g1", "42", "just chatting", nil)
	f.message("3", "ch1", "g1", "42", "<@900> what time is it", map[string]any{"mentions": []any{map[string]any{"id": "900", "username": "hermes"}}})
	f.message("4", "ch1", "g1", "42", "<@!900> and <@777> hi", map[string]any{"mentions": []any{map[string]any{"id": "900"}, map[string]any{"id": "777"}}})
	f.message("5", "ch1", "g1", "42", "and you?", map[string]any{"message_reference": map[string]any{"message_id": "9", "channel_id": "ch1"},
		"referenced_message": map[string]any{"id": "9", "channel_id": "ch1", "content": "x", "author": map[string]any{"id": "900", "bot": true}, "timestamp": "2026-06-01T12:00:00.000000+00:00", "type": 0}})
	f.message("6", "ch1", "g1", "42", "replying to a person", map[string]any{"message_reference": map[string]any{"message_id": "8", "channel_id": "ch1"},
		"referenced_message": map[string]any{"id": "8", "channel_id": "ch1", "content": "x", "author": map[string]any{"id": "55"}, "timestamp": "2026-06-01T12:00:00.000000+00:00", "type": 0}})
	evs := in.wait(t, 6)
	want := []struct {
		text     string
		dm, addr bool
	}{{"hello in private", true, true}, {"just chatting", false, false}, {"what time is it", false, true}, {"and <@777> hi", false, true}, {"and you?", false, true}, {"replying to a person", false, false}}
	for i, w := range want {
		e := evs[i]
		if e.Text != w.text || e.DM != w.dm || e.Addressed != w.addr || e.Platform != "discord" || e.UserID != "42" {
			t.Errorf("event %d: %+v, want %+v", i, e, w)
		}
	}
	if evs[0].ChatID != "dm1" || evs[2].ChatID != "ch1" || evs[2].MessageID != "3" {
		t.Errorf("%+v %+v", evs[0], evs[2])
	}
}

func TestBotsOwnMessagesEmptyTextAndCommandAliases(t *testing.T) {
	f := newDiscord(t)
	_, in := connect(t, f)
	botMsg := map[string]any{"author": map[string]any{"id": "55", "username": "otherbot", "bot": true}}
	f.message("1", "dm1", "", "55", "from another bot", botMsg)
	f.message("2", "dm1", "", "900", "my own echo", nil)
	f.message("3", "dm1", "", "42", "   ", nil)
	f.message("4", "dm1", "", "42", "!new", nil)
	f.message("5", "dm1", "", "42", "!STOP now", nil)
	f.message("6", "dm1", "", "42", "!notacommand x", nil)
	f.message("7", "dm1", "", "42", "hi!", nil)
	evs := in.wait(t, 4)
	time.Sleep(150 * time.Millisecond)
	if in.count() != 4 {
		t.Fatalf("%d events", in.count())
	}
	for i, want := range []string{"/new", "/stop now", "!notacommand x", "hi!"} {
		if evs[i].Text != want {
			t.Errorf("event %d: %q, want %q", i, evs[i].Text, want)
		}
	}
}

func TestSendEditTypingGoOutWithoutPings(t *testing.T) {
	f := newDiscord(t)
	a, _ := connect(t, f)
	ctx := context.Background()
	id, err := a.Send(ctx, gateway.Target{ChatID: "ch9"}, "hello @everyone and <@123>")
	if err != nil || id == "" {
		t.Fatalf("%q %v", id, err)
	}
	post := f.calls("POST", "/channels/ch9/messages")
	if len(post) != 1 || post[0].Body["content"] != "hello @everyone and <@123>" || post[0].Auth != "Bot "+token {
		t.Fatalf("%+v", post)
	}
	am, _ := post[0].Body["allowed_mentions"].(map[string]any)
	if am == nil {
		t.Fatal("no allowed_mentions: model output could ping people")
	}
	if parse, _ := am["parse"].([]any); len(parse) != 0 {
		t.Errorf("parse must be empty so nothing pings: %v", am)
	}
	if err := a.Edit(ctx, gateway.Target{ChatID: "ch9"}, id, "edited @here"); err != nil {
		t.Fatal(err)
	}
	patch := f.calls("PATCH", "/channels/ch9/messages/"+id)
	if len(patch) != 1 || patch[0].Body["content"] != "edited @here" || patch[0].Body["allowed_mentions"] == nil {
		t.Errorf("%+v", patch)
	}
	if err := a.Typing(ctx, gateway.Target{ChatID: "ch9"}); err != nil || len(f.calls("POST", "/channels/ch9/typing")) != 1 {
		t.Errorf("typing: %v", err)
	}
}

func TestFailuresAreReportedWithoutTheToken(t *testing.T) {
	f := newDiscord(t)
	a, _ := connect(t, f)
	f.mu.Lock()
	f.failSend = 1
	f.mu.Unlock()
	_, err := a.Send(context.Background(), gateway.Target{ChatID: "ch9"}, "x")
	if err == nil || !strings.Contains(err.Error(), "Missing Permissions") || strings.Contains(err.Error(), token) {
		t.Errorf("%v", err)
	}
	if scrub(fmt.Errorf("Authorization: Bot %s failed", token), token).Error() != "Authorization: Bot *** failed" {
		t.Error("scrub")
	}
	if scrubNil(nil, token) != nil {
		t.Error("scrubNil")
	}
}

func TestNotConnectedAndStopAreSafe(t *testing.T) {
	a := adapterFor(t)
	ctx := context.Background()
	if _, err := a.Send(ctx, gateway.Target{ChatID: "1"}, "x"); err == nil {
		t.Error("send before Start")
	}
	if a.Edit(ctx, gateway.Target{ChatID: "1"}, "2", "x") == nil || a.Typing(ctx, gateway.Target{ChatID: "1"}) == nil {
		t.Error("edit/typing before Start")
	}
	if err := a.Stop(ctx); err != nil {
		t.Errorf("Stop before Start: %v", err)
	}
	f := newDiscord(t)
	b, _ := connect(t, f)
	if err := b.Stop(ctx); err != nil {
		t.Errorf("%v", err)
	}
	if err := b.Stop(ctx); err != nil {
		t.Errorf("a second Stop: %v", err)
	}
	if _, err := b.Send(ctx, gateway.Target{ChatID: "1"}, "x"); err == nil {
		t.Error("send after Stop")
	}
}

// ---- end to end ----

type echoModel struct{}

func (echoModel) Complete(_ context.Context, in agent.CallInput, onText func(string)) (llm.Response, error) {
	reply := "You said: " + in.Messages[len(in.Messages)-1].Content.AsText()
	if onText != nil {
		onText(reply[:8])
		time.Sleep(40 * time.Millisecond)
		onText(reply[8:])
	}
	return llm.Response{Content: &reply, FinishReason: "stop", StopReason: "end_turn"}, nil
}

func TestADiscordChatEndToEnd(t *testing.T) {
	f := newDiscord(t)
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, ".env"), []byte(TokenEnv+"="+token+"\n"), 0o600)
	os.WriteFile(filepath.Join(home, "config.yaml"), []byte("auxiliary:\n  background_review:\n    enabled: false\ngateway:\n  platforms:\n    discord:\n      enabled: true\n      allowed_users: [42]\n"), 0o644)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv(TokenEnv, "")
	ctx, cancel := context.WithCancel(config.WithHome(context.Background(), home))
	defer cancel()
	rt, err := app.Open(ctx, home, app.Options{Provider: echoModel{}, LongRunning: true})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	gateway.Register("discord", New)
	adapters, err := gateway.Build(rt.Config, rt.Secrets)
	if err != nil || len(adapters) != 1 {
		t.Fatalf("%v %v", adapters, err)
	}
	gw := gateway.New(gateway.Options{RT: rt, Adapters: adapters, Pairing: gateway.NewPairing(home), EditInterval: 10 * time.Millisecond, TypingInterval: time.Hour})
	done := make(chan struct{})
	go func() { gw.Run(ctx); close(done) }()
	deadline := time.Now().Add(10 * time.Second)
	wait := func(cond func() bool) {
		for !cond() && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
	}
	wait(func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.ready })
	f.message("1", "dm42", "", "42", "ping", nil)
	wait(func() bool { return len(f.calls("PATCH", "")) > 0 })
	posts, patches := f.calls("POST", "/channels/dm42/messages"), f.calls("PATCH", "")
	if len(posts) != 1 || len(patches) == 0 {
		t.Fatalf("a streamed reply is one message edited in place: %d posts %d patches", len(posts), len(patches))
	}
	if final := patches[len(patches)-1].Body["content"]; final != "You said: ping" {
		t.Errorf("final text %q", final)
	}
	f.message("2", "dm99", "", "99", "let me in", nil)
	wait(func() bool { return len(f.calls("POST", "/channels/dm99/messages")) > 0 })
	if p := f.calls("POST", "/channels/dm99/messages"); len(p) != 1 || !strings.Contains(p[0].Body["content"].(string), "pairing code") {
		t.Errorf("%+v", p)
	}
	cancel()
	<-done
}
