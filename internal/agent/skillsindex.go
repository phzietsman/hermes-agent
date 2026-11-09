package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/phzietsman/hermes-agent/internal/skills"
)

// promptDescLimit caps a skill description in the index (SKILL_PROMPT_DESC_LIMIT).
const promptDescLimit = 60

// SkillsIndexOptions configures the <available_skills> block.
type SkillsIndexOptions struct {
	// Settings carries the skill roots and the disabled names (skills.LoadSettings).
	Settings skills.Settings
	// Filter gates skills by host OS, environment and apps (skills.HostFilter()).
	Filter skills.Filter
	// Tools and Toolsets are the names this session offers. nil means unknown: nothing is filtered by
	// conditions. An empty non-nil set means "none available".
	Tools    map[string]bool
	Toolsets map[string]bool
	// Platform is the gateway session platform ("" for the CLI); it gates session_platforms skills.
	Platform string
	// CompactCategories lists top-level categories rendered as a names-only line.
	CompactCategories map[string]bool
}

const skillsIntro = "## Skills\n" +
	"Before replying, scan the skills below. If a skill matches or is even partially relevant to your " +
	"task, you MUST load it with skill_view(name) and follow its instructions. Err on the side of " +
	"loading — it is always better to have context you don't need than to miss critical steps, pitfalls, " +
	"or established workflows. Skills contain specialized knowledge — API endpoints, tool-specific " +
	"commands, and proven workflows that outperform general-purpose approaches. Load the skill " +
	"even if you think you could handle the task with basic tools like %s. " +
	"Skills also encode the user's preferred approach, conventions, and quality standards for tasks like " +
	"code review, planning, and testing — load them even for tasks you already know how to do, because " +
	"the skill defines how it should be done here.\n" +
	"If a skill has issues, fix it with skill_manage(action='patch').\n" +
	"After difficult/iterative tasks, offer to save as a skill. If a skill you loaded was missing steps, " +
	"had wrong commands, or needed pitfalls you discovered, update it before finishing.\n" +
	"\n" +
	"<available_skills>\n"

// indexCategory reproduces Python's category label for an index row: a skill at the top of the root is
// filed under its own directory name, and deeper skills under every directory above the skill's own.
func indexCategory(relDir string) string {
	if relDir == "." || relDir == "" {
		return "general"
	}
	parts := strings.Split(relDir, "/")
	if len(parts) == 1 {
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], "/")
}

// stripQuotes trims whitespace, then surrounding quote characters (str.strip().strip("'\"")).
func stripQuotes(s string) string { return strings.Trim(pyStrip(s), "'\"") }

func promptDescription(fm map[string]any) string {
	raw, ok := fm["description"]
	if !ok || raw == nil || raw == "" {
		return ""
	}
	d := stripQuotes(fmt.Sprint(raw))
	if utf8.RuneCountInString(d) > promptDescLimit {
		return string([]rune(d)[:promptDescLimit-3]) + "..."
	}
	return d
}

