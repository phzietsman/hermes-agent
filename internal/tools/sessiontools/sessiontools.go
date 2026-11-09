// Package sessiontools offers session_search: recall of past conversations straight from the session store
// (FTS5, no LLM). Compaction-archived rows are searchable, so history the model no longer sees is recoverable.
package sessiontools

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/phzietsman/hermes-agent/internal/redact"
	"github.com/phzietsman/hermes-agent/internal/session"
	"github.com/phzietsman/hermes-agent/internal/tools"
)

const description = "Recall past conversations: search or read old Hermes sessions (FTS5), or scroll inside one. Four shapes, picked by args: " +
	"`query` = discovery (top-N matching sessions, top result fully hydrated); `session_id` + `around_message_id` = scroll (window of messages around an anchor); " +
	"`session_id` alone = read a whole session — how you resolve an `@session:<profile>/<id>` link (split on '/' into profile + id); no args = browse recent sessions. " +
	"Results are actual DB messages, no LLM. Searches conversation history ONLY — when the user gave a direct source (URL, file, contact, live system), inspect that first; " +
	"never conclude 'not found' from history alone. Use for questions about past conversations: 'what did we do about X', 'where did we leave Y'. " +
	"When referring the user to a session, write its `link` value verbatim inline (it renders as a titled link)."

const (
	defaultLimit = 3
	maxLimit     = 10
	defaultWin   = 5
	maxWin       = 20
	msgChars     = 2000
	maxReadMsgs  = 200
	// hitPool is how many message hits are grouped into sessions before the top N are kept.
	hitPool = 60
)

// Register adds session_search to r, reading from st.
func Register(r *tools.Registry, st *session.Store) {
	str := func(d string) map[string]any { return map[string]any{"type": "string", "description": d} }
	num := func(d string) map[string]any { return map[string]any{"type": "integer", "description": d} }
	r.MustRegister(tools.Tool{
		Name: "session_search", Toolset: "session_search", Description: description, Parallel: true,
		Parameters: map[string]any{"type": "object", "required": []any{}, "properties": map[string]any{
			"query":             str("Search query (discovery shape). Keywords, phrases, or boolean expressions to find in past sessions. Omit to browse recent sessions. Ignored when session_id + around_message_id are set (scroll shape)."),
			"limit":             num("Discovery/browse shapes only. Max sessions to return (default 3, max 10). Bump to 5–10 when the topic likely spans several sessions."),
			"sort":              map[string]any{"type": "string", "enum": []any{"newest", "oldest"}, "description": "Discovery shape only. Temporal bias on top of FTS5 ranking: omit for relevance-only, 'newest' for \"where did we leave X\", 'oldest' for \"how did X start\"."},
			"detail":            map[string]any{"type": "string", "enum": []any{"adaptive", "full"}, "description": "Discovery shape only. 'adaptive' (default) fully hydrates the top-ranked result and returns only the anchor message for the rest. 'full' returns the anchored window for every result."},
			"after":             str("Discovery shape only. Inclusive lower bound on session start time. ISO date/datetime (e.g. 2026-06-01) or relative duration (7d, 24h, 2w = within the last N). Use only when the user names a time frame."),
			"before":            str("Discovery shape only. Exclusive upper bound on session start time; same formats as `after`."),
			"role_filter":       str("Discovery shape only. Comma-separated roles to search (user,assistant,tool). Default: user and assistant."),
			"session_id":        str("Read or scroll shape: the session to open."),
			"around_message_id": num("Scroll shape: the anchor message id; returns a window of messages around it."),
			"window":            num("Scroll/hydration shapes: messages on each side of the anchor (default 5, max 20)."),
		}},
		Handler: func(ctx context.Context, a tools.Args) (string, error) {
			return (&search{st: st, now: time.Now}).run(ctx, a), nil
		},
	})
}

type search struct {
	st  *session.Store
	now func() time.Time
}

func fail(msg string) string { return tools.Error(msg, tools.Field{Key: "success", Value: false}) }

func iso(ts float64) string {
	return time.Unix(int64(ts), 0).UTC().Format("2006-01-02T15:04:05Z")
}

func clip(s string) string {
	s = redact.Tokens(s)
	if utf8.RuneCountInString(s) <= msgChars {
		return s
	}
	return string([]rune(s)[:msgChars]) + "… [truncated]"
}

