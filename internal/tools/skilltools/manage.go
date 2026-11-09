package skilltools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/goccy/go-yaml"

	"github.com/phzietsman/hermes-agent/internal/fuzzy"
	"github.com/phzietsman/hermes-agent/internal/skills"
	"github.com/phzietsman/hermes-agent/internal/threats"
	"github.com/phzietsman/hermes-agent/internal/tools"
)

const (
	maxName        = 64
	maxDescription = 1024
	// promptDescLimit is the system-prompt budget for a NEW skill's description: the index truncates longer ones.
	promptDescLimit = 60
	maxContentChars = 100_000
	maxFileBytes    = 1 << 20
)

var (
	validName   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	fmEnd       = regexp.MustCompile(`\n---\s*\n`)
	allowedDirs = map[string]bool{"references": true, "templates": true, "scripts": true, "assets": true}
)

const nameRule = "Use lowercase letters, numbers, hyphens, dots, and underscores."

func fail(msg string) string { return tools.Error(msg, tools.Field{Key: "success", Value: false}) }

func checkIdentifier(v, label, invalid string) string {
	if utf8.RuneCountInString(v) > maxName {
		return fmt.Sprintf("%s exceeds %d characters.", label, maxName)
	}
	if !validName.MatchString(v) {
		return invalid
	}
	return ""
}

func validateName(name string) string {
	if name == "" {
		return "Skill name is required."
	}
	return checkIdentifier(name, "Skill name", fmt.Sprintf("Invalid skill name '%s'. %s Must start with a letter or digit.", name, nameRule))
}

func validateCategory(c string) string {
	c = strings.TrimSpace(c)
	if c == "" {
		return ""
	}
	invalid := fmt.Sprintf("Invalid category '%s'. %s Categories must be a single directory name.", c, nameRule)
	if strings.ContainsAny(c, `/\`) {
		return invalid
	}
	return checkIdentifier(c, "Category", invalid)
}

// validateFrontmatter checks name + description and a non-empty body. newSkill also enforces the
// system-prompt description budget, so a created skill never loses its routing signal to index truncation.
func validateFrontmatter(content string, newSkill bool) string {
	if strings.TrimSpace(content) == "" {
		return "Content cannot be empty."
	}
	content = strings.TrimLeft(content, "\ufeff")
	if !strings.HasPrefix(content, "---") {
		return "SKILL.md must start with YAML frontmatter (---). See existing skills for format."
	}
	loc := fmEnd.FindStringIndex(content[3:])
	if loc == nil {
		return "SKILL.md frontmatter is not closed. Ensure you have a closing '---' line."
	}
	var parsed any
	if err := yaml.Unmarshal([]byte(content[3:loc[0]+3]), &parsed); err != nil {
		return "YAML frontmatter parse error: " + err.Error()
	}
	m, ok := parsed.(map[string]any)
	if !ok {
		return "Frontmatter must be a YAML mapping (key: value pairs)."
	}
	for _, f := range []string{"name", "description"} {
		if _, ok := m[f]; !ok {
			return fmt.Sprintf("Frontmatter must include '%s' field.", f)
		}
	}
	desc := fmt.Sprint(m["description"])
	if utf8.RuneCountInString(desc) > maxDescription {
		return fmt.Sprintf("Description exceeds %d characters.", maxDescription)
	}
	if n := utf8.RuneCountInString(strings.Trim(strings.TrimSpace(desc), `'"`)); newSkill && n > promptDescLimit {
		return fmt.Sprintf("Description is %d chars — new skills must fit the %d-char system-prompt budget (one sentence, trigger first, "+
			"ends with a period). The skill index truncates longer descriptions to %d chars + '...', destroying the routing signal. "+
			"Move detail into the skill body.", utf8.RuneCountInString(strings.TrimSpace(desc)), promptDescLimit, promptDescLimit-3)
	}
	if strings.TrimSpace(content[loc[1]+3:]) == "" {
		return "SKILL.md must have content after the frontmatter (instructions, procedures, etc.)."
	}
	return ""
}

func validateSize(content, label string) string {
	if n := utf8.RuneCountInString(content); n > maxContentChars {
		return fmt.Sprintf("%s content is %d characters (limit: %d). Consider splitting into a smaller SKILL.md with supporting files in references/ or templates/.",
			label, n, maxContentChars)
	}
	return ""
}

