package session

// schemaVersion is the Go store's own version. The Python store used its own numbering (31) in
// the same schema_version table; Open refuses a database carrying any other version rather than
// writing into a schema it does not own.
const schemaVersion = 1

// ftsToolPrefixChars bounds how much of a tool result is indexed (oversized tool output would
// otherwise dominate the index). An explicit tool-role search scans the full body with LIKE.
const ftsToolPrefixChars = 8192

// Column names match the Python store so the two stay recognisable side by side. Only the columns
// Stage 1 reads or writes exist; gateway routing, display ordering, leases and billing are not
// carried over.
const schemaSQL = `
CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL);

CREATE TABLE IF NOT EXISTS system_prompts (
    hash   TEXT PRIMARY KEY,
    prompt TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    id                                TEXT PRIMARY KEY,
    source                            TEXT NOT NULL,
    user_id                           TEXT,
    session_key                       TEXT,
    chat_id                           TEXT,
    chat_type                         TEXT,
    thread_id                         TEXT,
    display_name                      TEXT,
    model                             TEXT,
    system_prompt                     TEXT,
    system_prompt_hash                TEXT REFERENCES system_prompts(hash),
    parent_session_id                 TEXT REFERENCES sessions(id),
    started_at                        REAL NOT NULL,
    ended_at                          REAL,
    end_reason                        TEXT,
    message_count                     INTEGER NOT NULL DEFAULT 0,
    tool_call_count                   INTEGER NOT NULL DEFAULT 0,
    api_call_count                    INTEGER NOT NULL DEFAULT 0,
    input_tokens                      INTEGER NOT NULL DEFAULT 0,
    output_tokens                     INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens                 INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens                INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens                  INTEGER NOT NULL DEFAULT 0,
    cwd                               TEXT,
    title                             TEXT,
    profile_name                      TEXT,
    tool_names                        TEXT,
    compression_failure_cooldown_until REAL,
    compression_failure_error         TEXT,
    compression_fallback_streak       INTEGER NOT NULL DEFAULT 0,
    compression_ineffective_count     INTEGER NOT NULL DEFAULT 0,
    compression_recovery_deadline     REAL,
    compression_overload_streak       INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS messages (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id          TEXT NOT NULL REFERENCES sessions(id),
    role                TEXT NOT NULL,
    content             TEXT,
    tool_call_id        TEXT,
    tool_calls          TEXT,
    tool_name           TEXT,
    timestamp           REAL NOT NULL,
    token_count         INTEGER,
    finish_reason       TEXT,
    reasoning           TEXT,
    reasoning_content   TEXT,
    reasoning_details   TEXT,
    platform_message_id TEXT,
    _compressed_summary INTEGER NOT NULL DEFAULT 0,
    active              INTEGER NOT NULL DEFAULT 1,
    compacted           INTEGER NOT NULL DEFAULT 0,
    api_content         TEXT
);

CREATE INDEX IF NOT EXISTS idx_sessions_source  ON sessions(source);
CREATE INDEX IF NOT EXISTS idx_sessions_parent  ON sessions(parent_session_id);
CREATE INDEX IF NOT EXISTS idx_sessions_started ON sessions(started_at DESC);
CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id, id);

-- Stable projection the word index reads and writes through, so 'rebuild' and the triggers agree.
CREATE VIEW IF NOT EXISTS messages_fts_src AS
    SELECT id,
           CASE WHEN role = 'tool'
                THEN substr(COALESCE(content, ''), 1, 8192)
                ELSE content END AS content,
           tool_name, tool_calls
    FROM messages;

CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
    content, tool_name, tool_calls,
    content='messages_fts_src', content_rowid='id'
);

CREATE TRIGGER IF NOT EXISTS messages_fts_insert AFTER INSERT ON messages BEGIN
    INSERT INTO messages_fts(rowid, content, tool_name, tool_calls)
    VALUES (new.id,
            CASE WHEN new.role = 'tool' THEN substr(COALESCE(new.content, ''), 1, 8192) ELSE new.content END,
            new.tool_name, new.tool_calls);
END;

CREATE TRIGGER IF NOT EXISTS messages_fts_delete AFTER DELETE ON messages BEGIN
    INSERT INTO messages_fts(messages_fts, rowid, content, tool_name, tool_calls)
    VALUES ('delete', old.id,
            CASE WHEN old.role = 'tool' THEN substr(COALESCE(old.content, ''), 1, 8192) ELSE old.content END,
            old.tool_name, old.tool_calls);
END;

-- UPDATE OF skips the trigger for non-content writes (active, compacted, token counts).
CREATE TRIGGER IF NOT EXISTS messages_fts_update
AFTER UPDATE OF content, tool_name, tool_calls, role ON messages
WHEN old.content IS NOT new.content OR old.tool_name IS NOT new.tool_name
  OR old.tool_calls IS NOT new.tool_calls OR old.role IS NOT new.role
BEGIN
    INSERT INTO messages_fts(messages_fts, rowid, content, tool_name, tool_calls)
    VALUES ('delete', old.id,
            CASE WHEN old.role = 'tool' THEN substr(COALESCE(old.content, ''), 1, 8192) ELSE old.content END,
            old.tool_name, old.tool_calls);
    INSERT INTO messages_fts(rowid, content, tool_name, tool_calls)
    VALUES (new.id,
            CASE WHEN new.role = 'tool' THEN substr(COALESCE(new.content, ''), 1, 8192) ELSE new.content END,
            new.tool_name, new.tool_calls);
END;
`
