package skills

import (
	"sort"
	"strings"
	"unicode"
)

// SlugifyName turns a skill name into a /command slug (Foo Bar becomes foo-bar). Characters that
// would make an invalid chat-platform command name (+, /, ...) are dropped.
func SlugifyName(name string) string {
	name = strings.ToLower(name)
	name = strings.NewReplacer(" ", "-", "_", "-").Replace(name)
	var b strings.Builder
	for _, r := range name {
		if r == '-' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	slug := b.String()
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	return strings.Trim(slug, "-")
}

// Command is a skill offered as /<slug>.
type Command struct {
	Key         string // "/skill-name"
	Name        string
	Description string
	Path        string // the SKILL.md
	Dir         string
}

// Skipped records a skill that is not offered as a command, and why.
type Skipped struct {
	Name   string
	Reason string
}

// Commands builds the slash-command set from a scanned catalog. Only skills whose name resolves
// uniquely get a command (a bare slug never guesses between same-tier duplicates), and only if the
// platform, environment and apps gates pass. Disabled names, empty slugs and slugs that collide
// with a reserved core command are skipped (the skill stays loadable by name), and when two skills
// share a slug the first in root order wins, so local beats external. The result is sorted by key.
func Commands(catalog []Skill, disabled map[string]bool, reserved func(slug string) bool) ([]Command, []Skipped) {
	var out []Command
	var skipped []Skipped
	seenNames := map[string]bool{}
	byKey := map[string]string{}
	for _, s := range catalog {
		if s.Status != StatusUnique || !s.Visible {
			continue
		}
		if seenNames[s.Name] || disabled[s.Name] {
			continue
		}
		seenNames[s.Name] = true
		slug := SlugifyName(s.Name)
		if slug == "" {
			continue
		}
		if reserved != nil && reserved(slug) {
			skipped = append(skipped, Skipped{s.Name, "collides with the core command /" + slug})
			continue
		}
		key := "/" + slug
		if first, ok := byKey[key]; ok {
			skipped = append(skipped, Skipped{s.Name, "slug " + key + " already claimed by " + first})
			continue
		}
		byKey[key] = s.Name
		desc := s.Description
		if desc == "" {
			desc = "Invoke the " + s.Name + " skill"
		}
		out = append(out, Command{Key: key, Name: s.Name, Description: desc, Path: s.Path, Dir: s.Dir})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, skipped
}
