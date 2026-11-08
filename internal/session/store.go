// Package session is the SQLite session store: sessions, messages, the stored system prompt and
// full-text search. It ports the Stage 1 subset of hermes_state*.py (about 40 Python files) onto
// modernc.org/sqlite. Gateway routing tables, turn leases, compression locks, display ordering
// and the CJK/trigram indexes are not carried over.
package session

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// ErrForeignSchema means state.db was written by another engine (the Python store).
var ErrForeignSchema = errors.New("state.db has a schema this build does not own")

// ErrNotFound is returned when a session does not exist.
var ErrNotFound = errors.New("session not found")

// Store is a handle on <home>/state.db. It is safe for concurrent use.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// Open opens (creating if needed) <home>/state.db in WAL mode with foreign keys on.
func Open(home string) (*Store, error) {
	return OpenPath(filepath.Join(home, "state.db"))
}

// OpenPath opens a database file directly.
func OpenPath(path string) (*Store, error) {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(NORMAL)")
	// BEGIN IMMEDIATE: a deferred transaction that later upgrades to a write skips the busy timeout
	// and fails with SQLITE_BUSY under concurrent writers.
	q.Add("_txlock", "immediate")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, now: time.Now}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) ts() float64 { return float64(s.now().UnixNano()) / 1e9 }

func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	var have int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='schema_version'`).Scan(&have)
	if err != nil {
		return err
	}
	if have > 0 {
		var v sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT version FROM schema_version LIMIT 1`).Scan(&v); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if v.Valid && v.Int64 != schemaVersion {
			return fmt.Errorf("%w: schema_version %d, expected %d (a Python-era database must be imported, not opened)", ErrForeignSchema, v.Int64, schemaVersion)
		}
	}
	if _, err := tx.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_version(version) SELECT ? WHERE NOT EXISTS (SELECT 1 FROM schema_version)`, schemaVersion); err != nil {
		return err
	}
	return tx.Commit()
}

// Session is one conversation. Optional text columns read back as "" when NULL.
type Session struct {
	ID               string
	Source           string
	UserID           string
	SessionKey       string
	ChatID           string
	ChatType         string
	ThreadID         string
	DisplayName      string
	Model            string
	SystemPrompt     string
	SystemPromptHash string
	ParentSessionID  string
	StartedAt        float64
	EndedAt          *float64
	EndReason        string
	MessageCount     int
	ToolCallCount    int
	APICallCount     int
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	ReasoningTokens  int
	Cwd              string
	Title            string
	ProfileName      string
}

// NewSession holds the fields set at creation.
type NewSession struct {
	ID              string
	Source          string
	Model           string
	Cwd             string
	ParentSessionID string
	UserID          string
	SessionKey      string
	ChatID          string
	ChatType        string
	ThreadID        string
	DisplayName     string
	ProfileName     string
}

// CreateSession inserts a session. It is idempotent: an existing id is left untouched.
func (s *Store) CreateSession(ctx context.Context, n NewSession) error {
	if n.ID == "" || n.Source == "" {
		return errors.New("session id and source are required")
	}
	_, err := s.db.ExecContext(ctx, `
INSERT OR IGNORE INTO sessions (id, source, model, cwd, parent_session_id, user_id, session_key, chat_id,
                                chat_type, thread_id, display_name, profile_name, started_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		n.ID, n.Source, nullStr(n.Model), nullStr(n.Cwd), nullStr(n.ParentSessionID), nullStr(n.UserID),
		nullStr(n.SessionKey), nullStr(n.ChatID), nullStr(n.ChatType), nullStr(n.ThreadID),
		nullStr(n.DisplayName), nullStr(n.ProfileName), s.ts())
	return err
}

func nullStr(v string) any {
	if v == "" {
		return nil
	}
	return v
}

const sessionCols = `id, source, COALESCE(user_id,''), COALESCE(session_key,''), COALESCE(chat_id,''),
 COALESCE(chat_type,''), COALESCE(thread_id,''), COALESCE(display_name,''), COALESCE(model,''),
 COALESCE(system_prompt,''), COALESCE(system_prompt_hash,''), COALESCE(parent_session_id,''), started_at,
 ended_at, COALESCE(end_reason,''), message_count, tool_call_count, api_call_count, input_tokens,
 output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens, COALESCE(cwd,''),
 COALESCE(title,''), COALESCE(profile_name,'')`

