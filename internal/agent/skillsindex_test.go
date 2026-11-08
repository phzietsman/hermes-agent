package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phzietsman/hermes-agent/internal/config"
	"github.com/phzietsman/hermes-agent/internal/skills"
)

type skillsCase struct {
	Name     string            `json:"name"`
	Files    map[string]string `json:"files"`
	Cfg      map[string]any    `json:"cfg"`
	Tools    *[]string         `json:"tools"`
	Toolsets *[]string         `json:"toolsets"`
	Platform string            `json:"platform"`
	Compact  *[]string         `json:"compact"`
	Repo     bool              `json:"repo"`
	Expected string            `json:"expected"`
}

func ptrSet(p *[]string) map[string]bool {
	if p == nil {
		return nil
	}
	m := map[string]bool{}
	for _, s := range *p {
		m[s] = true
	}
	return m
}

func repoRoot(t testing.TB) string {
	t.Helper()
	wd, _ := os.Getwd()
	return filepath.Join(wd, "..", "..")
}

// Differential: BuildSkillsIndex reproduces build_skills_system_prompt byte for byte.
func TestBuildSkillsIndexMatchesPython(t *testing.T) {
	b, err := os.ReadFile("testdata/skills_index.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []skillsCase
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			base, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			for rel, content := range c.Files {
				p := filepath.Join(base, rel)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			home := filepath.Join(base, "home")
			if err := os.MkdirAll(home, 0o755); err != nil {
				t.Fatal(err)
			}
			cfgJSON, _ := json.Marshal(c.Cfg)
			cfgJSON = []byte(strings.ReplaceAll(string(cfgJSON), "{BASE}", base))
			var cfgMap map[string]any
			_ = json.Unmarshal(cfgJSON, &cfgMap)
			if c.Repo {
				if err := os.Symlink(filepath.Join(repoRoot(t), "skills"), filepath.Join(home, "skills")); err != nil {
					t.Fatal(err)
				}
				cfgMap = map[string]any{"skills": map[string]any{"external_dirs": []any{filepath.Join(repoRoot(t), "optional-skills")}}}
			}
			y, _ := json.Marshal(cfgMap) // JSON is valid YAML
			if err := os.WriteFile(filepath.Join(home, "config.yaml"), y, 0o644); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(home, nil)
			if err != nil {
				t.Fatal(err)
			}
			f := skills.HostFilter()
			got := BuildSkillsIndex(SkillsIndexOptions{
				Settings: skills.LoadSettings(home, cfg, c.Platform), Filter: f,
				Tools: ptrSet(c.Tools), Toolsets: ptrSet(c.Toolsets), Platform: c.Platform, CompactCategories: ptrSet(c.Compact),
			})
			if got = strings.ReplaceAll(got, base, "<BASE>"); got != c.Expected {
				t.Fatalf("mismatch\n got: %q\nwant: %q", clip(got), clip(c.Expected))
			}
		})
	}
}
