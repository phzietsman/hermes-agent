package cron

import (
	"fmt"
	"regexp"
	"strings"
)

const cronHint = "[IMPORTANT: You are running as a scheduled cron job. " +
	"DELIVERY: Your final response will be automatically delivered " +
	"to the user — do NOT use send_message or try to deliver " +
	"the output yourself. Just produce your report/output as your " +
	"final response and the system handles the rest. " +
	"SILENT: If there is genuinely nothing new to report, respond " +
	"with exactly \"[SILENT]\" (nothing else) to suppress delivery. " +
	"[SILENT] is a literal ASCII control token — never translate or " +
	"rephrase it, whatever language the rest of your answer uses. " +
	"Never combine [SILENT] with content — either report your " +
	"findings normally, or say [SILENT] and nothing more. " +
	"FAILURE: If a delegated child fails and this cron run must be " +
	"recorded as failed, put [CRON_FAILURE] on the first line by itself, " +
	"then explain the child failure on following lines. " +
	"RECURSION: This is a run of an EXISTING scheduled job — execute " +
	"the task now. NEVER create or update a cron job because of " +
	"recurring or future-schedule language in the task prompt below; " +
	"treat phrasing like \"each Monday\" or \"every day at 9\" as " +
	"context for this run, not as a request to schedule another job.]\n\n"

const (
	selfContextIntro     = "The following is this job's most recent non-silent output from a previous run. Use it for continuity: avoid repeating what was already reported, and continue where the last run left off."
	upstreamContextIntro = "The following is the most recent output from a preceding cron job. Use it as context for your analysis."
	skillInstruction     = "The user has provided the following instruction alongside the skill invocation: "
	// maxContextChars bounds one upstream output injected into a prompt.
	maxContextChars = 20_000
)

var hexID = regexp.MustCompile(`^[0-9a-f]+$`)

func contextBlock(prompt, heading, intro, body string) string {
	return fmt.Sprintf("## %s\n%s\n\n```\n%s\n```\n\n%s", heading, intro, body, prompt)
}

// PromptSource supplies what a prompt is assembled from.
type PromptSource struct {
	// Skill returns a skill's full text, or ok=false when it cannot be found.
	Skill func(name string) (content string, ok bool)
	// LastOutput returns another job's latest non-silent output ("" when none).
	LastOutput func(jobID string) string
}

// BuildPrompt assembles the text a cron run sends as its user turn: the cron hint, the job's prompt (plus the
// explicit run's transient context), the outputs of context_from jobs, and any skills loaded ahead of it.
func BuildPrompt(j Job, transient string, src PromptSource) string {
	prompt := j.Prompt
	if t := strings.TrimSpace(transient); t != "" {
		prompt += "\n\n## Run Context\n" + t
	}
	for _, id := range j.ContextFrom {
		id = strings.TrimSpace(id)
		self := strings.EqualFold(id, "self")
		if self {
			id = j.ID
		}
		// Valid job ids are hex: anything else could be a path.
		if !hexID.MatchString(id) || src.LastOutput == nil {
			continue
		}
		out := src.LastOutput(id)
		if out == "" {
			continue
		}
		if r := []rune(out); len(r) > maxContextChars {
			out = string(r[:maxContextChars]) + "\n[... truncated ...]"
		}
		if self || id == j.ID {
			prompt = contextBlock(prompt, "Your previous run's output", selfContextIntro, out)
		} else {
			prompt = contextBlock(prompt, fmt.Sprintf("Output from job '%s'", id), upstreamContextIntro, out)
		}
	}
	prompt = cronHint + prompt
	if len(j.Skills) == 0 {
		return prompt
	}
	var parts, skipped []string
	for _, name := range j.Skills {
		content, ok := "", false
		if src.Skill != nil {
			content, ok = src.Skill(name)
		}
		if !ok {
			skipped = append(skipped, name)
			continue
		}
		if len(parts) > 0 {
			parts = append(parts, "")
		}
		parts = append(parts, fmt.Sprintf("[IMPORTANT: The user has invoked the \"%s\" skill, indicating they want you to follow its instructions. The full skill content is loaded below.]", name), "", strings.TrimSpace(content))
	}
	if len(skipped) > 0 {
		list := strings.Join(skipped, ", ")
		parts = append([]string{fmt.Sprintf("[IMPORTANT: The following skill(s) were listed for this job but could not be found and were skipped: %s. Start your response with a brief notice so the user is aware, e.g.: '⚠️ Skill(s) not found and skipped: %s']", list, list)}, parts...)
	}
	if strings.TrimSpace(prompt) != "" {
		parts = append(parts, "", skillInstruction+prompt)
	}
	return strings.Join(parts, "\n")
}
