package anthropic

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// env is a Lookuper over a map.
type env map[string]string

func (e env) Lookup(k string) (string, bool) { v, ok := e[k]; return v, ok }

func newCreds(t *testing.T, e env, adopt bool) (*Credentials, Store, *fakeKeychain) {
	t.Helper()
	s, kc := newStore(t)
	return &Credentials{Env: e, AdoptExternalLogin: adopt, Store: s, Version: func() string { return "9.9.9" }}, s, kc
}

func freshLogin(t *testing.T, s Store, access, refresh string) {
	t.Helper()
	writeCredsFile(t, s, oauthPayload(access, refresh, t0.UnixMilli()+3_600_000, nil))
}

func TestTokenResolutionOrder(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name  string
		env   env
		adopt bool
		login func(t *testing.T, s Store)
		want  string
		err   error
	}{
		{name: "nothing configured", err: ErrNoCredentials},
		{name: "api key", env: env{"ANTHROPIC_API_KEY": "sk-ant-api03-K"}, want: "sk-ant-api03-K"},
		{name: "blank values are unset", env: env{"ANTHROPIC_API_KEY": "  ", "ANTHROPIC_TOKEN": ""}, err: ErrNoCredentials},
		{name: "values are trimmed", env: env{"ANTHROPIC_API_KEY": "  sk-ant-api03-K \n"}, want: "sk-ant-api03-K"},
		{name: "ANTHROPIC_TOKEN beats the api key", env: env{"ANTHROPIC_TOKEN": "sk-ant-oat01-T", "ANTHROPIC_API_KEY": "sk-ant-api03-K"}, want: "sk-ant-oat01-T"},
		{name: "CLAUDE_CODE_OAUTH_TOKEN is the second name", env: env{"CLAUDE_CODE_OAUTH_TOKEN": "sk-ant-oat01-C"}, want: "sk-ant-oat01-C"},
		{name: "ANTHROPIC_TOKEN is checked first", env: env{"ANTHROPIC_TOKEN": "sk-ant-oat01-T", "CLAUDE_CODE_OAUTH_TOKEN": "sk-ant-oat01-C"}, want: "sk-ant-oat01-T"},
		{
			name: "an explicit api key is never shadowed by a discovered login", env: env{"ANTHROPIC_API_KEY": "sk-ant-api03-K"}, adopt: true,
			login: func(t *testing.T, s Store) { freshLogin(t, s, "sk-ant-oat01-LOGIN", "rt") }, want: "sk-ant-api03-K",
		},
		{
			name: "the borrowed login is ignored unless adoption is on", login: func(t *testing.T, s Store) { freshLogin(t, s, "sk-ant-oat01-LOGIN", "rt") },
			err: ErrNoCredentials,
		},
		{
			name: "adopted login", adopt: true, login: func(t *testing.T, s Store) { freshLogin(t, s, "sk-ant-oat01-LOGIN", "rt") },
			want: "sk-ant-oat01-LOGIN",
		},
		{
			name: "a static env OAuth token yields to a refreshable adopted login", env: env{"ANTHROPIC_TOKEN": "sk-ant-oat01-STATIC"}, adopt: true,
			login: func(t *testing.T, s Store) { freshLogin(t, s, "sk-ant-oat01-LOGIN", "rt") }, want: "sk-ant-oat01-LOGIN",
		},
		{
			name: "the env token stays when the login has no refresh token", env: env{"ANTHROPIC_TOKEN": "sk-ant-oat01-STATIC"}, adopt: true,
			login: func(t *testing.T, s Store) { freshLogin(t, s, "sk-ant-oat01-LOGIN", "") }, want: "sk-ant-oat01-STATIC",
		},
		{
			name: "a non-OAuth env token is never replaced", env: env{"ANTHROPIC_TOKEN": "plain-key"}, adopt: true,
			login: func(t *testing.T, s Store) { freshLogin(t, s, "sk-ant-oat01-LOGIN", "rt") }, want: "plain-key",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, s, _ := newCreds(t, tc.env, tc.adopt)
			if tc.login != nil {
				tc.login(t, s)
			}
			got, err := c.Token(ctx)
			if !errors.Is(err, tc.err) || got != tc.want {
				t.Fatalf("token = %q, err = %v; want %q, %v", got, err, tc.want, tc.err)
			}
		})
	}
}

