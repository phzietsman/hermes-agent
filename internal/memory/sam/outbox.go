package sam

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

// Op is what a queued write does.
type Op string

const (
	OpRemember     Op = "remember"
	OpRememberMany Op = "remember_many"
	OpForget       Op = "forget"
)

// Entry is one queued write. The file format is Python's (one JSON object per line), so a queue written by the
// Python agent replays here and the other way round.
type Entry struct {
	EntryID   string         `json:"entry_id"`
	Op        Op             `json:"op"`
	Payload   map[string]any `json:"payload"`
	CreatedAt time.Time      `json:"created_at"`
	Attempts  int            `json:"attempts"`
}

// EntryID is content-addressed (sha256 of "op:<sorted compact-ish JSON>", first 24 hex), computed with Python's
// json.dumps(sort_keys=True) layout so ids match the Python agent's: an identical write queued twice replays once.
func EntryID(op Op, payload map[string]any) string {
	sum := sha256.Sum256([]byte(string(op) + ":" + pyJSON(payload)))
	return hex.EncodeToString(sum[:])[:24]
}

// pyJSON encodes v like Python's json.dumps(v, sort_keys=True): ", " and ": " separators, non-ASCII escaped.
func pyJSON(v any) string {
	var b strings.Builder
	pyEncode(&b, v)
	return b.String()
}

func pyEncode(b *strings.Builder, v any) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case string:
		pyString(b, x)
	case json.Number:
		b.WriteString(x.String())
	case int:
		b.WriteString(strconv.Itoa(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case float64:
		b.WriteString(pyFloat(x))
	case []string:
		a := make([]any, len(x))
		for i, s := range x {
			a[i] = s
		}
		pyEncode(b, a)
	case []map[string]string:
		a := make([]any, len(x))
		for i, m := range x {
			mm := make(map[string]any, len(m))
			for k, s := range m {
				mm[k] = s
			}
			a[i] = mm
		}
		pyEncode(b, a)
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			pyEncode(b, e)
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			pyString(b, k)
			b.WriteString(": ")
			pyEncode(b, x[k])
		}
		b.WriteByte('}')
	default:
		raw, _ := json.Marshal(x)
		var back any
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		_ = d.Decode(&back)
		pyEncode(b, back)
	}
}

func pyFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eEn") {
		s += ".0"
	}
	return s
}

func pyString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\f':
			b.WriteString(`\f`)
		case r < 0x20:
			fmt.Fprintf(b, `\u%04x`, r)
		case r < 0x7f:
			b.WriteRune(r)
		case r > 0xffff:
			hi, lo := utf16.EncodeRune(r)
			fmt.Fprintf(b, `\u%04x\u%04x`, hi, lo)
		default:
			fmt.Fprintf(b, `\u%04x`, r)
		}
	}
	b.WriteByte('"')
}

// Outbox is the durable queue of writes that did not reach Agent Memory.
type Outbox struct {
	path string
	mu   sync.Mutex
	now  func() time.Time
}

// NewOutbox returns an outbox stored at path (created on first write).
func NewOutbox(path string) *Outbox { return &Outbox{path: path, now: time.Now} }

func (o *Outbox) read() ([]Entry, error) {
	f, err := os.Open(o.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Entry
		d := json.NewDecoder(strings.NewReader(line))
		d.UseNumber()
		if err := d.Decode(&e); err != nil {
			return nil, fmt.Errorf("outbox %s: unreadable entry: %w", o.path, err)
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

func (o *Outbox) write(entries []Entry) error {
	if len(entries) == 0 {
		if err := os.Remove(o.path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(o.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(o.path), ".outbox-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	w := bufio.NewWriter(tmp)
	for _, e := range entries {
		b, err := json.Marshal(e)
		if err != nil {
			tmp.Close()
			return err
		}
		w.Write(b)
		w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), o.path)
}

// Append queues a write. It returns false when an identical entry is already pending.
func (o *Outbox) Append(op Op, payload map[string]any) (bool, error) {
	e := Entry{EntryID: EntryID(op, payload), Op: op, Payload: payload, CreatedAt: o.now().UTC()}
	o.mu.Lock()
	defer o.mu.Unlock()
	entries, err := o.read()
	if err != nil {
		return false, err
	}
	for _, x := range entries {
		if x.EntryID == e.EntryID {
			return false, nil
		}
	}
	return true, o.write(append(entries, e))
}

// Pending lists the queued entries, oldest first.
func (o *Outbox) Pending() ([]Entry, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.read()
}

// Replay applies entries oldest-first. An applied entry is dropped; an entry the service rejects for good is
// dropped too (it can never succeed and would block everything behind it); any other failure stops the replay
// (the backend is down: do not hammer it) and keeps that entry, with its attempt count raised, and everything
// after it. It returns how many entries were applied.
func (o *Outbox) Replay(ctx context.Context, apply func(context.Context, Entry) error) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	entries, err := o.read()
	if err != nil {
		return 0, err
	}
	applied := 0
	for i, e := range entries {
		if ctx.Err() != nil {
			return applied, o.write(entries[i:])
		}
		if err := apply(ctx, e); err != nil {
			if IsRejected(err) {
				continue
			}
			e.Attempts++
			return applied, o.write(append([]Entry{e}, entries[i+1:]...))
		}
		applied++
	}
	return applied, o.write(nil)
}

// Apply is the one place a queued op becomes a client call again.
func Apply(ctx context.Context, c *Client, e Entry) error {
	p := e.Payload
	str := func(k string) string { s, _ := p[k].(string); return s }
	strs := func(k string) []string {
		var out []string
		if l, ok := p[k].([]any); ok {
			for _, x := range l {
				if s, ok := x.(string); ok {
					out = append(out, s)
				}
			}
		}
		return out
	}
	switch e.Op {
	case OpRemember:
		_, err := c.Remember(ctx, str("text"), RememberOptions{Labels: strs("labels"), Category: str("category"), Scopes: str("scopes"), SessionID: str("session_id")})
		return err
	case OpRememberMany:
		var items []Message
		if l, ok := p["items"].([]any); ok {
			for _, x := range l {
				if m, ok := x.(map[string]any); ok {
					items = append(items, Message{Role: fmt.Sprint(m["role"]), Content: fmt.Sprint(m["content"])})
				}
			}
		}
		_, err := c.RememberMany(ctx, items, str("session_id"), str("scopes"), strs("labels"))
		return err
	case OpForget:
		purge, _ := p["purge"].(bool)
		_, err := c.Forget(ctx, str("query"), purge)
		return err
	}
	return &Error{Kind: Rejected, Message: "unknown outbox op " + string(e.Op)}
}
