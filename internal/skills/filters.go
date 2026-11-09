package skills

import (
	"os"
	"runtime"
	"strings"
)

// platformAlias maps the names skills use to runtime.GOOS values.
var platformAlias = map[string]string{"macos": "darwin", "linux": "linux"}

// Filter decides which skills this host offers. The OS gate is hard; environments: and
// requires_apps: are offer-time relevance gates (an explicit load by name always works).
type Filter struct {
	// GOOS is the host OS (runtime.GOOS).
	GOOS string
	// EnvActive reports whether a named runtime environment (docker, s6, kanban) is active.
	// Unknown names must report true: an unrecognised tag fails open.
	EnvActive func(tag string) bool
	// AppAvailable reports whether a declared app can be offered. An unknown app hides the skill.
	AppAvailable func(name string) bool
}

// HostFilter is the filter for this machine. No app declarations exist in the Go port yet, so a
// skill that requires an app is hidden, matching Python's behaviour for an unknown app. Kanban is
// not ported, so kanban-tagged skills are never offered.
func HostFilter() Filter {
	return Filter{
		GOOS:         runtime.GOOS,
		EnvActive:    hostEnvActive,
		AppAvailable: func(string) bool { return false },
	}
}

func hostEnvActive(tag string) bool {
	switch tag {
	case "docker":
		return isContainer()
	case "s6":
		return isDir("/run/s6") || isDir("/package/admin/s6-overlay")
	case "kanban":
		return false
	}
	return true
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func isContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	if _, err := os.Stat("/run/.containerenv"); err == nil {
		return true
	}
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return true
	}
	b, err := os.ReadFile("/proc/1/cgroup")
	if err != nil {
		return false
	}
	for _, m := range []string{"docker", "podman", "/lxc/", "kubepods", "containerd", "crio"} {
		if strings.Contains(string(b), m) {
			return true
		}
	}
	return false
}

// MatchesPlatform is true when the skill's platforms: list (absent or empty means all) names this OS.
func (f Filter) MatchesPlatform(fm map[string]any) bool {
	list := stringList(fm["platforms"])
	if len(list) == 0 {
		return true
	}
	for _, p := range list {
		n := strings.ToLower(strings.TrimSpace(p))
		if mapped, ok := platformAlias[n]; ok {
			n = mapped
		}
		if strings.HasPrefix(f.GOOS, n) {
			return true
		}
	}
	return false
}

// MatchesEnvironment is true when any declared environments: tag is active (absent means all).
func (f Filter) MatchesEnvironment(fm map[string]any) bool {
	tags := stringList(fm["environments"])
	if len(tags) == 0 {
		return true
	}
	for _, t := range tags {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" && f.EnvActive(t) {
			return true
		}
	}
	return false
}

// MatchesApps is true when every app in requires_apps: is available (absent means none needed).
func (f Filter) MatchesApps(fm map[string]any) bool {
	for _, n := range stringList(fm["requires_apps"]) {
		if !f.AppAvailable(strings.TrimSpace(n)) {
			return false
		}
	}
	return true
}

// Visible applies all three gates.
func (f Filter) Visible(fm map[string]any) bool {
	return f.MatchesPlatform(fm) && f.MatchesEnvironment(fm) && f.MatchesApps(fm)
}
