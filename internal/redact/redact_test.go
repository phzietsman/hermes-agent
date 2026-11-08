package redact

import (
	"strings"
	"testing"
)

// The cases were checked against agent/redact.py: Python leaves every benign text alone and removes every
// secret value. The Go package must do at least that.
func TestSecretsAreRemoved(t *testing.T) {
	for _, c := range []struct{ in, secret string }{
		{"OPENAI_API_KEY=sk-abcdefghijklmnopqrstuv", "sk-abcdefghijklmnopqrstuv"},
		{"token ghp_abcdefghij1234567890", "ghp_abcdefghij1234567890"},
		{"Authorization: Bearer abc123def456ghi789", "abc123def456ghi789"},
		{"curl https://user:hunter2pass@host.example/x", "hunter2pass"},
		{`{"api_key": "zzzz1111aaaa2222"}`, "zzzz1111aaaa2222"},
		{"AKIAABCDEFGHIJKLMNOP", "AKIAABCDEFGHIJKLMNOP"},
		{"postgres://admin:s3cr3tpw@db:5432/app", "s3cr3tpw"},
		{"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghijk", "eyJhbGciOiJIUzI1NiJ9"},
		{"-----BEGIN RSA PRIVATE KEY-----\nMIIEabc\n-----END RSA PRIVATE KEY-----", "MIIEabc"},
		{"https://x.example/cb?access_token=abcd1234efgh5678", "abcd1234efgh5678"},
		{"MY_SECRET=supersecretvalue99", "supersecretvalue99"},
		{"xoxb-1234567890-abcdefghij", "xoxb-1234567890-abcdefghij"},
		{"123456789:AAFabcdefghijklmnopqrstuvwxyz0123456", "AAFabcdefghijklmnopqrstuvwxyz0123456"},
		{"x-api-key: sk-ant-api03-abcdefghijklmnop", "abcdefghijklmnop"},
		{"here (sk-abcdefghijklmnopqrstuv) in prose", "sk-abcdefghijklmnopqrstuv"},
	} {
		if out := Text(c.in); strings.Contains(out, c.secret) || !strings.Contains(out, Mask) {
			t.Errorf("%q -> %q", c.in, out)
		}
	}
}

func TestBenignTextIsUntouched(t *testing.T) {
	for _, s := range []string{
		"The quick brown fox", "see https://example.com/docs?page=2 for details", "export PATH=/usr/bin:$PATH", "KEYBOARD=us",
		`func main() { fmt.Println("hi") }`, "commit 0123456789abcdef0123456789abcdef01234567 fixed it",
		"password rules: at least 12 chars", "git log --oneline -5", "token count: 4096", "ls -la /tmp", "",
		"task-runner-config and risk-assessment are fine", "PASSAGE=chapter1",
	} {
		if out := Text(s); out != s {
			t.Errorf("%q changed to %q", s, out)
		}
	}
}

func TestRedactionIsIdempotent(t *testing.T) {
	in := "OPENAI_API_KEY=sk-abcdefghijklmnopqrstuv and postgres://a:b@h/d and ghp_abcdefghij1234567890"
	once := Text(in)
	if twice := Text(once); twice != once {
		t.Fatalf("not idempotent: %q -> %q", once, twice)
	}
}

func FuzzText(f *testing.F) {
	for _, s := range []string{"", "KEY=abc", "sk-aaaaaaaaaaaa", "Authorization: x", "-----BEGIN PRIVATE KEY-----"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := Text(s)
		if again := Text(out); again != out {
			t.Fatalf("not idempotent: %q -> %q -> %q", s, out, again)
		}
	})
}
