package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.UnixMilli(1_800_000_000_000)

// fakeKeychain is an in-memory Keychain: tests never touch the real one.
type fakeKeychain struct {
	item     *KeychainItem
	writes   []map[string]any
	writeErr error
}

func (k *fakeKeychain) Find(context.Context) (*KeychainItem, error) { return k.item, nil }

func (k *fakeKeychain) Write(_ context.Context, account string, payload map[string]any) error {
	if k.writeErr != nil {
		return k.writeErr
	}
	k.writes = append(k.writes, payload)
	k.item = &KeychainItem{Account: account, Payload: payload}
	return nil
}

func oauthPayload(access, refresh string, expiresAt int64, extra map[string]any) map[string]any {
	oauth := map[string]any{"accessToken": access, "refreshToken": refresh, "expiresAt": json.Number(fmt.Sprint(expiresAt))}
	for k, v := range extra {
		oauth[k] = v
	}
	return map[string]any{"claudeAiOauth": oauth}
}

// newStore returns a Store over a temp dir with a fixed clock and a fake Keychain.
func newStore(t *testing.T) (Store, *fakeKeychain) {
	t.Helper()
	resetRegistryForTest()
	t.Cleanup(resetRegistryForTest)
	kc := &fakeKeychain{}
	return Store{Dir: filepath.Join(t.TempDir(), ".claude"), Keychain: kc, Now: func() time.Time { return t0 }}, kc
}

