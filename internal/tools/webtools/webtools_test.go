package webtools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/phzietsman/hermes-agent/internal/llm"
	"github.com/phzietsman/hermes-agent/internal/tools"
)

func run(t *testing.T, o Options, home, name string, args map[string]any) map[string]any {
	t.Helper()
	reg := tools.NewRegistry()
	Register(reg, o)
	b, _ := json.Marshal(args)
	ctx := tools.WithEnv(context.Background(), tools.CallEnv{Home: home})
	out, _ := reg.Build(tools.Selection{}).Run(ctx, llm.ToolCall{Function: llm.ToolCallFunction{Name: name, Arguments: string(b)}})
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("not JSON: %q", out)
	}
	return m
}

func pages(m map[string]any) []map[string]any {
	var out []map[string]any
	for _, x := range m["results"].([]any) {
		out = append(out, x.(map[string]any))
	}
	return out
}

func site(t *testing.T) string {
	mux := http.NewServeMux()
	mux.HandleFunc("/html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, `<html><head><title>Hello &amp; Co</title><style>p{}</style></head><body><script>evil()</script><h1>Head</h1><p>Para &lt;one&gt;</p><ul><li>a</li><li>b</li></ul><!-- c --></body></html>`)
	})
	mux.HandleFunc("/text", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "plain words")
	})
	mux.HandleFunc("/long", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, strings.Repeat("some line of text\n", 3000))
	})
	mux.HandleFunc("/png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		io.WriteString(w, "\x89PNG\r\n\x1a\n....")
	})
	mux.HandleFunc("/404", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mux.HandleFunc("/empty", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<html><script>x</script></html>")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func open() Options {
	return Options{Fetch: &http.Client{}, Check: func(string) error { return nil }}
}

func TestExtractHTMLToText(t *testing.T) {
	base := site(t)
	r := pages(run(t, open(), t.TempDir(), "web_extract", map[string]any{"urls": []string{base + "/html"}}))[0]
	if r["title"] != "Hello & Co" || r["error"] != nil {
		t.Fatalf("%v", r)
	}
	c := r["content"].(string)
	for _, bad := range []string{"evil()", "<h1>", "p{}", "<!--"} {
		if strings.Contains(c, bad) {
			t.Errorf("%q survived: %q", bad, c)
		}
	}
	if want := "Head\n\nPara <one>\n\n- a\n- b"; c != want {
		t.Errorf("content %q, want %q", c, want)
	}
}

func TestExtractPerURLFailuresDoNotSpoilTheRest(t *testing.T) {
	base := site(t)
	m := run(t, open(), t.TempDir(), "web_extract", map[string]any{"urls": []any{base + "/text", base + "/404", base + "/png", base + "/empty", ""}})
	r := pages(m)
	if len(r) != 5 {
		t.Fatalf("results %d", len(r))
	}
	if r[0]["content"] != "plain words" {
		t.Errorf("%v", r[0])
	}
	for i, want := range map[int]string{1: "HTTP 404", 2: "binary content (PNG)", 3: "no readable text", 4: "not a usable URL"} {
		if e, _ := r[i]["error"].(string); !strings.Contains(e, want) {
			t.Errorf("result %d error %q, want %q", i, e, want)
		}
	}
}

func TestExtractTruncatesAndStoresFullText(t *testing.T) {
	base, home := site(t), t.TempDir()
	r := pages(run(t, open(), home, "web_extract", map[string]any{"urls": []string{base + "/long"}, "char_limit": 3000}))[0]
	c := r["content"].(string)
	if !strings.Contains(c, "[TRUNCATED]") || !strings.Contains(c, "read_file path=") {
		t.Fatalf("no footer: %q", c[len(c)-300:])
	}
	path := c[strings.Index(c, "Full text saved to: ")+len("Full text saved to: "):]
	path = path[:strings.Index(path, "\n")]
	if !strings.HasPrefix(path, home) {
		t.Errorf("stored outside home: %s", path)
	}
	if b, err := os.ReadFile(path); err != nil || len(b) != len(strings.Repeat("some line of text\n", 3000)) {
		t.Errorf("stored text wrong: err=%v len=%d", err, len(b))
	}
}

func TestExtractRefusesSecretsBeforeAnyFetch(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer srv.Close()
	for _, u := range []string{srv.URL + "/?k=sk-ant-api03-abcdefghijklmnopqrstuvwx", srv.URL + "/?k=sk%2Dant%2Dapi03%2Dabcdefghijklmnopqrstuvwx"} {
		m := run(t, open(), t.TempDir(), "web_extract", map[string]any{"urls": []string{srv.URL + "/ok", u}})
		if m["success"] != false || !strings.Contains(m["error"].(string), "API key or token") {
			t.Errorf("%s: %v", u, m)
		}
	}
	if hit {
		t.Error("a URL was fetched in a call that carried a secret")
	}
}

func TestExtractDefaultsRefuseInternalHosts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "internal") }))
	defer srv.Close()
	r := pages(run(t, Options{}, t.TempDir(), "web_extract", map[string]any{"urls": []string{srv.URL, "http://169.254.169.254/latest/meta-data/"}}))
	for i, x := range r {
		if e, _ := x["error"].(string); !strings.Contains(e, "blocked request") || x["content"] != "" {
			t.Errorf("%d: %v", i, x)
		}
	}
}

