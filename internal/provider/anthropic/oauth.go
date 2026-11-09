package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	// oauthClientID is Claude Code's public OAuth client id.
	oauthClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	// oauthTokenUserAgent: Anthropic 429s token-endpoint requests whose UA starts with claude-code/ (or
	// Mozilla); the real CLI uses bare axios there. Inference still needs claude-code/.
	oauthTokenUserAgent = "axios/1.7.9"
	refreshTimeout      = 10 * time.Second
	// defaultLockTimeout bounds waiting for another process's refresh.
	defaultLockTimeout = 25 * time.Second
)

// defaultTokenURLs: platform.claude.com is the live token host; console.anthropic.com 404s but stays
// as a fallback.
var defaultTokenURLs = []string{"https://platform.claude.com/v1/oauth/token", "https://console.anthropic.com/v1/oauth/token"}

// deadGrantCodes are the OAuth error codes (RFC 6749 section 5.2 plus the provider's reuse detection)
// after which replaying the same refresh token can never succeed; only a fresh login recovers.
var deadGrantCodes = map[string]bool{"invalid_grant": true, "invalid_token": true, "refresh_token_reused": true}

// OAuthError is a rejection from the token endpoint. Code is the OAuth error field of the body.
type OAuthError struct {
	Status      int
	Code        string
	Description string
	What        string
}

func (e *OAuthError) Error() string {
	detail := ""
	if e.Description != "" {
		detail = " (" + e.Description + ")"
	}
	code := e.Code
	if code == "" {
		code = "error"
	}
	return fmt.Sprintf("anthropic token %s failed: HTTP %d %s%s", e.What, e.Status, code, detail)
}

// ReloginRequired is true when the grant is dead: replaying the same refresh token cannot succeed.
func (e *OAuthError) ReloginRequired() bool {
	return (e.Status == 400 || e.Status == 401) && deadGrantCodes[e.Code]
}

// IsTerminalRefreshError is true when retrying the same refresh token cannot succeed.
func IsTerminalRefreshError(err error) bool {
	var oe *OAuthError
	return errors.As(err, &oe) && oe.ReloginRequired()
}

// Token errors a caller can act on.
var (
	// ErrNoCredentials means no token source produced a credential.
	ErrNoCredentials = errors.New("no Anthropic credentials: set ANTHROPIC_API_KEY, or ANTHROPIC_TOKEN, or enable auth.adopt_external_logins to use Claude Code's login")
	// ErrNoRefreshToken means the login has no refresh token.
	ErrNoRefreshToken = errors.New("the Claude Code login has no refresh token")
	// ErrRotationSpent means this refresh token was already consumed by a rotation that never
	// committed, so replaying it would fail. Run a fresh login.
	ErrRotationSpent = errors.New("the refresh token was consumed by a rotation that was never committed; log in again")
)

// TokenState is a refreshed token pair.
type TokenState struct {
	AccessToken  string
	RefreshToken string
	ExpiresAtMS  int64
}

// Refresher exchanges refresh tokens at the OAuth token endpoint.
type Refresher struct {
	HTTP      *http.Client
	TokenURLs []string
	Now       func() time.Time
	// LockTimeout bounds the wait for the cross-process credential lock.
	LockTimeout time.Duration
}

func (r *Refresher) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Refresher) urls() []string {
	if len(r.TokenURLs) > 0 {
		return r.TokenURLs
	}
	return defaultTokenURLs
}

func (r *Refresher) client() *http.Client {
	if r.HTTP != nil {
		return r.HTTP
	}
	return &http.Client{Timeout: refreshTimeout}
}

// Exchange trades a refresh token for a new pair without touching any credential file. It tries the
// token endpoints in order and returns the last error when all fail; a dead grant is dead at every
// endpoint, so it stops there rather than replaying it.
func (r *Refresher) Exchange(ctx context.Context, refreshToken string) (TokenState, error) {
	if refreshToken == "" {
		return TokenState{}, errors.New("refresh_token is required")
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}, "client_id": {oauthClientID}}
	var last error
	for _, endpoint := range r.urls() {
		state, err := r.post(ctx, endpoint, form, refreshToken)
		if err == nil {
			return state, nil
		}
		last = err
		slog.Debug("anthropic token refresh failed", "endpoint", endpoint, "err", err)
		var oe *OAuthError
		if errors.As(err, &oe) && oe.ReloginRequired() {
			break
		}
		if ctx.Err() != nil {
			break
		}
	}
	return TokenState{}, last
}

