package webtools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type goldens struct {
	Truncate []struct {
		Name, Content, Stored, Text string
		Limit                       int
		Truncated                   bool
	} `json:"truncate"`
	Base64 []struct{ Name, In, Out string } `json:"base64"`
	Clamp  []struct {
		In  any `json:"in"`
		Out int `json:"out"`
	} `json:"clamp"`
}

func load(t *testing.T) goldens {
	raw, err := os.ReadFile("testdata/truncate.json")
	if err != nil {
		t.Fatal(err)
	}
	var g goldens
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestTruncateMatchesPython(t *testing.T) {
	for _, c := range load(t).Truncate {
		got, was := TruncateWithFooter(c.Content, c.Limit, func() string { return c.Stored })
		if got != c.Text || was != c.Truncated {
			t.Errorf("%s: truncated=%v want %v\n got: %q\nwant: %q", c.Name, was, c.Truncated, head(got), head(c.Text))
		}
	}
}

func head(s string) string {
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

func TestBase64MatchesPython(t *testing.T) {
	for _, c := range load(t).Base64 {
		if got := ConvertBase64Images(c.In); got != c.Out {
			t.Errorf("%s: got %q want %q", c.Name, got, c.Out)
		}
	}
}

func TestClampMatchesPython(t *testing.T) {
	for _, c := range load(t).Clamp {
		var n int
		set := false
		switch v := c.In.(type) {
		case float64:
			n, set = int(v), true
		default:
			continue // null and numeric strings are resolved by argument coercion before the tool runs
		}
		if got := clampLimit(n, set); got != c.Out {
			t.Errorf("clamp(%v) = %d, want %d", c.In, got, c.Out)
		}
	}
	if clampLimit(0, false) != DefaultCharLimit {
		t.Error("unset limit must use the default")
	}
}

func TestStoreFullTextWritesCapAndReplacesSymlink(t *testing.T) {
	home := t.TempDir()
	p := storeFullText(home, "https://example.com/page", "full body")
	if !strings.HasPrefix(p, filepath.Join(home, "cache", "web", "example.com-")) || !strings.HasSuffix(p, ".md") {
		t.Fatalf("path %q", p)
	}
	if b, _ := os.ReadFile(p); string(b) != "full body" {
		t.Errorf("stored %q", b)
	}
	// A symlink planted at the deterministic path must not redirect the write.
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, p); err != nil {
		t.Fatal(err)
	}
	if got := storeFullText(home, "https://example.com/page", "second"); got != p {
		t.Fatalf("path changed: %q", got)
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Error("write followed the symlink")
	}
	if b, _ := os.ReadFile(p); string(b) != "second" {
		t.Errorf("re-extraction did not overwrite: %q", b)
	}
	big := strings.Repeat("z", maxStoredChars+10)
	p2 := storeFullText(home, "https://big.example/", big)
	b, _ := os.ReadFile(p2)
	if !strings.Contains(string(b), "stored copy truncated at 2,000,000 chars of 2,000,010") {
		t.Error("oversize stored copy not capped with a notice")
	}
	if storeFullText("", "https://x/", "y") != "" {
		t.Error("no home means no store")
	}
}

func TestHostSlug(t *testing.T) {
	cases := map[string]string{
		"https://Example.com/a": "example.com",
		"https://[::1]:8080/x":  "__1",
		"file:///etc/passwd":    "page",
		"https://a b.example/":  "a-b.example",
		"https://ex_ample.com/": "ex_ample.com",
		"":                      "page",
		"https://u:p@h.io:99/x": "h.io",
	}
	for in, want := range cases {
		if got := hostSlug(in); got != want {
			t.Errorf("hostSlug(%q) = %q, want %q", in, got, want)
		}
	}
}