func TestExpiredAdoptedLoginIsRefreshed(t *testing.T) {
	c, s, _ := newCreds(t, nil, true)
	writeCredsFile(t, s, oauthPayload("sk-ant-oat01-STALE", "RT0", t0.UnixMilli()-5000, nil))
	ts := newTokenServer(t, okTokens("sk-ant-oat01-FRESH", "RT1", 3600))
	c.Refresher = refresherFor(ts)
	got, err := c.Token(context.Background())
	if err != nil || got != "sk-ant-oat01-FRESH" || ts.count() != 1 {
		t.Fatalf("token = %q, err = %v, posts = %d", got, err, ts.count())
	}
	// The rotated pair is on disk, so the next resolve is a plain read with no network.
	c.Store.Now = func() time.Time { return t0.Add(time.Minute) }
	if got, err := c.Token(context.Background()); err != nil || got != "sk-ant-oat01-FRESH" || ts.count() != 1 {
		t.Fatalf("second resolve: token = %q, err = %v, posts = %d", got, err, ts.count())
	}
}

func TestExpiredLoginRefreshFailureIsReported(t *testing.T) {
	c, s, _ := newCreds(t, nil, true)
	writeCredsFile(t, s, oauthPayload("sk-ant-oat01-STALE", "RT0", t0.UnixMilli()-5000, nil))
	ts := newTokenServer(t, rejectWith(400, "invalid_grant"))
	c.Refresher = refresherFor(ts)
	_, err := c.Token(context.Background())
	if !IsTerminalRefreshError(err) || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("err = %v: the caller needs to know a re-login is required", err)
	}
}

func TestSpentAccessTokenIsRefused(t *testing.T) {
	c, s, _ := newCreds(t, nil, true)
	freshLogin(t, s, "sk-ant-oat01-SPENT", "rt")
	MarkRotationConsumedUncommitted(s.CredentialsPath(), "sk-ant-oat01-SPENT")
	if _, err := c.Token(context.Background()); !errors.Is(err, ErrRotationSpent) {
		t.Fatalf("err = %v: the file still holds the spent copy of a failed commit", err)
	}
}

