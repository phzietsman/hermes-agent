package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/phzietsman/hermes-agent/internal/agent/prompttext"
	"github.com/phzietsman/hermes-agent/internal/config"
	"github.com/phzietsman/hermes-agent/internal/session"
)

// HintOverride is the platform_hints.<platform> config override: Replace swaps the default hint, Append adds text.
type HintOverride struct{ Replace, Append string }

// PromptInput is everything the system prompt depends on. It is gathered once when a session starts: the
// prompt is built then and never again until compaction, so none of it may change under a live conversation.
type PromptInput struct {
	// Home is the profile home (SOUL.md, skills, profile line). Explicit, never read from the environment.
	Home      string
	SessionID string
	// RootSessionID is the lineage root; its minted timestamp anchors "Conversation started" across compaction.
	RootSessionID string
	// SessionStart is the fallback start when no session id carries a timestamp.
	SessionStart  time.Time
	PassSessionID bool

	Model, Provider, Platform string
	// Tools and Toolsets are the names this session offers. An empty Tools set drops all tool guidance.
	Tools    map[string]bool
	Toolsets map[string]bool

	// SystemMessage is the caller's extra system text; nil adds nothing.
	SystemMessage *string
	// SkipContextFiles drops project context files (and SOUL.md unless LoadSoulIdentity).
	SkipContextFiles bool
	LoadSoulIdentity bool
	CWD              string
	// ContextLength scales the context-file caps; MaxChars pins them.
	ContextLength int
	MaxChars      int

	// SkillsIndex is passed through to BuildSkillsIndex when a skills tool is offered.
	SkillsIndex SkillsIndexOptions
	// MemoryBlock is the memory provider's text, already rendered; empty adds nothing.
	MemoryBlock string

	// TaskCompletion and ParallelToolCall default to on; ToolUseEnforcement and ExecutionGuidance take
	// "auto" (default), a bool, a bool-like string, or a list of model-name substrings.
	TaskCompletion     *bool
	ParallelToolCall   *bool
	ToolUseEnforcement any
	ExecutionGuidance  any

	HintOverrides map[string]HintOverride
	// TelegramRichMessages is platforms.telegram.extra.rich_messages.
	TelegramRichMessages bool
	// CronDeliveryPlatform is the channel a cron job's reply lands on.
	CronDeliveryPlatform string

	// Now and Zone drive the date line. Zero Now means time.Now(); nil Zone means the local zone.
	Now  time.Time
	Zone *time.Location
}

// PromptParts is the system prompt in its three cache tiers plus context-file truncation warnings to surface
// in chat.
type PromptParts struct {
	Stable, Context, Volatile string
	Warnings                  []string
}

// String joins the non-empty tiers, stable first so a longest-prefix cache keeps the unchanged scaffold.
func (p PromptParts) String() string {
	var out []string
	for _, t := range []string{p.Stable, p.Context, p.Volatile} {
		if t != "" {
			out = append(out, t)
		}
	}
	return strings.Join(out, "\n\n")
}

