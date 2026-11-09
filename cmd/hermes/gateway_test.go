package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phzietsman/hermes-agent/internal/app"
	"github.com/phzietsman/hermes-agent/internal/config"
	"github.com/phzietsman/hermes-agent/internal/gateway"
	"github.com/phzietsman/hermes-agent/internal/llm"
)

type cliAdapter struct {
	mu     sync.Mutex
	handle func(gateway.Event)
	texts  []string
}

func (a *cliAdapter) Name() string { return "fake" }
func (a *cliAdapter) MaxLen() int  { return 4000 }
func (a *cliAdapter) Start(_ context.Context, h func(gateway.Event)) error {
	a.mu.Lock()
	a.handle = h
	a.mu.Unlock()
	return nil
}
func (a *cliAdapter) Stop(context.Context) error { return nil }
func (a *cliAdapter) Send(_ context.Context, _ gateway.Target, text string) (string, error) {
	a.mu.Lock()
	a.texts = append(a.texts, text)
	a.mu.Unlock()
	return "1", nil
}
func (a *cliAdapter) Edit(_ context.Context, _ gateway.Target, _, text string) error {
	a.mu.Lock()
	a.texts = append(a.texts, text)
	a.mu.Unlock()
	return nil
}
func (a *cliAdapter) Typing(context.Context, gateway.Target) error { return nil }

func (a *cliAdapter) seen(sub string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, t := range a.texts {
		if strings.Contains(t, sub) {
			return true
		}
	}
	return false
}

func TestGatewayRunServesAPlatformUntilInterrupted(t *testing.T) {
	ctx0, home := env(t)
	os.WriteFile(filepath.Join(home, "config.yaml"), []byte("auxiliary:\n  background_review:\n    enabled: false\ngateway:\n  platforms:\n    fake:\n      enabled: true\n      allowed_users: [1]\n"), 0o644)
	ad := &cliAdapter{}
	old := buildAdapters
	buildAdapters = func(*config.Config, *config.Secrets) ([]gateway.Adapter, error) { return []gateway.Adapter{ad}, nil }
	defer func() { buildAdapters = old }()
	p := &script{steps: []llm.Response{say("hello from the gateway")}}
	ctx, cancel := context.WithCancel(ctx0)
	var o, e bytes.Buffer
	done := make(chan int)
	go func() {
		done <- run(ctx, []string{"gateway", "run"}, stdio{in: strings.NewReader(""), out: &o, err: &e}, app.Options{Provider: p})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ad.mu.Lock()
		h := ad.handle
		ad.mu.Unlock()
		if h != nil {
			h(gateway.Event{Platform: "fake", Target: gateway.Target{ChatID: "c"}, UserID: "1", Text: "hi", DM: true})
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	for !ad.seen("hello from the gateway") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !ad.seen("hello from the gateway") {
		t.Fatalf("no reply; stderr: %s", e.String())
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit %d: %s", code, e.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the gateway did not stop")
	}
	if !strings.Contains(e.String(), "serving fake") {
		t.Errorf("%s", e.String())
	}
}

func TestGatewayWithoutPlatformsExplainsAndUsageIsShown(t *testing.T) {
	ctx, _ := env(t)
	var o, e bytes.Buffer
	if code := run(ctx, []string{"gateway", "run"}, stdio{in: strings.NewReader(""), out: &o, err: &e}, app.Options{Provider: &script{}}); code != 1 || !strings.Contains(e.String(), "enabled") {
		t.Errorf("%d %s", code, e.String())
	}
	e.Reset()
	if code := run(ctx, []string{"gateway"}, stdio{in: strings.NewReader(""), out: &o, err: &e}, app.Options{}); code != 2 || !strings.Contains(e.String(), "usage:") {
		t.Errorf("%d %s", code, e.String())
	}
}

func TestPairingCommands(t *testing.T) {
	ctx, home := env(t)
	do := func(args ...string) (int, string, string) {
		var o, e bytes.Buffer
		code := run(ctx, append([]string{"pairing"}, args...), stdio{in: strings.NewReader(""), out: &o, err: &e}, app.Options{})
		return code, o.String(), e.String()
	}
	if code, out, _ := do("list"); code != 0 || !strings.Contains(out, "no pairing records") {
		t.Errorf("%d %s", code, out)
	}
	code, _, _ := gatewayRequest(home, "fake", "77", "Eve")
	if code == "" {
		t.Fatal("no code")
	}
	if c, out, _ := do("list"); c != 0 || !strings.Contains(out, "waiting   "+code) || !strings.Contains(out, "77 Eve") {
		t.Errorf("%d %s", c, out)
	}
	if c, out, _ := do("approve", "fake", code); c != 0 || !strings.Contains(out, "approved 77 on fake") {
		t.Errorf("%d %s", c, out)
	}
	if c, out, _ := do("list", "fake"); c != 0 || !strings.Contains(out, "approved  77 Eve") {
		t.Errorf("%d %s", c, out)
	}
	if c, _, _ := do("approve", "fake", code); c == 0 {
		t.Error("a code works once")
	}
	if c, out, _ := do("revoke", "fake", "77"); c != 0 || !strings.Contains(out, "revoked") {
		t.Errorf("%d %s", c, out)
	}
	if c, _, _ := do("revoke", "fake", "77"); c == 0 {
		t.Error("revoking twice")
	}
	for _, bad := range [][]string{{}, {"bogus"}, {"approve", "fake"}, {"revoke"}} {
		if c, _, _ := do(bad...); c != 2 {
			t.Errorf("%v: exit %d", bad, c)
		}
	}
}

func gatewayRequest(home, platform, user, name string) (string, bool, error) {
	return gateway.NewPairing(home).Request(platform, user, name)
}

func TestTelegramIsAKnownPlatformAndNeedsItsToken(t *testing.T) {
	ctx, home := env(t)
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	os.WriteFile(filepath.Join(home, "config.yaml"), []byte("gateway:\n  platforms:\n    telegram:\n      enabled: true\n"), 0o644)
	var o, e bytes.Buffer
	code := run(ctx, []string{"gateway", "run"}, stdio{in: strings.NewReader(""), out: &o, err: &e}, app.Options{Provider: &script{}})
	if code != 1 || !strings.Contains(e.String(), "telegram") || !strings.Contains(e.String(), "TELEGRAM_BOT_TOKEN") {
		t.Errorf("%d %s", code, e.String())
	}
}

func TestDiscordIsAKnownPlatformAndNeedsItsToken(t *testing.T) {
	ctx, home := env(t)
	t.Setenv("DISCORD_BOT_TOKEN", "")
	os.WriteFile(filepath.Join(home, "config.yaml"), []byte("gateway:\n  platforms:\n    discord:\n      enabled: true\n"), 0o644)
	var o, e bytes.Buffer
	code := run(ctx, []string{"gateway", "run"}, stdio{in: strings.NewReader(""), out: &o, err: &e}, app.Options{Provider: &script{}})
	if code != 1 || !strings.Contains(e.String(), "DISCORD_BOT_TOKEN") {
		t.Errorf("%d %s", code, e.String())
	}
}
