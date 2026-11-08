// Package learning is the reliable learning loop: a durable queue of review jobs, a scheduler that decides when a
// review is due, a runner with bounded retries, and the loop that ties them to an agent. A review replays a
// conversation in a background fork and lets it save memory and skills; it never competes with the live turn.
package learning

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// Trigger says why a review was queued.
type Trigger string

const (
	TriggerIterations   Trigger = "iterations"
	TriggerSessionEnd   Trigger = "session_end"
	TriggerSessionReset Trigger = "session_reset"
	TriggerPreCompress  Trigger = "pre_compress"
	TriggerIdle         Trigger = "idle"
	TriggerShutdown     Trigger = "shutdown"
)

// Outcome is how a review fork ended.
type Outcome string

const (
	// Done: the review ran.
	Done Outcome = "done"
	// Skipped: reviews are disabled or unsupported; nothing to retry.
	Skipped Outcome = "skipped"
	// Interrupted: a live turn or teardown cancelled it; retry with attempts unchanged.
	Interrupted Outcome = "interrupted"
	// Failed: counts an attempt.
	Failed Outcome = "failed"
)

// Status is a job's place in its life.
type Status string

const (
	Pending  Status = "pending"
	Running  Status = "running"
	Finished Status = "done"
	Dead     Status = "failed"
)

// Job is a unit of review work over messages [Start, End) of a session.
type Job struct {
	ID         string
	SessionID  string
	Trigger    Trigger
	Start, End int
	Digest     string
	Attempts   int
	CreatedAt  time.Time
	Status     Status
	NotBefore  time.Time // zero: runnable now
	LastError  string
}

// DigestRange is a stable digest of a message range; identical ranges dedupe in the queue.
func DigestRange(sessionID string, start, end int, texts []string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s:%d:%d", sessionID, start, end)
	for _, t := range texts {
		h.Write([]byte{0})
		h.Write([]byte(t))
	}
	return hex.EncodeToString(h.Sum(nil))
}
