package anthropic

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type call struct {
	stdin string
	name  string
	args  []string
}

// runnerOf scripts a CommandRunner and records its calls.
func runnerOf(stdout, stderr string, err error, calls *[]call) CommandRunner {
	return func(_ context.Context, stdin string, name string, args ...string) (string, string, error) {
		*calls = append(*calls, call{stdin, name, args})
		return stdout, stderr, err
	}
}

const payloadJSON = `{"claudeAiOauth":{"accessToken":"AT","refreshToken":"RT","expiresAt":1700000000000},"other":true}`

func TestKeychainFindParsesBothAttributeEncodings(t *testing.T) {
	tests := []struct {
		name, stdout, stderr, wantAccount string
	}{
		{"plain ascii", "    \"acct\"<blob>=\"alice\"\n", "password: \"" + strings.ReplaceAll(payloadJSON, `"`, `"`) + "\"\n", "alice"},
		{"hex with echo", "    \"acct\"<blob>=0x626F62  \"bob\"\n", "password: 0x" + hex.EncodeToString([]byte(payloadJSON)) + "  \"ignored echo\"\n", "bob"},
		{"account with spaces", "  \"acct\"<blob>=\"first last\"\n", "password: \"" + payloadJSON + "\"\n", "first last"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var calls []call
			item, err := NewSecurityKeychain(runnerOf(tc.stdout, tc.stderr, nil, &calls)).Find(context.Background())
			if err != nil || item == nil {
				t.Fatalf("item = %v, err = %v", item, err)
			}
			if item.Account != tc.wantAccount {
				t.Errorf("account = %q, want %q", item.Account, tc.wantAccount)
			}
			if l := loginFromPayload(item.Payload, "macos_keychain"); l == nil || l.AccessToken != "AT" || l.RefreshToken != "RT" || l.ExpiresAtMS != 1700000000000 {
				t.Errorf("payload = %v", item.Payload)
			}
			if len(calls) != 1 || calls[0].name != "security" || strings.Join(calls[0].args, " ") != "find-generic-password -s Claude Code-credentials -g" || calls[0].stdin != "" {
				t.Errorf("calls = %+v", calls)
			}
		})
	}
}

func TestKeychainFindReturnsNothingForUnusableOutput(t *testing.T) {
	good := "  \"acct\"<blob>=\"alice\"\n"
	tests := []struct {
		name, stdout, stderr string
		err                  error
	}{
		{"security failed (no item)", good, "password: \"" + payloadJSON + "\"\n", errors.New("exit status 44")},
		{"no account", "nothing\n", "password: \"" + payloadJSON + "\"\n", nil},
		{"no password", good, "", nil},
		{"password not JSON", good, "password: \"not json\"\n", nil},
		{"password JSON but not an object", good, "password: \"[1,2]\"\n", nil},
		{"hex that is not hex", good, "password: 0xZZ \"x\"\n", nil},
	}
	for _, tc := range tests {
		var calls []call
		item, err := NewSecurityKeychain(runnerOf(tc.stdout, tc.stderr, tc.err, &calls)).Find(context.Background())
		if item != nil || err != nil {
			t.Errorf("%s: item = %+v, err = %v; want nothing", tc.name, item, err)
		}
	}
}

func TestMirrorCommandKeepsTheSecretOffArgvAndHexEncoded(t *testing.T) {
	payload := map[string]any{"claudeAiOauth": map[string]any{"accessToken": "sk-ant-oat01-SECRET", "name": "café \U0001F600"}}
	var calls []call
	kc := NewSecurityKeychain(runnerOf("", "", nil, &calls))
	if err := kc.Write(context.Background(), `acc"ount\x`, payload); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].name != "security" || strings.Join(calls[0].args, " ") != "-i" {
		t.Fatalf("calls = %+v: the command must go to `security -i`", calls)
	}
	for _, a := range calls[0].args {
		if strings.Contains(a, "SECRET") {
			t.Fatal("the secret is on argv")
		}
	}
	line := calls[0].stdin
	if strings.Contains(line, "SECRET") {
		t.Fatal("the secret must be hex encoded, not written in clear")
	}
	wantPrefix := `add-generic-password -U -a "acc\"ount\\x" -s "Claude Code-credentials" -X `
	if !strings.HasPrefix(line, wantPrefix) || !strings.HasSuffix(line, "\n") {
		t.Fatalf("line = %q", line)
	}
	raw, err := hex.DecodeString(strings.TrimSpace(strings.TrimPrefix(line, wantPrefix)))
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range raw {
		if b >= 0x80 {
			t.Fatalf("payload must be pure ASCII, found byte %#x", b)
		}
	}
	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	oauth := back["claudeAiOauth"].(map[string]any)
	if oauth["accessToken"] != "sk-ant-oat01-SECRET" || oauth["name"] != "café \U0001F600" {
		t.Fatalf("round trip = %v", oauth)
	}
}

