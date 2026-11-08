package cron

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// OneShotGrace is how late a one-shot may still run (a job created just after its minute).
	OneShotGrace = 120 * time.Second
	minGrace     = 120 * time.Second
	maxGrace     = 2 * time.Hour
)

// Store is the jobs.json store of one profile. Mutations take an exclusive file lock, so a gateway and a CLI
// never overwrite each other's changes.
type Store struct {
	// Dir is <home>/cron.
	Dir string
	// Now and Loc are the clock and the configured time zone; nil means time.Now and time.Local.
	Now func() time.Time
	Loc *time.Location

	mu sync.Mutex
}

// NewStore returns the store under home/cron.
func NewStore(home string) *Store { return &Store{Dir: filepath.Join(home, "cron")} }

func (s *Store) now() time.Time {
	loc := s.Loc
	if loc == nil {
		loc = time.Local
	}
	if s.Now != nil {
		return s.Now().In(loc)
	}
	return time.Now().In(loc)
}

func (s *Store) loc() *time.Location {
	if s.Loc != nil {
		return s.Loc
	}
	return time.Local
}

func (s *Store) file() string { return filepath.Join(s.Dir, "jobs.json") }

// OutputDir is where a job's run outputs are saved.
func (s *Store) OutputDir(jobID string) string { return filepath.Join(s.Dir, "output", jobID) }

type payload struct {
	Jobs      []Job  `json:"jobs"`
	UpdatedAt string `json:"updated_at"`
}

func (s *Store) read() ([]Job, error) {
	raw, err := os.ReadFile(s.file())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, nil
	}
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("%s is corrupt: %w (fix or remove it; nothing was changed)", s.file(), err)
	}
	return p.Jobs, nil
}

func (s *Store) write(jobs []Job) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, ".jobs_")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if jobs == nil {
		jobs = []Job{}
	}
	if err := enc.Encode(payload{Jobs: jobs, UpdatedAt: Iso(s.now())}); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.file())
}

