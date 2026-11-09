package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// tokenServer is a fake OAuth token endpoint. handler decides each response; calls records the form bodies.
type tokenServer struct {
	*httptest.Server
	mu    sync.Mutex
	calls []url.Values
	uas   []string
}

func newTokenServer(t *testing.T, handler func(n int, form url.Values, w http.ResponseWriter)) *tokenServer {
	t.Helper()
	ts := &tokenServer{}
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(b))
		ts.mu.Lock()
		ts.calls = append(ts.calls, form)
		ts.uas = append(ts.uas, r.Header.Get("User-Agent"))
		n := len(ts.calls)
		ts.mu.Unlock()
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("content-type = %q", r.Header.Get("Content-Type"))
		}
		w.Header().Set("Content-Type", "application/json")
		handler(n, form, w)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func (ts *tokenServer) count() int { ts.mu.Lock(); defer ts.mu.Unlock(); return len(ts.calls) }

func okTokens(access, refresh string, expiresIn int) func(int, url.Values, http.ResponseWriter) {
	return func(_ int, _ url.Values, w http.ResponseWriter) {
		body := map[string]any{"access_token": access}
		if refresh != "" {
			body["refresh_token"] = refresh
		}
		if expiresIn != 0 {
			body["expires_in"] = expiresIn
		}
		_ = json.NewEncoder(w).Encode(body)
	}
}

func rejectWith(status int, code string) func(int, url.Values, http.ResponseWriter) {
	return func(_ int, _ url.Values, w http.ResponseWriter) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": code, "error_description": "nope"})
	}
}

func refresherFor(ts *tokenServer, urls ...string) *Refresher {
	if len(urls) == 0 {
		urls = []string{ts.URL}
	}
	return &Refresher{TokenURLs: urls, Now: func() time.Time { return t0 }}
}

func TestExchangeSendsTheRefreshGrant(t *testing.T) {
	ts := newTokenServer(t, okTokens("NEW", "NEWR", 7200))
	st, err := refresherFor(ts).Exchange(context.Background(), "OLDR")
	if err != nil {
		t.Fatal(err)
	}
	if st.AccessToken != "NEW" || st.RefreshToken != "NEWR" || st.ExpiresAtMS != t0.UnixMilli()+7_200_000 {
		t.Fatalf("state = %+v", st)
	}
	f := ts.calls[0]
	if f.Get("grant_type") != "refresh_token" || f.Get("refresh_token") != "OLDR" || f.Get("client_id") != oauthClientID {
		t.Errorf("form = %v", f)
	}
	if ts.uas[0] != "axios/1.7.9" {
		t.Errorf("user agent = %q: a claude-code/ UA is 429ed at the token endpoint", ts.uas[0])
	}
}

func TestExchangeDefaults(t *testing.T) {
	ts := newTokenServer(t, okTokens("NEW", "", 0)) // no refresh_token, no expires_in
	st, err := refresherFor(ts).Exchange(context.Background(), "KEEP")
	if err != nil {
		t.Fatal(err)
	}
	if st.RefreshToken != "KEEP" {
		t.Errorf("refresh token = %q: the old one stays when none is returned", st.RefreshToken)
	}
	if st.ExpiresAtMS != t0.UnixMilli()+3_600_000 {
		t.Errorf("expiry = %d: default 3600s", st.ExpiresAtMS)
	}
}

func TestExchangeErrors(t *testing.T) {
	if _, err := refresherFor(nil, "http://unused").Exchange(context.Background(), ""); err == nil {
		t.Error("an empty refresh token is an error")
	}
	ts := newTokenServer(t, func(_ int, _ url.Values, w http.ResponseWriter) {
		_ = json.NewEncoder(w).Encode(map[string]any{"token_type": "x"})
	})
	if _, err := refresherFor(ts).Exchange(context.Background(), "R"); err == nil || !strings.Contains(err.Error(), "access_token") {
		t.Errorf("missing access_token: %v", err)
	}
	ts2 := newTokenServer(t, func(_ int, _ url.Values, w http.ResponseWriter) { io.WriteString(w, "<html>") })
	if _, err := refresherFor(ts2).Exchange(context.Background(), "R"); err == nil {
		t.Error("a non-JSON success body is an error")
	}
}