// GetSession loads a session or returns ErrNotFound.
func (s *Store) GetSession(ctx context.Context, id string) (*Session, error) {
	var x Session
	var ended sql.NullFloat64
	err := s.db.QueryRowContext(ctx, `SELECT `+sessionCols+` FROM sessions WHERE id = ?`, id).Scan(
		&x.ID, &x.Source, &x.UserID, &x.SessionKey, &x.ChatID, &x.ChatType, &x.ThreadID, &x.DisplayName,
		&x.Model, &x.SystemPrompt, &x.SystemPromptHash, &x.ParentSessionID, &x.StartedAt, &ended,
		&x.EndReason, &x.MessageCount, &x.ToolCallCount, &x.APICallCount, &x.InputTokens, &x.OutputTokens,
		&x.CacheReadTokens, &x.CacheWriteTokens, &x.ReasoningTokens, &x.Cwd, &x.Title, &x.ProfileName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if ended.Valid {
		x.EndedAt = &ended.Float64
	}
	return &x, nil
}

// SetSystemPrompt stores the session's system prompt once. Later calls do not overwrite it and
// report false: the prompt is built once per conversation and reused on resume, because changing
// it mid-conversation would break the provider's prompt cache. The text is deduplicated by hash.
func (s *Store) SetSystemPrompt(ctx context.Context, sessionID, prompt string) (bool, error) {
	sum := sha256.Sum256([]byte(prompt))
	hash := hex.EncodeToString(sum[:])
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck
	var current sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT system_prompt FROM sessions WHERE id = ?`, sessionID).Scan(&current); errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	} else if err != nil {
		return false, err
	}
	if current.Valid {
		return false, nil // already stored: neither the session nor system_prompts changes
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO system_prompts(hash, prompt) VALUES (?, ?)`, hash, prompt); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET system_prompt = ?, system_prompt_hash = ? WHERE id = ?`, prompt, hash, sessionID); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// EndSession marks a session ended. The first end reason wins, so a compression split keeps
// "compression" even if a stale end lands later; use Reopen to end it again.
func (s *Store) EndSession(ctx context.Context, id, reason string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET ended_at = ?, end_reason = ? WHERE id = ? AND ended_at IS NULL`, s.ts(), reason, id)
	return err
}

// Reopen clears the end marker so a resumed session can be ended again.
func (s *Store) Reopen(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET ended_at = NULL, end_reason = NULL WHERE id = ?`, id)
	return err
}

// SetTitle names a session.
func (s *Store) SetTitle(ctx context.Context, id, title string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE sessions SET title = ? WHERE id = ?`, nullStr(title), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Usage is one API call's token accounting, added to the session totals.
type Usage struct {
	Input, Output, CacheRead, CacheWrite, Reasoning int
}

// AddUsage adds an API call's tokens to the session and counts the call.
func (s *Store) AddUsage(ctx context.Context, id string, u Usage) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE sessions SET input_tokens = input_tokens + ?, output_tokens = output_tokens + ?,
  cache_read_tokens = cache_read_tokens + ?, cache_write_tokens = cache_write_tokens + ?,
  reasoning_tokens = reasoning_tokens + ?, api_call_count = api_call_count + 1
WHERE id = ?`, u.Input, u.Output, u.CacheRead, u.CacheWrite, u.Reasoning, id)
	return err
}

// RecentSessions lists up to limit sessions that have messages, newest first, skipping exclude (the live one).
func (s *Store) RecentSessions(ctx context.Context, limit int, exclude string) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM sessions WHERE id != ? AND message_count > 0 ORDER BY started_at DESC, id DESC LIMIT ?`, exclude, limit)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Session, 0, len(ids))
	for _, id := range ids {
		x, err := s.GetSession(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, *x)
	}
	return out, nil
}
