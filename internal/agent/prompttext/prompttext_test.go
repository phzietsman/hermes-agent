package prompttext

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestEveryTextIsPresent(t *testing.T) {
	strs := map[string]string{
		"DefaultAgentIdentity": T.DefaultAgentIdentity, "HelpGuidance": T.HelpGuidance, "HelpGuidanceNoSkills": T.HelpGuidanceNoSkills,
		"SessionSearchGuidance": T.SessionSearchGuidance, "SkillsGuidance": T.SkillsGuidance, "SkillSafetyHeading": T.SkillSafetyHeading,
		"ToolUseEnforcementGuidance": T.ToolUseEnforcementGuidance, "ExecutionGuidance": T.ExecutionGuidance,
		"TaskCompletionGuidance": T.TaskCompletionGuidance, "AsyncHandoffGuidance": T.AsyncHandoffGuidance,
		"ParallelToolCallGuidance": T.ParallelToolCallGuidance, "SteerChannelNote": T.SteerChannelNote,
		"GoogleModelOperationalGuidance": T.GoogleModelOperationalGuidance, "TelegramRichMessagesHint": T.TelegramRichMessagesHint,
		"RuntimeEnvironmentHeading": T.RuntimeEnvironmentHeading, "RuntimeEnvironmentEnd": T.RuntimeEnvironmentEnd,
	}
	for name, v := range strs {
		if strings.TrimSpace(v) == "" {
			t.Errorf("%s is empty", name)
		}
	}
	if len(T.ToolUseEnforcementModels) == 0 || len(T.ExecutionGuidanceModels) == 0 || len(T.ExecutionGuidanceLineTools) != 7 {
		t.Errorf("model lists or line map missing: %d %d %d", len(T.ToolUseEnforcementModels), len(T.ExecutionGuidanceModels), len(T.ExecutionGuidanceLineTools))
	}
	for _, p := range []string{"cli", "telegram", "discord", "cron", "slack", "whatsapp", "email", "api_server", "whatsapp_cloud"} {
		if PlatformHint(p) == "" {
			t.Errorf("no platform hint for %s", p)
		}
	}
	for _, p := range []string{"tui", "desktop", "nonexistent", ""} {
		if PlatformHint(p) != "" {
			t.Errorf("%q must have no hint: the desktop and TUI frontends are out of scope", p)
		}
	}
}

func TestExecutionGuidanceMatchesPython(t *testing.T) {
	b, err := os.ReadFile("execution_guidance_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Tools *[]string `json:"tools"`
		Text  string    `json:"text"`
	}
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 8 {
		t.Fatalf("only %d cases", len(cases))
	}
	for _, c := range cases {
		var valid map[string]bool
		if c.Tools != nil {
			valid = map[string]bool{}
			for _, n := range *c.Tools {
				valid[n] = true
			}
		}
		if got := ExecutionGuidanceFor(valid); got != c.Text {
			t.Errorf("tools %v: text differs from Python (%d vs %d bytes)", c.Tools, len(got), len(c.Text))
		}
	}
}

func TestExecutionGuidanceDropsDanglingToolLines(t *testing.T) {
	none := ExecutionGuidanceFor(map[string]bool{})
	for _, line := range []string{"use terminal or execute_code", "sha256sum", "run `date`"} {
		if strings.Contains(none, line) {
			t.Errorf("a toolset with no terminal must not be told to %q", line)
		}
	}
	all := ExecutionGuidanceFor(nil)
	if !strings.Contains(all, "use terminal or execute_code") || len(all) <= len(none) {
		t.Error("a nil toolset keeps every line")
	}
}