// validateFilePath accepts SKILL.md or a file below an allowed subdirectory, never a traversal.
func validateFilePath(p string) string {
	if p == "" {
		return "file_path is required."
	}
	parts := strings.Split(filepath.ToSlash(filepath.Clean(p)), "/")
	for _, seg := range strings.Split(filepath.ToSlash(p), "/") {
		if seg == ".." {
			return "Path traversal ('..') is not allowed."
		}
	}
	if last := parts[len(parts)-1]; last == "SKILL.md" && len(parts) <= 2 {
		return ""
	}
	if !allowedDirs[parts[0]] {
		return fmt.Sprintf("File must be under one of: assets, references, scripts, templates. Got: '%s'", p)
	}
	if len(parts) < 2 {
		return fmt.Sprintf("Provide a file path, not just a directory. Example: '%s/myfile.md'", parts[0])
	}
	return ""
}

func scan(content string) string {
	msg, _ := threats.FirstThreatMessage(content, threats.ScopeStrict)
	return msg
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".skill-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func find(st skills.Settings, name string) *skills.Skill {
	for _, s := range skills.Scan(st.Roots, skills.Filter{}) {
		if s.Status != skills.StatusShadowed && (s.LoadName == name || s.Name == name || s.RelativePath == name) {
			s := s
			return &s
		}
	}
	return nil
}

func manage(load Loader) tools.Handler {
	return func(ctx context.Context, a tools.Args) (string, error) {
		st := load(tools.Env(ctx).Home)
		action, name := a.String("action", ""), a.String("name", "")
		switch action {
		case "create":
			return create(st, name, a.String("content", ""), a.String("category", "")), nil
		case "edit":
			return edit(st, name, a.String("content", "")), nil
		case "patch":
			return patch(st, a, name), nil
		case "delete":
			return remove(st, name), nil
		case "write_file":
			return writeSupport(st, name, a.String("file_path", ""), a.String("file_content", "")), nil
		case "remove_file":
			return removeSupport(st, name, a.String("file_path", "")), nil
		}
		return fail(fmt.Sprintf("Unknown action '%s'. Use: create, edit, patch, delete, write_file, remove_file", action)), nil
	}
}

func create(st skills.Settings, name, content, category string) string {
	if content == "" {
		return fail("content is required for 'create'. Provide the full SKILL.md text (frontmatter + body).")
	}
	for _, msg := range []string{validateName(name), validateCategory(category), validateFrontmatter(content, true), validateSize(content, "SKILL.md")} {
		if msg != "" {
			return fail(msg)
		}
	}
	if ex := find(st, name); ex != nil {
		return fail(fmt.Sprintf("A skill named '%s' already exists at %s.", name, ex.Dir))
	}
	if msg := scan(content); msg != "" {
		return fail(msg)
	}
	root := st.Roots[0].Dir
	dir := filepath.Join(root, strings.TrimSpace(category), name)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return fail(err.Error())
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		return fail(fmt.Sprintf("Cannot create skill '%s': %s already exists or is unwritable. Choose another name, or move/remove that path and retry.", name, dir))
	}
	md := filepath.Join(dir, "SKILL.md")
	if err := writeFile(md, content); err != nil {
		os.Remove(dir)
		return fail(err.Error())
	}
	rel, _ := filepath.Rel(root, dir)
	fields := []tools.Field{{Key: "success", Value: true}, {Key: "message", Value: fmt.Sprintf("Skill '%s' created.", name)},
		{Key: "path", Value: rel}, {Key: "skill_md", Value: md}}
	if c := strings.TrimSpace(category); c != "" {
		fields = append(fields, tools.Field{Key: "category", Value: c})
	}
	fields = append(fields, tools.Field{Key: "hint", Value: fmt.Sprintf("To add reference files, templates, or scripts, use skill_manage(action='write_file', name='%s', file_path='references/example.md', file_content='...')", name)})
	return tools.Result(fields...)
}

func locate(st skills.Settings, name string) (*skills.Skill, string) {
	if name == "" {
		return nil, fail("name is required.")
	}
	s := find(st, name)
	if s == nil {
		return nil, fail(fmt.Sprintf("Skill '%s' not found. Use skills_list() to see available skills.", name))
	}
	return s, ""
}

func edit(st skills.Settings, name, content string) string {
	if content == "" {
		return fail("content is required for 'edit'. Provide the full updated SKILL.md text.")
	}
	if msg := validateFrontmatter(content, false); msg != "" {
		return fail(msg)
	}
	if msg := validateSize(content, "SKILL.md"); msg != "" {
		return fail(msg)
	}
	s, msg := locate(st, name)
	if s == nil {
		return msg
	}
	if m := scan(content); m != "" {
		return fail(m)
	}
	if err := writeFile(s.Path, content); err != nil {
		return fail(err.Error())
	}
	return tools.Result(tools.Field{Key: "success", Value: true}, tools.Field{Key: "message", Value: fmt.Sprintf("Skill '%s' updated (full rewrite).", name)}, tools.Field{Key: "path", Value: s.Dir})
}

