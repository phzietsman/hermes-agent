package prompttext

import (
	"strings"
	"testing"
)

// The prompts are generated from Python; these checks catch a stale or truncated generation.
func TestPromptsAreComplete(t *testing.T) {
	for name, p := range map[string]string{"memory": P.Memory, "skills": P.Skills, "combined": P.Combined} {
		if !strings.HasPrefix(p, "Review the conversation above") || !strings.Contains(p, "Nothing to save.") {
			t.Errorf("%s prompt looks truncated: %.60q", name, p)
		}
	}
	if !strings.Contains(P.Memory, "agent_memory_remember") || !strings.Contains(P.Combined, "agent_memory_remember") {
		t.Error("the memory-bearing prompts must name the memory tool the fork is allowed to call")
	}
	if !strings.Contains(P.Skills, "skill_manage") || !strings.Contains(P.Combined, "skill_manage") {
		t.Error("the skill-bearing prompts must name skill_manage")
	}
}
