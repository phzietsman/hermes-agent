// Package prompttext holds the system-prompt texts. They are extracted verbatim from the Python source by
// scripts/port/gen_prompt_texts.py into texts.json and embedded, so the wording the model sees is
// byte-identical to what Hermes has always sent and a prompt edit is a data change reviewed as text.
package prompttext

import (
	_ "embed"
	"encoding/json"
	"strings"
)

//go:embed texts.json
var raw []byte

// Texts is every system-prompt text block.
type Texts struct {
	DefaultAgentIdentity           string            `json:"default_agent_identity"`
	HelpGuidance                   string            `json:"help_guidance"`
	HelpGuidanceNoSkills           string            `json:"help_guidance_no_skills"`
	SessionSearchGuidance          string            `json:"session_search_guidance"`
	SkillsGuidance                 string            `json:"skills_guidance"`
	SkillSafetyHeading             string            `json:"skill_safety_heading"`
	ToolUseEnforcementGuidance     string            `json:"tool_use_enforcement_guidance"`
	ToolUseEnforcementModels       []string          `json:"tool_use_enforcement_models"`
	ExecutionGuidanceModels        []string          `json:"execution_guidance_models"`
	ExecutionGuidance              string            `json:"execution_guidance"`
	ExecutionGuidanceLineTools     [][]any           `json:"execution_guidance_line_tools"`
	TaskCompletionGuidance         string            `json:"task_completion_guidance"`
	AsyncHandoffGuidance           string            `json:"async_handoff_guidance"`
	ParallelToolCallGuidance       string            `json:"parallel_tool_call_guidance"`
	SteerChannelNote               string            `json:"steer_channel_note"`
	GoogleModelOperationalGuidance string            `json:"google_model_operational_guidance"`
	TelegramRichMessagesHint       string            `json:"telegram_rich_messages_hint"`
	RuntimeEnvironmentHeading      string            `json:"runtime_environment_heading"`
	RuntimeEnvironmentEnd          string            `json:"runtime_environment_end"`
	PlatformHints                  map[string]string `json:"platform_hints"`
}

// T is the loaded texts. A malformed embedded file is a build defect, so loading panics rather than
// returning a prompt with silently missing sections.
var T = func() Texts {
	var t Texts
	if err := json.Unmarshal(raw, &t); err != nil {
		panic("prompttext: texts.json is malformed: " + err.Error())
	}
	return t
}()

// ExecutionGuidanceFor is the execution-discipline text for the session's toolset. A line that tells the model
// to reach for a tool absent from the toolset would dangle, so it is dropped (the toolset is fixed per
// session, so this is cache-safe). A nil toolset keeps every line.
func ExecutionGuidanceFor(validTools map[string]bool) string {
	text := T.ExecutionGuidance
	if validTools == nil {
		return text
	}
	for _, entry := range T.ExecutionGuidanceLineTools {
		line, _ := entry[0].(string)
		reachable := false
		for _, name := range entry[1].([]any) {
			if validTools[name.(string)] {
				reachable = true
				break
			}
		}
		if !reachable {
			text = strings.ReplaceAll(text, line, "")
		}
	}
	return text
}

// PlatformHint is the built-in hint for a platform key, or "".
func PlatformHint(platform string) string { return T.PlatformHints[platform] }
