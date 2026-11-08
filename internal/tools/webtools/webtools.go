// Package webtools offers web_search and web_extract. Search goes to Tavily (keyless when no key is set);
// extract fetches pages directly through a guard that refuses internal networks, converts HTML to text and
// returns a head+tail window of long pages with the full text stored under <home>/cache/web.
package webtools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/phzietsman/hermes-agent/internal/netguard"
	"github.com/phzietsman/hermes-agent/internal/redact"
	"github.com/phzietsman/hermes-agent/internal/tools"
)

const (
	searchDescription  = "Search the web for information. Returns up to 5 results by default with titles, URLs, and descriptions. The query is passed through to the configured backend, so operators such as site:domain, filetype:pdf, intitle:word, -term, and \"exact phrase\" may work when the backend supports them."
	extractDescription = "Extract content from web page URLs. Returns clean page content in markdown/text (no LLM summarization — fast). Pages within the char budget (default 15000) return whole; larger pages return a head+tail window with a footer telling you the full text's saved file path and the read_file call to page through the omitted middle. Inline images appear as [IMAGE: alt] placeholders; real image URLs are kept as links. If a URL fails or times out, use the browser tool instead."

	maxURLs        = 5
	searchCap      = 20 // Tavily caps max_results server-side
	maxBodyBytes   = 8 << 20
	fetchTimeout   = 30 * time.Second
	searchTimeout  = 60 * time.Second
	invalidItemFmt = "Item %d is not a usable URL string."
	secretURLError = "Blocked: URL contains what appears to be an API key or token. Secrets must not be sent in URLs."
)

// Options wires the tools. The zero value is the production setup; tests replace the fields.
type Options struct {
	// SearchURL is the Tavily endpoint; APIKey is optional (keyless mode without it).
	SearchURL string
	APIKey    func() string
	// Fetch is the client for extract; nil means a netguard client.
	Fetch *http.Client
	// Check vets a URL before fetching; nil means netguard.CheckURL.
	Check func(string) error
	// SearchClient is the client for search; nil means a plain client with a timeout.
	SearchClient *http.Client
}

// Register adds web_search and web_extract to r.
func Register(r *tools.Registry, o Options) {
	if o.SearchURL == "" {
		o.SearchURL = "https://api.tavily.com/search"
	}
	if o.APIKey == nil {
		o.APIKey = func() string { return os.Getenv("TAVILY_API_KEY") }
	}
	if o.Fetch == nil {
		o.Fetch = netguard.Client(fetchTimeout)
	}
	if o.Check == nil {
		o.Check = netguard.CheckURL
	}
	if o.SearchClient == nil {
		o.SearchClient = &http.Client{Timeout: searchTimeout}
	}
	r.MustRegister(
		tools.Tool{
			Name: "web_search", Toolset: "web", Description: searchDescription, Parallel: true,
			Parameters: map[string]any{"type": "object", "required": []any{"query"}, "properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "The search query to look up on the web. You may include backend-supported operators such as site:example.com, filetype:pdf, intitle:word, -term, or \"exact phrase\"."},
				"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 5, "description": "Maximum number of results to return. Defaults to 5."},
			}},
			Handler: func(ctx context.Context, a tools.Args) (string, error) { return o.search(ctx, a), nil },
		},
		tools.Tool{
			Name: "web_extract", Toolset: "web", Description: extractDescription, Parallel: true,
			Parameters: map[string]any{"type": "object", "required": []any{"urls"}, "properties": map[string]any{
				"urls":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": maxURLs, "description": "List of URLs to extract content from (max 5 URLs per call)"},
				"char_limit": map[string]any{"type": "integer", "minimum": charLimitFloor, "description": "Optional per-page character budget sent back (default 15000). Pages larger than this are head+tail truncated with the full text stored to disk. Raise it when you need more of a long page inline."},
			}},
			Handler: func(ctx context.Context, a tools.Args) (string, error) { return o.extract(ctx, a), nil },
		},
	)
}

func fail(msg string) string { return tools.Error(msg, tools.Field{Key: "success", Value: false}) }

// ---- search ----

type hit struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
	Position    int    `json:"position"`
}

