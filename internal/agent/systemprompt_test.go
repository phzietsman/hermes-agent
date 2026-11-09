package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/phzietsman/hermes-agent/internal/config"
	"github.com/phzietsman/hermes-agent/internal/session"
	"github.com/phzietsman/hermes-agent/internal/skills"
)

func init() { time.Local = time.UTC } // session-id stamps are box-local; the fixtures were made with TZ=UTC

type promptCase struct {
	Name           string            `json:"name"`
	Files          map[string]string `json:"files"`
	Cfg            map[string]any    `json:"cfg"`
	Tools          []string          `json:"tools"`
	Toolsets       []string          `json:"toolsets"`
	Platform       string            `json:"platform"`
	Model          string            `json:"model"`
	Provider       string            `json:"provider"`
	SessionID      string            `json:"session_id"`
	Root           *string           `json:"root_session_id"`
	PassSessionID  bool              `json:"pass_session_id"`
	SystemMessage  *string           `json:"system_message"`
	SkipContext    bool              `json:"skip_context_files"`
	LoadSoul       bool              `json:"load_soul"`
	CtxLen         *int              `json:"ctx_len"`
	Enforcement    any               `json:"enforcement"`
	Execution      any               `json:"execution"`
	TaskCompletion *bool             `json:"task_completion"`
	Parallel       *bool             `json:"parallel"`
	Hints          map[string]any    `json:"hints"`
	Rich           bool              `json:"rich"`
	CronDelivery   string            `json:"cron_delivery"`
	Now            string            `json:"now"`
	Zone           string            `json:"zone"`
	SessionStart   *string           `json:"session_start"`
	Memory         string            `json:"memory"`
	HomeKind       string            `json:"home_kind"`
	Expected       map[string]string `json:"expected"`
	Warnings       []string          `json:"expected_warnings"`
}

func toSet(xs []string) map[string]bool {
	m := map[string]bool{}
	for _, x := range xs {
		m[x] = true
	}
	return m
}

func hintOverrides(h map[string]any) map[string]HintOverride {
	if h == nil {
		return nil
	}
	out := map[string]HintOverride{}
	for k, v := range h {
		switch t := v.(type) {
		case string:
			out[k] = HintOverride{Append: t}
		case map[string]any:
			o := HintOverride{}
			o.Replace, _ = t["replace"].(string)
			o.Append, _ = t["append"].(string)
			out[k] = o
		}
	}
	return out
}

func (c promptCase) input(t testing.TB, base string) PromptInput {
	t.Helper()
	home := filepath.Join(base, "home")
	files := c.Files
	if c.HomeKind == "profile" {
		home = filepath.Join(base, "root", "profiles", "work")
		files = map[string]string{}
		for k, v := range c.Files {
			files[strings.Replace(k, "home/", "root/profiles/work/", 1)] = v
		}
	}
	for rel, content := range files {
		p := filepath.Join(base, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{home, filepath.Join(base, "proj")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	y, _ := json.Marshal(c.Cfg) // JSON is valid YAML
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), y, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	zone, err := time.LoadLocation(c.Zone)
	if err != nil {
		t.Fatal(err)
	}
	parse := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02T15:04:05", s, zone)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	in := PromptInput{
		Home: home, SessionID: c.SessionID, PassSessionID: c.PassSessionID,
		Model: c.Model, Provider: c.Provider, Platform: c.Platform,
		Tools: toSet(c.Tools), Toolsets: toSet(c.Toolsets),
		SystemMessage: c.SystemMessage, SkipContextFiles: c.SkipContext, LoadSoulIdentity: c.LoadSoul,
		CWD: filepath.Join(base, "proj"), MemoryBlock: c.Memory,
		TaskCompletion: c.TaskCompletion, ParallelToolCall: c.Parallel,
		ToolUseEnforcement: c.Enforcement, ExecutionGuidance: c.Execution,
		HintOverrides: hintOverrides(c.Hints), TelegramRichMessages: c.Rich, CronDeliveryPlatform: c.CronDelivery,
		Now: parse(c.Now), Zone: zone,
		SkillsIndex: SkillsIndexOptions{Settings: skills.LoadSettings(home, cfg, c.Platform), Filter: skills.HostFilter()},
	}
	if c.Root != nil {
		in.RootSessionID = *c.Root
	}
	if c.CtxLen != nil {
		in.ContextLength = *c.CtxLen
	}
	if c.SessionStart != nil {
		in.SessionStart = parse(*c.SessionStart)
	}
	return in
}

// Differential: every tier of the Go prompt equals Python's build_system_prompt_parts, byte for byte.
func TestBuildSystemPromptPartsMatchesPython(t *testing.T) {
	b, err := os.ReadFile("testdata/system_prompt.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []promptCase
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			base, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			got := BuildSystemPromptParts(c.input(t, base))
			for tier, g := range map[string]string{"stable": got.Stable, "context": got.Context, "volatile": got.Volatile} {
				if g = strings.ReplaceAll(g, base, "<BASE>"); g != c.Expected[tier] {
					t.Errorf("%s tier mismatch\n got: %q\nwant: %q", tier, clip(g), clip(c.Expected[tier]))
				}
			}
			var w []string
			for _, x := range got.Warnings {
				w = append(w, strings.ReplaceAll(x, base, "<BASE>"))
			}
			if strings.Join(w, "\n") != strings.Join(c.Warnings, "\n") {
				t.Errorf("warnings\n got: %q\nwant: %q", w, c.Warnings)
			}
		})
	}
}

