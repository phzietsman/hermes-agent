package anthropic

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Login is an OAuth login read from Claude Code's credential store.
type Login struct {
	AccessToken  string
	RefreshToken string
	// ExpiresAtMS is the access token's expiry in Unix milliseconds. Zero means a managed key or an
	// unknown expiry: the token is treated as valid while present.
	ExpiresAtMS int64
	// Source says where it came from: "macos_keychain" or "claude_code_credentials_file".
	Source string
}

// expiryBuffer is the margin before expiry at which a token already counts as expired.
const expiryBuffer = 60 * time.Second

// IsOAuthToken is true for Anthropic OAuth and setup tokens (sk-ant-*, eyJ JWTs, cc-) and false for
// sk-ant-api* Console keys.
func IsOAuthToken(key string) bool {
	if key == "" || strings.HasPrefix(key, "sk-ant-api") {
		return false
	}
	return strings.HasPrefix(key, "sk-ant-") || strings.HasPrefix(key, "eyJ") || strings.HasPrefix(key, "cc-")
}

// Valid reports whether the access token is unexpired with a 60 second margin.
func (l Login) Valid(now time.Time) bool {
	if l.ExpiresAtMS == 0 {
		return l.AccessToken != ""
	}
	return now.UnixMilli() < l.ExpiresAtMS-expiryBuffer.Milliseconds()
}

// CredentialPersistError means a rotated single-use credential could not be durably committed. The
// refresh POST already spent the old refresh token, so a swallowed write failure would leave a
// consumed pair on disk that later replays as invalid_grant.
type CredentialPersistError struct {
	Path string
	Err  error
}

func (e *CredentialPersistError) Error() string {
	return fmt.Sprintf("failed to durably persist rotated Anthropic credentials to %s: %v", e.Path, e.Err)
}

func (e *CredentialPersistError) Unwrap() error { return e.Err }

// ---- Claude Code's credential store -----------------------------------------------------------

// Store is Claude Code's login: the shared credentials file and, on macOS, the Keychain item. Every
// profile reads and writes the same files.
type Store struct {
	// Dir is Claude Code's config directory (CLAUDE_CONFIG_DIR, else ~/.claude).
	Dir string
	// Keychain is the macOS Keychain item, nil where there is none.
	Keychain Keychain
	Now      func() time.Time
}

// DefaultStore locates Claude Code's store the way the CLI does: CLAUDE_CONFIG_DIR when set and not
// blank, else ~/.claude. The Keychain is used on macOS.
func DefaultStore(getenv func(string) string) Store {
	dir := strings.TrimSpace(getenv("CLAUDE_CONFIG_DIR"))
	if strings.HasPrefix(dir, "~/") || dir == "~" {
		if h, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(h, strings.TrimPrefix(dir, "~"))
		}
	}
	if dir == "" {
		if h, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(h, ".claude")
		}
	}
	return Store{Dir: dir, Keychain: SecurityKeychain(), Now: time.Now}
}

func (s Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// CredentialsPath is the shared OAuth file.
func (s Store) CredentialsPath() string { return filepath.Join(s.Dir, ".credentials.json") }

// loginFromPayload normalises a {"claudeAiOauth": {...}} payload.
func loginFromPayload(payload map[string]any, source string) *Login {
	oauth, _ := payload["claudeAiOauth"].(map[string]any)
	access, _ := oauth["accessToken"].(string)
	if access == "" {
		return nil
	}
	l := &Login{AccessToken: access, Source: source}
	l.RefreshToken, _ = oauth["refreshToken"].(string)
	l.ExpiresAtMS = int64Of(oauth["expiresAt"])
	return l
}

func int64Of(v any) int64 {
	switch n := v.(type) {
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i
		}
		f, _ := n.Float64()
		return int64(f)
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	}
	return 0
}

// readJSONFile reads a JSON object, tolerating a BOM. Missing, unreadable or corrupt files give nil.
func readJSONFile(path string) map[string]any {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber() // keep large integers (expiresAt in milliseconds) exact
	var m map[string]any
	if dec.Decode(&m) != nil {
		return nil
	}
	return m
}

func (s Store) readFile() *Login {
	if m := readJSONFile(s.CredentialsPath()); m != nil {
		return loginFromPayload(m, "claude_code_credentials_file")
	}
	return nil
}

func (s Store) readKeychain(ctx context.Context) *Login {
	if s.Keychain == nil {
		return nil
	}
	item, err := s.Keychain.Find(ctx)
	if err != nil || item == nil {
		return nil
	}
	return loginFromPayload(item.Payload, "macos_keychain")
}

// Read returns Claude Code's login, or nil when there is none. When both the Keychain and the file
// hold one it prefers the only unexpired one (Claude Code 2.1.x refreshes one source but not the
// other), else the later expiry so a refresh uses the freshest refresh token. ~/.claude.json's
// primaryApiKey is deliberately excluded.
func (s Store) Read(ctx context.Context) *Login {
	kc, file := s.readKeychain(ctx), s.readFile()
	if kc == nil || file == nil {
		if kc != nil {
			return kc
		}
		return file
	}
	now := s.now()
	if kv, fv := kc.Valid(now), file.Valid(now); kv != fv {
		if kv {
			return kc
		}
		return file
	}
	if kc.ExpiresAtMS >= file.ExpiresAtMS {
		return kc
	}
	return file
}

