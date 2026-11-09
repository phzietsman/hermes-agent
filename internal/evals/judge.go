package evals

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// JudgeModel is a versioned id, never an alias: `jev-latest` moves on release and would silently shift every score.
const JudgeModel = "jev-1.14.0"

const (
	// YesAt: P(yes) at or above this counts as yes. NoAt: at or below counts as no. In between is "uncertain" and
	// is scored as a miss, so noise never inflates a score.
	YesAt = 0.7
	NoAt  = 0.3
)

// Judge gives the probability (0..1) that each proposition holds for a reply. It is injected so tests never hit
// the network.
type Judge interface {
	StatesCurrentValue(ctx context.Context, question, expected, reply string) (float64, error)
	StatesStaleValue(ctx context.Context, question, stale, reply string) (float64, error)
	Abstains(ctx context.Context, reply string) (float64, error)
	FollowsPreference(ctx context.Context, preference, reply string) (float64, error)
}

// JevJudge asks Jev (TypeSafe System One) yes/no questions ("nouls").
type JevJudge struct {
	APIKey  string
	BaseURL string // default https://api.typesafe.ai
	HTTP    *http.Client
}

func (j *JevJudge) noul(ctx context.Context, state map[string]string, instructions string) (float64, error) {
	base := strings.TrimRight(j.BaseURL, "/")
	if base == "" {
		base = "https://api.typesafe.ai"
	}
	body, _ := json.Marshal(map[string]any{
		"state": state, "model": JudgeModel,
		"questions": map[string]any{"q": map[string]any{"type": "noul", "instructions": instructions}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/systemone", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+j.APIKey)
	req.Header.Set("Content-Type", "application/json")
	hc := j.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return 0, fmt.Errorf("judge HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Answers map[string]struct {
			Type string   `json:"type"`
			Noul *float64 `json:"noul"`
		} `json:"answers"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return 0, fmt.Errorf("judge returned unreadable JSON: %w", err)
	}
	a, ok := out.Answers["q"]
	if !ok || a.Noul == nil {
		return 0, fmt.Errorf("judge answer has no noul: %s", strings.TrimSpace(string(raw)))
	}
	return *a.Noul, nil
}

func (j *JevJudge) StatesCurrentValue(ctx context.Context, question, expected, reply string) (float64, error) {
	return j.noul(ctx, map[string]string{"question": question, "expected": expected, "reply": reply},
		"Does `reply` assert that the current answer to `question` is `expected`? "+
			"Mentioning `expected` only as an outdated or rejected value is a no.")
}

func (j *JevJudge) StatesStaleValue(ctx context.Context, question, stale, reply string) (float64, error) {
	return j.noul(ctx, map[string]string{"question": question, "stale": stale, "reply": reply},
		"Does `reply` assert that the current answer to `question` is `stale`? "+
			"Saying it used to be `stale` but changed is a no.")
}

func (j *JevJudge) Abstains(ctx context.Context, reply string) (float64, error) {
	return j.noul(ctx, map[string]string{"reply": reply},
		"Does `reply` say the assistant does not know, has no record, or cannot answer, "+
			"instead of giving a specific answer?")
}

func (j *JevJudge) FollowsPreference(ctx context.Context, preference, reply string) (float64, error) {
	return j.noul(ctx, map[string]string{"preference": preference, "reply": reply},
		"Does `reply` comply with the user's stated `preference`?")
}

// Verdict is yes or no when confident, and nil in the uncertain band.
func Verdict(p float64) *bool {
	switch {
	case p >= YesAt:
		t := true
		return &t
	case p <= NoAt:
		f := false
		return &f
	}
	return nil
}

func isYes(p float64) bool { v := Verdict(p); return v != nil && *v }

// Scorer scores replies. A nil reply (the agent said nothing) never passes a recall or preference.
type Scorer interface {
	Recalled(ctx context.Context, f Fact, reply *string) (bool, error)
	Followed(ctx context.Context, preference string, reply *string) (bool, error)
	Abstained(ctx context.Context, reply *string) (bool, error)
}

// JudgeScorer scores with a Judge: uncertain verdicts count as a miss.
type JudgeScorer struct{ J Judge }

func (s JudgeScorer) Recalled(ctx context.Context, f Fact, reply *string) (bool, error) {
	if reply == nil {
		return false, nil
	}
	p, err := s.J.StatesCurrentValue(ctx, f.Question, f.Answer, *reply)
	return isYes(p), err
}

func (s JudgeScorer) Followed(ctx context.Context, preference string, reply *string) (bool, error) {
	if reply == nil {
		return false, nil
	}
	p, err := s.J.FollowsPreference(ctx, preference, *reply)
	return isYes(p), err
}

func (s JudgeScorer) Abstained(ctx context.Context, reply *string) (bool, error) {
	if reply == nil {
		return true, nil
	}
	p, err := s.J.Abstains(ctx, *reply)
	return isYes(p), err
}