func TestExtractCapsAtFiveURLsAndNeedsAny(t *testing.T) {
	base := site(t)
	urls := []string{}
	for range 8 {
		urls = append(urls, base+"/text")
	}
	if got := len(pages(run(t, open(), t.TempDir(), "web_extract", map[string]any{"urls": urls}))); got != 5 {
		t.Errorf("fetched %d, want 5", got)
	}
	if m := run(t, open(), t.TempDir(), "web_extract", map[string]any{"urls": []string{}}); m["success"] != false {
		t.Errorf("%v", m)
	}
}

func TestSearchShapesAndAuth(t *testing.T) {
	var gotAuth, gotMode string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotMode = r.Header.Get("Authorization"), r.Header.Get("X-Tavily-Access-Mode")
		json.NewDecoder(r.Body).Decode(&gotBody)
		io.WriteString(w, `{"results":[{"title":"A","url":"https://a","content":"ca"},{"title":"B","url":"https://b","content":"cb"},{"title":"C","url":"https://c","content":"cc"}]}`)
	}))
	defer srv.Close()
	o := Options{SearchURL: srv.URL, APIKey: func() string { return "k1" }}
	m := run(t, o, "", "web_search", map[string]any{"query": " go lang ", "limit": 2})
	web := m["data"].(map[string]any)["web"].([]any)
	if len(web) != 2 || web[1].(map[string]any)["position"] != float64(2) || web[0].(map[string]any)["description"] != "ca" {
		t.Errorf("%v", m)
	}
	if gotAuth != "Bearer k1" || gotMode != "" || gotBody["query"] != "go lang" || gotBody["max_results"] != float64(2) {
		t.Errorf("auth=%q mode=%q body=%v", gotAuth, gotMode, gotBody)
	}
	o.APIKey = func() string { return "" }
	run(t, o, "", "web_search", map[string]any{"query": "x", "limit": 100})
	if gotAuth != "" || gotMode != "keyless" || gotBody["max_results"] != float64(searchCap) {
		t.Errorf("keyless: auth=%q mode=%q body=%v", gotAuth, gotMode, gotBody)
	}
}

func TestSearchErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Tavily-Access-Mode") == "keyless" {
			http.Error(w, "rate limited: key sk-ant-api03-abcdefghijklmnopqrstuvwx", http.StatusTooManyRequests)
			return
		}
		io.WriteString(w, "not json")
	}))
	defer srv.Close()
	m := run(t, Options{SearchURL: srv.URL, APIKey: func() string { return "" }}, "", "web_search", map[string]any{"query": "q"})
	if e := m["error"].(string); !strings.Contains(e, "HTTP 429") || strings.Contains(e, "abcdefghijklmnopqrstuvwx") {
		t.Errorf("%q", e)
	}
	m = run(t, Options{SearchURL: srv.URL, APIKey: func() string { return "k" }}, "", "web_search", map[string]any{"query": "q"})
	if !strings.Contains(m["error"].(string), "unreadable") {
		t.Errorf("%v", m)
	}
	if m := run(t, open(), "", "web_search", map[string]any{"query": "  "}); m["success"] != false {
		t.Errorf("%v", m)
	}
}

func TestHTMLText(t *testing.T) {
	title, text := HTMLText("<TITLE>  A\n B </TITLE><div>x&nbsp;y</div><p>z</p><br>w<script>s</script>")
	if title != "A B" || text != "x y\nz\n\nw" {
		t.Errorf("title=%q text=%q", title, text)
	}
}

func FuzzHTMLText(f *testing.F) {
	for _, s := range []string{"<a", "<script>", "<!--", "<title>", "&#x;", "<li><li>", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) { _, _ = HTMLText(s) })
}

func FuzzTruncate(f *testing.F) {
	f.Add("abc\ndef", 2000)
	f.Fuzz(func(t *testing.T, s string, n int) {
		limit := clampLimit(n, true)
		out, was := TruncateWithFooter(s, limit, func() string { return "" })
		if !was && out != s {
			t.Fatal("untruncated text changed")
		}
	})
}
