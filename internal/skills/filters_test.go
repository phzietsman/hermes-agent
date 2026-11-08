package skills

import "testing"

// HostFilterFor is HostFilter pinned to an OS, with no environments active and no apps available.
func HostFilterFor(goos string) Filter {
	return Filter{GOOS: goos, EnvActive: func(string) bool { return false }, AppAvailable: func(string) bool { return false }}
}

func fm(kv ...any) map[string]any {
	m := map[string]any{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

func TestMatchesPlatform(t *testing.T) {
	tests := []struct {
		name string
		goos string
		fm   map[string]any
		want bool
	}{
		{"absent means all", "linux", fm(), true},
		{"empty list means all", "linux", fm("platforms", []any{}), true},
		{"macos alias on darwin", "darwin", fm("platforms", []any{"macos"}), true},
		{"macos alias on linux", "linux", fm("platforms", []any{"macos"}), false},
		{"linux on linux", "linux", fm("platforms", []any{"linux"}), true},
		{"any listed OS matches", "linux", fm("platforms", []any{"macos", "linux"}), true},
		{"scalar string", "darwin", fm("platforms", "macos"), true},
		{"case and space insensitive", "darwin", fm("platforms", []any{" MacOS "}), true},
		{"raw GOOS name accepted", "darwin", fm("platforms", []any{"darwin"}), true},
		{"windows is not supported", "linux", fm("platforms", []any{"windows"}), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := HostFilterFor(tc.goos).MatchesPlatform(tc.fm); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMatchesEnvironment(t *testing.T) {
	active := map[string]bool{"docker": true}
	f := Filter{GOOS: "linux", EnvActive: func(tag string) bool { return active[tag] }, AppAvailable: func(string) bool { return true }}
	tests := []struct {
		name string
		fm   map[string]any
		want bool
	}{
		{"absent means all", fm(), true},
		{"active tag", fm("environments", []any{"docker"}), true},
		{"inactive tag", fm("environments", []any{"kanban"}), false},
		{"any active tag is enough", fm("environments", []any{"kanban", "docker"}), true},
		{"scalar", fm("environments", "docker"), true},
		{"case insensitive", fm("environments", []any{"DOCKER"}), true},
		{"blank tags ignored", fm("environments", []any{"  "}), false},
	}
	for _, tc := range tests {
		if got := f.MatchesEnvironment(tc.fm); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
	// Unknown tags fail open in the host filter.
	if !HostFilter().MatchesEnvironment(fm("environments", []any{"some-future-tag"})) {
		t.Error("an unknown environment tag must fail open")
	}
	// Kanban is not ported, so a kanban-only skill is never offered.
	if HostFilter().MatchesEnvironment(fm("environments", []any{"kanban"})) {
		t.Error("kanban skills must be hidden")
	}
}

func TestMatchesApps(t *testing.T) {
	f := Filter{GOOS: "linux", EnvActive: func(string) bool { return true }, AppAvailable: func(n string) bool { return n == "browser" }}
	if !f.MatchesApps(fm()) {
		t.Error("no requirement must match")
	}
	if !f.MatchesApps(fm("requires_apps", []any{"browser"})) {
		t.Error("an available app must match")
	}
	if f.MatchesApps(fm("requires_apps", []any{"browser", "figma"})) {
		t.Error("every app must be available")
	}
	if f.MatchesApps(fm("requires_apps", "figma")) {
		t.Error("a scalar app name is one requirement")
	}
	// No app declarations exist in the Go port: any required app hides the skill.
	if HostFilter().MatchesApps(fm("requires_apps", []any{"browser"})) {
		t.Error("the host filter has no apps")
	}
}

func TestVisibleNeedsAllGates(t *testing.T) {
	f := Filter{GOOS: "linux", EnvActive: func(string) bool { return true }, AppAvailable: func(string) bool { return true }}
	if !f.Visible(fm("platforms", []any{"linux"}, "environments", []any{"docker"}, "requires_apps", []any{"x"})) {
		t.Error("all gates pass")
	}
	if f.Visible(fm("platforms", []any{"macos"})) {
		t.Error("the platform gate must hide the skill")
	}
}
