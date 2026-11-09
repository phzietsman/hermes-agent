package skills

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	maxNameLength        = 64
	maxDescriptionLength = 1024
	// frontmatterWindow bounds how much of a SKILL.md is parsed while cataloguing; a frontmatter block
	// longer than this is not recognised, as in the Python scanner.
	frontmatterWindow = 4000
)

// Tier orders skill roots: a lower tier shadows a higher one carrying the same name.
type Tier int

const (
	// TierLocal is the profile's own <home>/skills directory.
	TierLocal Tier = iota
	// TierExternal is a directory from skills.external_dirs.
	TierExternal
)

// Root is one directory scanned for skills.
type Root struct {
	Tier Tier
	Dir  string
}

// Status says how a skill's name resolves.
type Status string

const (
	// StatusUnique: the name resolves to exactly this skill.
	StatusUnique Status = "unique"
	// StatusAmbiguous: another skill in the same tier shares the name; only the exact relative path loads.
	StatusAmbiguous Status = "ambiguous"
	// StatusShadowed: a higher-precedence copy owns the name; this one is hidden.
	StatusShadowed Status = "shadowed"
)

// Skill is a scanned SKILL.md.
type Skill struct {
	Name         string
	Description  string
	Category     string // first path component under the root when the skill is nested two levels deep
	Tier         Tier
	Root         string
	Path         string // the SKILL.md
	Dir          string
	RelativePath string // Dir relative to Root, slash-separated
	Frontmatter  map[string]any
	// Visible is false when the platform, environment or apps gate hides the skill from offer surfaces.
	Visible  bool
	Status   Status
	LoadName string // the name that loads this skill, "" when none does
}

// excludedDirs are never scanned: VCS, dependency and cache directories.
var excludedDirs = map[string]bool{
	".git": true, ".github": true, ".hub": true, ".archive": true, ".curator_backups": true, ".locks": true,
	".venv": true, "venv": true, "node_modules": true, "site-packages": true, "__pycache__": true,
	".tox": true, ".nox": true, ".pytest_cache": true, ".mypy_cache": true, ".ruff_cache": true,
}

// supportDirs sit inside a skill package and are loaded explicitly, never scanned as skills.
var supportDirs = map[string]bool{"references": true, "templates": true, "assets": true, "scripts": true}

// IndexFiles lists every file named name under dir, sorted, following symlinks (each directory is
// visited once) and pruning excluded directories and the support directories of a skill root.
func IndexFiles(dir, name string) []string {
	var out []string
	seen := map[string]bool{}
	var walk func(d string)
	walk = func(d string) {
		real, err := filepath.EvalSymlinks(d)
		if err != nil || seen[real] {
			return
		}
		seen[real] = true
		entries, err := os.ReadDir(d)
		if err != nil {
			return
		}
		hasSkill := false
		for _, e := range entries {
			if !e.IsDir() && e.Name() == "SKILL.md" {
				hasSkill = true
			}
		}
		for _, e := range entries {
			p := filepath.Join(d, e.Name())
			isDir := e.IsDir()
			if !isDir && e.Type()&fs.ModeSymlink != 0 {
				if fi, err := os.Stat(p); err == nil && fi.IsDir() {
					isDir = true
				}
			}
			if isDir {
				if excludedDirs[e.Name()] || (hasSkill && supportDirs[e.Name()]) {
					continue
				}
				walk(p)
			} else if e.Name() == name {
				out = append(out, p)
			}
		}
	}
	walk(dir)
	sort.Strings(out)
	return out
}

// readSkill reads a SKILL.md, tolerating a BOM and bytes that are not valid UTF-8.
func readSkill(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(b) {
		return "", fmt.Errorf("%s is not valid UTF-8", path)
	}
	return strings.TrimPrefix(string(b), "\ufeff"), nil
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func toString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}

// Scan reads every SKILL.md under the roots and resolves how each name loads. Files that cannot be
// read or are not UTF-8 are skipped. It does not apply the disabled list: skill_view-style lookups
// need hidden and disabled skills too.
func Scan(roots []Root, f Filter) []Skill {
	var skills []Skill
	for _, r := range roots {
		for _, path := range IndexFiles(r.Dir, "SKILL.md") {
			text, err := readSkill(path)
			if err != nil {
				continue
			}
			fm, body := ParseFrontmatter(truncateRunes(text, frontmatterWindow))
			desc := toString(fm["description"])
			if desc == "" {
				desc = firstProseLine(body)
			}
			dir := filepath.Dir(path)
			rel, err := filepath.Rel(r.Dir, dir)
			if err != nil {
				continue
			}
			name := filepath.Base(dir)
			if n, ok := fm["name"]; ok && toString(n) != "" {
				name = toString(n)
			}
			s := Skill{
				Name:         truncateRunes(name, maxNameLength),
				Description:  truncateDescription(desc),
				Category:     category(path, r.Dir),
				Tier:         r.Tier,
				Root:         r.Dir,
				Path:         path,
				Dir:          dir,
				RelativePath: filepath.ToSlash(rel),
				Frontmatter:  fm,
				Visible:      f.Visible(fm),
			}
			skills = append(skills, s)
		}
	}
	return resolveCatalog(skills)
}