func TestExchangeFallsBackAcrossEndpointsButNotOnADeadGrant(t *testing.T) {
	down := newTokenServer(t, rejectWith(404, ""))
	up := newTokenServer(t, okTokens("NEW", "NEWR", 3600))
	st, err := refresherFor(nil, down.URL, up.URL).Exchange(context.Background(), "R")
	if err != nil || st.AccessToken != "NEW" || down.count() != 1 || up.count() != 1 {
		t.Fatalf("state = %+v, err = %v, calls = %d/%d", st, err, down.count(), up.count())
	}

	dead := newTokenServer(t, rejectWith(400, "invalid_grant"))
	second := newTokenServer(t, okTokens("SHOULD-NOT-BE-USED", "", 0))
	_, err = refresherFor(nil, dead.URL, second.URL).Exchange(context.Background(), "R")
	if !IsTerminalRefreshError(err) || second.count() != 0 {
		t.Fatalf("err = %v, second endpoint calls = %d: a dead grant is dead at every endpoint", err, second.count())
	}

	allDown := newTokenServer(t, rejectWith(500, "server_error"))
	_, err = refresherFor(nil, allDown.URL, allDown.URL).Exchange(context.Background(), "R")
	var oe *OAuthError
	if !errors.As(err, &oe) || oe.Status != 500 || allDown.count() != 2 {
		t.Fatalf("err = %v, calls = %d: the last error is returned after trying every endpoint", err, allDown.count())
	}
}

func TestOAuthErrorClassification(t *testing.T) {
	tests := []struct {
		status int
		code   string
		dead   bool
	}{
		{400, "invalid_grant", true}, {401, "invalid_token", true}, {400, "refresh_token_reused", true},
		{429, "invalid_grant", false}, {500, "invalid_grant", false}, {400, "invalid_request", false}, {400, "", false},
	}
	for _, tc := range tests {
		e := &OAuthError{Status: tc.status, Code: tc.code, What: "refresh"}
		if e.ReloginRequired() != tc.dead || IsTerminalRefreshError(e) != tc.dead {
			t.Errorf("%d %q: ReloginRequired = %v, want %v", tc.status, tc.code, e.ReloginRequired(), tc.dead)
		}
	}
	if IsTerminalRefreshError(errors.New("network")) || IsTerminalRefreshError(nil) {
		t.Error("only OAuthError can be terminal")
	}
	msg := (&OAuthError{Status: 400, Code: "invalid_grant", Description: "expired", What: "refresh"}).Error()
	if !strings.Contains(msg, "400") || !strings.Contains(msg, "invalid_grant") || !strings.Contains(msg, "(expired)") {
		t.Errorf("message = %q", msg)
	}
	if got := (&OAuthError{Status: 502, What: "refresh"}).Error(); !strings.Contains(got, "error") {
		t.Errorf("message without a code = %q", got)
	}
}

func staleLogin(refresh string) Login {
	return Login{AccessToken: "STALE", RefreshToken: refresh, ExpiresAtMS: t0.UnixMilli() - 1000, Source: "claude_code_credentials_file"}
}

func TestRefreshLoginRotatesAndCommitsToFileAndKeychain(t *testing.T) {
	s, kc := newStore(t)
	writeCredsFile(t, s, oauthPayload("STALE", "RT0", t0.UnixMilli()-1000, map[string]any{"scopes": []any{"user:inference"}}))
	kc.item = &KeychainItem{Account: "alice", Payload: oauthPayload("STALE", "RT0", t0.UnixMilli()-1000, nil)}
	ts := newTokenServer(t, okTokens("FRESH", "RT1", 3600))

	got, err := refresherFor(ts).RefreshLogin(context.Background(), s, staleLogin("RT0"))
	if err != nil || got != "FRESH" {
		t.Fatalf("token = %q, err = %v", got, err)
	}
	if ts.calls[0].Get("refresh_token") != "RT0" {
		t.Errorf("posted %v", ts.calls[0])
	}
	file := readJSONFile(s.CredentialsPath())["claudeAiOauth"].(map[string]any)
	if file["accessToken"] != "FRESH" || file["refreshToken"] != "RT1" || file["scopes"] == nil {
		t.Errorf("file = %v", file)
	}
	if len(kc.writes) != 1 || kc.item.Account != "alice" {
		t.Fatalf("keychain writes = %d: the rotated pair must be mirrored", len(kc.writes))
	}
	if rt := kc.item.Payload["claudeAiOauth"].(map[string]any)["refreshToken"]; rt != "RT1" {
		t.Errorf("keychain refresh token = %v", rt)
	}
	if fi, _ := os.Stat(s.CredentialsPath()); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %o", fi.Mode().Perm())
	}
}

