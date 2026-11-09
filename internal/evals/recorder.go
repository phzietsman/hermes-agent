package evals

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
)

// Recorder is an http.RoundTripper that sits between the agent and the memory service. It records every write the
// service accepted (so "landed" means the service said yes, with the origin label that says who wrote it) and
// serves only the core-profile entries that carry this run's tags: the core profile is context-wide, so on a shared
// sandbox it holds facts from every earlier run.
type Recorder struct {
	Next http.RoundTripper

	mu     sync.Mutex
	writes []Write
	tags   []string
	served int
}

// Serve starts a local reverse proxy in front of target that passes every request through the recorder, and
// returns its base URL. The official SDK owns its HTTP client, so the recorder sits on the wire instead of in
// the client. stop shuts the proxy down.
func (r *Recorder) Serve(target string) (base string, stop func(), err error) {
	u, err := url.Parse(strings.TrimRight(target, "/"))
	if err != nil {
		return "", nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	rp := &httputil.ReverseProxy{
		Rewrite:   func(pr *httputil.ProxyRequest) { pr.SetURL(u); pr.Out.Host = u.Host },
		Transport: r,
	}
	srv := &http.Server{Handler: rp}
	go srv.Serve(ln)
	return "http://" + ln.Addr().String(), func() { srv.Close() }, nil
}

// Write is one accepted write.
type Write struct{ Origin, Text string }

// SetProfileTags admits profile entries containing any tag; with none, the profile is served empty.
func (r *Recorder) SetProfileTags(tags []string) {
	r.mu.Lock()
	r.tags = append([]string(nil), tags...)
	r.mu.Unlock()
}

// ProfileServed is how many profile entries were admitted so far.
func (r *Recorder) ProfileServed() int { r.mu.Lock(); defer r.mu.Unlock(); return r.served }

// Writes is a copy of the accepted writes.
func (r *Recorder) Writes() []Write {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Write(nil), r.writes...)
}

func originOf(labels []string) string {
	for _, l := range labels {
		if v, ok := strings.CutPrefix(l, "origin="); ok {
			return v
		}
	}
	return "unknown"
}

func (r *Recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	next := r.Next
	if next == nil {
		next = http.DefaultTransport
	}
	resp, err := next.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	path := req.URL.Path
	switch {
	case resp.StatusCode < 300 && req.Method == http.MethodPost && strings.HasSuffix(path, "/facts/batch"):
		var b struct {
			Messages []struct{ Content string } `json:"messages"`
			Labels   []string                   `json:"labels"`
		}
		_ = json.Unmarshal(body, &b)
		var texts []string
		for _, m := range b.Messages {
			texts = append(texts, m.Content)
		}
		r.record(originOf(b.Labels), strings.Join(texts, "\n"))
	case resp.StatusCode < 300 && req.Method == http.MethodPost && strings.HasSuffix(path, "/facts"):
		var b struct {
			Text   string   `json:"text"`
			Labels []string `json:"labels"`
		}
		_ = json.Unmarshal(body, &b)
		r.record(originOf(b.Labels), b.Text)
	case resp.StatusCode < 300 && req.Method == http.MethodGet && strings.HasSuffix(path, "/profile"):
		r.filterProfile(resp)
	}
	return resp, nil
}

func (r *Recorder) record(origin, text string) {
	r.mu.Lock()
	r.writes = append(r.writes, Write{origin, text})
	r.mu.Unlock()
}

// filterProfile rewrites a profile response to the admitted entries.
func (r *Recorder) filterProfile(resp *http.Response) {
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		resp.Body = io.NopCloser(bytes.NewReader(raw))
		return
	}
	r.mu.Lock()
	tags := append([]string(nil), r.tags...)
	r.mu.Unlock()
	served := 0
	for _, field := range []string{"instructions", "preferences", "static"} {
		var entries []json.RawMessage
		_ = json.Unmarshal(doc[field], &entries)
		kept := []json.RawMessage{}
		for _, e := range entries {
			for _, t := range tags {
				if strings.Contains(string(e), t) {
					kept = append(kept, e)
					break
				}
			}
		}
		served += len(kept)
		doc[field], _ = json.Marshal(kept)
	}
	r.mu.Lock()
	r.served += served
	r.mu.Unlock()
	out, _ := json.Marshal(doc)
	resp.Body = io.NopCloser(bytes.NewReader(out))
	resp.ContentLength = int64(len(out))
	resp.Header.Del("Content-Length")
}
