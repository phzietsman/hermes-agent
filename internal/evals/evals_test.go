package evals

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var ctx = context.Background()

// ---- judge ----

func TestJevJudgeSpeaksTheSystemOneWire(t *testing.T) {
	var got struct {
		Auth string
		Path string
		Body map[string]any
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Auth, got.Path = r.Header.Get("Authorization"), r.URL.Path
		json.NewDecoder(r.Body).Decode(&got.Body)
		io.WriteString(w, `{"model":"jev-1.14.0","usage":{},"answers":{"q":{"type":"noul","noul":0.83}}}`)
	}))
	defer srv.Close()
	j := &JevJudge{APIKey: "k-typesafe", BaseURL: srv.URL + "/"}
	p, err := j.StatesCurrentValue(ctx, "What is my editor?", "teal", "It is teal.")
	if err != nil || p != 0.83 {
		t.Fatalf("%v %v", p, err)
	}
	if got.Auth != "Bearer k-typesafe" || got.Path != "/v1/systemone" || got.Body["model"] != "jev-1.14.0" {
		t.Errorf("%+v", got)
	}
	state := got.Body["state"].(map[string]any)
	if state["question"] != "What is my editor?" || state["expected"] != "teal" || state["reply"] != "It is teal." {
		t.Errorf("state %v", state)
	}
	q := got.Body["questions"].(map[string]any)["q"].(map[string]any)
	if q["type"] != "noul" || !strings.Contains(q["instructions"].(string), "outdated or rejected value is a no") {
		t.Errorf("question %v", q)
	}
	for name, call := range map[string]func() (float64, error){
		"stale":      func() (float64, error) { return j.StatesStaleValue(ctx, "q", "old", "r") },
		"abstains":   func() (float64, error) { return j.Abstains(ctx, "r") },
		"preference": func() (float64, error) { return j.FollowsPreference(ctx, "p", "r") },
	} {
		if _, err := call(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestJevJudgeErrors(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"http error":  func(w http.ResponseWriter, r *http.Request) { http.Error(w, "nope", http.StatusUnauthorized) },
		"bad json":    func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "not json") },
		"no noul":     func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"answers":{"q":{"type":"choice"}}}`) },
		"no answer q": func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"answers":{}}`) },
	} {
		srv := httptest.NewServer(h)
		if _, err := (&JevJudge{APIKey: "k", BaseURL: srv.URL}).Abstains(ctx, "r"); err == nil {
			t.Errorf("%s: no error", name)
		}
		srv.Close()
	}
}

func TestVerdictBandsAndScorer(t *testing.T) {
	for p, want := range map[float64]string{1: "yes", 0.7: "yes", 0.69: "uncertain", 0.5: "uncertain", 0.31: "uncertain", 0.3: "no", 0: "no"} {
		v := Verdict(p)
		got := "uncertain"
		if v != nil && *v {
			got = "yes"
		} else if v != nil {
			got = "no"
		}
		if got != want {
			t.Errorf("Verdict(%v) = %s, want %s", p, got, want)
		}
	}
	sc := JudgeScorer{J: fixedJudge(0.5)}
	if ok, _ := sc.Recalled(ctx, Fact{}, ptr("x")); ok {
		t.Error("an uncertain verdict must score as a miss")
	}
	if ok, _ := sc.Abstained(ctx, ptr("x")); ok {
		t.Error("an uncertain abstention must score as a miss")
	}
	hi := JudgeScorer{J: fixedJudge(0.9)}
	if ok, _ := hi.Recalled(ctx, Fact{}, nil); ok {
		t.Error("no reply never recalls")
	}
	if ok, _ := hi.Followed(ctx, "p", nil); ok {
		t.Error("no reply never follows a preference")
	}
	if ok, _ := hi.Abstained(ctx, nil); !ok {
		t.Error("no reply is an abstention")
	}
}

type fixedJudge float64

