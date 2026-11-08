package prompttext

import (
	_ "embed"
	"encoding/json"
)

//go:embed compaction.json
var compactionRaw []byte

// SectionInstructions are the summarizer's per-section guidance, one set per "the transcript has a real user turn".
type SectionInstructions struct {
	Language          string `json:"language"`
	HistoricalTask    string `json:"historical_task"`
	Goal              string `json:"goal"`
	Constraints       string `json:"constraints"`
	ResolvedQuestions string `json:"resolved_questions"`
}

// CompactionTexts are the compaction prompt texts, extracted verbatim from Python by
// legacy/python/scripts/port/gen_compaction_golden.py. The summary prefix and the summarizer template change what the model
// does after a compaction, so they are data, never retyped.
type CompactionTexts struct {
	SummaryPrefix             string                         `json:"summary_prefix"`
	LegacySummaryPrefix       string                         `json:"legacy_summary_prefix"`
	HistoricalTaskHeading     string                         `json:"historical_task_heading"`
	EndMarker                 string                         `json:"end_marker"`
	CompressionNote           string                         `json:"compression_note"`
	MergedPriorContextHeader  string                         `json:"merged_prior_context_header"`
	MergedSummaryDelimiter    string                         `json:"merged_summary_delimiter"`
	PrunedToolPlaceholder     string                         `json:"pruned_tool_placeholder"`
	NoUserTaskSentinel        string                         `json:"no_user_task_sentinel"`
	ContinuationUserContent   string                         `json:"continuation_user_content"`
	ElisionMarkerTemplate     string                         `json:"elision_marker_template"`
	LeanSessionLogSection     string                         `json:"lean_session_log_section"`
	LeanSessionLogBudget      int                            `json:"lean_session_log_budget_tokens"`
	PrunedSkillsSectionHeader string                         `json:"pruned_skills_section_heading"`
	LeanUserMessagesHeading   string                         `json:"lean_user_messages_heading"`
	LeanRecoveryHeading       string                         `json:"lean_recovery_heading"`
	SectionInstructions       map[string]SectionInstructions `json:"section_instructions"`
}

// C is the loaded compaction texts. A malformed embedded file is a build defect, so loading panics.
var C = func() CompactionTexts {
	var t CompactionTexts
	if err := json.Unmarshal(compactionRaw, &t); err != nil {
		panic("prompttext: compaction.json is malformed: " + err.Error())
	}
	return t
}()
