package skills

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/phzietsman/hermes-agent/internal/config"
)

// loadCfg writes yaml as <home>/config.yaml and loads it.
func loadCfg(t *testing.T, home, yaml string) *config.Config {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestLoadSettingsRoots(t *testing.T) {
	home := t.TempDir()
	user := t.TempDir()
	t.Setenv("HOME", user)
	t.Setenv("SHARED_SKILLS", filepath.Join(user, "shared"))
	for _, d := range []string{"shared", "tilde-skills", "rel"} {
		if err := os.MkdirAll(filepath.Join(user, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(home, "rel"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := loadCfg(t, home, `skills:
  external_dirs:
    - ${SHARED_SKILLS}
    - ~/tilde-skills
    - rel
    - ${SHARED_SKILLS}
    - /does/not/exist
    - ""
    - skills
`)
	st := LoadSettings(home, cfg, "")
	// resolvePath follows symlinks (macOS /var -> /private/var), so compare resolved forms.
	want := []Root{
		{TierLocal, filepath.Join(home, "skills")},
		{TierExternal, resolvePath(filepath.Join(user, "shared"))},
		{TierExternal, resolvePath(filepath.Join(user, "tilde-skills"))},
		{TierExternal, resolvePath(filepath.Join(home, "rel"))}, // relative entries are anchored at the home
	}
	if !reflect.DeepEqual(st.Roots, want) {
		t.Fatalf("roots:\n got %+v\nwant %+v", st.Roots, want)
	}
}

func TestLoadSettingsLocalRootKeptWhenMissingAndNoConfig(t *testing.T) {
	home := t.TempDir()
	cfg, err := config.Load(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	st := LoadSettings(home, cfg, "")
	if len(st.Roots) != 1 || st.Roots[0].Dir != filepath.Join(home, "skills") || len(st.Disabled) != 0 {
		t.Fatalf("got %+v", st)
	}
}

func TestLoadSettingsDisabled(t *testing.T) {
	tests := []struct {
		name, yaml, platform string
		want                 []string
	}{
		{"list", "skills:\n  disabled: [a, b]\n", "", []string{"a", "b"}},
		{"scalar is one name", "skills:\n  disabled: solo\n", "", []string{"solo"}},
		{"json array string", `skills:
  disabled: '["a", "b"]'
`, "", []string{"a", "b"}},
		{"python literal string", `skills:
  disabled: "['a', 'b']"
`, "", []string{"a", "b"}},
		{"malformed array string is one name", "skills:\n  disabled: '[a, b'\n", "", []string{"[a, b"}},
		{"empty array string", "skills:\n  disabled: '[]'\n", "", nil},
		{"platform list is unioned", "skills:\n  disabled: [a]\n  platform_disabled:\n    telegram: [b]\n    discord: [c]\n", "telegram", []string{"a", "b"}},
		{"no platform ignores platform lists", "skills:\n  disabled: [a]\n  platform_disabled:\n    telegram: [b]\n", "", []string{"a"}},
		{"unlisted platform adds nothing", "skills:\n  disabled: [a]\n  platform_disabled:\n    telegram: [b]\n", "slack", []string{"a"}},
		{"names are trimmed, blanks dropped", "skills:\n  disabled: [' a ', '', '  ']\n", "", []string{"a"}},
		{"hermes-agent can never be disabled", "skills:\n  disabled: [hermes-agent, other]\n", "", []string{"other"}},
		{"essential skill survives a platform list too", "skills:\n  platform_disabled:\n    telegram: [hermes-agent]\n", "telegram", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			got := keys(LoadSettings(home, loadCfg(t, home, tc.yaml), tc.platform).Disabled)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseConfigStringList(t *testing.T) {
	tests := []struct {
		in   any
		want []string
	}{
		{nil, nil},
		{42, nil},
		{"solo", []string{"solo"}},
		{`["a","b"]`, []string{"a", "b"}},
		{`['a', "b"]`, []string{"a", "b"}},
		{`[]`, []string{}},
		{`[a, b]`, []string{"[a, b]"}}, // not a literal: one name
		{[]any{"a", 1, true}, []string{"a", "1", "true"}},
	}
	for _, tc := range tests {
		if got := ParseConfigStringList(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ParseConfigStringList(%#v) = %#v, want %#v", tc.in, got, tc.want)
		}
	}
}