func (f fixedJudge) StatesCurrentValue(context.Context, string, string, string) (float64, error) {
	return float64(f), nil
}
func (f fixedJudge) StatesStaleValue(context.Context, string, string, string) (float64, error) {
	return float64(f), nil
}
func (f fixedJudge) Abstains(context.Context, string) (float64, error) { return float64(f), nil }
func (f fixedJudge) FollowsPreference(context.Context, string, string) (float64, error) {
	return float64(f), nil
}

// ---- exported scenario data ----

func TestExportedInstancesAreConsistent(t *testing.T) {
	code := regexp.MustCompile(`[a-z]+-\d{3}`)
	for key, raw := range instances {
		kind, _, _ := strings.Cut(key, "/")
		switch kind {
		case "recall", "abstention":
			var in struct{ Facts []Fact }
			json.Unmarshal(raw, &in)
			seen := map[string]bool{}
			for _, f := range in.Facts {
				if f.Key == "" || f.Question == "" || seen[f.Key] {
					t.Fatalf("%s: bad or duplicate fact %+v", key, f)
				}
				seen[f.Key] = true
				if kind == "recall" && !strings.Contains(f.Statement, f.Answer) {
					t.Errorf("%s: statement lacks its answer: %+v", key, f)
				}
				if !code.MatchString(f.Answer) {
					t.Errorf("%s: value %q is guessable (not a codeword)", key, f.Answer)
				}
			}
		case "supersession":
			var in struct{ Old, Updates []Fact }
			json.Unmarshal(raw, &in)
			if len(in.Old) != len(in.Updates) {
				t.Fatalf("%s: %d old, %d updates", key, len(in.Old), len(in.Updates))
			}
			for i := range in.Old {
				if in.Old[i].Answer == in.Updates[i].Answer || !strings.Contains(in.Updates[i].Statement, in.Updates[i].Answer) || in.Old[i].Key != in.Updates[i].Key {
					t.Errorf("%s[%d]: %+v -> %+v", key, i, in.Old[i], in.Updates[i])
				}
			}
		case "preference":
			var in struct {
				Items  []struct{ Statement, Preference, Value string }
				Filler []string
			}
			json.Unmarshal(raw, &in)
			if len(in.Filler) != 5 {
				t.Errorf("%s: %d filler turns, the scenario needs five sessions", key, len(in.Filler))
			}
			for _, it := range in.Items {
				if !strings.Contains(it.Statement, it.Value) || !strings.Contains(it.Preference, it.Value) {
					t.Errorf("%s: %+v", key, it)
				}
			}
		case "procedural":
			var in struct {
				Weeks  []week
				Recipe string
			}
			json.Unmarshal(raw, &in)
			if len(in.Weeks) != 4 || !strings.Contains(in.Recipe, "{dir}") {
				t.Fatalf("%s: weeks=%d", key, len(in.Weeks))
			}
			for i, w := range in.Weeks {
				if got := rollup(w.Files); !jsonEqual(got, w.Expected) {
					t.Errorf("%s week %d: expected %s, recomputed %s", key, i, w.Expected, got)
				}
			}
		}
	}
	if len(instances) < 70 {
		t.Errorf("only %d instances exported", len(instances))
	}
}

func jsonEqual(a, b json.RawMessage) bool {
	var x, y any
	json.Unmarshal(a, &x)
	json.Unmarshal(b, &y)
	ja, _ := json.Marshal(x)
	jb, _ := json.Marshal(y)
	return string(ja) == string(jb)
}

// rollup recomputes the recipe's output from the week's files.
func rollup(files map[string]string) json.RawMessage {
	type entry struct {
		Name  string `json:"name"`
		Total int    `json:"total"`
	}
	var es []entry
	grand := 0
	for name, body := range files {
		store := strings.TrimSuffix(strings.TrimPrefix(name, "sales_"), ".txt")
		sum := 0
		for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
			_, q, _ := strings.Cut(line, ",")
			n := 0
			for _, c := range q {
				n = n*10 + int(c-'0')
			}
			sum += n
		}
		es = append(es, entry{store, sum})
		grand += sum
	}
	for i := range es {
		for j := i + 1; j < len(es); j++ {
			if es[j].Name < es[i].Name {
				es[i], es[j] = es[j], es[i]
			}
		}
	}
	b, _ := json.Marshal(map[string]any{"files": es, "grand_total": grand, "check": grand % 97})
	return b
}

