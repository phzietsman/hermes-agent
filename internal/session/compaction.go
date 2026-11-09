package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

// ReplaceLive archives every live message of the session (active=0, compacted=1: out of the transcript, still
// found by search) and writes rows as the new live transcript, in order, in one transaction. The session id
// does not change, so a compaction is invisible to anything that holds it. message_count becomes the live
// count; tool_call_count is cumulative and stays.
func (s *Store) ReplaceLive(ctx context.Context, sessionID string, rows []Message) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `UPDATE messages SET active = 0, compacted = 1 WHERE session_id = ? AND active = 1`, sessionID); err != nil {
		return err
	}
	for _, m := range rows {
		if m.Role == "" {
			return errors.New("message needs a role")
		}
		ts := m.Timestamp
		if ts == 0 {
			ts = s.ts()
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO messages (session_id, role, content, tool_call_id, tool_calls, tool_name, timestamp, token_count,
                      finish_reason, reasoning, reasoning_content, reasoning_details, platform_message_id,
                      _compressed_summary, api_content)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			sessionID, m.Role, nullStr(m.Content), nullStr(m.ToolCallID), rawOrNil(m.ToolCalls), nullStr(m.ToolName), ts,
			nullInt(m.TokenCount), nullStr(m.FinishReason), nullStr(m.Reasoning), nullStr(m.ReasoningContent),
			rawOrNil(m.ReasoningDetails), nullStr(m.PlatformMessageID), boolInt(m.CompressedSummary), nullStr(m.APIContent)); err != nil {
			return err
		}
	}
	res, err := tx.ExecContext(ctx, `UPDATE sessions SET message_count = ? WHERE id = ?`, len(rows), sessionID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// RewriteSystemPrompt replaces the session's stored system prompt. It exists for the one moment the prompt may
// change, a compaction, when the cached prefix is being rebuilt anyway. Anything else must use SetSystemPrompt,
// which writes once.
func (s *Store) RewriteSystemPrompt(ctx context.Context, sessionID, prompt string) error {
	sum := sha256.Sum256([]byte(prompt))
	hash := hex.EncodeToString(sum[:])
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO system_prompts(hash, prompt) VALUES (?, ?)`, hash, prompt); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE sessions SET system_prompt = ?, system_prompt_hash = ? WHERE id = ?`, prompt, hash, sessionID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}
