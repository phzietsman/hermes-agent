package skills

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// write creates dir/rel with body, making parents.
func write(t *testing.T, dir, rel, body string) string {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func skillMD(name, desc string) string {
	return "---\nname: " + name + "\ndescription: " + desc + "\n---\nBody of " + name + "\n"
}

func rels(root string, paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i], _ = filepath.Rel(root, p)
	}
	return out
}

func TestIndexFilesPrunesExcludedAndSupportDirs(t *testing.T) {
	root := t.TempDir()
	write(t, root, "cat/real/SKILL.md", skillMD("real", "d"))
	write(t, root, "cat/real/references/nested/SKILL.md", skillMD("nested-in-refs", "d")) // support dir of a skill
	write(t, root, "cat/real/scripts/SKILL.md", skillMD("in-scripts", "d"))
	write(t, root, ".git/hooks/SKILL.md", skillMD("git", "d"))
	write(t, root, "node_modules/pkg/SKILL.md", skillMD("npm", "d"))
	write(t, root, ".archive/old/SKILL.md", skillMD("archived", "d"))
	// scripts/ is only a support dir when a SKILL.md sits directly beside it:
	write(t, root, "scripts/tool/SKILL.md", skillMD("category-named-scripts", "d"))
	write(t, root, "b/SKILL.md", skillMD("b", "d"))

	got := rels(root, IndexFiles(root, "SKILL.md"))
	want := []string{"b/SKILL.md", "cat/real/SKILL.md", "scripts/tool/SKILL.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestIndexFilesFollowsSymlinksOnceAndSurvivesCycles(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	write(t, outside, "linked/SKILL.md", skillMD("linked", "d"))
	write(t, root, "a/SKILL.md", skillMD("a", "d"))
	if err := os.Symlink(outside, filepath.Join(root, "ext")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(root, "a", "loop")); err != nil { // cycle back to the root
		t.Fatal(err)
	}
	got := rels(root, IndexFiles(root, "SKILL.md"))
	want := []string{"a/SKILL.md", "ext/linked/SKILL.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := IndexFiles(filepath.Join(root, "missing"), "SKILL.md"); got != nil {
		t.Fatalf("a missing root must yield nothing, got %v", got)
	}
}

func TestScanFieldsAndFallbacks(t *testing.T) {
	root := t.TempDir()
	write(t, root, "devops/deploy/SKILL.md", skillMD("deploy-it", "Ship to prod"))
	write(t, root, "no-name/SKILL.md", "---\ndescription: from dir\n---\nBody")
	write(t, root, "no-front/SKILL.md", "# Heading\n\nFirst prose line here.\nsecond\n")
	write(t, root, "long/SKILL.md", "---\nname: "+strings.Repeat("n", 100)+"\ndescription: "+strings.Repeat("d", 2000)+"\n---\n")
	write(t, root, "bom/SKILL.md", "\ufeff"+skillMD("bom-skill", "d"))
	write(t, root, "bad-utf8/SKILL.md", "---\nname: bad\n---\n\xff\xfe")

	byName := map[string]Skill{}
	for _, s := range Scan([]Root{{Tier: TierLocal, Dir: root}}, HostFilter()) {
		byName[s.Name] = s
	}
	if _, ok := byName["bad"]; ok {
		t.Error("a file that is not valid UTF-8 must be skipped")
	}

	d := byName["deploy-it"]
	if d.Description != "Ship to prod" || d.Category != "devops" || d.RelativePath != "devops/deploy" || d.Tier != TierLocal ||
		d.Path != filepath.Join(root, "devops/deploy/SKILL.md") || d.Dir != filepath.Join(root, "devops/deploy") || !d.Visible {
		t.Errorf("deploy: %+v", d)
	}
	if s := byName["no-name"]; s.Description != "from dir" || s.Category != "" {
		t.Errorf("name must default to the directory: %+v", byName)
	}
	if s := byName["no-front"]; s.Description != "First prose line here." {
		t.Errorf("description fallback = %q", s.Description)
	}
	long := byName[strings.Repeat("n", maxNameLength)]
	if long.Name == "" {
		t.Fatalf("name was not capped to %d", maxNameLength)
	}
	if n := len([]rune(long.Description)); n != maxDescriptionLength || !strings.HasSuffix(long.Description, "...") {
		t.Errorf("description length %d, suffix %q", n, long.Description[len(long.Description)-3:])
	}
	if _, ok := byName["bom-skill"]; !ok {
		t.Error("a BOM must not hide the frontmatter")
	}
}

func TestScanFrontmatterWindow(t *testing.T) {
	root := t.TempDir()
	// A frontmatter block that closes after the 4000-character window is not recognised.
	padding := strings.Repeat("# filler line to push the closing fence out\n", 120)
	write(t, root, "wide/SKILL.md", "---\nname: wide\n"+padding+"---\nBody")
	skills := Scan([]Root{{Dir: root}}, HostFilter())
	if len(skills) != 1 || skills[0].Name != "wide" { // the name falls back to the directory
		t.Fatalf("skills = %+v", skills)
	}
	if got := skills[0].Frontmatter; len(got) != 0 {
		t.Fatalf("frontmatter beyond the window must not parse: %v", got)
	}
}

func TestScanVisibleReflectsGates(t *testing.T) {
	root := t.TempDir()
	write(t, root, "mac/SKILL.md", "---\nname: mac-only\nplatforms: [macos]\n---\n")
	write(t, root, "any/SKILL.md", skillMD("any", "d"))
	f := HostFilterFor("linux")
	got := map[string]bool{}
	for _, s := range Scan([]Root{{Dir: root}}, f) {
		got[s.Name] = s.Visible
	}
	// Hidden skills are still scanned: an explicit load by name ignores the offer-time gates.
	if v, ok := got["mac-only"]; !ok || v {
		t.Errorf("mac-only visible=%v present=%v, want present and hidden", v, ok)
	}
	if !got["any"] {
		t.Error("any must be visible")
	}
}

func byPath(skills []Skill) map[string]Skill {
	m := map[string]Skill{}
	for _, s := range skills {
		m[filepath.ToSlash(strings.TrimPrefix(s.Path, s.Root+"/"))] = s
	}
	return m
}

func TestCatalogLocalShadowsExternal(t *testing.T) {
	local, ext := t.TempDir(), t.TempDir()
	write(t, local, "git/SKILL.md", skillMD("git", "local copy"))
	write(t, ext, "vcs/git/SKILL.md", skillMD("git", "external copy"))
	write(t, ext, "extra/SKILL.md", skillMD("extra", "only external"))

	cat := Scan([]Root{{TierLocal, local}, {TierExternal, ext}}, HostFilter())
	local1, external1, extra := byPath(cat)["git/SKILL.md"], byPath(cat)["vcs/git/SKILL.md"], byPath(cat)["extra/SKILL.md"]
	if local1.Status != StatusUnique || local1.LoadName != "git" {
		t.Errorf("local = %+v", local1)
	}
	if external1.Status != StatusShadowed || external1.LoadName != "" {
		t.Errorf("external copy must be shadowed: %+v", external1)
	}
	if extra.Status != StatusUnique || extra.LoadName != "extra" {
		t.Errorf("a name only in the external tier loads: %+v", extra)
	}
}

func TestCatalogSameTierDifferentContentIsAmbiguous(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a/helper/SKILL.md", skillMD("helper", "from a"))
	write(t, root, "b/helper/SKILL.md", skillMD("helper", "from b"))
	write(t, root, "solo/SKILL.md", skillMD("solo", "d"))
	cat := byPath(Scan([]Root{{Dir: root}}, HostFilter()))
	for _, rel := range []string{"a/helper/SKILL.md", "b/helper/SKILL.md"} {
		s := cat[rel]
		if s.Status != StatusAmbiguous {
			t.Errorf("%s status = %q", rel, s.Status)
		}
	}
	// An ambiguous skill still loads by its exact relative path, which is unique.
	if got := cat["a/helper/SKILL.md"].LoadName; got != "a/helper" {
		t.Errorf("load name = %q, want the relative path", got)
	}
	if cat["solo/SKILL.md"].Status != StatusUnique {
		t.Error("solo must be unique")
	}
}

func TestCatalogIdenticalCopiesPickTheShallowerOne(t *testing.T) {
	root := t.TempDir()
	body := skillMD("dup", "same bytes")
	write(t, root, "dup/SKILL.md", body)
	write(t, root, "deep/er/dup/SKILL.md", body)
	cat := byPath(Scan([]Root{{Dir: root}}, HostFilter()))
	if s := cat["dup/SKILL.md"]; s.Status != StatusUnique || s.LoadName != "dup" {
		t.Errorf("shallow copy = %+v", s)
	}
	if s := cat["deep/er/dup/SKILL.md"]; s.Status != StatusShadowed {
		t.Errorf("deep copy = %+v", s)
	}
}

func TestCatalogIdenticalCopiesAtTheSameDepthStayAmbiguous(t *testing.T) {
	// A symlink view of one skill is "provably the same" but has the same rank as the original, so the
	// tie-break cannot pick one: both are ambiguous and load by their relative path (as in Python).
	root := t.TempDir()
	real := write(t, root, "real/SKILL.md", skillMD("twin", "d"))
	if err := os.MkdirAll(filepath.Join(root, "alias"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(root, "alias", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	cat := byPath(Scan([]Root{{Dir: root}}, HostFilter()))
	for rel, want := range map[string]string{"real/SKILL.md": "real", "alias/SKILL.md": "alias"} {
		s := cat[rel]
		if s.Status != StatusAmbiguous || s.LoadName != want {
			t.Errorf("%s = %s / %q, want ambiguous / %q", rel, s.Status, s.LoadName, want)
		}
	}
	if !provablySame([]string{cat["real/SKILL.md"].Path, cat["alias/SKILL.md"].Path}) {
		t.Error("a symlink view must count as provably the same skill")
	}
}

func TestCatalogNameCollidesWithAnotherSkillsDirectory(t *testing.T) {
	// Skill "tools" in dir x/ must not make skill "x" (dir x/) ambiguous with a skill literally named x
	// elsewhere unless they share an alias; aliases are name, directory name and relative path.
	root := t.TempDir()
	write(t, root, "one/SKILL.md", skillMD("alpha", "d"))
	write(t, root, "two/SKILL.md", skillMD("beta", "d"))
	for _, s := range Scan([]Root{{Dir: root}}, HostFilter()) {
		if s.Status != StatusUnique {
			t.Errorf("%s = %s", s.Name, s.Status)
		}
	}
}