func TestMissingInstanceNamesTheGenerator(t *testing.T) {
	var out any
	err := load("recall/999/8", &out)
	if err == nil || !strings.Contains(err.Error(), "gen_eval_scenarios.py") {
		t.Errorf("%v", err)
	}
}

func TestToldValue(t *testing.T) {
	for in, want := range map[string]string{
		"Side note: our staging cluster is called zephyr-417.":               "zephyr-417",
		"Our internal package registry lives at ember-102.example.internal.": "ember-102",
		"My laptop's asset tag is X9.":                                       "X9.", // no codeword: last word, trailing dot already trimmed below
	} {
		got := ToldValue(in)
		if want == "X9." {
			want = "X9"
		}
		if got != want {
			t.Errorf("ToldValue(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---- scenarios over a fake arm ----

type fakeArm struct {
	store    map[string]string // key -> latest told value
	sessions int
	forget   bool
	told     []string
	admitted []string
	tellKeys []string
	replyAll string // when set, every ask gets this reply
	noReply  bool
}

func newFake() *fakeArm { return &fakeArm{store: map[string]string{}} }

func (a *fakeArm) StartSession(context.Context) error { a.sessions++; return nil }
func (a *fakeArm) Tell(_ context.Context, key, st string) error {
	a.told = append(a.told, st)
	a.tellKeys = append(a.tellKeys, key)
	a.store[key] = ToldValue(st)
	return nil
}
func (a *fakeArm) Ask(_ context.Context, key, q string) (*string, error) {
	if a.noReply {
		return nil, nil
	}
	if a.replyAll != "" {
		return ptr(a.replyAll), nil
	}
	if a.forget {
		return ptr("I don't know."), nil
	}
	if v, ok := a.store[key]; ok {
		return ptr("It is " + v + "."), nil
	}
	return ptr("I don't know."), nil
}
func (a *fakeArm) WritesLanded(context.Context) (int, error) { return len(a.told), nil }
func (a *fakeArm) WritesClaimed() int                        { return len(a.told) }
func (a *fakeArm) LandingDetail() map[string]int             { return map[string]int{"landed": len(a.told)} }
func (a *fakeArm) AllowProfile(tags []string)                { a.admitted = tags }
func (a *fakeArm) ProfileEntriesServed() int                 { return 7 }

// substring scores like the Python SubstringScorer: exact tokens, valid for an arm that returns stored text.
type substr struct{}

func (substr) Recalled(_ context.Context, f Fact, r *string) (bool, error) {
	return r != nil && strings.Contains(*r, f.Answer), nil
}
func (substr) Followed(_ context.Context, p string, r *string) (bool, error) {
	return r != nil && strings.Contains(*r, p), nil
}
func (substr) Abstained(_ context.Context, r *string) (bool, error) {
	return r == nil || strings.Contains(*r, "don't know"), nil
}

func TestRecallAndLandingFlow(t *testing.T) {
	a := newFake()
	res, err := RecallAndLanding(ctx, a, 1, 8, substr{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || res[0].Scenario != "1_recall" || res[0].Passed != 8 || res[0].Total != 8 || res[1].Scenario != "6_write_landing" || res[1].Total != 8 || res[1].Seed != 1 {
		t.Fatalf("%+v", res)
	}
	// 8 facts, two per session: 3 session breaks while telling, 1 before asking, 1 per question, 1 to drain.
	if a.sessions != 3+1+8 {
		t.Errorf("sessions started: %d", a.sessions)
	}
	// An amnesiac arm scores zero: the scorer is not generous.
	b := newFake()
	b.forget = true
	if res, _ := RecallAndLanding(ctx, b, 1, 8, substr{}); res[0].Passed != 0 {
		t.Errorf("amnesia scored %d", res[0].Passed)
	}
	if _, err := RecallAndLanding(ctx, newFake(), 1, 5, substr{}); err == nil {
		t.Error("an unexported size must be an error, not a silent empty run")
	}
}

func TestSupersessionScoresTheCorrectedValue(t *testing.T) {
	a := newFake()
	res, err := Supersession(ctx, a, 1, 4, substr{})
	if err != nil || res[0].Scenario != "2_supersession" || res[0].Passed != 4 || res[0].Total != 4 {
		t.Fatalf("%+v %v", res, err)
	}
	// An arm stuck on the first value fails every one.
	stuck := &stuckArm{fakeArm: newFake()}
	if res, _ := Supersession(ctx, stuck, 1, 4, substr{}); res[0].Passed != 0 {
		t.Errorf("a stale arm scored %d", res[0].Passed)
	}
}

type stuckArm struct{ *fakeArm }

func (a *stuckArm) Tell(_ context.Context, key, st string) error {
	if _, seen := a.store[key]; !seen {
		a.store[key] = ToldValue(st)
	}
	return nil
}

func TestAbstentionRewardsNotKnowing(t *testing.T) {
	res, err := Abstention(ctx, newFake(), 1, 4, substr{})
	if err != nil || res[0].Passed != 4 {
		t.Fatalf("%+v %v", res, err)
	}
	guesser := newFake()
	guesser.replyAll = "It is probably teal-123."
	if res, _ := Abstention(ctx, guesser, 1, 4, substr{}); res[0].Passed != 0 {
		t.Errorf("a guesser scored %d", res[0].Passed)
	}
	silent := newFake()
	silent.noReply = true
	if res, _ := Abstention(ctx, silent, 1, 4, substr{}); res[0].Passed != 4 {
		t.Errorf("silence is an abstention: %d", res[0].Passed)
	}
}

func TestPreferenceAdmitsOnlyThisRunsProfileAndSpansFiveSessions(t *testing.T) {
	a := newFake()
	a.replyAll = "" // replies below are built per preference
	res, err := Preference(ctx, a, 1, 3, followAll{a})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.admitted) != 3 || !regexp.MustCompile(`^[a-z]+-\d{3}$`).MatchString(a.admitted[0]) {
		t.Errorf("admitted tags %v", a.admitted)
	}
	if res[0].Scenario != "4_preference_following" || res[0].Total != 3 || res[0].Detail["profile_entries_served"] != 7 {
		t.Errorf("%+v", res[0])
	}
	// told once each, then 5 filler sessions, then one session per preference.
	if len(a.told) != 3 || a.sessions != 5+3 {
		t.Errorf("told %d, sessions %d", len(a.told), a.sessions)
	}
}

type followAll struct{ a *fakeArm }

func (followAll) Recalled(context.Context, Fact, *string) (bool, error)   { return true, nil }
func (followAll) Followed(context.Context, string, *string) (bool, error) { return true, nil }
func (followAll) Abstained(context.Context, *string) (bool, error)        { return true, nil }

// ---- procedural ----

// recipeArm carries out the recipe correctly when it is told it in full and only remembers it afterwards.
type recipeArm struct {
	learned  bool
	sessions int
	skills   int
	lazy     bool
}

func (a *recipeArm) StartSession(context.Context) error { a.sessions++; return nil }
func (a *recipeArm) SkillsCreated() int                 { return a.skills }
func (a *recipeArm) RunTask(_ context.Context, text string) (TaskMetrics, error) {
	m := regexp.MustCompile(`(/\S*week\d)`).FindString(text)
	if strings.Contains(text, "sales_<store>.txt") {
		a.learned = true
		a.skills = 1
	}
	if a.learned && !a.lazy && m != "" {
		files := map[string]string{}
		entries, _ := os.ReadDir(m)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "sales_") {
				b, _ := os.ReadFile(filepath.Join(m, e.Name()))
				files[e.Name()] = string(b)
			}
		}
		os.WriteFile(filepath.Join(m, "rollup.json"), rollup(files), 0o644)
	}
	return TaskMetrics{"calls": 2, "errors": 0, "tokens": 100}, nil
}

func TestProceduralScoresFileCorrectnessAndSumsCosts(t *testing.T) {
	arm := &recipeArm{}
	res, err := Procedural(ctx, arm, t.TempDir(), 1)
	if err != nil {
		t.Fatal(err)
	}
	r := res[0]
	if r.Scenario != "5_procedural" || r.Passed != 3 || r.Total != 3 || r.Detail["run1_correct"] != 1 || r.Detail["skills_created"] != 1 ||
		r.Detail["reuse_calls"] != 6 || r.Detail["reuse_tokens"] != 300 || r.Detail["run1_calls"] != 2 {
		t.Errorf("%+v", r)
	}
	if _, ok := r.Detail["reuse_ms"]; !ok {
		t.Error("wall time per reuse run must be reported")
	}
	lazy, _ := Procedural(ctx, &recipeArm{lazy: true}, t.TempDir(), 1)
	if lazy[0].Passed != 0 || lazy[0].Detail["run1_correct"] != 0 {
		t.Errorf("an arm that writes nothing must fail: %+v", lazy[0])
	}
}

func TestCorrectIsAStructuralComparison(t *testing.T) {
	dir := t.TempDir()
	want := json.RawMessage(`{"a":1,"b":[1,2]}`)
	if Correct(dir, want) {
		t.Error("a missing file is not correct")
	}
	os.WriteFile(filepath.Join(dir, "rollup.json"), []byte("{\n  \"b\": [1, 2],\n  \"a\": 1\n}"), 0o644)
	if !Correct(dir, want) {
		t.Error("key order and whitespace must not matter")
	}
	os.WriteFile(filepath.Join(dir, "rollup.json"), []byte(`{"a":2,"b":[1,2]}`), 0o644)
	if Correct(dir, want) {
		t.Error("a different value is not correct")
	}
	os.WriteFile(filepath.Join(dir, "rollup.json"), []byte(`nope`), 0o644)
	if Correct(dir, want) {
		t.Error("garbage is not correct")
	}
}

// ---- report ----

func TestSummarizeAndReportShapeMatchPython(t *testing.T) {
	rs := []ScenarioResult{result("1_recall", 9, 10, 2, nil), result("1_recall", 8, 10, 1, nil), result("6_write_landing", 10, 10, 1, map[string]int{"landed": 10})}
	lines := Summarize(rs)
	if len(lines) != 2 || !strings.Contains(lines[0], "1_recall") || !strings.Contains(lines[0], "mean 85% (min 80%, max 90%)  [8/10 9/10]") {
		t.Errorf("%v", lines)
	}
	path := filepath.Join(t.TempDir(), "r.json")
	if err := WriteReport(path, "loopfix_sam", []int{1, 2}, rs); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var back map[string]any
	json.Unmarshal(raw, &back)
	if back["arm"] != "loopfix_sam" || back["seed"] != float64(1) || back["harness_only"] != false {
		t.Errorf("%v", back)
	}
	first := back["results"].([]any)[0].(map[string]any)
	for _, k := range []string{"scenario", "passed", "total", "requires_live", "seed", "detail"} {
		if _, ok := first[k]; !ok {
			t.Errorf("result lacks %q", k)
		}
	}
	if _, err := ParseSizes("1,2"); err == nil {
		t.Error("bad sizes")
	}
	if z, err := ParseSizes("8,4,4,3"); err != nil || z != DefaultSizes {
		t.Errorf("%v %v", z, err)
	}
}

func TestStatsMatchesPython(t *testing.T) {
	// Python: sorted, p95 = ordered[min(n-1, round(0.95*(n-1)))], median of the sorted list.
	s := Stats([]float64{5, 1, 3, 2, 4})
	if s.N != 5 || s.P50 != 3 || s.P95 != 5 || s.Max != 5 {
		t.Errorf("%+v", s)
	}
	s = Stats([]float64{10, 20, 30, 40})
	if s.P50 != 25 {
		t.Errorf("even-length median: %+v", s)
	}
	twenty := make([]float64, 20)
	for i := range twenty {
		twenty[i] = float64(i + 1)
	}
	if s := Stats(twenty); s.P95 != 19 { // round(0.95*19)=18 -> 19th value
		t.Errorf("%+v", s)
	}
	if (Stats(nil) != LatencyStats{}) {
		t.Error("empty")
	}
	_ = time.Second
}
