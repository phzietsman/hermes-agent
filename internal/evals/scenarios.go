package evals

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
)

//go:embed data/scenarios.json
var scenarioData []byte

// Fact is one thing told to the agent and later asked about.
type Fact struct {
	Key       string `json:"key"`
	Statement string `json:"statement"`
	Question  string `json:"question"`
	Answer    string `json:"answer"`
}

var instances = func() map[string]json.RawMessage {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(scenarioData, &m); err != nil {
		panic("evals: data/scenarios.json is corrupt: " + err.Error())
	}
	return m
}()

// load decodes the exported instance for key into out. A missing key means that seed or size was not exported:
// rerun scripts/port/gen_eval_scenarios.py with it added.
func load(key string, out any) error {
	raw, ok := instances[key]
	if !ok {
		return fmt.Errorf("scenario instance %q was not exported (see scripts/port/gen_eval_scenarios.py)", key)
	}
	return json.Unmarshal(raw, out)
}

// Arm is the agent under test.
type Arm interface {
	// StartSession ends the current session (running the end-of-session lifecycle and waiting for background
	// reviews, so their writes are counted) and begins a fresh one on the next turn.
	StartSession(ctx context.Context) error
	Tell(ctx context.Context, key, statement string) error
	// Ask returns nil when the agent produced no reply.
	Ask(ctx context.Context, key, question string) (*string, error)
	// WritesLanded drains background work, then counts told facts that reached the store.
	WritesLanded(ctx context.Context) (int, error)
	WritesClaimed() int
	LandingDetail() map[string]int
}

// ProfileAdmitter is an arm whose core profile is context-wide: it serves only entries carrying this run's tags.
type ProfileAdmitter interface {
	AllowProfile(tags []string)
	ProfileEntriesServed() int
}

func ptr(s string) *string { return &s }

// RecallAndLanding is scenarios 1 and 6: tell n facts as asides (per_session per session), then ask each in a fresh
// session. Landing is judged from the arm's persisted store.
func RecallAndLanding(ctx context.Context, arm Arm, seed, n int, sc Scorer) ([]ScenarioResult, error) {
	var in struct {
		Facts      []Fact `json:"facts"`
		PerSession int    `json:"per_session"`
	}
	if err := load(fmt.Sprintf("recall/%d/%d", seed, n), &in); err != nil {
		return nil, err
	}
	for i, f := range in.Facts {
		if i > 0 && i%in.PerSession == 0 {
			if err := arm.StartSession(ctx); err != nil {
				return nil, err
			}
		}
		if err := arm.Tell(ctx, f.Key, f.Statement); err != nil {
			return nil, err
		}
	}
	if err := arm.StartSession(ctx); err != nil {
		return nil, err
	}
	passed := 0
	for _, f := range in.Facts {
		ok, err := askAndScore(ctx, arm, sc, f)
		if err != nil {
			return nil, err
		}
		if ok {
			passed++
		}
	}
	landed, err := arm.WritesLanded(ctx)
	if err != nil {
		return nil, err
	}
	return []ScenarioResult{
		result("1_recall", passed, n, seed, nil),
		result("6_write_landing", landed, arm.WritesClaimed(), seed, arm.LandingDetail()),
	}, nil
}

// askAndScore asks in a fresh session (no in-context leakage between answers) and scores the reply.
func askAndScore(ctx context.Context, arm Arm, sc Scorer, f Fact) (bool, error) {
	if err := arm.StartSession(ctx); err != nil {
		return false, err
	}
	reply, err := arm.Ask(ctx, f.Key, f.Question)
	if err != nil {
		return false, err
	}
	return sc.Recalled(ctx, f, reply)
}

