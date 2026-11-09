package cron

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
)

// OptStr is a nullable string: "" is written as null and null reads as "", the shape Python's jobs.json uses.
type OptStr string

func (s OptStr) MarshalJSON() ([]byte, error) {
	if s == "" {
		return []byte("null"), nil
	}
	return json.Marshal(string(s))
}

func (s *OptStr) UnmarshalJSON(b []byte) error {
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		*s = ""
		return nil
	}
	var v string
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*s = OptStr(v)
	return nil
}

// Repeat is how many times a job runs (Times nil: forever).
type Repeat struct {
	Times     *int `json:"times"`
	Completed int  `json:"completed"`
}

// Job states.
const (
	StateScheduled = "scheduled"
	StatePaused    = "paused"
	StateCompleted = "completed"
	StateError     = "error"
)

// Job is one scheduled job. Fields this build does not know are kept verbatim in Extra, so a store written by the
// Python agent survives a rewrite.
type Job struct {
	ID                string                     `json:"id"`
	Name              string                     `json:"name"`
	Prompt            string                     `json:"prompt"`
	Skills            []string                   `json:"skills"`
	Skill             OptStr                     `json:"skill"`
	Model             OptStr                     `json:"model"`
	Provider          OptStr                     `json:"provider"`
	ContextFrom       []string                   `json:"context_from,omitempty"`
	Schedule          Schedule                   `json:"schedule"`
	ScheduleDisplay   string                     `json:"schedule_display"`
	Repeat            Repeat                     `json:"repeat"`
	Enabled           bool                       `json:"enabled"`
	State             string                     `json:"state"`
	PausedAt          OptStr                     `json:"paused_at"`
	PausedReason      OptStr                     `json:"paused_reason"`
	CreatedAt         string                     `json:"created_at"`
	NextRunAt         OptStr                     `json:"next_run_at"`
	LastRunAt         OptStr                     `json:"last_run_at"`
	LastStatus        OptStr                     `json:"last_status"`
	LastError         OptStr                     `json:"last_error"`
	LastDeliveryError OptStr                     `json:"last_delivery_error"`
	FailureStreak     int                        `json:"failure_streak"`
	Deliver           string                     `json:"deliver"`
	Origin            *Origin                    `json:"origin"`
	EnabledToolsets   []string                   `json:"enabled_toolsets"`
	Workdir           OptStr                     `json:"workdir"`
	Extra             map[string]json.RawMessage `json:"-"`
}

// Origin is where a job was created, for "origin" delivery.
type Origin struct {
	Platform string `json:"platform"`
	ChatID   string `json:"chat_id"`
	ThreadID string `json:"thread_id,omitempty"`
	Name     string `json:"chat_name,omitempty"`
}

type jobAlias Job

var knownFields = func() map[string]bool {
	b, _ := json.Marshal(jobAlias{})
	var m map[string]json.RawMessage
	_ = json.Unmarshal(b, &m)
	known := map[string]bool{}
	for k := range m {
		known[k] = true
	}
	// omitted-when-empty fields are not in the zero value's JSON
	for _, k := range []string{"context_from"} {
		known[k] = true
	}
	return known
}()

func (j *Job) UnmarshalJSON(b []byte) error {
	var a jobAlias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return err
	}
	a.Extra = map[string]json.RawMessage{}
	for k, v := range all {
		if !knownFields[k] {
			a.Extra[k] = v
		}
	}
	*j = Job(a)
	return nil
}

func (j Job) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(jobAlias(j))
	if err != nil {
		return nil, err
	}
	if len(j.Extra) == 0 {
		return b, nil
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(b, &m)
	for k, v := range j.Extra {
		if _, taken := m[k]; !taken {
			m[k] = v
		}
	}
	return json.Marshal(m)
}

// Runnable reports whether the scheduler may fire the job on its own.
func (j *Job) Runnable() bool {
	return j.Enabled && j.State != StatePaused && j.State != StateCompleted
}

// Terminal reports a job that will never run again.
func (j *Job) Terminal() bool { return j.State == StateCompleted }

func parseTime(s OptStr) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := parseISO(strings.TrimSpace(string(s)), time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// Next is the job's next run, if it has one.
func (j *Job) Next() (time.Time, bool) { return parseTime(j.NextRunAt) }