// writeFile commits a rotated pair to the credentials file: read, merge, write atomically as 0600.
// Scopes already stored are kept because Claude Code 2.1.81+ gates on "user:inference" being present.
// A corrupt existing file fails closed rather than being overwritten.
func (s Store) writeFile(access, refresh string, expiresAtMS int64) error {
	path := s.CredentialsPath()
	existing := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber()
		if err := dec.Decode(&existing); err != nil {
			return &CredentialPersistError{Path: path, Err: err}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return &CredentialPersistError{Path: path, Err: err}
	}
	oauth := map[string]any{"accessToken": access, "refreshToken": refresh, "expiresAt": expiresAtMS}
	if prev, ok := existing["claudeAiOauth"].(map[string]any); ok {
		if scopes, ok := prev["scopes"]; ok {
			oauth["scopes"] = scopes
		}
	}
	existing["claudeAiOauth"] = oauth
	if err := atomicWritePrivateJSON(path, existing); err != nil {
		return &CredentialPersistError{Path: path, Err: err}
	}
	return nil
}

// atomicWritePrivateJSON writes with a 0600-from-creation temp file, fsync and an atomic replace, so
// the token is never briefly umask-readable. The parent directory's mode is left alone (it belongs
// to Claude Code).
func atomicWritePrivateJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))+"_*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() { _ = os.Remove(name) }
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(name, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// ---- spent-rotation registry ------------------------------------------------------------------

// A refresh whose POST succeeded but whose replacement never reached its store leaves a spent
// single-use pair behind. The registry holds one-way fingerprints of those secrets, in two scopes: the
// process (a bounded list) and a durable sidecar next to the shared credentials file, so other
// processes fail closed too instead of replaying a spent refresh token.
const (
	spentMaxTracked = 64
	sidecarComment  = "Non-secret one-way fingerprints of Anthropic OAuth credentials whose rotation was consumed server-side but never durably committed. Written by Hermes so sibling processes sharing this credential source fail closed instead of replaying a spent single-use refresh token."
)

var (
	spentMu   sync.Mutex
	spentList []string // oldest first
	// deadRefresh holds fingerprints of refresh tokens the endpoint rejected terminally, so later
	// attempts skip the POST. A re-login rotates the token, and a new one is tried normally.
	deadRefresh = map[string]bool{}
)

// fingerprint is a non-reversible sha256:<16 hex> digest of a secret, "" for an empty one.
func fingerprint(secret string) string {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(secret))
	return "sha256:" + hex.EncodeToString(sum[:])[:16]
}

func sidecarPath(credentialsPath string) string {
	return credentialsPath + ".hermes-spent-rotations.json"
}

func readSidecar(credentialsPath string) map[string]bool {
	out := map[string]bool{}
	if credentialsPath == "" {
		return out
	}
	m := readJSONFile(sidecarPath(credentialsPath))
	list, _ := m["fingerprints"].([]any)
	for _, x := range list {
		if s, ok := x.(string); ok && s != "" {
			out[s] = true
		}
	}
	return out
}

// appendSidecar merges fingerprints into the sidecar. It is fail-soft: a sidecar write failure must
// never mask the process-local verdict. The caller holds the credential lock.
func appendSidecar(credentialsPath string, fps []string) {
	merged := readSidecar(credentialsPath)
	for _, f := range fps {
		merged[f] = true
	}
	all := make([]string, 0, len(merged))
	for f := range merged {
		all = append(all, f)
	}
	sort.Strings(all)
	if n := spentMaxTracked * 4; len(all) > n {
		all = all[len(all)-n:]
	}
	_ = atomicWritePrivateJSON(sidecarPath(credentialsPath), map[string]any{"version": 1, "comment": sidecarComment, "fingerprints": all})
}

// MarkRotationConsumedUncommitted records the pre-rotation pair of a refresh whose replacement never
// committed. With a credentialsPath the verdict is also persisted to that file's sidecar.
func MarkRotationConsumedUncommitted(credentialsPath string, secrets ...string) {
	var recorded []string
	for _, s := range secrets {
		if f := fingerprint(s); f != "" {
			recorded = append(recorded, f)
		}
	}
	spentMu.Lock()
	for _, f := range recorded {
		for i, x := range spentList {
			if x == f {
				spentList = append(spentList[:i], spentList[i+1:]...)
				break
			}
		}
		spentList = append(spentList, f)
		if len(spentList) > spentMaxTracked {
			spentList = spentList[len(spentList)-spentMaxTracked:]
		}
	}
	spentMu.Unlock()
	if len(recorded) > 0 && credentialsPath != "" {
		appendSidecar(credentialsPath, recorded)
	}
}

// IsRotationConsumedUncommitted is true when secret belongs to a rotation that was spent but never committed.
func IsRotationConsumedUncommitted(credentialsPath, secret string) bool {
	f := fingerprint(secret)
	if f == "" {
		return false
	}
	spentMu.Lock()
	for _, x := range spentList {
		if x == f {
			spentMu.Unlock()
			return true
		}
	}
	spentMu.Unlock()
	return readSidecar(credentialsPath)[f]
}

func resetRegistryForTest() {
	spentMu.Lock()
	spentList = nil
	spentMu.Unlock()
	deadRefresh = map[string]bool{}
}