func stringSet(xs []string) []string {
	var out []string
	for _, x := range xs {
		if x = strings.ToLower(strings.TrimSpace(x)); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// shouldShow applies a skill's conditional-activation rules (_skill_should_show).
func (o SkillsIndexOptions) shouldShow(c skills.Conditions) bool {
	if want := stringSet(c.SessionPlatforms); len(want) > 0 && strings.TrimSpace(o.Platform) != "" {
		have := strings.ToLower(strings.TrimSpace(o.Platform))
		found := false
		for _, w := range want {
			found = found || w == have
		}
		if !found {
			return false
		}
	}
	if o.Tools == nil && o.Toolsets == nil {
		return true
	}
	for _, ts := range c.FallbackForToolsets {
		if o.Toolsets[ts] {
			return false
		}
	}
	for _, t := range c.FallbackForTools {
		if o.Tools[t] {
			return false
		}
	}
	for _, ts := range c.RequiresToolsets {
		if !o.Toolsets[ts] {
			return false
		}
	}
	for _, t := range c.RequiresTools {
		if !o.Tools[t] {
			return false
		}
	}
	return true
}

func categoryDescriptions(roots []skills.Root) map[string]string {
	out := map[string]string{}
	for _, r := range roots {
		for _, p := range skills.IndexFiles(r.Dir, "DESCRIPTION.md") {
			b, err := os.ReadFile(p)
			if err != nil || !utf8.Valid(b) {
				continue
			}
			fm, _ := skills.ParseFrontmatter(strings.TrimPrefix(string(b), "\ufeff"))
			raw, ok := fm["description"]
			if !ok || raw == nil || raw == "" {
				continue
			}
			rel, err := filepath.Rel(r.Dir, filepath.Dir(p))
			if err != nil {
				continue
			}
			key := "general"
			if rel != "." {
				key = filepath.ToSlash(rel)
			}
			d := stripQuotes(fmt.Sprint(raw))
			// The local root is first and wins; a later root only fills categories nobody described.
			// Within one root a later file overwrites an earlier one.
			if _, taken := out[key]; taken && r.Tier != skills.TierLocal {
				continue
			}
			out[key] = d
		}
	}
	return out
}

type indexRow struct{ name, desc string }

// BuildSkillsIndex renders the "## Skills" block for the system prompt, or "" when nothing is offered.
// Names resolve exactly as skill_view resolves them; categories named in CompactCategories collapse to a
// names-only line, and nothing is ever hidden by that.
func BuildSkillsIndex(o SkillsIndexOptions) string {
	catalog := skills.Scan(o.Settings.Roots, o.Filter)
	byCategory := map[string][]indexRow{}
	var unloadable []string
	seenUnloadable := map[string]bool{}
	for _, s := range catalog {
		dirName := filepath.Base(s.Dir)
		hidden := o.Settings.Disabled[dirName] || !o.shouldShow(skills.ExtractConditions(s.Frontmatter))
		if !s.Visible || hidden || s.Status == skills.StatusShadowed ||
			o.Settings.Disabled[s.Name] || (s.LoadName != "" && o.Settings.Disabled[s.LoadName]) {
			continue
		}
		if s.LoadName == "" {
			if !seenUnloadable[s.Name] {
				seenUnloadable[s.Name] = true
				unloadable = append(unloadable, s.Name)
			}
			continue
		}
		rel := s.RelativePath
		byCategory[indexCategory(rel)] = append(byCategory[indexCategory(rel)], indexRow{s.LoadName, promptDescription(s.Frontmatter)})
	}
	sort.Strings(unloadable)
	return renderSkillsIndex(byCategory, categoryDescriptions(o.Settings.Roots), o, unloadable)
}

func renderSkillsIndex(byCategory map[string][]indexRow, descs map[string]string, o SkillsIndexOptions, unloadable []string) string {
	if len(byCategory) == 0 {
		return ""
	}
	demoted := map[string]bool{}
	for cat := range byCategory {
		if o.CompactCategories[strings.SplitN(cat, "/", 2)[0]] {
			demoted[cat] = true
		}
	}
	note := ""
	if len(demoted) > 0 {
		note = "\n(Categories marked [names only] are outside the current coding " +
			"context, so their descriptions are omitted — the skills work " +
			"normally and load with skill_view(name) as usual.)"
	}
	if len(unloadable) > 0 {
		note += fmt.Sprintf("\n(A copy of %s is not listed: it shares both its name and its path "+
			"with a different skill in the same skills directory tier (e.g. another external_dirs entry), "+
			"so skill_view cannot load it — rename one.)", strings.Join(unloadable, ", "))
	}
	basic := "web_search or terminal"
	if o.Tools != nil && !o.Tools["web_search"] {
		basic = "terminal"
	}
	cats := make([]string, 0, len(byCategory))
	for c := range byCategory {
		cats = append(cats, c)
	}
	sort.Strings(cats)
	var lines []string
	for _, cat := range cats {
		rows := byCategory[cat]
		if demoted[cat] {
			set := map[string]bool{}
			var names []string
			for _, r := range rows {
				if !set[r.name] {
					set[r.name] = true
					names = append(names, r.name)
				}
			}
			sort.Strings(names)
			lines = append(lines, fmt.Sprintf("  %s [names only]: %s", cat, strings.Join(names, ", ")))
			continue
		}
		if d := descs[cat]; d != "" {
			lines = append(lines, fmt.Sprintf("  %s: %s", cat, d))
		} else {
			lines = append(lines, fmt.Sprintf("  %s:", cat))
		}
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].name < rows[j].name })
		seen := map[string]bool{}
		for _, r := range rows {
			if seen[r.name] {
				continue
			}
			seen[r.name] = true
			if r.desc != "" {
				lines = append(lines, fmt.Sprintf("    - %s: %s", r.name, r.desc))
			} else {
				lines = append(lines, "    - "+r.name)
			}
		}
	}
	return fmt.Sprintf(skillsIntro, basic) + strings.Join(lines, "\n") + "\n</available_skills>\n\n" +
		"Only proceed without loading a skill if genuinely none are relevant to the task." + note
}