func patch(st skills.Settings, a tools.Args, name string) string {
	oldS, newS := a.String("old_string", ""), a.String("new_string", "")
	if oldS == "" {
		return fail("old_string is required for 'patch'.")
	}
	s, msg := locate(st, name)
	if s == nil {
		return msg
	}
	target, label := s.Path, "SKILL.md"
	if fp := a.String("file_path", ""); fp != "" {
		if m := validateFilePath(fp); m != "" {
			return fail(m)
		}
		if filepath.Base(fp) != "SKILL.md" {
			target, label = filepath.Join(s.Dir, filepath.FromSlash(fp)), fp
		}
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		return fail(fmt.Sprintf("File not found: %s", label))
	}
	res := fuzzy.FindAndReplace(string(raw), oldS, newS, a.Bool("replace_all", false))
	if res.Error != "" {
		return fail(res.Error + fuzzy.NoMatchHint(res.Error, res.Count, oldS, string(raw)))
	}
	if label == "SKILL.md" {
		if m := validateFrontmatter(res.Content, false); m != "" {
			return fail("Patch would break SKILL.md structure: " + m)
		}
	}
	if m := validateSize(res.Content, label); m != "" {
		return fail(m)
	}
	if m := scan(res.Content); m != "" {
		return fail(m)
	}
	if err := writeFile(target, res.Content); err != nil {
		return fail(err.Error())
	}
	return tools.Result(tools.Field{Key: "success", Value: true},
		tools.Field{Key: "message", Value: fmt.Sprintf("Patched %s in skill '%s' (%d replacement%s).", label, name, res.Count, plural(res.Count))})
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func remove(st skills.Settings, name string) string {
	s, msg := locate(st, name)
	if s == nil {
		return msg
	}
	if err := os.RemoveAll(s.Dir); err != nil {
		return fail(err.Error())
	}
	// Drop the category directory when this was its last skill.
	if parent := filepath.Dir(s.Dir); parent != s.Root {
		os.Remove(parent)
	}
	return tools.Result(tools.Field{Key: "success", Value: true}, tools.Field{Key: "message", Value: fmt.Sprintf("Skill '%s' deleted.", name)})
}

func supportTarget(st skills.Settings, name, fp string) (*skills.Skill, string, string) {
	s, msg := locate(st, name)
	if s == nil {
		return nil, "", msg
	}
	if m := validateFilePath(fp); m != "" {
		return nil, "", fail(m)
	}
	target := filepath.Join(s.Dir, filepath.FromSlash(fp))
	if rel, err := filepath.Rel(s.Dir, target); err != nil || strings.HasPrefix(rel, "..") {
		return nil, "", fail("Path escapes the skill directory.")
	}
	return s, target, ""
}

func writeSupport(st skills.Settings, name, fp, content string) string {
	if content == "" {
		return fail("file_content is required for 'write_file'.")
	}
	if len(content) > maxFileBytes {
		return fail(fmt.Sprintf("File content is %d bytes (limit: %d bytes / 1 MiB).", len(content), maxFileBytes))
	}
	s, target, msg := supportTarget(st, name, fp)
	if s == nil {
		return msg
	}
	if m := scan(content); m != "" {
		return fail(m)
	}
	if err := writeFile(target, content); err != nil {
		return fail(err.Error())
	}
	return tools.Result(tools.Field{Key: "success", Value: true}, tools.Field{Key: "message", Value: fmt.Sprintf("File '%s' written to skill '%s'.", fp, name)}, tools.Field{Key: "path", Value: target})
}

func removeSupport(st skills.Settings, name, fp string) string {
	s, target, msg := supportTarget(st, name, fp)
	if s == nil {
		return msg
	}
	if filepath.Base(target) == "SKILL.md" {
		return fail("Cannot remove SKILL.md. Use action='delete' to remove the whole skill.")
	}
	if err := os.Remove(target); err != nil {
		return fail(fmt.Sprintf("File '%s' not found in skill '%s'.", fp, name))
	}
	return tools.Result(tools.Field{Key: "success", Value: true}, tools.Field{Key: "message", Value: fmt.Sprintf("File '%s' removed from skill '%s'.", fp, name)})
}