func TestKeychainWriteReportsFailure(t *testing.T) {
	var calls []call
	err := NewSecurityKeychain(runnerOf("", "boom: denied\n", errors.New("exit status 1"), &calls)).Write(context.Background(), "a", map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("err = %v", err)
	}
}

func TestMergeKeychainPayloadKeepsMetadata(t *testing.T) {
	existing := map[string]any{
		"claudeAiOauth": map[string]any{"accessToken": "old", "refreshToken": "oldr", "expiresAt": 1, "subscriptionType": "max", "rateLimitTier": "t5", "scopes": []any{"user:inference"}},
		"other":         "kept",
	}
	got := mergeKeychainPayload(existing, "A", "R", 99)
	oauth := got["claudeAiOauth"].(map[string]any)
	if oauth["accessToken"] != "A" || oauth["refreshToken"] != "R" || oauth["expiresAt"] != int64(99) {
		t.Errorf("triple = %v", oauth)
	}
	if oauth["subscriptionType"] != "max" || oauth["rateLimitTier"] != "t5" || oauth["scopes"] == nil || got["other"] != "kept" {
		t.Errorf("metadata lost: %v", got)
	}
	if existing["claudeAiOauth"].(map[string]any)["accessToken"] != "old" {
		t.Error("the input must not be mutated")
	}
}

func TestMirrorToKeychainOnlyUpdatesTheItemHoldingTheSpentPair(t *testing.T) {
	ctx := context.Background()
	t.Run("matching item is updated under its own account", func(t *testing.T) {
		kc := &fakeKeychain{item: &KeychainItem{Account: "alice", Payload: oauthPayload("old", "SPENT", 1, map[string]any{"subscriptionType": "max"})}}
		if err := mirrorToKeychain(ctx, kc, "NEW", "NEWR", 5, "SPENT"); err != nil {
			t.Fatal(err)
		}
		if len(kc.writes) != 1 || kc.item.Account != "alice" {
			t.Fatalf("writes = %d, account = %q", len(kc.writes), kc.item.Account)
		}
		oauth := kc.writes[0]["claudeAiOauth"].(map[string]any)
		if oauth["refreshToken"] != "NEWR" || oauth["subscriptionType"] != "max" {
			t.Errorf("oauth = %v", oauth)
		}
	})
	t.Run("a different pair is a different login or an already-made rotation", func(t *testing.T) {
		kc := &fakeKeychain{item: &KeychainItem{Account: "alice", Payload: oauthPayload("x", "SOMEONE-ELSE", 1, nil)}}
		if err := mirrorToKeychain(ctx, kc, "NEW", "NEWR", 5, "SPENT"); err != nil || len(kc.writes) != 0 {
			t.Fatalf("writes = %d, err = %v: overwriting would clobber the other login", len(kc.writes), err)
		}
	})
	t.Run("no item is never created", func(t *testing.T) {
		kc := &fakeKeychain{}
		if err := mirrorToKeychain(ctx, kc, "A", "R", 1, "SPENT"); err != nil || len(kc.writes) != 0 {
			t.Fatalf("writes = %d, err = %v", len(kc.writes), err)
		}
	})
	t.Run("no keychain at all", func(t *testing.T) {
		if err := mirrorToKeychain(ctx, nil, "A", "R", 1, "SPENT"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a write failure is reported to the caller", func(t *testing.T) {
		kc := &fakeKeychain{item: &KeychainItem{Account: "a", Payload: oauthPayload("o", "SPENT", 1, nil)}, writeErr: fmt.Errorf("locked")}
		if err := mirrorToKeychain(ctx, kc, "A", "R", 1, "SPENT"); err == nil {
			t.Fatal("want the error")
		}
	})
}

func TestAsciiJSON(t *testing.T) {
	in := []byte(`{"a":"caf` + "é" + `","b":"` + "\U0001F600" + `","c":"plain"}`)
	out := asciiJSON(in)
	for _, b := range out {
		if b >= 0x80 {
			t.Fatalf("non-ASCII byte in %s", out)
		}
	}
	var got, want map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(in, &want)
	if got["a"] != want["a"] || got["b"] != want["b"] || got["c"] != "plain" {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func FuzzKeychainFindNeverPanics(f *testing.F) {
	f.Add("    \"acct\"<blob>=\"alice\"\n", "password: \""+payloadJSON+"\"\n")
	f.Add("\"acct\"<blob>=0x41 \"A\"", "password: 0x7B7D \"{}\"")
	f.Add("\"acct\"<blob>=", "password: ")
	f.Add("", "")
	f.Fuzz(func(t *testing.T, stdout, stderr string) {
		var calls []call
		item, err := NewSecurityKeychain(runnerOf(stdout, stderr, nil, &calls)).Find(context.Background())
		if err != nil {
			t.Fatalf("Find must never error on odd output: %v", err)
		}
		if item != nil && (item.Account == "" || item.Payload == nil) {
			t.Fatalf("an item must have an account and a payload: %+v", item)
		}
	})
}
