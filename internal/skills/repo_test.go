package skills

import (
	"path/filepath"
	"testing"
)

// The repository's skills are data and must load unchanged.
func TestRepoSkillsLoadWithoutError(t *testing.T) {
	for _, dir := range []string{"skills", "optional-skills"} {
		root := filepath.Join("..", "..", dir)
		files := IndexFiles(root, "SKILL.md")
		if len(files) == 0 {
			t.Skipf("%s not present", root)
		}
		scanned := Scan([]Root{{Tier: TierLocal, Dir: root}}, HostFilter())
		if len(scanned) != len(files) {
			t.Fatalf("%s: %d SKILL.md files but %d scanned (unreadable or non-UTF-8 files are skipped)", dir, len(files), len(scanned))
		}
		for _, s := range scanned {
			if s.Name == "" || s.Status == "" {
				t.Errorf("%s: incomplete entry %+v", s.Path, s)
			}
			if len(s.Frontmatter) == 0 {
				t.Errorf("%s: no frontmatter parsed", s.Path)
			}
		}
		t.Logf("%s: %d skills", dir, len(scanned))
	}
}
