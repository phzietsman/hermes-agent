package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phzietsman/hermes-agent/internal/agent"
	"github.com/phzietsman/hermes-agent/internal/config"
	"github.com/phzietsman/hermes-agent/internal/cron"
	"github.com/phzietsman/hermes-agent/internal/llm"
)

func TestCronRunExecutesAJobInAReadOnlySessionWithoutTheCronTool(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	home, _, reqs := memHome(t)
	skillDir := filepath.Join(home, "skills", "brief")
	os.MkdirAll(skillDir, 0o755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: brief\ndescription: Briefs.\n---\nAlways lead with the headline.\n"), 0o644)
	p := &script{steps: []llm.Response{say("Headline: all quiet.")}}
	ctx := context.Background()
	rt, err := Open(config.WithHome(ctx, home), home, Options{Provider: p})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	c := rt.Cron()
	j, err := c.Store().Create(cron.CreateOptions{Prompt: "Write the morning brief", Schedule: "every 1h", Skills: []string{"brief", "missing-skill"}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Scheduler().RunNow(ctx, j.ID, "focus on markets")
	if err != nil || res.Err != nil || res.Output != "Headline: all quiet." {
		t.Fatalf("%+v %v", res, err)
	}
	call := p.calls[0]
	user := call.Messages[len(call.Messages)-1].Content.AsText()
	for _, want := range []string{"You are running as a scheduled cron job", "Always lead with the headline", "Skill(s) not found and skipped: missing-skill", "Write the morning brief", "## Run Context\nfocus on markets"} {
		if !strings.Contains(user, want) {
			t.Errorf("prompt lacks %q:\n%s", want, user)
		}
	}
	for _, d := range call.Tools {
		if d.Name == "cronjob_manage" {
			t.Error("a cron run was offered the cron tool")
		}
	}
	got, _ := c.Store().Get(j.ID)
	if got.LastStatus != cron.StatusOK || got.Repeat.Completed != 1 {
		t.Errorf("%+v", got)
	}
	// Read-only: the run's turn and transcript are not written to memory.
	time.Sleep(200 * time.Millisecond)
	for _, r := range *reqs {
		if strings.Contains(r, "/facts") {
			t.Errorf("a cron run wrote memory: %s", r)
		}
	}
	// The next job can chain on this one's output.
	if out := c.Store().LastOutput(j.ID); out != "Headline: all quiet." {
		t.Errorf("last output %q", out)
	}
}

func TestCronRunFailsCleanlyWhenTheModelFails(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	home := t.TempDir()
	p := &script{steps: []llm.Response{{FinishReason: llm.FinishContentFilter}}}
	ctx := context.Background()
	rt, err := Open(config.WithHome(ctx, home), home, Options{Provider: p})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	j, _ := rt.Cron().Store().Create(cron.CreateOptions{Prompt: "x", Schedule: "every 1h"})
	res, err := rt.Cron().Scheduler().RunNow(ctx, j.ID, "")
	if err != nil || res.Err == nil {
		t.Fatalf("a refusal must be a failed run: %+v %v", res, err)
	}
	got, _ := rt.Cron().Store().Get(j.ID)
	if got.LastStatus != cron.StatusError || got.FailureStreak != 1 || got.State != cron.StateScheduled {
		t.Errorf("%+v", got)
	}
}

func TestCronToolCreatesJobsThatTheTickRuns(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	home := t.TempDir()
	args := `{"action":"create","prompt":"say hi","schedule":"every 1m"}`
	p := &script{steps: []llm.Response{
		{FinishReason: "tool_calls", StopReason: "tool_use", ToolCalls: []llm.ToolCall{{ID: "c1", Type: "function", Function: llm.ToolCallFunction{Name: "cronjob_manage", Arguments: args}}}},
		say("scheduled it"), say("hi from cron"),
	}}
	ctx := context.Background()
	rt, err := Open(config.WithHome(ctx, home), home, Options{Provider: p})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	s, _ := rt.NewSession(ctx, SessionOptions{})
	if res, err := s.Agent.RunTurn(ctx, "schedule a greeting"); err != nil || res.FinalResponse != "scheduled it" {
		t.Fatalf("%+v %v", res, err)
	}
	jobs, _ := rt.Cron().Store().List()
	if len(jobs) != 1 || jobs[0].Prompt != "say hi" {
		t.Fatalf("the agent's create did not land in the store: %+v", jobs)
	}
	// Move the clock past the slot and tick: the job runs through the agent.
	st := rt.Cron().Store()
	st.Now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if n, err := rt.Cron().Scheduler().Tick(ctx); err != nil || n != 1 {
		t.Fatalf("tick ran %d: %v", n, err)
	}
	got, _ := st.Get(jobs[0].ID)
	if got.LastStatus != cron.StatusOK || st.LastOutput(jobs[0].ID) != "hi from cron" {
		t.Errorf("%+v", got)
	}
}

// blockedModel never answers: it waits for the run to be cancelled.
type blockedModel struct{}

func (blockedModel) Complete(ctx context.Context, _ agent.CallInput, _ func(string)) (llm.Response, error) {
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}

func TestCronInactivityWatchdogStopsAStalledRunButNotAnActiveOne(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "config.yaml"), []byte("cron:\n  inactivity_timeout: 1\n"), 0o644)
	ctx := context.Background()
	rt, err := Open(config.WithHome(ctx, home), home, Options{Provider: blockedModel{}})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	j, _ := rt.Cron().Store().Create(cron.CreateOptions{Prompt: "x", Schedule: "every 1h"})
	start := time.Now()
	res, err := rt.Cron().Scheduler().RunNow(ctx, j.ID, "")
	if err != nil || res.Err == nil || !strings.Contains(res.Err.Error(), "idle") {
		t.Fatalf("a stalled run must be stopped as idle: %+v %v", res, err)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("took %v", time.Since(start))
	}
	got, _ := rt.Cron().Store().Get(j.ID)
	if got.LastStatus != cron.StatusError || !strings.Contains(string(got.LastError), "idle") {
		t.Errorf("%+v", got)
	}
}