func TestApplyHeaders(t *testing.T) {
	ctx := context.Background()
	t.Run("api key", func(t *testing.T) {
		c, _, _ := newCreds(t, env{"ANTHROPIC_API_KEY": "sk-ant-api03-K"}, false)
		h := http.Header{}
		oauth, err := c.Apply(ctx, h)
		if err != nil || oauth || h.Get("x-api-key") != "sk-ant-api03-K" || h.Get("Authorization") != "" || h.Get("User-Agent") != "" {
			t.Fatalf("oauth = %v, err = %v, headers = %v", oauth, err, h)
		}
	})
	t.Run("oauth token carries the Claude Code identity", func(t *testing.T) {
		c, _, _ := newCreds(t, env{"ANTHROPIC_TOKEN": "sk-ant-oat01-T"}, false)
		h := http.Header{}
		oauth, err := c.Apply(ctx, h)
		if err != nil || !oauth {
			t.Fatalf("oauth = %v, err = %v", oauth, err)
		}
		if h.Get("Authorization") != "Bearer sk-ant-oat01-T" || h.Get("User-Agent") != "claude-code/9.9.9 (external, cli)" || h.Get("x-app") != "cli" || h.Get("x-api-key") != "" {
			t.Fatalf("headers = %v", h)
		}
	})
	t.Run("no credentials", func(t *testing.T) {
		c, _, _ := newCreds(t, nil, false)
		if _, err := c.Apply(ctx, http.Header{}); !errors.Is(err, ErrNoCredentials) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestForceRefresh(t *testing.T) {
	ctx := context.Background()
	c, s, _ := newCreds(t, nil, true)
	freshLogin(t, s, "sk-ant-oat01-LOOKS-VALID", "RT0") // unexpired, yet the API said 401
	ts := newTokenServer(t, okTokens("sk-ant-oat01-NEW", "RT1", 3600))
	c.Refresher = refresherFor(ts)
	got, err := c.ForceRefresh(ctx)
	if err != nil || got != "sk-ant-oat01-NEW" || ts.count() != 1 {
		t.Fatalf("token = %q, err = %v, posts = %d", got, err, ts.count())
	}
	off, _, _ := newCreds(t, nil, false)
	if _, err := off.ForceRefresh(ctx); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("without adoption there is nothing to refresh: %v", err)
	}
}

func TestDetectClaudeCodeVersion(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	mk := func(p string) string {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	prefixed := mk(filepath.Join(bin, "claude"))
	onPath := mk(filepath.Join(home, "claude-on-path"))
	lookPath := func(name string) (string, error) {
		if name == "claude" {
			return onPath, nil
		}
		return "", errors.New("not found")
	}
	outputs := map[string]string{}
	var tried []string
	run := func(_ context.Context, _ string, name string, args ...string) (string, string, error) {
		tried = append(tried, name)
		if len(args) != 1 || args[0] != "--version" {
			t.Errorf("args = %v", args)
		}
		out, ok := outputs[name]
		if !ok {
			return "", "", errors.New("exec failed")
		}
		return out, "", nil
	}

	outputs[onPath] = "2.1.99 (Claude Code)\n"
	if got := DetectClaudeCodeVersion(run, lookPath, home); got != "2.1.99" || tried[0] != onPath {
		t.Fatalf("got %q after %v: PATH hits come before the well-known prefixes", got, tried)
	}
	tried = nil
	outputs[onPath] = "3.0.1\n"
	if got := DetectClaudeCodeVersion(run, lookPath, home); got != "3.0.1" {
		t.Fatalf("got %q", got)
	}
	tried = nil
	outputs[onPath] = "Claude Code version unknown" // not digit-led: try the next candidate
	outputs[prefixed] = "2.2.0"
	if got := DetectClaudeCodeVersion(run, lookPath, home); got != "2.2.0" || len(tried) != 2 {
		t.Fatalf("got %q after %v", got, tried)
	}
	delete(outputs, onPath)
	delete(outputs, prefixed)
	if got := DetectClaudeCodeVersion(run, lookPath, home); got != claudeCodeVersionFallback {
		t.Fatalf("got %q, want the fallback", got)
	}
	if got := DetectClaudeCodeVersion(run, func(string) (string, error) { return "", errors.New("none") }, t.TempDir()); got != claudeCodeVersionFallback {
		t.Fatalf("no candidates at all: %q", got)
	}
}

func TestCredentialsWorkWithTheClient(t *testing.T) {
	var gotHeader http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(okStream))
	}))
	defer srv.Close()
	c, _, _ := newCreds(t, env{"ANTHROPIC_TOKEN": "sk-ant-oat01-T"}, false)
	if _, err := (&Client{BaseURL: srv.URL, Auth: c}).Create(context.Background(), buildReq(t, RequestOptions{OAuth: true}), CallOptions{}); err != nil {
		t.Fatal(err)
	}
	if gotHeader.Get("Authorization") != "Bearer sk-ant-oat01-T" || gotHeader.Get("Anthropic-Beta") != BetaHeader(true) || gotHeader.Get("User-Agent") != "claude-code/9.9.9 (external, cli)" {
		t.Fatalf("headers = %v", gotHeader)
	}
}
