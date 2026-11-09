package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, home, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMissingAndEmptyYieldDefaults(t *testing.T) {
	for _, body := range []string{"", "   \n", "# only a comment\n", "~\n"} {
		home := t.TempDir()
		if body != "" {
			writeConfig(t, home, body)
		}
		c, err := Load(home, nil)
		if err != nil {
			t.Fatalf("body %q: %v", body, err)
		}
		if got := c.Strings("toolsets", nil); !reflect.DeepEqual(got, []string{"hermes-cli"}) {
			t.Fatalf("body %q: toolsets = %v", body, got)
		}
	}
}

func TestLoadRejectsBadRoots(t *testing.T) {
	for name, body := range map[string]string{
		"list root":   "- a\n- b\n",
		"scalar root": "just a string\n",
		"bad yaml":    "model: [unclosed\n",
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			writeConfig(t, home, body)
			if _, err := Load(home, nil); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestDeepMerge(t *testing.T) {
	base := map[string]any{
		"agent": map[string]any{"max_turns": 90, "verbose": false},
		"keep":  "default",
		"list":  []any{"a"},
	}
	over := map[string]any{
		"agent": map[string]any{"max_turns": 12, "extra": "x"},
		"new":   1,
		"list":  []any{"b"},
	}
	got := deepMerge(base, over)
	want := map[string]any{
		"agent": map[string]any{"max_turns": 12, "verbose": false, "extra": "x"},
		"keep":  "default",
		"new":   1,
		"list":  []any{"b"}, // lists replace, they do not concatenate
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if base["agent"].(map[string]any)["max_turns"] != 90 {
		t.Fatal("deepMerge mutated its base")
	}
	// A scalar replacing a map, and a map replacing a scalar, both win.
	got = deepMerge(map[string]any{"a": map[string]any{"x": 1}, "b": "s"}, map[string]any{"a": "flat", "b": map[string]any{"y": 2}})
	if got["a"] != "flat" || !reflect.DeepEqual(got["b"], map[string]any{"y": 2}) {
		t.Fatalf("type changes must win: %v", got)
	}
}

func TestLoadOverridesDefaults(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "toolsets: [web]\nmodel: claude-opus-5-5\nextra: {a: 1}\n")
	c, err := Load(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Strings("toolsets", nil); !reflect.DeepEqual(got, []string{"web"}) {
		t.Errorf("toolsets = %v", got)
	}
	if got := c.String("model", ""); got != "claude-opus-5-5" {
		t.Errorf("model = %q", got)
	}
	if got := c.Int("extra.a", 0); got != 1 {
		t.Errorf("extra.a = %d", got)
	}
	if got := c.Strings("fallback_providers", []string{"x"}); len(got) != 0 {
		t.Errorf("untouched default lost: %v", got)
	}
}

func TestDefaultsReturnsFreshMap(t *testing.T) {
	a := Defaults()
	a["model"] = "mutated"
	if Defaults()["model"] != "" {
		t.Fatal("Defaults must not share state between calls")
	}
}

func TestEnvExpansion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GOOGLE_API_KEY", "gsk-test-key")
	t.Setenv("TELEGRAM_BOT_TOKEN", "1234567:ABC-token")
	t.Setenv("ENV_ONLY", "from-env")
	t.Setenv("MULTI_A", "a")
	t.Setenv("MULTI_B", "b")
	writeEnv(t, home, "FROM_DOTENV=dot\nENV_ONLY=dotenv-wins\n")
	writeConfig(t, home, strings.Join([]string{
		"model:",
		"  api_key: ${GOOGLE_API_KEY}",
		"platforms:",
		"  telegram:",
		"    token: ${TELEGRAM_BOT_TOKEN}",
		"plain: no-substitution",
		"prefixed: ${env:ENV_ONLY}",
		"dotenv: ${FROM_DOTENV}",
		"multi: x-${MULTI_A}-${MULTI_B}-y",
		"unresolved: ${NOT_SET_ANYWHERE}",
		"unresolved_env: ${env:NOT_SET_ANYWHERE}",
		"vault: ${vault:secret/x}",
		"empty_env: ${env:}",
		"list:",
		"  - ${MULTI_A}",
		"  - 42",
		"  - true",
		"number: 42",
		"flag: true",
		"${NOT_A_KEY}: v",
	}, "\n")+"\n")

	c, err := Load(home, LoadSecrets(home))
	if err != nil {
		t.Fatal(err)
	}
	str := func(p string) string { return c.String(p, "<missing>") }
	tests := []struct{ path, want string }{
		{"model.api_key", "gsk-test-key"},
		{"platforms.telegram.token", "1234567:ABC-token"},
		{"plain", "no-substitution"},
		{"prefixed", "dotenv-wins"}, // .env beats the process env, as for every other secret
		{"dotenv", "dot"},
		{"multi", "x-a-b-y"},
		{"unresolved", "${NOT_SET_ANYWHERE}"},
		{"unresolved_env", "${env:NOT_SET_ANYWHERE}"},
		{"vault", "${vault:secret/x}"}, // non-env sources are never resolved here
		{"empty_env", "${env:}"},
	}
	for _, tc := range tests {
		if got := str(tc.path); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.path, got, tc.want)
		}
	}
	if got := c.Int("number", 0); got != 42 {
		t.Errorf("number = %d", got)
	}
	if !c.Bool("flag", false) {
		t.Error("flag lost")
	}
	if got := c.Strings("list", nil); !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("list strings = %v", got)
	}
	if _, ok := c.Get("${NOT_A_KEY}"); !ok {
		t.Error("keys must not be expanded")
	}
}

