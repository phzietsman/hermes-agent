package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// Message is one transcript row. Text columns read back as "" when NULL; ToolCalls and
// ReasoningDetails hold raw JSON.
type Message struct {
	ID                int64
	SessionID         string
	Role              string
	Content           string
	ToolCallID        string
	ToolCalls         json.RawMessage
	ToolName          string
	Timestamp         float64
	TokenCount        int
	FinishReason      string
	Reasoning         string
	ReasoningContent  string
	ReasoningDetails  json.RawMessage
	PlatformMessageID string
	// CompressedSummary marks the summary row a compaction wrote.
	CompressedSummary bool
	// Active is false for rewound rows; Compacted marks rows archived by compaction.
	Active    bool
	Compacted bool
	// APIContent is the byte-exact string sent to the API when it differed from Content.
	APIContent string
}

// AppendMessage stores m (ID and Active are ignored: new rows are live) and bumps the session's
// message and tool-call counters in the same transaction. It returns the row id. A zero
// Timestamp means now.
func (s *Store) AppendMessage(ctx context.Context, m Message) (int64, error) {
	if m.SessionID == "" || m.Role == "" {
		return 0, errors.New("message needs a session id and a role")
	}
	ts := m.Timestamp
	if ts == 0 {
		ts = s.ts()
	}
	toolCalls := rawOrNil(m.ToolCalls)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck
	res, err := tx.ExecContext(ctx, `
INSERT INTO messages (session_id, role, content, tool_call_id, tool_calls, tool_name, timestamp, token_count,
                      finish_reason, reasoning, reasoning_content, reasoning_details, platform_message_id,
                      _compressed_summary, api_content)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.SessionID, m.Role, nullStr(m.Content), nullStr(m.ToolCallID), toolCalls, nullStr(m.ToolName), ts,
		nullInt(m.TokenCount), nullStr(m.FinishReason), nullStr(m.Reasoning), nullStr(m.ReasoningContent),
		rawOrNil(m.ReasoningDetails), nullStr(m.PlatformMessageID), boolInt(m.CompressedSummary), nullStr(m.APIContent))
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET message_count = message_count + 1, tool_call_count = tool_call_count + ? WHERE id = ?`,
		countToolCalls(m.ToolCalls), m.SessionID); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func rawOrNil(r json.RawMessage) any {
	if len(r) == 0 || string(r) == "null" {
		return nil
	}
	return string(r)
}

func nullInt(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// countToolCalls is the length of a JSON array of tool calls, or 0 when it is not one.
func countToolCalls(raw json.RawMessage) int {
	var calls []json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &calls) != nil {
		return 0
	}
	return len(calls)
}

// ListOptions selects messages. The default is live rows (active) in insertion order.
type ListOptions struct {
	// IncludeInactive returns every row, including rewound ones (audit view).
	IncludeInactive bool
	// IncludeCompacted adds compaction-archived rows but never rewound ones (display view).
	IncludeCompacted bool
	Limit            int // 0 = no limit
	Offset           int
	// Latest pages back from the newest row but still returns chronological order.
	Latest bool
	// AfterID is keyset paging; it cannot combine with Latest or Offset.
	AfterID int64
}

// Messages loads a session's messages in insertion order (id, never timestamp: clocks regress).
func (s *Store) Messages(ctx context.Context, sessionID string, o ListOptions) ([]Message, error) {
	if o.AfterID != 0 && (o.Latest || o.Offset != 0) {
		return nil, errors.New("AfterID is incompatible with Latest and Offset paging")
	}
	var sb strings.Builder
	sb.WriteString(`SELECT id, session_id, role, COALESCE(content,''), COALESCE(tool_call_id,''), tool_calls,
 COALESCE(tool_name,''), timestamp, COALESCE(token_count,0), COALESCE(finish_reason,''), COALESCE(reasoning,''),
 COALESCE(reasoning_content,''), reasoning_details, COALESCE(platform_message_id,''), _compressed_summary,
 active, compacted, COALESCE(api_content,'') FROM messages WHERE session_id = ?`)
	args := []any{sessionID}
	switch {
	case o.IncludeInactive:
	case o.IncludeCompacted:
		sb.WriteString(" AND (active = 1 OR compacted = 1)")
	default:
		sb.WriteString(" AND active = 1")
	}
	if o.AfterID != 0 {
		sb.WriteString(" AND id > ?")
		args = append(args, o.AfterID)
	}
	if o.Latest {
		sb.WriteString(" ORDER BY id DESC")
	} else {
		sb.WriteString(" ORDER BY id ASC")
	}
	if o.Limit > 0 || o.Offset > 0 {
		limit := -1 // SQLite's OFFSET needs a LIMIT; -1 means none
		if o.Limit > 0 {
			limit = o.Limit
		}
		sb.WriteString(" LIMIT ? OFFSET ?")
		args = append(args, limit, o.Offset)
	}
	rows, err := s.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var calls, details sql.NullString
		var summary, active, compacted int
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &m.ToolCallID, &calls, &m.ToolName,
			&m.Timestamp, &m.TokenCount, &m.FinishReason, &m.Reasoning, &m.ReasoningContent, &details,
			&m.PlatformMessageID, &summary, &active, &compacted, &m.APIContent); err != nil {
			return nil, err
		}
		if calls.Valid {
			m.ToolCalls = json.RawMessage(calls.String)
		}
		if details.Valid {
			m.ReasoningDetails = json.RawMessage(details.String)
		}
		m.CompressedSummary, m.Active, m.Compacted = summary != 0, active != 0, compacted != 0
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if o.Latest {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out, nil
}