func (r *Refresher) post(ctx context.Context, endpoint string, form url.Values, fallbackRefresh string) (TokenState, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return TokenState{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", oauthTokenUserAgent)
	resp, err := r.client().Do(req)
	if err != nil {
		return TokenState{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		oe := &OAuthError{Status: resp.StatusCode, What: "refresh"}
		var body struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		if json.Unmarshal(raw, &body) == nil {
			oe.Code, oe.Description = body.Error, body.Description
		}
		return TokenState{}, oe
	}
	var body struct {
		AccessToken  string  `json:"access_token"`
		RefreshToken string  `json:"refresh_token"`
		ExpiresIn    float64 `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return TokenState{}, fmt.Errorf("anthropic refresh response is not JSON: %w", err)
	}
	if body.AccessToken == "" {
		return TokenState{}, errors.New("anthropic refresh response was missing access_token")
	}
	if body.RefreshToken == "" {
		body.RefreshToken = fallbackRefresh
	}
	if body.ExpiresIn == 0 {
		body.ExpiresIn = 3600
	}
	return TokenState{
		AccessToken: body.AccessToken, RefreshToken: body.RefreshToken,
		ExpiresAtMS: r.now().UnixMilli() + int64(body.ExpiresIn*1000),
	}, nil
}

// lockFile takes an exclusive advisory lock on path, waiting up to timeout. Refresh tokens are
// single-use, so reading the login, deciding, POSTing and writing back must be one critical section
// across every process sharing the credential file, or two of them can spend the same token.
func lockFile(ctx context.Context, path string, timeout time.Duration) (unlock func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for {
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
		} else if !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close()
			return nil, err
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("timed out waiting for the credential lock %s", path)
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// RefreshLogin refreshes an expired Claude Code login and returns the new access token. Refresh
// tokens are single-use and Claude Code refreshes on its own schedule, so it first re-reads the live
// sources and adopts an already-rotated token instead of racing it into invalid_grant. The read, the
// decision, the POST and the write-back all happen under the credential file's lock.
func (r *Refresher) RefreshLogin(ctx context.Context, store Store, stale Login) (string, error) {
	path := store.CredentialsPath()
	timeout := r.LockTimeout
	if timeout <= 0 {
		timeout = defaultLockTimeout
	}
	unlock, err := lockFile(ctx, path+".lock", timeout)
	if err != nil {
		return "", err
	}
	defer unlock()

	// Adopt only a DIFFERENT token with a real future expiry (0 or absent means a managed key).
	current := store.Read(ctx)
	if current == nil {
		current = &Login{}
	}
	if current.AccessToken != "" && current.AccessToken != stale.AccessToken && current.ExpiresAtMS > 0 && current.Valid(r.now()) {
		slog.Debug("adopted Claude Code's already-refreshed OAuth token")
		return current.AccessToken, nil
	}
	refresh := current.RefreshToken
	if refresh == "" {
		refresh = stale.RefreshToken
	}
	if refresh == "" {
		return "", ErrNoRefreshToken
	}
	// Another process may have spent this token and lost the commit; its sidecar verdict is authoritative.
	if IsRotationConsumedUncommitted(path, refresh) {
		return "", ErrRotationSpent
	}
	fp := fingerprint(refresh)
	spentMu.Lock()
	dead := deadRefresh[fp]
	spentMu.Unlock()
	if dead {
		return "", &OAuthError{Status: 400, Code: "invalid_grant", What: "refresh (rejected earlier, not replayed)"}
	}
	state, err := r.Exchange(ctx, refresh)
	if err != nil {
		if IsTerminalRefreshError(err) {
			spentMu.Lock()
			deadRefresh[fp] = true
			spentMu.Unlock()
			slog.Warn("the Claude Code OAuth refresh token is terminally invalid; log in again", "err", err)
		}
		return "", err
	}
	// The POST spent `refresh`; the write below is the commit step. If it fails, fail closed and mark the
	// pre-rotation pair as spent so nothing replays it.
	if err := store.writeFile(state.AccessToken, state.RefreshToken, state.ExpiresAtMS); err != nil {
		MarkRotationConsumedUncommitted(path, refresh, stale.AccessToken, current.AccessToken, current.RefreshToken)
		return "", err
	}
	// The file commit succeeded; a Keychain hiccup must not fail the rotation.
	if err := mirrorToKeychain(ctx, store.Keychain, state.AccessToken, state.RefreshToken, state.ExpiresAtMS, refresh); err != nil {
		slog.Debug("keychain mirror skipped", "err", err)
	}
	return state.AccessToken, nil
}