func TestEnvExpansionLateEnvIsSeen(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "key: ${LATE_DOTENV_KEY}\n")
	os.Unsetenv("LATE_DOTENV_KEY")
	c, err := Load(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.String("key", ""); got != "${LATE_DOTENV_KEY}" {
		t.Fatalf("literal expected before the var exists, got %q", got)
	}
	// There is no cache: a later Load sees the new environment.
	t.Setenv("LATE_DOTENV_KEY", "real")
	c, _ = Load(home, nil)
	if got := c.String("key", ""); got != "real" {
		t.Fatalf("got %q", got)
	}
}

func TestTypedGetters(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, strings.Join([]string{
		"s: text",
		"i: 7",
		"f: 2.5",
		"whole: 3.0",
		"frac: 3.5",
		"numstr: ' 9 '",
		"b: true",
		"yes: 'Yes'",
		"off: 'off'",
		"junk: 'maybe'",
		"nil_val: ~",
		"list: [a, 1, b]",
		"nested: {x: {y: deep}}",
	}, "\n")+"\n")
	c, err := Load(home, nil)
	if err != nil {
		t.Fatal(err)
	}

	if got := c.String("s", "d"); got != "text" {
		t.Errorf("String = %q", got)
	}
	if got := c.String("i", "d"); got != "d" {
		t.Errorf("String of an int must give the default, got %q", got)
	}
	if got := c.String("missing", "d"); got != "d" {
		t.Errorf("String missing = %q", got)
	}
	if got := c.String("nil_val", "d"); got != "d" {
		t.Errorf("String of null = %q", got)
	}

	intTests := []struct {
		path string
		want int
	}{{"i", 7}, {"whole", 3}, {"frac", -1}, {"numstr", 9}, {"s", -1}, {"missing", -1}}
	for _, tc := range intTests {
		if got := c.Int(tc.path, -1); got != tc.want {
			t.Errorf("Int(%s) = %d, want %d", tc.path, got, tc.want)
		}
	}
	if got := c.Float("f", 0); got != 2.5 {
		t.Errorf("Float = %v", got)
	}
	if got := c.Float("i", 0); got != 7 {
		t.Errorf("Float of int = %v", got)
	}
	if got := c.Float("s", 1.5); got != 1.5 {
		t.Errorf("Float default = %v", got)
	}

	boolTests := []struct {
		path      string
		def, want bool
	}{{"b", false, true}, {"yes", false, true}, {"off", true, false}, {"junk", true, true}, {"junk", false, false}, {"missing", true, true}}
	for _, tc := range boolTests {
		if got := c.Bool(tc.path, tc.def); got != tc.want {
			t.Errorf("Bool(%s, %v) = %v, want %v", tc.path, tc.def, got, tc.want)
		}
	}

	if got := c.Strings("list", nil); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("Strings = %v", got)
	}
	if got := c.Strings("s", []string{"d"}); !reflect.DeepEqual(got, []string{"d"}) {
		t.Errorf("Strings of a scalar = %v", got)
	}
	if got := c.String("nested.x.y", ""); got != "deep" {
		t.Errorf("nested = %q", got)
	}
	if _, ok := c.Get("nested.x.y.z"); ok {
		t.Error("descending through a scalar must miss")
	}
}