func render(m session.Message) map[string]any {
	out := map[string]any{"id": m.ID, "role": m.Role, "content": clip(m.Content), "timestamp": iso(m.Timestamp)}
	if m.ToolName != "" {
		out["tool_name"] = m.ToolName
	}
	return out
}

var relRe = regexp.MustCompile(`^(\d+)([hdw])$`)

// parseBound reads an ISO date/datetime or a relative duration ("7d" = seven days ago) as a unix time.
func parseBound(s string, now time.Time) (float64, error) {
	s = strings.TrimSpace(s)
	if m := relRe.FindStringSubmatch(strings.ToLower(s)); m != nil {
		n, _ := strconv.Atoi(m[1])
		unit := map[string]time.Duration{"h": time.Hour, "d": 24 * time.Hour, "w": 7 * 24 * time.Hour}[m[2]]
		return float64(now.Add(-time.Duration(n)*unit).UnixNano()) / 1e9, nil
	}
	for _, layout := range []string{"2006-01-02", "2006-01-02T15:04:05", "2006-01-02 15:04:05", time.RFC3339} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return float64(t.UnixNano()) / 1e9, nil
		}
	}
	return 0, fmt.Errorf("cannot read %q as a time: use an ISO date (2026-06-01) or a relative duration (7d, 24h, 2w)", s)
}

func clamp(n, lo, hi, def int) int {
	if n == 0 {
		return def
	}
	return max(lo, min(hi, n))
}

func (s *search) run(ctx context.Context, a tools.Args) string {
	sid, anchor := a.String("session_id", ""), a.Int("around_message_id", 0)
	win := clamp(a.Int("window", 0), 1, maxWin, defaultWin)
	switch {
	case sid != "" && anchor != 0:
		return s.scroll(ctx, sid, int64(anchor), win)
	case sid != "":
		return s.read(ctx, sid)
	case strings.TrimSpace(a.String("query", "")) != "":
		return s.discover(ctx, a, win)
	}
	return s.browse(ctx, clamp(a.Int("limit", 0), 1, maxLimit, defaultLimit))
}

func (s *search) history(ctx context.Context, sid string) ([]session.Message, string) {
	if _, err := s.st.GetSession(ctx, sid); err != nil {
		return nil, fail(fmt.Sprintf("Session '%s' not found.", sid))
	}
	msgs, err := s.st.Messages(ctx, sid, session.ListOptions{IncludeCompacted: true})
	if err != nil {
		return nil, fail("Session read failed: " + err.Error())
	}
	return msgs, ""
}

func window(msgs []session.Message, anchor int64, win int) ([]map[string]any, bool, bool, bool) {
	at := -1
	for i, m := range msgs {
		if m.ID == anchor {
			at = i
		}
	}
	if at < 0 {
		return nil, false, false, false
	}
	lo, hi := max(0, at-win), min(len(msgs), at+win+1)
	out := make([]map[string]any, 0, hi-lo)
	for _, m := range msgs[lo:hi] {
		out = append(out, render(m))
	}
	return out, lo > 0, hi < len(msgs), true
}

func (s *search) scroll(ctx context.Context, sid string, anchor int64, win int) string {
	msgs, errJSON := s.history(ctx, sid)
	if errJSON != "" {
		return errJSON
	}
	out, before, after, ok := window(msgs, anchor, win)
	if !ok {
		return fail(fmt.Sprintf("Message %d is not in session '%s'.", anchor, sid))
	}
	return tools.Result(tools.Field{Key: "success", Value: true}, tools.Field{Key: "mode", Value: "scroll"},
		tools.Field{Key: "session_id", Value: sid}, tools.Field{Key: "around_message_id", Value: anchor},
		tools.Field{Key: "messages", Value: out}, tools.Field{Key: "has_more_before", Value: before}, tools.Field{Key: "has_more_after", Value: after})
}

func (s *search) read(ctx context.Context, sid string) string {
	msgs, errJSON := s.history(ctx, sid)
	if errJSON != "" {
		return errJSON
	}
	total := len(msgs)
	if total > maxReadMsgs {
		msgs = msgs[:maxReadMsgs]
	}
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, render(m))
	}
	f := []tools.Field{{Key: "success", Value: true}, {Key: "mode", Value: "read"}, {Key: "session_id", Value: sid},
		{Key: "message_count", Value: total}, {Key: "messages", Value: out}}
	if total > len(msgs) {
		f = append(f, tools.Field{Key: "note", Value: fmt.Sprintf("Showing the first %d of %d messages. Scroll with around_message_id to read further.", len(msgs), total)})
	}
	return tools.Result(f...)
}