func writeCredsFile(t *testing.T, s Store, payload map[string]any) {
	t.Helper()
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(payload)
	if err := os.WriteFile(s.CredentialsPath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestIsOAuthToken(t *testing.T) {
	tests := map[string]bool{
		"": false, "sk-ant-api03-abc": false, "sk-ant-api": false, "sk-ant-oat01-abc": true, "sk-ant-": true,
		"eyJhbGciOi": true, "cc-token": true, "sk-other": false, "random": false, "SK-ANT-oat": false,
	}
	for in, want := range tests {
		if got := IsOAuthToken(in); got != want {
			t.Errorf("IsOAuthToken(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestLoginValid(t *testing.T) {
	ms := t0.UnixMilli()
	tests := []struct {
		name string
		l    Login
		want bool
	}{
		{"future", Login{AccessToken: "a", ExpiresAtMS: ms + 3_600_000}, true},
		{"inside the 60s buffer", Login{AccessToken: "a", ExpiresAtMS: ms + 59_000}, false},
		{"just outside the buffer", Login{AccessToken: "a", ExpiresAtMS: ms + 61_000}, true},
		{"exactly at the buffer", Login{AccessToken: "a", ExpiresAtMS: ms + 60_000}, false},
		{"expired", Login{AccessToken: "a", ExpiresAtMS: ms - 1}, false},
		{"no expiry is a managed key: valid when present", Login{AccessToken: "a"}, true},
		{"no expiry and no token", Login{}, false},
	}
	for _, tc := range tests {
		if got := tc.l.Valid(t0); got != tc.want {
			t.Errorf("%s: Valid = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestDefaultStoreDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"default", nil, filepath.Join(home, ".claude")},
		{"blank is unset", map[string]string{"CLAUDE_CONFIG_DIR": "   "}, filepath.Join(home, ".claude")},
		{"override", map[string]string{"CLAUDE_CONFIG_DIR": "/opt/cc"}, "/opt/cc"},
		{"tilde", map[string]string{"CLAUDE_CONFIG_DIR": "~/cc"}, filepath.Join(home, "cc")},
	}
	for _, tc := range tests {
		if got := DefaultStore(env(tc.env)).Dir; got != tc.want {
			t.Errorf("%s: Dir = %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := (Store{Dir: "/x"}).CredentialsPath(); got != "/x/.credentials.json" {
		t.Errorf("CredentialsPath = %q", got)
	}
}

func TestStoreReadSources(t *testing.T) {
	ms := t0.UnixMilli()
	fresh, stale := ms+3_600_000, ms-1000
	tests := []struct {
		name        string
		kc, file    *Login
		wantAccess  string
		wantSource  string
		wantNothing bool
	}{
		{name: "nothing", wantNothing: true},
		{name: "file only", file: &Login{AccessToken: "F", ExpiresAtMS: fresh}, wantAccess: "F", wantSource: "claude_code_credentials_file"},
		{name: "keychain only", kc: &Login{AccessToken: "K", ExpiresAtMS: fresh}, wantAccess: "K", wantSource: "macos_keychain"},
		{name: "only the unexpired one wins (file)", kc: &Login{AccessToken: "K", ExpiresAtMS: stale}, file: &Login{AccessToken: "F", ExpiresAtMS: fresh}, wantAccess: "F", wantSource: "claude_code_credentials_file"},
		{name: "only the unexpired one wins (keychain)", kc: &Login{AccessToken: "K", ExpiresAtMS: fresh}, file: &Login{AccessToken: "F", ExpiresAtMS: stale}, wantAccess: "K", wantSource: "macos_keychain"},
		{name: "both valid: later expiry", kc: &Login{AccessToken: "K", ExpiresAtMS: fresh}, file: &Login{AccessToken: "F", ExpiresAtMS: fresh + 5000}, wantAccess: "F", wantSource: "claude_code_credentials_file"},
		{name: "both valid, tie: keychain", kc: &Login{AccessToken: "K", ExpiresAtMS: fresh}, file: &Login{AccessToken: "F", ExpiresAtMS: fresh}, wantAccess: "K", wantSource: "macos_keychain"},
		{name: "both expired: later expiry", kc: &Login{AccessToken: "K", ExpiresAtMS: stale}, file: &Login{AccessToken: "F", ExpiresAtMS: stale - 5000}, wantAccess: "K", wantSource: "macos_keychain"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, kc := newStore(t)
			if tc.kc != nil {
				kc.item = &KeychainItem{Account: "me", Payload: oauthPayload(tc.kc.AccessToken, "rk", tc.kc.ExpiresAtMS, nil)}
			}
			if tc.file != nil {
				writeCredsFile(t, s, oauthPayload(tc.file.AccessToken, "rf", tc.file.ExpiresAtMS, nil))
			}
			got := s.Read(context.Background())
			if tc.wantNothing {
				if got != nil {
					t.Fatalf("got %+v", got)
				}
				return
			}
			if got == nil || got.AccessToken != tc.wantAccess || got.Source != tc.wantSource {
				t.Fatalf("got %+v, want %s from %s", got, tc.wantAccess, tc.wantSource)
			}
		})
	}
}

func TestStoreReadTolerance(t *testing.T) {
	s, _ := newStore(t)
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cred := func(body string) { _ = os.WriteFile(s.CredentialsPath(), []byte(body), 0o600) }

	cred("\ufeff" + `{"claudeAiOauth":{"accessToken":"bom","refreshToken":"r","expiresAt":9007199254740993}}`)
	if l := s.Read(context.Background()); l == nil || l.AccessToken != "bom" {
		t.Fatalf("a BOM must not hide the login: %+v", l)
	} else if l.ExpiresAtMS != 9007199254740993 {
		t.Fatalf("a large integer must stay exact, got %d", l.ExpiresAtMS)
	}
	for name, body := range map[string]string{
		"corrupt":       `{not json`,
		"empty":         ``,
		"no oauth":      `{"other":1}`,
		"no access":     `{"claudeAiOauth":{"refreshToken":"r"}}`,
		"empty access":  `{"claudeAiOauth":{"accessToken":""}}`,
		"oauth is text": `{"claudeAiOauth":"nope"}`,
		"array root":    `[1,2]`,
	} {
		cred(body)
		if l := s.Read(context.Background()); l != nil {
			t.Errorf("%s: got %+v, want none", name, l)
		}
	}
}

func TestWriteFileKeepsSiblingsScopesAndMode(t *testing.T) {
	s, _ := newStore(t)
	writeCredsFile(t, s, map[string]any{
		"claudeAiOauth": map[string]any{"accessToken": "old", "refreshToken": "oldr", "expiresAt": 1, "scopes": []any{"user:inference", "user:profile"}, "subscriptionType": "max"},
		"mcpOAuth":      map[string]any{"server": "x"},
	})
	if err := os.Chmod(s.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.writeFile("NEW", "NEWR", 123456789012); err != nil {
		t.Fatal(err)
	}
	m := readJSONFile(s.CredentialsPath())
	oauth := m["claudeAiOauth"].(map[string]any)
	if oauth["accessToken"] != "NEW" || oauth["refreshToken"] != "NEWR" || oauth["expiresAt"].(json.Number).String() != "123456789012" {
		t.Fatalf("pair not rotated: %v", oauth)
	}
	if scopes, _ := oauth["scopes"].([]any); len(scopes) != 2 {
		t.Errorf("scopes lost: Claude Code 2.1.81+ gates on user:inference: %v", oauth)
	}
	if _, ok := m["mcpOAuth"]; !ok {
		t.Error("sibling keys must survive")
	}
	if fi, _ := os.Stat(s.CredentialsPath()); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(s.Dir); fi.Mode().Perm() != 0o755 {
		t.Errorf("the directory belongs to Claude Code, its mode must not change: %o", fi.Mode().Perm())
	}
	if entries, _ := os.ReadDir(s.Dir); len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
}

func TestWriteFileCreatesAndFailsClosedOnCorruption(t *testing.T) {
	s, _ := newStore(t)
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.writeFile("A", "R", 1); err != nil {
		t.Fatalf("a missing file is created: %v", err)
	}
	if l := s.Read(context.Background()); l == nil || l.AccessToken != "A" {
		t.Fatalf("written login not readable: %+v", l)
	}

	if err := os.WriteFile(s.CredentialsPath(), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := s.writeFile("B", "R2", 2)
	var pe *CredentialPersistError
	if !errors.As(err, &pe) || pe.Path != s.CredentialsPath() {
		t.Fatalf("err = %v, want CredentialPersistError", err)
	}
	if b, _ := os.ReadFile(s.CredentialsPath()); string(b) != "{corrupt" {
		t.Error("a corrupt file must not be overwritten")
	}
	if !strings.Contains(err.Error(), "durably persist") {
		t.Errorf("message = %q", err)
	}
}

func TestFingerprint(t *testing.T) {
	if fingerprint("") != "" || fingerprint("   ") != "" {
		t.Error("empty secrets have no fingerprint")
	}
	a, b := fingerprint("secret-1"), fingerprint(" secret-1 ")
	if a != b || !strings.HasPrefix(a, "sha256:") || len(a) != len("sha256:")+16 {
		t.Errorf("fingerprint = %q / %q", a, b)
	}
	if strings.Contains(a, "secret") || fingerprint("secret-2") == a {
		t.Error("fingerprints must be one-way and distinct")
	}
}

func TestSpentRotationRegistry(t *testing.T) {
	s, _ := newStore(t)
	path := s.CredentialsPath()
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if IsRotationConsumedUncommitted(path, "tok") {
		t.Fatal("unknown secret reported spent")
	}
	MarkRotationConsumedUncommitted(path, "tok-a", "", "tok-b")
	for _, sec := range []string{"tok-a", "tok-b"} {
		if !IsRotationConsumedUncommitted(path, sec) {
			t.Errorf("%s should be spent", sec)
		}
	}
	if IsRotationConsumedUncommitted(path, "") || IsRotationConsumedUncommitted(path, "tok-c") {
		t.Error("blank and unmarked secrets are not spent")
	}

	// The sidecar is the cross-process verdict: a fresh process (empty memory) still sees it.
	resetRegistryForTest()
	if !IsRotationConsumedUncommitted(path, "tok-a") {
		t.Fatal("another process must fail closed through the sidecar")
	}
	if IsRotationConsumedUncommitted("", "tok-a") {
		t.Error("with no path there is no sidecar to consult")
	}
	raw, _ := os.ReadFile(sidecarPath(path))
	if strings.Contains(string(raw), "tok-a") {
		t.Errorf("the sidecar must hold only fingerprints: %s", raw)
	}
	if fi, _ := os.Stat(sidecarPath(path)); fi.Mode().Perm() != 0o600 {
		t.Errorf("sidecar mode %o", fi.Mode().Perm())
	}
	// Later marks merge into it rather than replacing it.
	MarkRotationConsumedUncommitted(path, "tok-z")
	resetRegistryForTest()
	if !IsRotationConsumedUncommitted(path, "tok-a") || !IsRotationConsumedUncommitted(path, "tok-z") {
		t.Error("the sidecar must accumulate")
	}
}

func TestSpentRotationInMemoryIsBounded(t *testing.T) {
	resetRegistryForTest()
	t.Cleanup(resetRegistryForTest)
	for i := 0; i < spentMaxTracked+10; i++ {
		MarkRotationConsumedUncommitted("", fmt.Sprintf("tok-%d", i))
	}
	if IsRotationConsumedUncommitted("", "tok-0") {
		t.Error("the oldest entries are evicted")
	}
	if !IsRotationConsumedUncommitted("", fmt.Sprintf("tok-%d", spentMaxTracked+9)) {
		t.Error("the newest entry must remain")
	}
}