func newStoreSession(t *testing.T, id string) (*session.Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	st, err := session.OpenPath(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.CreateSession(ctx, session.NewSession{ID: id, Source: "cli", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	return st, ctx
}

// The prompt is built once: later calls and a resumed process get the stored bytes and never build.
func TestEnsureSystemPromptWritesOnce(t *testing.T) {
	st, ctx := newStoreSession(t, "s1")
	builds := 0
	build := func() PromptParts {
		builds++
		return PromptParts{Stable: "stable", Context: "ctx", Volatile: "vol-" + time.Now().Format(time.RFC3339Nano)}
	}
	first, err := EnsureSystemPrompt(ctx, st, "s1", build)
	if err != nil || first.Restored || first.Static != "stable" || !strings.HasPrefix(first.Text, "stable\n\nctx\n\nvol-") {
		t.Fatalf("first = %+v, %v", first, err)
	}
	for i := 0; i < 3; i++ {
		again, err := EnsureSystemPrompt(ctx, st, "s1", build)
		if err != nil || !again.Restored || again.Text != first.Text || again.Static != "" {
			t.Fatalf("again = %+v, %v", again, err)
		}
	}
	if builds != 1 {
		t.Fatalf("built %d times, want 1", builds)
	}
}

func TestEnsureSystemPromptUnknownSession(t *testing.T) {
	st, ctx := newStoreSession(t, "s1")
	if _, err := EnsureSystemPrompt(ctx, st, "nope", func() PromptParts { t.Fatal("must not build"); return PromptParts{} }); err == nil {
		t.Fatal("want an error for a missing session")
	}
}

// Concurrent first turns converge on one stored prompt.
func TestEnsureSystemPromptRace(t *testing.T) {
	st, ctx := newStoreSession(t, "s1")
	var wg sync.WaitGroup
	texts := make([]string, 8)
	for i := range texts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := EnsureSystemPrompt(ctx, st, "s1", func() PromptParts { return PromptParts{Stable: strings.Repeat("x", i+1)} })
			if err != nil {
				t.Error(err)
			}
			texts[i] = r.Text
		}()
	}
	wg.Wait()
	for _, x := range texts {
		if x != texts[0] {
			t.Fatalf("writers disagree: %q vs %q", x, texts[0])
		}
	}
}

func TestPromptPartsString(t *testing.T) {
	if got := (PromptParts{Stable: "a", Volatile: "c"}).String(); got != "a\n\nc" {
		t.Fatalf("got %q", got)
	}
	if got := (PromptParts{}).String(); got != "" {
		t.Fatalf("got %q", got)
	}
}