func joinTier(parts []string) string {
	var out []string
	for _, p := range parts {
		if p = pyStrip(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n\n")
}

var gateWords = map[string]bool{"true": true, "always": true, "yes": true, "on": true, "false": false, "never": false, "no": false, "off": false}

// modelGate resolves a config gate: a bool or bool-like word is itself, a list matches model-name substrings,
// and anything else ("auto") matches the built-in model list.
func modelGate(setting any, model string, defaults []string) bool {
	switch v := setting.(type) {
	case bool:
		return v
	case string:
		if b, ok := gateWords[strings.ToLower(v)]; ok {
			return b
		}
	case []string:
		return containsAny(strings.ToLower(model), lowerAll(v))
	case []any:
		var ss []string
		for _, x := range v {
			if s, ok := x.(string); ok {
				ss = append(ss, strings.ToLower(s))
			}
		}
		return containsAny(strings.ToLower(model), ss)
	}
	return containsAny(strings.ToLower(model), defaults)
}

func lowerAll(xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = strings.ToLower(x)
	}
	return out
}

func containsAny(s string, subs []string) bool {
	for _, p := range subs {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

func (in PromptInput) hint(platform string) string {
	def := prompttext.PlatformHint(platform)
	if platform == "telegram" && def != "" && in.TelegramRichMessages {
		def = strings.TrimRight(def, " \t\r\n") + " " + prompttext.T.TelegramRichMessagesHint
	}
	ov, ok := in.HintOverrides[platform]
	if !ok || platform == "" {
		return def
	}
	replace, appendText := pyStrip(ov.Replace), pyStrip(ov.Append)
	base := def
	if replace != "" {
		base = replace
	}
	if appendText != "" {
		return pyStrip(base + "\n\n" + appendText)
	}
	return base
}

func (in PromptInput) platformHint() string {
	key := strings.ToLower(pyStrip(in.Platform))
	h := in.hint(key)
	if key == "cron" {
		dk := strings.ToLower(pyStrip(in.CronDeliveryPlatform))
		if dk != "" && dk != "cron" {
			if dh := in.hint(dk); dh != "" {
				h = pyStrip(h + "\n\n" + fmt.Sprintf("Delivery destination (%s): %s", dk, dh))
			}
		}
	}
	return h
}

func (in PromptInput) toolGuidance() string {
	var g []string
	if in.Tools["session_search"] {
		g = append(g, prompttext.T.SessionSearchGuidance)
	}
	if in.Tools["skill_manage"] {
		g = append(g, prompttext.T.SkillsGuidance)
	}
	return strings.Join(g, " ")
}

func (in PromptInput) hasSkillTools() bool {
	return in.Tools["skills_list"] || in.Tools["skill_view"] || in.Tools["skill_manage"]
}

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

func (in PromptInput) guidance() []string {
	t := prompttext.T
	var parts []string
	if len(in.Tools) > 0 {
		if boolOr(in.TaskCompletion, true) {
			parts = append(parts, t.TaskCompletionGuidance)
		}
		if boolOr(in.ParallelToolCall, true) {
			parts = append(parts, t.ParallelToolCallGuidance)
		}
	}
	parts = append(parts, in.toolGuidance())
	if len(in.Tools) == 0 {
		return parts
	}
	parts = append(parts, t.SteerChannelNote)
	enforce := in.ToolUseEnforcement
	if enforce == nil {
		enforce = "auto"
	}
	if modelGate(enforce, in.Model, t.ToolUseEnforcementModels) {
		parts = append(parts, t.ToolUseEnforcementGuidance)
		if containsAny(strings.ToLower(in.Model), []string{"gemini", "gemma"}) {
			parts = append(parts, t.GoogleModelOperationalGuidance)
		}
	}
	exec := in.ExecutionGuidance
	if exec == nil {
		exec = "auto"
	}
	if modelGate(exec, in.Model, t.ExecutionGuidanceModels) {
		parts = append(parts, prompttext.ExecutionGuidanceFor(in.Tools))
	}
	if in.Tools["delegate_task"] {
		parts = append(parts, t.AsyncHandoffGuidance)
	}
	return parts
}

// profileNameForHome is "<root>/profiles/X" -> "X" and anything else "default".
func profileNameForHome(home string) string {
	root := config.DefaultRoot(home)
	profiles, err := filepath.EvalSymlinks(filepath.Join(root, "profiles"))
	if err != nil {
		profiles = filepath.Join(root, "profiles")
	}
	h, err := filepath.EvalSymlinks(home)
	if err != nil {
		h = home
	}
	rel, err := filepath.Rel(profiles, h)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "default"
	}
	return strings.Split(rel, string(filepath.Separator))[0]
}

func (in PromptInput) profileLine() string {
	name := profileNameForHome(in.Home)
	root := config.DefaultRoot(in.Home)
	if name == "default" {
		return "Active Hermes profile: default. Other profiles (if any) live " +
			"under " + root + "/profiles/<name>/. Each profile has its own " +
			"skills/, plugins/, cron/, and memories/ that affect a different " +
			"session than this one. Do not modify another profile's " +
			"skills/plugins/cron/memories unless the user explicitly directs " +
			"you to."
	}
	return fmt.Sprintf("Active Hermes profile: %s. This session reads and writes %s/. The default "+
		"profile's data lives at %s/skills/, %s/plugins/, %s/cron/, %s/memories/ — those belong to a "+
		"different session run from a different shell. Do NOT modify "+
		"another profile's skills/plugins/cron/memories unless the user "+
		"explicitly directs you to.", name, in.Home, root, root, root, root)
}

var sessionStampRe = regexp.MustCompile(`^(\d{8})_(\d{6})`)

func (in PromptInput) clock() (time.Time, *time.Location) {
	zone := in.Zone
	if zone == nil {
		zone = time.Local
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	return now.In(zone), zone
}

// sessionStart is the best-known, immutable conversation start: the lineage root's minted stamp, then this
// session's, then SessionStart, then now. Stamps are wall-clock in the box's zone.
func (in PromptInput) sessionStart(now time.Time, zone *time.Location) time.Time {
	for _, id := range []string{in.RootSessionID, in.SessionID} {
		if m := sessionStampRe.FindStringSubmatch(id); m != nil {
			if t, err := time.ParseInLocation("20060102_150405", m[1]+"_"+m[2], time.Local); err == nil {
				return t.In(zone)
			}
		}
	}
	if !in.SessionStart.IsZero() {
		return in.SessionStart.In(zone)
	}
	return now
}

func zoneBits(now time.Time, zone *time.Location) []string {
	var bits []string
	iana := ""
	if n := zone.String(); n != "Local" && n != "" {
		iana = n
		bits = append(bits, iana)
	}
	if abbrev := now.Format("MST"); abbrev != "" && abbrev != iana {
		bits = append(bits, abbrev)
	}
	if off := now.Format("-0700"); off != "" {
		bits = append(bits, "UTC"+off[:3]+":"+off[3:])
	}
	return bits
}

const dateLayout = "Monday, January 02, 2006"

func (in PromptInput) timestampLine() string {
	now, zone := in.clock()
	bits := zoneBits(now, zone)
	suffix := ""
	if len(bits) > 0 {
		suffix = " (" + strings.Join(bits, ", ") + ")"
	}
	start := in.sessionStart(now, zone)
	line := "Conversation started: " + start.Format(dateLayout) + suffix
	if now.Format("20060102") != start.Format("20060102") {
		line += "\nToday's date (as of the last context rebuild): " + now.Format(dateLayout) +
			" — trust this over the start date for what day it is now; query tools for exact time."
	}
	type kv struct{ k, v string }
	sid := ""
	if in.PassSessionID {
		sid = in.SessionID
	}
	for _, p := range []kv{{"Session ID", sid}, {"Model", in.Model}, {"Provider", in.Provider}, {"Platform", in.Platform}} {
		if p.v != "" {
			line += "\n" + p.k + ": " + p.v
		}
	}
	return line
}

// BuildSystemPromptParts assembles the prompt in three cache tiers: stable (identity, guidance, platform hint),
// context (caller system message, project context files) and volatile (skills index, memory, profile line,
// date line). Never call it mid-session; use EnsureSystemPrompt.
func BuildSystemPromptParts(in PromptInput) PromptParts {
	ctxOpts := ContextOptions{CWD: in.CWD, ContextLength: in.ContextLength, MaxChars: in.MaxChars, Home: in.Home}
	var warnings []string

	var stable []string
	soulLoaded := false
	if in.LoadSoulIdentity || !in.SkipContextFiles {
		r := LoadSoulMD(ctxOpts)
		warnings = append(warnings, r.Warnings...)
		if r.Prompt != "" {
			stable, soulLoaded = append(stable, r.Prompt), true
		}
	}
	if !soulLoaded {
		stable = append(stable, prompttext.T.DefaultAgentIdentity)
	}
	helpSlot := len(stable)
	stable = append(stable, prompttext.T.HelpGuidanceNoSkills)
	stable = append(stable, in.guidance()...)

	skillsPrompt := ""
	if in.hasSkillTools() {
		o := in.SkillsIndex
		// A session always knows its toolset, so "unknown" (nil) never reaches the index.
		o.Tools, o.Toolsets, o.Platform = in.Tools, in.Toolsets, in.Platform
		if o.Toolsets == nil {
			o.Toolsets = map[string]bool{}
		}
		skillsPrompt = BuildSkillsIndex(o)
	}
	if in.Tools["skill_view"] && strings.Contains(skillsPrompt, "- hermes-agent:") {
		stable[helpSlot] = prompttext.T.HelpGuidance
	}
	// Platform hint last in the stable tier: there is no workspace snapshot to follow it.
	stable = append(stable, in.platformHint())

	var contextTier []string
	if in.SystemMessage != nil {
		contextTier = append(contextTier, *in.SystemMessage)
	}
	if !in.SkipContextFiles {
		o := ctxOpts
		o.SkipSoul = soulLoaded
		r := BuildContextFiles(o)
		warnings = append(warnings, r.Warnings...)
		contextTier = append(contextTier, r.Prompt)
	}

	volatile := []string{skillsPrompt, in.MemoryBlock, in.profileLine(), in.timestampLine()}
	return PromptParts{Stable: joinTier(stable), Context: joinTier(contextTier), Volatile: joinTier(volatile), Warnings: warnings}
}

// PromptStore is the slice of the session store EnsureSystemPrompt needs.
type PromptStore interface {
	GetSession(ctx context.Context, id string) (*session.Session, error)
	SetSystemPrompt(ctx context.Context, sessionID, prompt string) (bool, error)
}

// SessionPrompt is a session's system prompt and where it came from.
type SessionPrompt struct {
	Text string
	// Static is the stable tier when this process built the prompt; empty after a resume, where only the
	// stored bytes are known (callers then send the stored prompt untouched, with no static breakpoint
	// unless the text starts with a rebuilt stable tier).
	Static string
	// Restored is true when the text came from the store rather than a fresh build.
	Restored bool
	Warnings []string
}

// EnsureSystemPrompt returns the session's system prompt, building and storing it on first use and reusing
// the stored bytes on every later call and on resume. Building runs only when nothing is stored, so a prompt
// is written once and cannot drift under a live conversation. If two writers race, the stored copy wins.
func EnsureSystemPrompt(ctx context.Context, store PromptStore, sessionID string, build func() PromptParts) (SessionPrompt, error) {
	s, err := store.GetSession(ctx, sessionID)
	if err != nil {
		return SessionPrompt{}, err
	}
	if s.SystemPrompt != "" {
		return SessionPrompt{Text: s.SystemPrompt, Restored: true}, nil
	}
	parts := build()
	text := parts.String()
	stored, err := store.SetSystemPrompt(ctx, sessionID, text)
	if err != nil {
		return SessionPrompt{}, err
	}
	if !stored {
		s, err = store.GetSession(ctx, sessionID)
		if err != nil {
			return SessionPrompt{}, err
		}
		return SessionPrompt{Text: s.SystemPrompt, Restored: true}, nil
	}
	return SessionPrompt{Text: text, Static: parts.Stable, Warnings: parts.Warnings}, nil
}
