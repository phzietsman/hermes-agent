//go:build live

package evals

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phzietsman/hermes-agent/internal/config"
)

// Live check of the Jev judge with the key in .secrets/creds.env. Run: go test -tags live -run Live ./internal/evals
func TestLiveJevJudgeSeparatesClearCases(t *testing.T) {
	root, _ := filepath.Abs("../..")
	env := config.LoadEnvFile(filepath.Join(root, ".secrets", "creds.env"))
	key := env["TYPESAFE_API_KEY"]
	if key == "" {
		if key = os.Getenv("TYPESAFE_API_KEY"); key == "" {
			t.Skip("no TYPESAFE_API_KEY")
		}
	}
	j := &JevJudge{APIKey: key}
	yes, err := j.StatesCurrentValue(ctx, "What is our staging cluster called?", "zephyr-417", "Your staging cluster is called zephyr-417.")
	if err != nil {
		t.Fatal(err)
	}
	no, err := j.StatesCurrentValue(ctx, "What is our staging cluster called?", "zephyr-417", "I don't have a record of that.")
	if err != nil {
		t.Fatal(err)
	}
	stale, _ := j.StatesCurrentValue(ctx, "What is our staging cluster called?", "zephyr-417", "It used to be zephyr-417 but it was renamed.")
	t.Logf("correct=%.2f missing=%.2f outdated=%.2f", yes, no, stale)
	if !isYes(yes) {
		t.Errorf("a plain correct answer must be a yes: %.2f", yes)
	}
	if isYes(no) {
		t.Errorf("an abstention must not count as recall: %.2f", no)
	}
	if abst, _ := j.Abstains(ctx, "I don't have a record of that."); !isYes(abst) {
		t.Errorf("an abstention must be recognised: %.2f", abst)
	}
}