func (o Options) search(ctx context.Context, a tools.Args) string {
	query := strings.TrimSpace(a.String("query", ""))
	if query == "" {
		return fail("query is required.")
	}
	limit := max(1, min(a.Int("limit", 5), 100))
	body, _ := json.Marshal(map[string]any{"query": query, "max_results": min(limit, searchCap), "include_raw_content": false, "include_images": false})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.SearchURL, bytes.NewReader(body))
	if err != nil {
		return fail("Error searching web: " + err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client-Name", "hermes-agent")
	if key := o.APIKey(); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	} else {
		req.Header.Set("X-Tavily-Access-Mode", "keyless")
	}
	resp, err := o.SearchClient.Do(req)
	if err != nil {
		return fail("Error searching web: " + redact.Text(err.Error()))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if resp.StatusCode >= 400 {
		return fail(fmt.Sprintf("Search failed (HTTP %d): %s", resp.StatusCode, redact.Text(strings.TrimSpace(string(raw)))))
	}
	var parsed struct {
		Results []struct{ Title, URL, Content string } `json:"results"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return fail("Search backend returned an unreadable response.")
	}
	hits := make([]hit, 0, len(parsed.Results))
	for i, r := range parsed.Results {
		if i >= limit {
			break
		}
		hits = append(hits, hit{r.Title, r.URL, r.Content, i + 1})
	}
	return tools.Result(tools.Field{Key: "success", Value: true}, tools.Field{Key: "data", Value: map[string]any{"web": hits}})
}

// ---- extract ----

type page struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	Content string `json:"content"`
	Error   any    `json:"error"`
}

func (o Options) extract(ctx context.Context, a tools.Args) string {
	raw, _ := a["urls"].([]any)
	if len(raw) == 0 {
		return fail("urls is required: a list of up to 5 URLs.")
	}
	if len(raw) > maxURLs {
		raw = raw[:maxURLs]
	}
	_, set := a["char_limit"]
	limit := clampLimit(a.Int("char_limit", 0), set)
	home := tools.Env(ctx).Home

	urls := make([]string, len(raw))
	for i, item := range raw {
		s, ok := item.(string)
		if s = strings.TrimSpace(s); !ok || s == "" {
			continue
		}
		if leaksSecret(s) {
			return fail(secretURLError)
		}
		urls[i] = s
	}
	results := make([]page, len(urls))
	var wg sync.WaitGroup
	for i, u := range urls {
		if u == "" {
			results[i] = page{Error: fmt.Sprintf(invalidItemFmt, i)}
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = o.fetchOne(ctx, home, u, limit)
		}()
	}
	wg.Wait()
	return tools.Result(tools.Field{Key: "results", Value: results})
}

// leaksSecret reports a token-shaped string in the URL in any common encoding.
func leaksSecret(u string) bool {
	cands := []string{u}
	if un, err := url.QueryUnescape(u); err == nil {
		cands = append(cands, un)
	}
	if un, err := url.PathUnescape(u); err == nil {
		cands = append(cands, un)
	}
	for _, c := range cands {
		if redact.Tokens(c) != c {
			return true
		}
	}
	return false
}

func (o Options) fetchOne(ctx context.Context, home, u string, limit int) page {
	if err := o.Check(u); err != nil {
		return page{URL: u, Error: err.Error()}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return page{URL: u, Error: err.Error()}
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; hermes-agent)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain,application/json;q=0.9,*/*;q=0.5")
	resp, err := o.Fetch.Do(req)
	if err != nil {
		return page{URL: u, Error: "Fetch failed: " + redact.Text(err.Error())}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return page{URL: u, Error: fmt.Sprintf("HTTP %d fetching the page.", resp.StatusCode)}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return page{URL: u, Error: "Fetch failed: " + redact.Text(err.Error())}
	}
	if kind := binaryKind(body); kind != "" {
		return page{URL: u, Error: fmt.Sprintf("URL returned binary content (%s), not a page. Download it with the terminal (curl -L -o) and use read_file (SQLite/Office/PDF auto-extract) or terminal utilities on the file.", kind)}
	}
	text := string(bytes.ToValidUTF8(body, []byte("�")))
	title := ""
	if ct := strings.ToLower(resp.Header.Get("Content-Type")); strings.Contains(ct, "html") || (ct == "" && looksHTML(text)) {
		title, text = HTMLText(text)
	}
	if strings.TrimSpace(text) == "" {
		return page{URL: u, Title: title, Error: "The page returned no readable text."}
	}
	text = ConvertBase64Images(text)
	out, _ := TruncateWithFooter(text, limit, func() string { return storeFullText(home, u, text) })
	return page{URL: u, Title: title, Content: out}
}

func looksHTML(s string) bool {
	head := strings.ToLower(strings.TrimSpace(s[:min(len(s), 512)]))
	return strings.HasPrefix(head, "<!doctype html") || strings.HasPrefix(head, "<html")
}

// binaryKind names a raw binary payload by magic bytes (the unambiguous kinds only), else "".
func binaryKind(b []byte) string {
	for _, m := range []struct {
		sig  string
		name string
	}{
		{"SQLite format 3", "SQLite"}, {"PK\x03\x04", "ZIP"}, {"\x1f\x8b", "gzip"}, {"BZh", "bzip2"}, {"\xfd7zXZ", "xz"},
		{"7z\xbc\xaf\x27\x1c", "7-Zip"}, {"\x7fELF", "ELF"}, {"\xcf\xfa\xed\xfe", "Mach-O"}, {"\xca\xfe\xba\xbe", "Mach-O"},
		{"\x89PNG", "PNG"}, {"\xff\xd8\xff", "JPEG"}, {"GIF8", "GIF"}, {"II*\x00", "TIFF"}, {"MM\x00*", "TIFF"},
		{"fLaC", "FLAC"}, {"OggS", "Ogg"}, {"%PDF-", "PDF"},
	} {
		if bytes.HasPrefix(b, []byte(m.sig)) {
			return m.name
		}
	}
	return ""
}