func TestRefreshLoginAdoptsAnAlreadyRotatedTokenWithoutPosting(t *testing.T) {
	s, _ := newStore(t)
	// Claude Code refreshed on its own schedule since we read the stale login.
	writeCredsFile(t, s, oauthPayload("CLAUDE-REFRESHED", "RT9", t0.UnixMilli()+3_600_000, nil))
	ts := newTokenServer(t, okTokens("NOPE", "NOPE", 1))
	got, err := refresherFor(ts).RefreshLogin(context.Background(), s, staleLogin("RT0"))
	if err != nil || got != "CLAUDE-REFRESHED" || ts.count() != 0 {
		t.Fatalf("token = %q, err = %v, posts = %d: racing a rotation into invalid_grant is the bug", got, err, ts.count())
	}
}

func TestRefreshLoginDoesNotAdoptAnUnknownExpiry(t *testing.T) {
	s, _ := newStore(t)
	writeCredsFile(t, s, map[string]any{"claudeAiOauth": map[string]any{"accessToken": "MANAGED", "refreshToken": "RT0"}}) // expiresAt absent
	ts := newTokenServer(t, okTokens("FRESH", "RT1", 3600))
	got, err := refresherFor(ts).RefreshLogin(context.Background(), s, staleLogin("RT0"))
	if err != nil || got != "FRESH" || ts.count() != 1 {
		t.Fatalf("token = %q, err = %v, posts = %d", got, err, ts.count())
	}
}

func TestRefreshLoginPreconditions(t *testing.T) {
	ctx := context.Background()
	t.Run("no refresh token", func(t *testing.T) {
		s, _ := newStore(t)
		ts := newTokenServer(t, okTokens("x", "", 0))
		if _, err := refresherFor(ts).RefreshLogin(ctx, s, staleLogin("")); !errors.Is(err, ErrNoRefreshToken) || ts.count() != 0 {
			t.Fatalf("err = %v, posts = %d", err, ts.count())
		}
	})
	t.Run("a spent rotation is never replayed, even by another process", func(t *testing.T) {
		s, _ := newStore(t)
		if err := os.MkdirAll(s.Dir, 0o755); err != nil {
			t.Fatal(err)
		}
		MarkRotationConsumedUncommitted(s.CredentialsPath(), "RT0")
		resetRegistryForTest() // a different process: only the sidecar remembers
		ts := newTokenServer(t, okTokens("x", "", 0))
		if _, err := refresherFor(ts).RefreshLogin(ctx, s, staleLogin("RT0")); !errors.Is(err, ErrRotationSpent) || ts.count() != 0 {
			t.Fatalf("err = %v, posts = %d", err, ts.count())
		}
	})
}

func TestRefreshLoginRemembersADeadGrant(t *testing.T) {
	s, _ := newStore(t)
	writeCredsFile(t, s, oauthPayload("STALE", "RT0", t0.UnixMilli()-1000, nil))
	ts := newTokenServer(t, rejectWith(400, "invalid_grant"))
	r := refresherFor(ts)
	for i := 0; i < 3; i++ {
		_, err := r.RefreshLogin(context.Background(), s, staleLogin("RT0"))
		if !IsTerminalRefreshError(err) {
			t.Fatalf("attempt %d: err = %v", i, err)
		}
	}
	if ts.count() != 1 {
		t.Fatalf("posts = %d: a rejected refresh token must not be replayed", ts.count())
	}
}