func (s *search) link(ctx context.Context, x session.Session) string {
	profile := x.ProfileName
	if profile == "" {
		profile = "default"
	}
	return fmt.Sprintf("@session:%s/%s", profile, x.ID)
}

func (s *search) meta(ctx context.Context, id string) (map[string]any, bool) {
	x, err := s.st.GetSession(ctx, id)
	if err != nil {
		return nil, false
	}
	title := x.Title
	return map[string]any{"session_id": x.ID, "title": nullIfEmpty(title), "source": x.Source, "started_at": iso(x.StartedAt),
		"message_count": x.MessageCount, "link": s.link(ctx, *x)}, true
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *search) browse(ctx context.Context, limit int) string {
	rows, err := s.st.RecentSessions(ctx, limit, tools.Env(ctx).SessionID)
	if err != nil {
		return fail("Session browse failed: " + err.Error())
	}
	out := make([]map[string]any, 0, len(rows))
	for _, x := range rows {
		m, _ := s.meta(ctx, x.ID)
		out = append(out, m)
	}
	return tools.Result(tools.Field{Key: "success", Value: true}, tools.Field{Key: "mode", Value: "browse"},
		tools.Field{Key: "count", Value: len(out)}, tools.Field{Key: "sessions", Value: out})
}

func (s *search) discover(ctx context.Context, a tools.Args, win int) string {
	q := session.SearchQuery{Query: a.String("query", ""), Limit: hitPool, Sort: a.String("sort", "")}
	if q.Sort != "" && q.Sort != "newest" && q.Sort != "oldest" {
		return fail("sort must be 'newest' or 'oldest'.")
	}
	roles := a.String("role_filter", "")
	for _, r := range strings.Split(roles, ",") {
		if r = strings.TrimSpace(r); r != "" {
			q.Roles = append(q.Roles, r)
		}
	}
	if len(q.Roles) == 0 {
		q.Roles = []string{"user", "assistant"}
	}
	now := s.now()
	for _, b := range []struct {
		key string
		dst **float64
	}{{"after", &q.AfterTS}, {"before", &q.BeforeTS}} {
		if v := a.String(b.key, ""); v != "" {
			ts, err := parseBound(v, now)
			if err != nil {
				return fail(err.Error())
			}
			*b.dst = &ts
		}
	}
	hits, err := s.st.Search(ctx, q)
	if err != nil {
		return fail("Session search failed: " + err.Error())
	}
	current := tools.Env(ctx).SessionID
	limit := clamp(a.Int("limit", 0), 1, maxLimit, defaultLimit)
	full := a.String("detail", "adaptive") == "full"

	var order []string
	first := map[string]session.Hit{}
	counts := map[string]int{}
	for _, h := range hits {
		if h.SessionID == current {
			continue
		}
		if counts[h.SessionID]++; counts[h.SessionID] == 1 {
			order = append(order, h.SessionID)
			first[h.SessionID] = h
		}
	}
	if len(order) > limit {
		order = order[:limit]
	}
	results := make([]map[string]any, 0, len(order))
	for i, id := range order {
		m, ok := s.meta(ctx, id)
		if !ok {
			continue
		}
		h := first[id]
		m["match_count"] = counts[id]
		m["anchor_message_id"] = h.MessageID
		m["snippet"] = clip(h.Snippet)
		if i == 0 || full {
			if msgs, _ := s.st.Messages(ctx, id, session.ListOptions{IncludeCompacted: true}); len(msgs) > 0 {
				if w, _, _, ok := window(msgs, h.MessageID, win); ok {
					m["messages"] = w
				}
			}
		}
		results = append(results, m)
	}
	if len(results) == 0 {
		return tools.Result(tools.Field{Key: "success", Value: true}, tools.Field{Key: "mode", Value: "discover"},
			tools.Field{Key: "query", Value: q.Query}, tools.Field{Key: "count", Value: 0}, tools.Field{Key: "results", Value: []any{}},
			tools.Field{Key: "message", Value: "No matching sessions found."})
	}
	return tools.Result(tools.Field{Key: "success", Value: true}, tools.Field{Key: "mode", Value: "discover"},
		tools.Field{Key: "query", Value: q.Query}, tools.Field{Key: "count", Value: len(results)}, tools.Field{Key: "results", Value: results})
}
