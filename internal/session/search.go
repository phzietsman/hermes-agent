package session

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// maxQueryChars caps a search query before any regex runs, so adversarial input stays bounded.
const maxQueryChars = 2048

// ftsSpecial are the characters FTS5's grammar rejects outside a quoted phrase. % is not here:
// the LIKE fallback needs it literal and escapes it itself.
const ftsSpecial = "+{}():\"^@/#&|~[]<>,;!?$=\\'"

var (
	quotedPhraseRe = regexp.MustCompile(`"[^"]*"`)
	repeatStarRe   = regexp.MustCompile(`\*+`)
	leadingStarRe  = regexp.MustCompile(`(^|\s)\*`)
	leadingBoolRe  = regexp.MustCompile(`(?i)^(AND|OR|NOT)\b\s*`)
	trailingBoolRe = regexp.MustCompile(`(?i)\s+(AND|OR|NOT)\s*$`)
	compoundTermRe = regexp.MustCompile(`\b(\w+(?:[._-]\w+)+)\b`)
	likeTokenRe    = regexp.MustCompile(`"[^"]+"|\S+`)
)

func isOperator(tok string) bool {
	switch strings.ToUpper(tok) {
	case "AND", "OR", "NOT":
		return true
	}
	return false
}

func isCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

func containsCJK(s string) bool {
	for _, r := range s {
		if isCJK(r) {
			return true
		}
	}
	return false
}

// sanitizeQuery makes user input safe for FTS5 MATCH, where raw special characters raise:
// balanced "quoted phrases" are kept, other special characters become spaces, hyphenated, dotted
// and underscored terms (chat-send, P2.2, my-app.config.ts) are quoted so they match as phrases,
// and dangling boolean operators are dropped.
func sanitizeQuery(q string) string {
	if len(q) > maxQueryChars {
		q = truncateRunes(q, maxQueryChars)
	}
	var held []string
	// A numbered placeholder cannot appear in user text because NUL is stripped first.
	q = strings.ReplaceAll(q, "\x00", " ")
	s := quotedPhraseRe.ReplaceAllStringFunc(q, func(m string) string {
		held = append(held, m)
		return "\x00Q" + strconv.Itoa(len(held)-1) + "\x00"
	})
	s = strings.ReplaceAll(s, `"`, " ")
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(ftsSpecial, r) {
			return ' '
		}
		return r
	}, s)
	if strings.Contains(s, "%") && !containsCJK(s) {
		s = strings.ReplaceAll(s, "%", " ")
	}
	s = repeatStarRe.ReplaceAllString(s, "*")
	s = leadingStarRe.ReplaceAllString(s, "$1")
	s = leadingBoolRe.ReplaceAllString(strings.TrimSpace(s), "")
	s = trailingBoolRe.ReplaceAllString(strings.TrimSpace(s), "")
	s = compoundTermRe.ReplaceAllString(s, `"$1"`)
	for i, quoted := range held {
		s = strings.ReplaceAll(s, "\x00Q"+strconv.Itoa(i)+"\x00", quoted)
	}
	return strings.TrimSpace(s)
}

func truncateRunes(s string, n int) string {
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}

// orRelaxed rewrites an implicit-AND query as an any-term OR query, or returns "" when relaxation
// does not apply: fewer than two units, or an explicit OR/NOT (the caller meant exact semantics).
// Quoted phrases stay whole: `"docker networking" tls` becomes `"docker networking" OR tls`.
func orRelaxed(q string) string {
	var units []string
	for _, tok := range likeTokenRe.FindAllString(q, -1) {
		switch strings.ToUpper(tok) {
		case "OR", "NOT":
			return ""
		case "AND":
		default:
			units = append(units, tok)
		}
	}
	if len(units) < 2 {
		return ""
	}
	return strings.Join(units, " OR ")
}

// SearchQuery filters a message search. Zero values mean "no filter".
type SearchQuery struct {
	Query          string
	Sources        []string
	ExcludeSources []string
	Roles          []string
	Limit          int // default 20
	Offset         int
	// Sort is "", "newest" or "oldest". "" ranks by BM25 relevance.
	Sort string
	// IncludeInactive also searches rewound rows. Compaction-archived rows are always searched.
	IncludeInactive bool
	// AfterTS and BeforeTS bound the session start time: AfterTS inclusive, BeforeTS exclusive.
	AfterTS, BeforeTS *float64
}

// Hit is one search result: a snippet with matches wrapped in >>> <<<, plus session metadata.
type Hit struct {
	MessageID      int64
	SessionID      string
	Role           string
	Snippet        string
	Timestamp      float64
	ToolName       string
	Source         string
	Model          string
	SessionStarted float64
}

// Search finds messages by keywords, "phrases", AND/OR/NOT and prefix*. The query is sanitised so
// user text cannot raise an FTS5 syntax error. Routing:
//
//   - a query containing CJK, or a role filter that includes "tool", scans with LIKE (the word
//     index splits CJK into single characters and indexes only a prefix of tool output);
//   - otherwise the word index answers, ranked by BM25 unless Sort says otherwise;
//   - when that misses, the query is retried matching any term (OR-relaxed), so a paraphrase
//     that lacks one word still finds the sentence.
//
// Rewound rows are hidden unless IncludeInactive is set; compaction-archived rows are found.
func (s *Store) Search(ctx context.Context, q SearchQuery) ([]Hit, error) {
	if strings.TrimSpace(q.Query) == "" {
		return nil, nil
	}
	query := sanitizeQuery(q.Query)
	if query == "" {
		return nil, nil
	}
	if q.Limit <= 0 {
		q.Limit = 20
	}
	if wantsTool(q.Roles) || containsCJK(query) {
		return s.searchLike(ctx, query, q)
	}
	hits, err := s.searchFTS(ctx, query, q)
	if err != nil {
		return nil, err
	}
	if len(hits) == 0 {
		if relaxed := orRelaxed(query); relaxed != "" {
			return s.searchFTS(ctx, relaxed, q)
		}
	}
	return hits, nil
}

