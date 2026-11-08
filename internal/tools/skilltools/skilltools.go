// Package skilltools offers skills_list, skill_view and skill_manage. Skills are SKILL.md packages under the
// profile's skills directory (and read-only external dirs); only the local root is ever written.
package skilltools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/phzietsman/hermes-agent/internal/skills"
	"github.com/phzietsman/hermes-agent/internal/tools"
)

// Loader resolves the skill roots and disabled set for a profile home. It is called per call, so a skill
// written mid-session is visible to skill_view without touching the (frozen) system prompt.
type Loader func(home string) skills.Settings

const manageDescription = "Manage skills (create, update, delete). Skills are your procedural memory — reusable approaches for recurring task types. " +
	"Actions: create (full SKILL.md + optional category), patch (old_string/new_string find-and-replace within SKILL.md or a supporting file — preferred for fixes), " +
	"edit (full SKILL.md rewrite — major overhauls only), delete, write_file / remove_file (supporting files under references/, templates/, scripts/, assets/). " +
	"Create when a complex task succeeded (5+ tool calls), errors were overcome, or the user corrected your approach and the lesson will recur. " +
	"After difficult tasks, patch a skill you used if it was wrong or incomplete. Skills carry the HOW; facts about the user belong in memory."

// Register adds the three skill tools to r.
func Register(r *tools.Registry, load Loader) {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	r.MustRegister(
		tools.Tool{
			Name: "skills_list", Toolset: "skills", Parallel: true,
			Description: "List available skills (name + description). Use skill_view(name) to load full content.",
			Parameters: map[string]any{"type": "object", "required": []any{},
				"properties": map[string]any{"category": str("Optional category filter to narrow results")}},
			Handler: func(ctx context.Context, a tools.Args) (string, error) {
				return list(load(tools.Env(ctx).Home), a.String("category", "")), nil
			},
		},
		tools.Tool{
			Name: "skill_view", Toolset: "skills", Parallel: true,
			Description: "Skills allow for loading information about specific tasks and workflows, as well as scripts and templates. Load a skill's full content or access its linked files (references, templates, scripts). First call returns SKILL.md content plus a 'linked_files' dict showing available references/templates/scripts. To access those, call again with file_path parameter.",
			Parameters: map[string]any{"type": "object", "required": []any{"name"}, "properties": map[string]any{
				"name":      str("The skill name (use skills_list to see available skills)."),
				"file_path": str("OPTIONAL: Path to a linked file within the skill (e.g., 'references/api.md', 'templates/config.yaml', 'scripts/validate.py'). Omit to get the main SKILL.md content."),
			}},
			Handler: func(ctx context.Context, a tools.Args) (string, error) {
				return view(load(tools.Env(ctx).Home), a.String("name", ""), a.String("file_path", "")), nil
			},
		},
		tools.Tool{
			Name: "skill_manage", Toolset: "skills", Description: manageDescription,
			Parameters: map[string]any{"type": "object", "required": []any{"action", "name"}, "properties": map[string]any{
				"action":       map[string]any{"type": "string", "enum": []any{"create", "patch", "edit", "delete", "write_file", "remove_file"}, "description": "The operation."},
				"name":         str("Skill name (lowercase, hyphens/underscores/dots, max 64 chars)."),
				"content":      str("Full SKILL.md text (YAML frontmatter + markdown body) for create / edit."),
				"category":     str("Optional category subdir for create (e.g. 'devops')."),
				"old_string":   str("Text to find (patch). Must be unique unless replace_all."),
				"new_string":   str("Replacement text (patch). May be empty to delete the match."),
				"replace_all":  map[string]any{"type": "boolean", "description": "Replace all occurrences (default false)."},
				"file_path":    str("Supporting file under references/, templates/, scripts/ or assets/ (write_file, remove_file; optional for patch)."),
				"file_content": str("Full text of the supporting file (write_file)."),
			}},
			Handler: manage(load),
		},
	)
}

