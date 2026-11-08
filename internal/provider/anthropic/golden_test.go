package anthropic

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phzietsman/hermes-agent/internal/llm"
)

// The fixtures in testdata/ are produced by scripts/port/gen_anthropic_golden.py, which runs the
// Python converter over each scenario: the Go port is compared with what Python actually emitted.

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

// normalize round-trips through JSON so struct and map shapes compare as plain data.
func normalize(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func pretty(v any) string {
	b, _ := json.MarshalIndent(v, "", " ")
	return string(b)
}

func TestConvertMessagesMatchesPython(t *testing.T) {
	files, _ := filepath.Glob("testdata/convert_*.json")
	if len(files) < 30 {
		t.Fatalf("only %d convert fixtures found", len(files))
	}
	for _, f := range files {
		name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "convert_"), ".json")
		t.Run(name, func(t *testing.T) {
			var fx struct {
				Model            string          `json:"model"`
				Messages         []llm.Message   `json:"messages"`
				ExpectedSystem   any             `json:"expected_system"`
				ExpectedMessages json.RawMessage `json:"expected_messages"`
			}
			readJSON(t, f, &fx)
			system, got := ConvertMessages(fx.Messages, fx.Model)

			var want any
			if err := json.Unmarshal(fx.ExpectedMessages, &want); err != nil {
				t.Fatal(err)
			}
			if g := normalize(t, got); !reflect.DeepEqual(g, want) {
				t.Errorf("messages differ\n--- python\n%s\n--- go\n%s", pretty(want), pretty(g))
			}
			if g := normalize(t, system); !reflect.DeepEqual(g, fx.ExpectedSystem) {
				t.Errorf("system differs\n--- python\n%s\n--- go\n%s", pretty(fx.ExpectedSystem), pretty(g))
			}
		})
	}
}

func TestConvertToolsMatchesPython(t *testing.T) {
	files, _ := filepath.Glob("testdata/tools_*.json")
	if len(files) == 0 {
		t.Fatal("no tools fixtures")
	}
	for _, f := range files {
		t.Run(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "tools_"), ".json"), func(t *testing.T) {
			var fx struct {
				Tools    []llm.ToolDef `json:"tools"`
				Expected any           `json:"expected"`
			}
			readJSON(t, f, &fx)
			got := normalize(t, ConvertTools(fx.Tools))
			if !reflect.DeepEqual(got, fx.Expected) {
				t.Errorf("tools differ\n--- python\n%s\n--- go\n%s", pretty(fx.Expected), pretty(got))
			}
		})
	}
}

func TestModelHelpersMatchPython(t *testing.T) {
	var fx struct {
		Normalize []struct{ In, Out string } `json:"normalize"`
		Preserves []struct {
			Model string `json:"model"`
			Want  bool   `json:"want"`
		} `json:"preserves_prior_thinking"`
	}
	readJSON(t, "testdata/models.json", &fx)
	if len(fx.Normalize) == 0 || len(fx.Preserves) == 0 {
		t.Fatal("empty model fixtures")
	}
	for _, c := range fx.Normalize {
		if got := NormalizeModelName(c.In); got != c.Out {
			t.Errorf("NormalizeModelName(%q) = %q, python %q", c.In, got, c.Out)
		}
	}
	for _, c := range fx.Preserves {
		if got := ModelPreservesPriorThinking(c.Model); got != c.Want {
			t.Errorf("ModelPreservesPriorThinking(%q) = %v, python %v", c.Model, got, c.Want)
		}
	}
}