func firstProseLine(body string) string {
	for _, ln := range strings.Split(strings.TrimSpace(body), "\n") {
		if ln = strings.TrimSpace(ln); ln != "" && !strings.HasPrefix(ln, "#") {
			return ln
		}
	}
	return ""
}

func truncateDescription(d string) string {
	d = strings.TrimSpace(d)
	if utf8.RuneCountInString(d) > maxDescriptionLength {
		return truncateRunes(d, maxDescriptionLength-3) + "..."
	}
	return d
}

// category is the first path component of <root>/<category>/<skill>/SKILL.md.
func category(skillPath, root string) string {
	rel, err := filepath.Rel(root, skillPath)
	if err != nil {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) >= 3 {
		return parts[0]
	}
	return ""
}

// provablySame is true only when every path is the same skill: one resolved file (symlink views)
// or byte-identical content (copies). Anything else is two different skills sharing a name, and
// picking one by depth would let <root>/evil (name: github) shadow the real one.
func provablySame(paths []string) bool {
	reals := map[string]bool{}
	hashes := map[[32]byte]bool{}
	for _, p := range paths {
		r, err := filepath.EvalSymlinks(p)
		if err != nil {
			return false
		}
		reals[r] = true
		b, err := os.ReadFile(p)
		if err != nil {
			return false
		}
		hashes[sha256.Sum256(b)] = true
	}
	return len(reals) == 1 || len(hashes) == 1
}

// candidateRank orders identical copies under one root: a real SKILL.md beats a legacy flat
// <name>.md, then the shallower path wins.
func candidateRank(path, root string) [2]int {
	rel, _ := filepath.Rel(root, path)
	notSkillMD := 0
	if filepath.Base(path) != "SKILL.md" {
		notSkillMD = 1
	}
	return [2]int{notSkillMD, len(strings.Split(filepath.ToSlash(rel), "/"))}
}

// pickCandidate returns the winning index among one alias's candidates, or -1 when ambiguous. The
// lowest tier wins; within it a lone candidate wins, identical copies under one root resolve to
// the strictly best rank, and anything else is ambiguous.
func pickCandidate(skills []Skill, idxs []int) int {
	top := skills[idxs[0]].Tier
	for _, i := range idxs {
		if skills[i].Tier < top {
			top = skills[i].Tier
		}
	}
	var contenders []int
	for _, i := range idxs {
		if skills[i].Tier == top {
			contenders = append(contenders, i)
		}
	}
	if len(contenders) == 1 {
		return contenders[0]
	}
	sameRoot := true
	var paths []string
	for _, i := range contenders {
		if skills[i].Root != skills[contenders[0]].Root {
			sameRoot = false
		}
		paths = append(paths, skills[i].Path)
	}
	if sameRoot && provablySame(paths) {
		sort.SliceStable(contenders, func(a, b int) bool {
			ra, rb := candidateRank(skills[contenders[a]].Path, skills[contenders[a]].Root), candidateRank(skills[contenders[b]].Path, skills[contenders[b]].Root)
			return ra[0] < rb[0] || (ra[0] == rb[0] && ra[1] < rb[1])
		})
		ra := candidateRank(skills[contenders[0]].Path, skills[contenders[0]].Root)
		rb := candidateRank(skills[contenders[1]].Path, skills[contenders[1]].Root)
		if ra != rb {
			return contenders[0]
		}
	}
	return -1
}

// resolveCatalog annotates each skill with what a lookup by name resolves to, using its aliases
// (declared name, directory name, path relative to the root): unique, ambiguous or shadowed.
func resolveCatalog(skills []Skill) []Skill {
	owners := map[string][]int{}
	for i, s := range skills {
		for _, alias := range uniqueStrings(s.Name, filepath.Base(s.Dir), s.RelativePath) {
			owners[alias] = append(owners[alias], i)
		}
	}
	winner := map[string]int{}
	for alias, idxs := range owners {
		winner[alias] = pickCandidate(skills, idxs)
	}
	for i := range skills {
		s := &skills[i]
		higher := false
		for _, j := range owners[s.Name] {
			if skills[j].Tier < s.Tier {
				higher = true
			}
		}
		switch w := winner[s.Name]; {
		case higher || (w != -1 && w != i):
			s.Status, s.LoadName = StatusShadowed, ""
		case w == i:
			s.Status, s.LoadName = StatusUnique, s.Name
		default:
			s.Status = StatusAmbiguous
			if winner[s.RelativePath] == i {
				s.LoadName = s.RelativePath
			}
		}
	}
	return skills
}

func uniqueStrings(vs ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range vs {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
