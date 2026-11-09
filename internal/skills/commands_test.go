package skills

import (
	"reflect"
	"testing"
)

func TestSlugifyName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Foo Bar", "foo-bar"},
		{"git_helper", "git-helper"},
		{"git-helper", "git-helper"},
		{"C++ / Rust", "c-rust"},
		{"--lead--and--trail--", "lead-and-trail"},
		{"a    b", "a-b"},
		{"UPPER", "upper"},
		{"naïve café", "naïve-café"},
		{"日本語", "日本語"},
		{"!!!", ""},
		{"", ""},
	}
	for _, tc := range tests {
		if got := SlugifyName(tc.in); got != tc.want {
			t.Errorf("SlugifyName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func sk(name string, status Status, visible bool) Skill {
	return Skill{Name: name, Status: status, Visible: visible, Path: "/s/" + name + "/SKILL.md", Dir: "/s/" + name, Description: "about " + name}
}

func TestCommands(t *testing.T) {
	catalog := []Skill{
		sk("zeta", StatusUnique, true),
		sk("alpha", StatusUnique, true),
		sk("hidden", StatusUnique, false),
		sk("shadowed", StatusShadowed, true),
		sk("ambiguous", StatusAmbiguous, true),
		sk("off", StatusUnique, true),
		sk("Git Helper", StatusUnique, true),
		sk("git_helper", StatusUnique, true), // same slug as the one before: first wins
		sk("help", StatusUnique, true),       // collides with a core command
		sk("!!!", StatusUnique, true),        // no usable slug
		{Name: "bare", Status: StatusUnique, Visible: true, Path: "/s/bare/SKILL.md", Dir: "/s/bare"},
	}
	reserved := func(slug string) bool { return slug == "help" }
	cmds, skipped := Commands(catalog, map[string]bool{"off": true}, reserved)

	var keys []string
	for _, c := range cmds {
		keys = append(keys, c.Key)
	}
	want := []string{"/alpha", "/bare", "/git-helper", "/zeta"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("keys = %v, want %v (sorted, filtered)", keys, want)
	}
	byKey := map[string]Command{}
	for _, c := range cmds {
		byKey[c.Key] = c
	}
	if c := byKey["/alpha"]; c.Description != "about alpha" || c.Path != "/s/alpha/SKILL.md" || c.Dir != "/s/alpha" || c.Name != "alpha" {
		t.Errorf("alpha = %+v", c)
	}
	if c := byKey["/bare"]; c.Description != "Invoke the bare skill" {
		t.Errorf("default description = %q", c.Description)
	}
	if c := byKey["/git-helper"]; c.Name != "Git Helper" {
		t.Errorf("first in catalog order must win the slug, got %q", c.Name)
	}

	reasons := map[string]string{}
	for _, s := range skipped {
		reasons[s.Name] = s.Reason
	}
	if len(skipped) != 2 || reasons["help"] == "" || reasons["git_helper"] == "" {
		t.Errorf("skipped = %+v", skipped)
	}
}

func TestCommandsDuplicateNamesRegisterOnce(t *testing.T) {
	cmds, _ := Commands([]Skill{sk("dup", StatusUnique, true), sk("dup", StatusUnique, true)}, nil, nil)
	if len(cmds) != 1 {
		t.Fatalf("commands = %d", len(cmds))
	}
}
