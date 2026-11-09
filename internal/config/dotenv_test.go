package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseEnv(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want map[string]string
	}{
		{"plain", "A=1\nB=two", map[string]string{"A": "1", "B": "two"}},
		{"comments and blanks", "# c\n\n  \nA=1\n# B=2", map[string]string{"A": "1"}},
		{"export prefix", "export A=1\nexport   B=2", map[string]string{"A": "1", "B": "2"}},
		{"empty assignment is a value", "A=\nB=''", map[string]string{"A": "", "B": ""}},
		{"no equals", "JUSTAKEY\nA=1", map[string]string{"A": "1"}},
		{"empty key", "=x\nA=1", map[string]string{"A": "1"}},
		{"spaces around key", "  A  =1", map[string]string{"A": "1"}},
		{"double quotes unescape", `A="he said \"hi\" \\ done"`, map[string]string{"A": `he said "hi" \ done`}},
		{"unknown escape kept", `A="a\nb"`, map[string]string{"A": `a\nb`}},
		{"single quotes literal", `A='it''s'`, map[string]string{"A": `it''s`}},
		{"value keeps inner equals", "A=x=y=z", map[string]string{"A": "x=y=z"}},
		{"inline comment dropped", "A=value # note", map[string]string{"A": "value"}},
		{"hash without whitespace kept", "A=foo#bar", map[string]string{"A": "foo#bar"}},
		{"leading hash value kept", "A=#frag", map[string]string{"A": "#frag"}},
		{"comment after quoted", `A="v" # note`, map[string]string{"A": "v"}},
		{"hash inside quotes kept", `A="v # not a comment"`, map[string]string{"A": "v # not a comment"}},
		{"escaped quote then comment", `A="a \" b" # note`, map[string]string{"A": `a " b`}},
		{"unterminated quote kept", `A="oops`, map[string]string{"A": `"oops`}},
		{"crlf and cr lines", "A=1\r\nB=2\rC=3", map[string]string{"A": "1", "B": "2", "C": "3"}},
		{"last wins", "A=1\nA=2", map[string]string{"A": "2"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseEnv([]byte(tc.in))
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if g, ok := got[k]; !ok || g != v {
					t.Fatalf("key %q = %q (present %v), want %q; all: %v", k, g, ok, v, got)
				}
			}
		})
	}
}

func TestParseEnvBOMDoesNotMangleFirstKey(t *testing.T) {
	got := ParseEnv(append([]byte{0xEF, 0xBB, 0xBF}, "FIRST=1\nSECOND=2\n"...))
	if got["FIRST"] != "1" || got["SECOND"] != "2" {
		t.Fatalf("got %v", got)
	}
}

func TestParseEnvLatin1Fallback(t *testing.T) {
	// "caf\xe9" is invalid UTF-8; it must decode as latin-1, not drop the file.
	got := ParseEnv([]byte("NAME=caf\xe9\nOK=1\n"))
	if got["NAME"] != "café" || got["OK"] != "1" {
		t.Fatalf("got %q", got)
	}
	// BOM plus invalid UTF-8 keeps the first key intact.
	got = ParseEnv(append([]byte{0xEF, 0xBB, 0xBF}, "FIRST=caf\xe9\n"...))
	if got["FIRST"] != "café" {
		t.Fatalf("got %q", got)
	}
}

func TestParseEnvUTF8Preserved(t *testing.T) {
	if got := ParseEnv([]byte("NAME=héllo wörld ✓\n")); got["NAME"] != "héllo wörld ✓" {
		t.Fatalf("got %q", got)
	}
}

func FuzzParseEnv(f *testing.F) {
	for _, s := range []string{"A=1", "export A='x' # c", "A=\"\\\"", "\xef\xbb\xbfA=1", "=", "A", "\r\r\n#", `A="`, "A=\x00"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		got := ParseEnv(in)
		for k := range got {
			if k == "" {
				t.Fatal("empty key emitted")
			}
		}
		// Parsing must be deterministic.
		again := ParseEnv(in)
		if len(again) != len(got) {
			t.Fatal("non-deterministic parse")
		}
	})
}

func TestLoadEnvFileMissingIsEmpty(t *testing.T) {
	if got := LoadEnvFile(filepath.Join(t.TempDir(), "nope")); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func writeEnv(t *testing.T, home, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, ".env"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSecretsPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SHELL_ONLY", "from-shell")
	t.Setenv("BOTH", "stale-shell")
	t.Setenv("EMPTIED", "shell-value")
	writeEnv(t, home, "BOTH=from-dotenv\nEMPTIED=\nFILE_ONLY=f\n")
	s := LoadSecrets(home)

	tests := []struct {
		name, want string
		ok         bool
	}{
		{"BOTH", "from-dotenv", true},      // .env overrides stale shell exports
		{"EMPTIED", "", true},              // an empty assignment is a value, not "unset"
		{"FILE_ONLY", "f", true},           // only in .env
		{"SHELL_ONLY", "from-shell", true}, // falls back to the process env
		{"NOWHERE_AT_ALL", "", false},
	}
	for _, tc := range tests {
		got, ok := s.Lookup(tc.name)
		if got != tc.want || ok != tc.ok {
			t.Errorf("Lookup(%s) = %q, %v; want %q, %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
	if got := s.Get("NOWHERE_AT_ALL", "dflt"); got != "dflt" {
		t.Errorf("Get default = %q", got)
	}
	if got := s.Get("EMPTIED", "dflt"); got != "" {
		t.Errorf("empty .env value must beat the default, got %q", got)
	}
}

func TestSecretsNilAndMissingFile(t *testing.T) {
	t.Setenv("ONLY_ENV", "e")
	var nilSecrets *Secrets
	if v, ok := nilSecrets.Lookup("ONLY_ENV"); !ok || v != "e" {
		t.Fatalf("nil Secrets must read the process env, got %q %v", v, ok)
	}
	if v, ok := LoadSecrets(t.TempDir()).Lookup("ONLY_ENV"); !ok || v != "e" {
		t.Fatalf("missing .env must read the process env, got %q %v", v, ok)
	}
}
