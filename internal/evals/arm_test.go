package evals

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phzietsman/hermes-agent/internal/agent"
	"github.com/phzietsman/hermes-agent/internal/app"
	"github.com/phzietsman/hermes-agent/internal/llm"
	"github.com/phzietsman/hermes-agent/internal/memory/sam"
)

// fakeService is a memory service that stores what it is told and recalls by word overlap.
type fakeService struct {
	*httptest.Server
	mu      sync.Mutex
	facts   []string
	profile string
}

var words = regexp.MustCompile(`[A-Za-z]{4,}`)

func newService(t *testing.T) *fakeService {
	s := &fakeService{profile: `{"instructions":[{"description":"old run said bluebird-111"},{"description":"unrelated"}],"preferences":[{"key":"k","value":"v"}],"static":[]}`}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/profile"):
			io.WriteString(w, s.profile)
		case strings.HasSuffix(r.URL.Path, "/facts"):
			var f struct{ Text string }
			json.Unmarshal(b, &f)
			s.mu.Lock()
			s.facts = append(s.facts, f.Text)
			s.mu.Unlock()
			io.WriteString(w, `{}`)
		case strings.HasSuffix(r.URL.Path, "/query"):
			var q struct{ Query string }
			json.Unmarshal(b, &q)
			qw := map[string]bool{}
			for _, x := range words.FindAllString(strings.ToLower(q.Query), -1) {
				qw[x] = true
			}
			var hits []map[string]any
			s.mu.Lock()
			for i, f := range s.facts {
				n := 0
				for _, x := range words.FindAllString(strings.ToLower(f), -1) {
					if qw[x] && x != "what" && x != "our" {
						n++
					}
				}
				if n >= 2 {
					hits = append(hits, map[string]any{"id": fmt.Sprint(i), "text": f, "score": 0.9})
				}
			}
			s.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"hits": hits})
		default:
			io.WriteString(w, `{}`)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

// memoryModel is a stand-in model: on a statement it saves the codeword with agent_memory_remember; on a question
// it answers from the "Recalled memory" block of the user message, or says it does not know.
type memoryModel struct {
	mu    sync.Mutex
	calls int
}

func (m *memoryModel) Complete(_ context.Context, in agent.CallInput, _ func(string)) (llm.Response, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	last := in.Messages[len(in.Messages)-1]
	say := func(s string) (llm.Response, error) {
		return llm.Response{Content: &s, FinishReason: "stop", StopReason: "end_turn"}, nil
	}
	if last.Role == llm.RoleTool {
		return say("Noted.")
	}
	text := last.Content.AsText()
	if strings.Contains(text, "Review the conversation above") {
		return say("Nothing to save.")
	}
	if strings.HasPrefix(strings.TrimSpace(text), "What") || strings.Contains(text, "Which") || strings.Contains(text, "Where") {
		if code := regexp.MustCompile(`[a-z]+-\d{3}`).FindString(text[strings.Index(text+"## Recalled memory", "## Recalled memory"):]); code != "" {
			return say("It is " + code + ".")
		}
		return say("I don't know.")
	}
	args, _ := json.Marshal(map[string]any{"text": text, "kind": "fact"})
	return llm.Response{FinishReason: "tool_calls", StopReason: "tool_use", ToolCalls: []llm.ToolCall{{ID: "c" + time.Now().Format("150405.000000"), Type: "function",
		Function: llm.ToolCallFunction{Name: "agent_memory_remember", Arguments: string(args)}}}}, nil
}