func usable(st skills.Settings) []skills.Skill {
	var out []skills.Skill
	for _, s := range skills.Scan(st.Roots, skills.HostFilter()) {
		if s.LoadName != "" && s.Visible && !st.Disabled[s.Name] {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func list(st skills.Settings, category string) string {
	var rows []map[string]any
	cats := map[string]bool{}
	for _, s := range usable(st) {
		if s.Category != "" {
			cats[s.Category] = true
		}
		if category != "" && s.Category != category {
			continue
		}
		rows = append(rows, map[string]any{"name": s.LoadName, "description": s.Description, "category": nullIfEmpty(s.Category)})
	}
	if len(rows) == 0 {
		return tools.Result(tools.Field{Key: "success", Value: true}, tools.Field{Key: "skills", Value: []any{}},
			tools.Field{Key: "categories", Value: []any{}}, tools.Field{Key: "message", Value: "No skills found."})
	}
	names := make([]string, 0, len(cats))
	for c := range cats {
		names = append(names, c)
	}
	sort.Strings(names)
	return tools.Result(tools.Field{Key: "success", Value: true}, tools.Field{Key: "skills", Value: rows},
		tools.Field{Key: "categories", Value: names}, tools.Field{Key: "count", Value: len(rows)},
		tools.Field{Key: "hint", Value: "Use skill_view(name) to see full content, tags, and linked files"})
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// linkedFiles groups a skill's supporting files by subdirectory.
func linkedFiles(dir string) map[string][]string {
	out := map[string][]string{}
	for sub := range allowedDirs {
		root := filepath.Join(dir, sub)
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				rel, _ := filepath.Rel(dir, p)
				out[sub] = append(out[sub], filepath.ToSlash(rel))
			}
			return nil
		})
		sort.Strings(out[sub])
	}
	for k, v := range out {
		if len(v) == 0 {
			delete(out, k)
		}
	}
	return out
}

func view(st skills.Settings, name, filePath string) string {
	if strings.TrimSpace(name) == "" {
		return fail("name is required.")
	}
	var found *skills.Skill
	var names []string
	for _, s := range skills.Scan(st.Roots, skills.Filter{}) {
		if s.Status == skills.StatusShadowed {
			continue
		}
		names = append(names, s.LoadName)
		if found == nil && (s.LoadName == name || s.Name == name || s.RelativePath == name) {
			s := s
			found = &s
		}
	}
	if found == nil {
		sort.Strings(names)
		if len(names) > 20 {
			names = names[:20]
		}
		return tools.Error(fmt.Sprintf("Skill '%s' not found.", name), tools.Field{Key: "success", Value: false},
			tools.Field{Key: "available_skills", Value: names}, tools.Field{Key: "hint", Value: "Use skills_list to see all available skills"})
	}
	if filePath != "" {
		if m := validateFilePath(filePath); m != "" {
			return fail(m)
		}
		target := filepath.Join(found.Dir, filepath.FromSlash(filePath))
		if rel, err := filepath.Rel(found.Dir, target); err != nil || strings.HasPrefix(rel, "..") {
			return fail("Path escapes the skill directory.")
		}
		raw, err := os.ReadFile(target)
		if err != nil {
			return tools.Error(fmt.Sprintf("File '%s' not found in skill '%s'.", filePath, found.Name), tools.Field{Key: "success", Value: false},
				tools.Field{Key: "available_files", Value: linkedFiles(found.Dir)})
		}
		return tools.Result(tools.Field{Key: "success", Value: true}, tools.Field{Key: "name", Value: found.Name},
			tools.Field{Key: "file", Value: filePath}, tools.Field{Key: "content", Value: string(raw)}, tools.Field{Key: "file_type", Value: filepath.Ext(filePath)})
	}
	raw, err := os.ReadFile(found.Path)
	if err != nil {
		return fail(fmt.Sprintf("Failed to read skill '%s': %v", name, err))
	}
	return tools.Result(tools.Field{Key: "success", Value: true}, tools.Field{Key: "name", Value: found.Name},
		tools.Field{Key: "description", Value: found.Description}, tools.Field{Key: "path", Value: found.RelativePath + "/SKILL.md"},
		tools.Field{Key: "content", Value: string(raw)}, tools.Field{Key: "linked_files", Value: nullIfEmptyMap(linkedFiles(found.Dir))})
}

func nullIfEmptyMap(m map[string][]string) any {
	if len(m) == 0 {
		return nil
	}
	return m
}