func wantsTool(roles []string) bool {
	for _, r := range roles {
		if r == "tool" {
			return true
		}
	}
	return false
}

// filterSQL appends the predicates every route shares and returns the extended args.
func filterSQL(sb *strings.Builder, args []any, q SearchQuery) []any {
	if !q.IncludeInactive {
		sb.WriteString(" AND (m.active = 1 OR m.compacted = 1)")
	}
	if q.Sources != nil {
		sb.WriteString(" AND s.source IN (" + placeholders(len(q.Sources)) + ")")
		for _, v := range q.Sources {
			args = append(args, v)
		}
	}
	if q.ExcludeSources != nil {
		sb.WriteString(" AND s.source NOT IN (" + placeholders(len(q.ExcludeSources)) + ")")
		for _, v := range q.ExcludeSources {
			args = append(args, v)
		}
	}
	if len(q.Roles) > 0 {
		sb.WriteString(" AND m.role IN (" + placeholders(len(q.Roles)) + ")")
		for _, v := range q.Roles {
			args = append(args, v)
		}
	}
	if q.AfterTS != nil {
		sb.WriteString(" AND s.started_at >= ?")
		args = append(args, *q.AfterTS)
	}
	if q.BeforeTS != nil {
		sb.WriteString(" AND s.started_at < ?")
		args = append(args, *q.BeforeTS)
	}
	return args
}

func placeholders(n int) string {
	if n == 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

const hitTail = `m.timestamp, COALESCE(m.tool_name,''), s.source, COALESCE(s.model,''), s.started_at`

func (s *Store) searchFTS(ctx context.Context, match string, q SearchQuery) ([]Hit, error) {
	var sb strings.Builder
	sb.WriteString(`SELECT m.id, m.session_id, m.role, snippet(messages_fts, -1, '>>>', '<<<', '...', 40), ` + hitTail + `
FROM messages_fts JOIN messages m ON m.id = messages_fts.rowid JOIN sessions s ON s.id = m.session_id
WHERE messages_fts MATCH ?`)
	args := filterSQL(&sb, []any{match}, q)
	switch strings.ToLower(strings.TrimSpace(q.Sort)) {
	case "newest":
		sb.WriteString(" ORDER BY m.timestamp DESC, rank")
	case "oldest":
		sb.WriteString(" ORDER BY m.timestamp ASC, rank")
	default:
		sb.WriteString(" ORDER BY rank")
	}
	sb.WriteString(" LIMIT ? OFFSET ?")
	args = append(args, q.Limit, q.Offset)
	hits, err := s.scanHits(ctx, sb.String(), args)
	if err != nil {
		// A syntax error despite sanitising must not surface to the model; treat it as no hits.
		return nil, nil
	}
	return hits, nil
}

// escapeLike escapes LIKE wildcards for use with ESCAPE '\'.
func escapeLike(t string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(t)
}

func (s *Store) searchLike(ctx context.Context, query string, q SearchQuery) ([]Hit, error) {
	var terms []string
	for _, tok := range strings.Fields(stripCJKWildcards(query)) {
		if !isOperator(tok) {
			terms = append(terms, strings.Trim(tok, `"`))
		}
	}
	if len(terms) == 0 {
		terms = []string{strings.Trim(query, `"`)}
	}
	var sb strings.Builder
	sb.WriteString(`SELECT m.id, m.session_id, m.role,
 substr(COALESCE(m.content,''), max(1, instr(COALESCE(m.content,''), ?) - 40), 120), ` + hitTail + `
FROM messages m JOIN sessions s ON s.id = m.session_id WHERE (`)
	args := []any{terms[0]}
	for i, t := range terms {
		if i > 0 {
			sb.WriteString(" OR ")
		}
		sb.WriteString(`(m.content LIKE ? ESCAPE '\' OR m.tool_name LIKE ? ESCAPE '\' OR m.tool_calls LIKE ? ESCAPE '\')`)
		p := "%" + escapeLike(t) + "%"
		args = append(args, p, p, p)
	}
	sb.WriteString(")")
	args = filterSQL(&sb, args, q)
	sb.WriteString(" ORDER BY m.timestamp DESC LIMIT ? OFFSET ?")
	args = append(args, q.Limit, q.Offset)
	return s.scanHits(ctx, sb.String(), args)
}

// stripCJKWildcards drops the trailing * callers add for prefix matching: LIKE has no such
// wildcard, so it would be searched for as a literal asterisk. A token of only stars is kept.
func stripCJKWildcards(q string) string {
	if !strings.Contains(q, "*") {
		return q
	}
	toks := strings.Fields(q)
	for i, t := range toks {
		if isOperator(t) {
			continue
		}
		if trimmed := strings.TrimRight(t, "*"); trimmed != "" {
			toks[i] = trimmed
		}
	}
	return strings.Join(toks, " ")
}

func (s *Store) scanHits(ctx context.Context, query string, args []any) ([]Hit, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.MessageID, &h.SessionID, &h.Role, &h.Snippet, &h.Timestamp, &h.ToolName, &h.Source, &h.Model, &h.SessionStarted); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