func TestSAMArmRunsTheRecallScenarioEndToEnd(t *testing.T) {
	svc := newService(t)
	env := Env{SAMEndpoint: svc.URL, SAMContext: "ctx", SAMKey: "k", Provider: &memoryModel{}}
	arm, err := NewSAMArm(ctx, t.TempDir(), env, app.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer arm.Close()
	arm.ReviewWait = 10 * time.Second
	res, err := RecallAndLanding(ctx, arm, 1, 4, substr{})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Passed != 4 || res[0].Total != 4 {
		t.Errorf("recall %+v (tools: %s)", res[0], arm.ToolSummary())
	}
	l := res[1]
	if l.Passed != 4 || l.Total != 4 || l.Detail["landed"] != 4 || l.Detail["assistant_tool"] != 4 || l.Detail["turn"] != 4 || l.Detail["session"] != 4 {
		t.Errorf("landing %+v", l)
	}
	if arm.TellTools["agent_memory_remember"] != 4 {
		t.Errorf("tools %s", arm.ToolSummary())
	}
}

func TestSAMArmAbstainsOnWhatWasNeverTold(t *testing.T) {
	svc := newService(t)
	arm, err := NewSAMArm(ctx, t.TempDir(), Env{SAMEndpoint: svc.URL, SAMContext: "ctx", SAMKey: "k", Provider: &memoryModel{}}, app.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer arm.Close()
	res, err := Abstention(ctx, arm, 1, 4, substr{})
	if err != nil || res[0].Passed != 4 {
		t.Errorf("%+v %v", res, err)
	}
}

func TestRecorderRecordsAcceptedWritesAndFiltersTheProfile(t *testing.T) {
	svc := newService(t)
	rec := &Recorder{}
	base, stop, err := rec.Serve(svc.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	c := &sam.Client{Endpoint: base, Context: "ctx", APIKey: "k", Timeout: 5 * time.Second}
	if _, err := c.Remember(ctx, "my staging cluster is teal-111", sam.RememberOptions{Labels: []string{"kind=fact", "origin=assistant_tool"}}); err != nil {
		t.Fatal(err)
	}
	c.RememberMany(ctx, []sam.Message{{Role: "user", Content: "hello"}, {Role: "assistant", Content: "hi"}}, "", "", []string{"origin=turn"})
	w := rec.Writes()
	if len(w) != 2 || w[0].Origin != "assistant_tool" || w[0].Text != "my staging cluster is teal-111" || w[1].Origin != "turn" || w[1].Text != "hello\nhi" {
		t.Errorf("%+v", w)
	}
	// Profile: empty until tags are admitted, then only matching entries.
	p, _ := c.Profile(ctx)
	if len(p.Instructions)+len(p.Preferences)+len(p.Static) != 0 || rec.ProfileServed() != 0 {
		t.Errorf("an unadmitted profile leaked: %+v", p)
	}
	rec.SetProfileTags([]string{"bluebird-111"})
	p, _ = c.Profile(ctx)
	if len(p.Instructions) != 1 || p.Instructions[0] != "old run said bluebird-111" || len(p.Preferences) != 0 || rec.ProfileServed() != 1 {
		t.Errorf("%+v served=%d", p, rec.ProfileServed())
	}
	// A refused write is not "landed".
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, `{"message":"no"}`, http.StatusForbidden) }))
	defer bad.Close()
	rec2 := &Recorder{}
	base2, stop2, err := rec2.Serve(bad.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer stop2()
	c2 := &sam.Client{Endpoint: base2, Context: "ctx", APIKey: "k", Timeout: time.Second}
	c2.Remember(ctx, "x", sam.RememberOptions{Labels: []string{"origin=turn"}})
	if len(rec2.Writes()) != 0 {
		t.Error("a write the service refused was recorded as accepted")
	}
}

func TestMeasureLatencyAgainstAFakeService(t *testing.T) {
	svc := newService(t)
	s := &sam.Settings{Endpoint: svc.URL, Context: "ctx", APIKey: "k", DefaultScope: "user/x", AgentScope: "agent/x", RecallMode: sam.RecallHybrid,
		TopK: 5, MaxPrefetchChars: 4000, CoreProfileMaxChars: 1500, Timeout: 5 * time.Second, PrefetchWait: 500 * time.Millisecond,
		BreakerThreshold: 3, ReplayEveryNTurns: 10}
	r, err := MeasureLatency(ctx, s, t.TempDir(), 3, 2, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if r.RawRecall.N != 3 || r.PrefetchCold.N != 3 || r.PrefetchWarm.N != 2 || r.GateP95Ms != 1500 || r.PrefetchWaitS != 0.5 {
		t.Errorf("%+v", r)
	}
	if r.PrefetchWarm.P95 > 100 {
		t.Errorf("a warmed prefetch should not block: %+v", r.PrefetchWarm)
	}
}
