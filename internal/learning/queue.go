package learning

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Queue is the durable job store.
type Queue interface {
	// Enqueue inserts job; false (a no-op) when the same session range was already queued.
	Enqueue(ctx context.Context, job Job) (bool, error)
	// Claim atomically moves the oldest runnable pending job to running; nil when none is due.
	Claim(ctx context.Context, now time.Time) (*Job, error)
	Complete(ctx context.Context, id string) error
	// Release returns a running job to pending with attempts unchanged (interrupted).
	Release(ctx context.Context, id string) error
	// Fail counts an attempt. A zero retryAt is terminal.
	Fail(ctx context.Context, id, errText string, retryAt time.Time) error
}

const schema = `
CREATE TABLE IF NOT EXISTS review_jobs (
    job_id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    trigger TEXT NOT NULL,
    start_msg_idx INTEGER NOT NULL,
    end_msg_idx INTEGER NOT NULL,
    transcript_digest TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    status TEXT NOT NULL,
    not_before TEXT,
    last_error TEXT,
    UNIQUE (session_id, transcript_digest)
)`

// tsFormat is fixed-width UTC, so lexicographic comparison in SQL is chronological.
const tsFormat = "2006-01-02T15:04:05.000000Z"

func ts(t time.Time) string { return t.UTC().Format(tsFormat) }

func parseTS(s string) time.Time {
	t, _ := time.Parse(tsFormat, s)
	return t
}

// SQLQueue keeps the queue in a SQLite database (state.db).
type SQLQueue struct{ db *sql.DB }

// NewSQLQueue creates the table and returns jobs a crash left running to pending (attempts unchanged).
func NewSQLQueue(ctx context.Context, db *sql.DB) (*SQLQueue, error) {
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return nil, err
	}
	if _, err := db.ExecContext(ctx, `UPDATE review_jobs SET status='pending' WHERE status='running'`); err != nil {
		return nil, err
	}
	return &SQLQueue{db: db}, nil
}

func (q *SQLQueue) Enqueue(ctx context.Context, j Job) (bool, error) {
	var nb any
	if !j.NotBefore.IsZero() {
		nb = ts(j.NotBefore)
	}
	res, err := q.db.ExecContext(ctx, `INSERT OR IGNORE INTO review_jobs
 (job_id, session_id, trigger, start_msg_idx, end_msg_idx, transcript_digest, attempts, created_at, status, not_before, last_error)
 VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		j.ID, j.SessionID, string(j.Trigger), j.Start, j.End, j.Digest, j.Attempts, ts(j.CreatedAt), string(orStatus(j.Status)), nb, nullStr(j.LastError))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func orStatus(s Status) Status {
	if s == "" {
		return Pending
	}
	return s
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (q *SQLQueue) Claim(ctx context.Context, now time.Time) (*Job, error) {
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var (
		j              Job
		trig, status   string
		created        string
		notBefore, msg sql.NullString
	)
	err = tx.QueryRowContext(ctx, `SELECT job_id, session_id, trigger, start_msg_idx, end_msg_idx, transcript_digest, attempts, created_at, status, not_before, last_error
 FROM review_jobs WHERE status='pending' AND (not_before IS NULL OR not_before <= ?) ORDER BY created_at, rowid LIMIT 1`, ts(now)).
		Scan(&j.ID, &j.SessionID, &trig, &j.Start, &j.End, &j.Digest, &j.Attempts, &created, &status, &notBefore, &msg)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE review_jobs SET status='running' WHERE job_id=? AND status='pending'`, j.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	j.Trigger, j.CreatedAt, j.Status, j.LastError = Trigger(trig), parseTS(created), Running, msg.String
	if notBefore.Valid {
		j.NotBefore = parseTS(notBefore.String)
	}
	return &j, nil
}

func (q *SQLQueue) Complete(ctx context.Context, id string) error {
	_, err := q.db.ExecContext(ctx, `UPDATE review_jobs SET status='done', last_error=NULL WHERE job_id=?`, id)
	return err
}

func (q *SQLQueue) Release(ctx context.Context, id string) error {
	_, err := q.db.ExecContext(ctx, `UPDATE review_jobs SET status='pending' WHERE job_id=?`, id)
	return err
}

func (q *SQLQueue) Fail(ctx context.Context, id, errText string, retryAt time.Time) error {
	status, nb := string(Dead), any(nil)
	if !retryAt.IsZero() {
		status, nb = string(Pending), ts(retryAt)
	}
	_, err := q.db.ExecContext(ctx, `UPDATE review_jobs SET attempts=attempts+1, status=?, not_before=?, last_error=? WHERE job_id=?`, status, nb, errText, id)
	return err
}
