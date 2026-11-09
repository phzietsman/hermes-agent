package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/phzietsman/hermes-agent/internal/app"
	"github.com/phzietsman/hermes-agent/internal/llm"
)

func cronCLI(t *testing.T, ctx context.Context, p *script, args ...string) (int, string, string) {
	t.Helper()
	var o, e bytes.Buffer
	opts := app.Options{}
	if p != nil {
		opts.Provider = p
	}
	code := run(ctx, append([]string{"cron"}, args...), stdio{in: strings.NewReader(""), out: &o, err: &e}, opts)
	return code, o.String(), e.String()
}

func TestCronAddListEditPauseResumeRemove(t *testing.T) {
	ctx, _ := env(t)
	code, out, errOut := cronCLI(t, ctx, nil, "add", "every 2h", "Check", "the", "build", "-name", "build watch", "-skill", "ci", "-skill", "lint")
	if code != 0 || !strings.Contains(out, "created") || !strings.Contains(out, "build watch") {
		t.Fatalf("add: %d %s %s", code, out, errOut)
	}
	id := regexp.MustCompile(`created ([0-9a-f]{12})`).FindStringSubmatch(out)[1]
	if code, out, _ := cronCLI(t, ctx, nil, "list"); code != 0 || !strings.Contains(out, id) || !strings.Contains(out, "every 120m") || !strings.Contains(out, "scheduled") {
		t.Errorf("list: %d %s", code, out)
	}
	if code, out, _ := cronCLI(t, ctx, nil, "edit", id, "-schedule", "every 4h", "-name", "slower"); code != 0 || !strings.Contains(out, "every 240m") || !strings.Contains(out, "slower") {
		t.Errorf("edit: %d %s", code, out)
	}
	if code, _, _ := cronCLI(t, ctx, nil, "pause", id, "-reason", "away"); code != 0 {
		t.Error("pause")
	}
	if _, out, _ := cronCLI(t, ctx, nil, "list"); !strings.Contains(out, "paused") {
		t.Errorf("%s", out)
	}
	if code, _, _ := cronCLI(t, ctx, nil, "resume", id); code != 0 {
		t.Error("resume")
	}
	if code, out, _ := cronCLI(t, ctx, nil, "remove", id); code != 0 || !strings.Contains(out, "removed") {
		t.Errorf("remove: %d %s", code, out)
	}
	if _, out, _ := cronCLI(t, ctx, nil, "list"); !strings.Contains(out, "no cron jobs") {
		t.Errorf("%s", out)
	}
}

func TestCronCLIErrors(t *testing.T) {
	ctx, _ := env(t)
	for name, args := range map[string][]string{
		"bad schedule":     {"add", "whenever", "do it"},
		"add too few args": {"add", "30m"},
		"unknown id":       {"pause", "nope"},
		"missing id":       {"resume"},
		"bad workdir":      {"add", "30m", "x", "-workdir", "relative"},
	} {
		if code, _, e := cronCLI(t, ctx, nil, args...); code == 0 {
			t.Errorf("%s: exit 0 (%s)", name, e)
		}
	}
	if code, _, e := cronCLI(t, ctx, nil); code != 2 || !strings.Contains(e, "usage:") {
		t.Errorf("%d %s", code, e)
	}
	if code, _, _ := cronCLI(t, ctx, nil, "bogus"); code != 2 {
		t.Errorf("%d", code)
	}
}

func TestCronRunAndTickDriveTheAgent(t *testing.T) {
	ctx, home := env(t)
	_, out, _ := cronCLI(t, ctx, nil, "add", "every 1m", "Say hello", "-name", "greeter")
	id := regexp.MustCompile(`created ([0-9a-f]{12})`).FindStringSubmatch(out)[1]
	p := &script{steps: []llm.Response{say("hello from the job")}}
	code, out, errOut := cronCLI(t, ctx, p, "run", id, "-prompt", "be brief")
	if code != 0 || !strings.Contains(out, "hello from the job") {
		t.Fatalf("run: %d %s %s", code, out, errOut)
	}
	if got := p.calls[0].Messages[len(p.calls[0].Messages)-1].Content.AsText(); !strings.Contains(got, "Say hello") || !strings.Contains(got, "be brief") {
		t.Errorf("%q", got)
	}
	saved, _ := filepath.Glob(filepath.Join(home, "cron", "output", id, "*.md"))
	if len(saved) != 1 {
		t.Errorf("saved outputs %v", saved)
	}
	if _, out, _ := cronCLI(t, ctx, nil, "list"); !strings.Contains(out, "ok") {
		t.Errorf("last status not shown: %s", out)
	}
	// Nothing is due a second after a run: tick runs nothing.
	if code, out, _ := cronCLI(t, ctx, &script{}, "tick"); code != 0 || !strings.Contains(out, "ran 0 job(s)") {
		t.Errorf("tick: %d %s", code, out)
	}
	if code, _, e := cronCLI(t, ctx, &script{}, "run", "nope"); code == 0 {
		t.Errorf("unknown job ran: %s", e)
	}
}

func TestCronStatusReportsAStalledTicker(t *testing.T) {
	ctx, home := env(t)
	if _, out, _ := cronCLI(t, ctx, nil, "status"); !strings.Contains(out, "never ran") {
		t.Errorf("%s", out)
	}
	cronCLI(t, ctx, &script{}, "tick") // a tick leaves its heartbeat
	if _, out, _ := cronCLI(t, ctx, nil, "status"); !strings.Contains(out, "ticker: running") {
		t.Errorf("%s", out)
	}
	os.WriteFile(filepath.Join(home, "cron", "ticker_heartbeat"), []byte("1"), 0o600)
	if _, out, _ := cronCLI(t, ctx, nil, "status"); !strings.Contains(out, "NOT running") {
		t.Errorf("%s", out)
	}
}