func TestRefreshLoginFailsClosedWhenTheCommitFails(t *testing.T) {
	s, kc := newStore(t)
	writeCredsFile(t, s, oauthPayload("STALE", "RT0", t0.UnixMilli()-1000, nil))
	if err := os.WriteFile(s.CredentialsPath(), []byte(`{corrupt`), 0o600); err != nil { // the commit will refuse a corrupt file
		t.Fatal(err)
	}
	ts := newTokenServer(t, okTokens("FRESH", "RT1", 3600))
	login := staleLogin("RT0")
	_, err := refresherFor(ts).RefreshLogin(context.Background(), s, login)
	var pe *CredentialPersistError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want CredentialPersistError", err)
	}
	if ts.count() != 1 || len(kc.writes) != 0 {
		t.Fatalf("posts = %d, keychain writes = %d", ts.count(), len(kc.writes))
	}
	// The POST spent RT0 and nothing durable holds its replacement: it must never be replayed.
	if !IsRotationConsumedUncommitted(s.CredentialsPath(), "RT0") || !IsRotationConsumedUncommitted(s.CredentialsPath(), "STALE") {
		t.Fatal("the spent pair must be recorded")
	}
	if _, err := refresherFor(ts).RefreshLogin(context.Background(), s, login); !errors.Is(err, ErrRotationSpent) || ts.count() != 1 {
		t.Fatalf("second attempt: err = %v, posts = %d", err, ts.count())
	}
}

func TestKeychainMirrorFailureDoesNotFailTheRotation(t *testing.T) {
	s, kc := newStore(t)
	writeCredsFile(t, s, oauthPayload("STALE", "RT0", t0.UnixMilli()-1000, nil))
	kc.item = &KeychainItem{Account: "a", Payload: oauthPayload("STALE", "RT0", 1, nil)}
	kc.writeErr = errors.New("keychain locked")
	ts := newTokenServer(t, okTokens("FRESH", "RT1", 3600))
	if got, err := refresherFor(ts).RefreshLogin(context.Background(), s, staleLogin("RT0")); err != nil || got != "FRESH" {
		t.Fatalf("token = %q, err = %v: the file commit already succeeded", got, err)
	}
}

func TestConcurrentRefreshesSpendTheSingleUseTokenOnce(t *testing.T) {
	s, _ := newStore(t)
	writeCredsFile(t, s, oauthPayload("STALE", "RT0", t0.UnixMilli()-1000, nil))
	var posts atomic.Int32
	ts := newTokenServer(t, func(_ int, form url.Values, w http.ResponseWriter) {
		posts.Add(1)
		time.Sleep(80 * time.Millisecond) // widen the race window
		if form.Get("refresh_token") != "RT0" {
			rejectWith(400, "invalid_grant")(0, form, w)
			return
		}
		okTokens("FRESH", "RT1", 3600)(0, form, w)
	})
	var wg sync.WaitGroup
	results := make([]string, 6)
	errs := make([]error, 6)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := refresherFor(ts)
			results[i], errs[i] = r.RefreshLogin(context.Background(), s, staleLogin("RT0"))
		}()
	}
	wg.Wait()
	if posts.Load() != 1 {
		t.Fatalf("posts = %d: two callers spent one single-use refresh token", posts.Load())
	}
	for i := range results {
		if errs[i] != nil || results[i] != "FRESH" {
			t.Errorf("caller %d: token = %q, err = %v", i, results[i], errs[i])
		}
	}
}

func TestLockFileTimesOutAndHonoursContext(t *testing.T) {
	path := t.TempDir() + "/x.lock"
	unlock, err := lockFile(context.Background(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := lockFile(context.Background(), path, 150*time.Millisecond); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("took too long")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := lockFile(ctx, path, time.Minute); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the context's error", err)
	}
	unlock()
	if u, err := lockFile(context.Background(), path, time.Second); err != nil {
		t.Fatalf("lock not released: %v", err)
	} else {
		u()
	}
}