// Supersession is scenario 2: facts are told, then corrected, and the corrected value must be what comes back.
func Supersession(ctx context.Context, arm Arm, seed, n int, sc Scorer) ([]ScenarioResult, error) {
	var in struct {
		Old     []Fact `json:"old"`
		Updates []Fact `json:"updates"`
	}
	if err := load(fmt.Sprintf("supersession/%d/%d", seed, n), &in); err != nil {
		return nil, err
	}
	for _, f := range in.Old {
		if err := arm.Tell(ctx, f.Key, f.Statement); err != nil {
			return nil, err
		}
	}
	if err := arm.StartSession(ctx); err != nil {
		return nil, err
	}
	for _, u := range in.Updates {
		if err := arm.Tell(ctx, u.Key, u.Statement); err != nil {
			return nil, err
		}
	}
	if err := arm.StartSession(ctx); err != nil {
		return nil, err
	}
	passed := 0
	for _, u := range in.Updates {
		ok, err := askAndScore(ctx, arm, sc, u)
		if err != nil {
			return nil, err
		}
		if ok {
			passed++
		}
	}
	return []ScenarioResult{result("2_supersession", passed, n, seed, nil)}, nil
}

// Abstention is scenario 3: facts never told must be answered with "I don't know", not a guess.
func Abstention(ctx context.Context, arm Arm, seed, n int, sc Scorer) ([]ScenarioResult, error) {
	var in struct {
		Facts []Fact `json:"facts"`
	}
	if err := load(fmt.Sprintf("abstention/%d/%d", seed, n), &in); err != nil {
		return nil, err
	}
	passed := 0
	for _, f := range in.Facts {
		if err := arm.StartSession(ctx); err != nil {
			return nil, err
		}
		reply, err := arm.Ask(ctx, f.Key, f.Question)
		if err != nil {
			return nil, err
		}
		ok, err := sc.Abstained(ctx, reply)
		if err != nil {
			return nil, err
		}
		if ok {
			passed++
		}
	}
	return []ScenarioResult{result("3_abstention", passed, n, seed, nil)}, nil
}

// Preference is scenario 4: a preference stated once must still be followed five sessions later, with no
// reminder. Each preference carries a run-unique codeword, so a follow can only come from memory.
func Preference(ctx context.Context, arm Arm, seed, n int, sc Scorer) ([]ScenarioResult, error) {
	var in struct {
		Items []struct {
			Key, Statement, Preference, Ask, Value string
		} `json:"items"`
		Filler []string `json:"filler"`
	}
	if err := load(fmt.Sprintf("preference/%d/%d", seed, n), &in); err != nil {
		return nil, err
	}
	if adm, ok := arm.(ProfileAdmitter); ok {
		tags := make([]string, len(in.Items))
		for i, it := range in.Items {
			tags[i] = it.Value
		}
		adm.AllowProfile(tags)
	}
	for _, it := range in.Items {
		if err := arm.Tell(ctx, it.Key, it.Statement); err != nil {
			return nil, err
		}
	}
	for _, task := range in.Filler { // five sessions of unrelated work, none restating the preference
		if err := arm.StartSession(ctx); err != nil {
			return nil, err
		}
		if _, err := arm.Ask(ctx, "filler", task); err != nil {
			return nil, err
		}
	}
	passed := 0
	for _, it := range in.Items {
		if err := arm.StartSession(ctx); err != nil {
			return nil, err
		}
		reply, err := arm.Ask(ctx, it.Key, it.Ask)
		if err != nil {
			return nil, err
		}
		ok, err := sc.Followed(ctx, it.Preference, reply)
		if err != nil {
			return nil, err
		}
		if ok {
			passed++
		}
	}
	detail := map[string]int{}
	if adm, ok := arm.(ProfileAdmitter); ok {
		detail["profile_entries_served"] = adm.ProfileEntriesServed()
	}
	return []ScenarioResult{result("4_preference_following", passed, len(in.Items), seed, detail)}, nil
}

var valueRe = regexp.MustCompile(`[a-z]+-\d{3}`)

// ToldValue is the unique value inside a told statement (a codeword like zephyr-417), else its last word.
func ToldValue(statement string) string {
	if m := valueRe.FindString(statement); m != "" {
		return m
	}
	s := statement
	for len(s) > 0 && (s[len(s)-1] == '.' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ' ' {
			return s[i+1:]
		}
	}
	return s
}