// Edit loads the jobs under the exclusive lock, lets fn change them, and saves when fn returns true. fn's error
// aborts without saving.
func (s *Store) Edit(fn func(jobs []Job) ([]Job, bool, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	lf, err := os.OpenFile(filepath.Join(s.Dir, ".jobs.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	unlock, err := flock(lf)
	if err != nil {
		return err
	}
	defer unlock()
	jobs, err := s.read()
	if err != nil {
		return err
	}
	out, save, err := fn(jobs)
	if err != nil || !save {
		return err
	}
	return s.write(out)
}

// List returns every job.
func (s *Store) List() ([]Job, error) {
	var out []Job
	err := s.Edit(func(jobs []Job) ([]Job, bool, error) { out = append(out, jobs...); return jobs, false, nil })
	return out, err
}

// ErrNotFound is returned for an unknown job id.
var ErrNotFound = errors.New("job not found")

// Get returns one job.
func (s *Store) Get(id string) (*Job, error) {
	jobs, err := s.List()
	if err != nil {
		return nil, err
	}
	for i := range jobs {
		if jobs[i].ID == id {
			return &jobs[i], nil
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
}

func (s *Store) with(id string, fn func(j *Job) error) error {
	found := false
	err := s.Edit(func(jobs []Job) ([]Job, bool, error) {
		for i := range jobs {
			if jobs[i].ID == id {
				found = true
				if err := fn(&jobs[i]); err != nil {
					return nil, false, err
				}
				return jobs, true, nil
			}
		}
		return nil, false, nil
	})
	if err == nil && !found {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return err
}

func newJobID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// CreateOptions describe a new job.
type CreateOptions struct {
	Prompt, Schedule, Name string
	// Repeat nil: once for a one-shot, forever for a recurring job. A value <= 0 means forever.
	Repeat          *int
	Deliver         string
	Origin          *Origin
	Skills          []string
	Model, Provider string
	ContextFrom     []string
	EnabledToolsets []string
	Workdir         string
	Paused          bool
	PausedReason    string
}

// ValidationError is a request the caller can fix.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func bad(format string, a ...any) error { return &ValidationError{fmt.Sprintf(format, a...)} }

func cleanList(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, x := range in {
		if x = strings.TrimSpace(x); x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func validateWorkdir(w string) (string, error) {
	w = strings.TrimSpace(w)
	if w == "" {
		return "", nil
	}
	if !filepath.IsAbs(w) {
		return "", bad("workdir must be an absolute path, got %q", w)
	}
	if fi, err := os.Stat(w); err != nil || !fi.IsDir() {
		return "", bad("workdir %q does not exist or is not a directory", w)
	}
	return filepath.Clean(w), nil
}

// Create validates and stores a new job.
func (s *Store) Create(o CreateOptions) (*Job, error) {
	now := s.now()
	sched, err := ParseSchedule(o.Schedule, now)
	if err != nil {
		return nil, &ValidationError{err.Error()}
	}
	if sched.Kind == KindInterval && (sched.Minutes == nil || *sched.Minutes < 1) {
		return nil, bad("Invalid schedule '%s': an interval must be at least 1 minute.", o.Schedule)
	}
	skills := cleanList(o.Skills)
	prompt := strings.TrimSpace(o.Prompt)
	if prompt == "" && len(skills) == 0 {
		return nil, bad("A job needs a prompt, skills, or both: say what the scheduled run should do.")
	}
	if msg := ScanPrompt(prompt); msg != "" {
		return nil, bad("%s", msg)
	}
	var times *int
	if o.Repeat != nil && *o.Repeat > 0 {
		n := *o.Repeat
		times = &n
	}
	if sched.Kind == KindOnce && times == nil {
		one := 1
		times = &one
	}
	deliver := o.Deliver
	if deliver == "" {
		deliver = "local"
		if o.Origin != nil {
			deliver = "origin"
		}
	}
	workdir, err := validateWorkdir(o.Workdir)
	if err != nil {
		return nil, err
	}
	if o.PausedReason != "" && !o.Paused {
		return nil, bad("paused_reason requires paused=true.")
	}
	name := strings.TrimSpace(o.Name)
	if name == "" {
		label := prompt
		if label == "" {
			label = skills[0]
		}
		name = strings.TrimSpace(truncate(label, 50))
	}
	next, err := s.firstRun(sched, now)
	if err != nil {
		return nil, err
	}
	j := Job{
		ID: newJobID(), Name: name, Prompt: prompt, Skills: skills, Model: OptStr(o.Model), Provider: OptStr(o.Provider),
		ContextFrom: cleanList(o.ContextFrom), Schedule: sched, ScheduleDisplay: sched.Display, Repeat: Repeat{Times: times},
		Enabled: !o.Paused, State: StateScheduled, CreatedAt: Iso(now), Deliver: deliver, Origin: o.Origin,
		EnabledToolsets: o.EnabledToolsets, Workdir: OptStr(workdir),
	}
	if len(skills) > 0 {
		j.Skill = OptStr(skills[0])
	}
	if o.Paused {
		j.State, j.PausedAt = StatePaused, OptStr(Iso(now))
		j.PausedReason = OptStr(strings.TrimSpace(o.PausedReason))
		if j.PausedReason == "" {
			j.PausedReason = "Created paused; awaiting operator approval."
		}
	} else {
		j.NextRunAt = OptStr(next)
	}
	err = s.Edit(func(jobs []Job) ([]Job, bool, error) { return append(jobs, j), true, nil })
	if err != nil {
		return nil, err
	}
	return &j, nil
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// firstRun is the first next_run_at; a one-shot already past its grace is refused rather than silently never run.
func (s *Store) firstRun(sc Schedule, now time.Time) (string, error) {
	next, ok := s.NextRun(sc, "", now)
	if !ok {
		if sc.Kind == KindOnce {
			return "", bad("The schedule '%s' is in the past: a one-shot must be in the future.", sc.Display)
		}
		return "", bad("Could not compute a next run for '%s'.", sc.Display)
	}
	return next, nil
}

// NextRun computes the next run as an ISO string. Recurring kinds anchor on lastRun (else now); a one-shot is
// eligible only until its grace window ends, and never again once it has run.
func (s *Store) NextRun(sc Schedule, lastRun string, now time.Time) (string, bool) {
	switch sc.Kind {
	case KindOnce:
		if lastRun != "" {
			return "", false
		}
		t, ok := parseTime(OptStr(sc.RunAt))
		if !ok || now.Sub(t) > OneShotGrace {
			return "", false
		}
		return sc.RunAt, true
	case KindInterval:
		if sc.Minutes == nil {
			return "", false
		}
		base := now
		if t, ok := parseTime(OptStr(lastRun)); ok {
			base = t
		}
		return Iso(base.Add(time.Duration(*sc.Minutes) * time.Minute).In(base.Location())), true
	case KindCron:
		e, err := ParseCron(sc.Expr)
		if err != nil {
			return "", false
		}
		base := now
		if t, ok := parseTime(OptStr(lastRun)); ok {
			base = t
		}
		t, ok := e.Next(base, s.loc())
		if !ok {
			return "", false
		}
		return Iso(t), true
	}
	return "", false
}

// Grace is how late a recurring job may run before the backlog is collapsed: half its period, clamped to 2 minutes
// to 2 hours, so daily jobs catch up and frequent jobs fast-forward.
func (s *Store) Grace(sc Schedule) time.Duration {
	period := s.period(sc)
	if period <= 0 {
		return minGrace
	}
	return max(minGrace, min(period/2, maxGrace))
}

// period is the cadence of a schedule: the interval, or the gap between the next two cron fires.
func (s *Store) period(sc Schedule) time.Duration {
	switch sc.Kind {
	case KindInterval:
		if sc.Minutes != nil {
			return time.Duration(*sc.Minutes) * time.Minute
		}
	case KindCron:
		e, err := ParseCron(sc.Expr)
		if err != nil {
			return 0
		}
		a, ok := e.Next(s.now(), s.loc())
		if !ok {
			return 0
		}
		b, ok := e.Next(a, s.loc())
		if !ok {
			return 0
		}
		return b.Sub(a)
	}
	return 0
}

// UpdateOptions change a job; nil leaves a field alone.
type UpdateOptions struct {
	Prompt, Schedule, Name, Deliver, Model, Workdir *string
	Skills, ContextFrom, EnabledToolsets            *[]string
	Repeat                                          *int
}

// Update edits a job. A new schedule recomputes next_run_at.
func (s *Store) Update(id string, u UpdateOptions) (*Job, error) {
	var out Job
	err := s.with(id, func(j *Job) error {
		now := s.now()
		if u.Prompt != nil {
			if msg := ScanPrompt(*u.Prompt); msg != "" {
				return bad("%s", msg)
			}
			j.Prompt = strings.TrimSpace(*u.Prompt)
		}
		if u.Name != nil && strings.TrimSpace(*u.Name) != "" {
			j.Name = strings.TrimSpace(*u.Name)
		}
		if u.Deliver != nil && *u.Deliver != "" {
			j.Deliver = *u.Deliver
		}
		if u.Model != nil {
			j.Model = OptStr(strings.TrimSpace(*u.Model))
		}
		if u.Workdir != nil {
			w, err := validateWorkdir(*u.Workdir)
			if err != nil {
				return err
			}
			j.Workdir = OptStr(w)
		}
		if u.Skills != nil {
			j.Skills = cleanList(*u.Skills)
			j.Skill = ""
			if len(j.Skills) > 0 {
				j.Skill = OptStr(j.Skills[0])
			}
		}
		if u.ContextFrom != nil {
			j.ContextFrom = cleanList(*u.ContextFrom)
		}
		if u.EnabledToolsets != nil {
			j.EnabledToolsets = *u.EnabledToolsets
		}
		if u.Repeat != nil {
			if *u.Repeat > 0 {
				n := *u.Repeat
				j.Repeat.Times = &n
			} else {
				j.Repeat.Times = nil
			}
		}
		if u.Schedule != nil {
			sc, err := ParseSchedule(*u.Schedule, now)
			if err != nil {
				return &ValidationError{err.Error()}
			}
			if sc.Kind == KindInterval && (sc.Minutes == nil || *sc.Minutes < 1) {
				return bad("an interval must be at least 1 minute.")
			}
			next, err := s.firstRun(sc, now)
			if err != nil {
				return err
			}
			j.Schedule, j.ScheduleDisplay = sc, sc.Display
			if j.Enabled && j.State != StatePaused {
				j.NextRunAt = OptStr(next)
			}
			if sc.Kind == KindOnce && u.Repeat == nil {
				one := 1
				j.Repeat.Times = &one
			}
		}
		if strings.TrimSpace(j.Prompt) == "" && len(j.Skills) == 0 {
			return bad("A job needs a prompt, skills, or both.")
		}
		out = *j
		return nil
	})
	return &out, err
}

// Pause stops a job from firing on its own; an explicit run still works.
func (s *Store) Pause(id, reason string) error {
	return s.with(id, func(j *Job) error {
		j.Enabled, j.State, j.PausedAt, j.PausedReason = false, StatePaused, OptStr(Iso(s.now())), OptStr(reason)
		return nil
	})
}

// Resume re-arms a paused job from now. A completed job cannot be resumed.
func (s *Store) Resume(id string) (*Job, error) {
	var out Job
	err := s.with(id, func(j *Job) error {
		if j.State == StateCompleted {
			return bad("Job %s has completed and cannot be resumed; create a new one.", id)
		}
		next, err := s.firstRun(j.Schedule, s.now())
		if err != nil {
			return err
		}
		j.Enabled, j.State, j.PausedAt, j.PausedReason, j.NextRunAt = true, StateScheduled, "", "", OptStr(next)
		out = *j
		return nil
	})
	return &out, err
}

// Remove deletes a job.
func (s *Store) Remove(id string) error {
	found := false
	err := s.Edit(func(jobs []Job) ([]Job, bool, error) {
		var keep []Job
		for _, j := range jobs {
			if j.ID == id {
				found = true
				continue
			}
			keep = append(keep, j)
		}
		return keep, found, nil
	})
	if err == nil && !found {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return err
}
