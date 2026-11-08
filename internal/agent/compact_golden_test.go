package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/phzietsman/hermes-agent/internal/llm"
)

type compactionGolden struct {
	Thresholds []struct {
		Ctx          int     `json:"ctx"`
		Pct          float64 `json:"pct"`
		MaxTokens    *int    `json:"max_tokens"`
		EffectivePct float64 `json:"effective_pct"`
		Threshold    int     `json:"threshold"`
	} `json:"thresholds"`
	TailBudgets []struct {
		Ctx        int `json:"ctx"`
		TailBudget int `json:"tail_budget"`
	} `json:"tail_budgets"`
	Prompts []struct {
		Previous     *string `json:"previous"`
		PreviousXLen int     `json:"previous_x_len"`
		HasUserTurn  bool    `json:"has_user_turn"`
		Focus        *string `json:"focus"`
		Budget       int     `json:"budget"`
		Prompt       string  `json:"prompt"`
		PromptLen    int     `json:"prompt_len"`
		PromptSHA    string  `json:"prompt_sha256"`
	} `json:"prompts"`
	Stubs []struct {
		Name    string `json:"name"`
		Args    string `json:"args"`
		Content string `json:"content"`
		Stub    string `json:"stub"`
	} `json:"stubs"`
	Serialize struct {
		Turns    []llm.Message `json:"turns"`
		Rendered string        `json:"rendered"`
	} `json:"serialize"`
	Elisions []struct {
		Pattern      string `json:"pattern"`
		N            int    `json:"n"`
		Limit        int    `json:"limit"`
		Head         int    `json:"head"`
		Tail         int    `json:"tail"`
		Elided       string `json:"elided"`
		ElidedMiddle string `json:"elided_middle"`
	} `json:"elisions"`
	Estimates []struct {
		Text   string `json:"text"`
		Tokens int    `json:"tokens"`
	} `json:"estimates"`
	UserSection struct {
		Turns         []llm.Message `json:"turns"`
		Section       string        `json:"section"`
		Recovery      string        `json:"recovery"`
		RecoveryEmpty string        `json:"recovery_empty"`
	} `json:"user_section"`
}

func loadCompactionGolden(t *testing.T) compactionGolden {
	t.Helper()
	b, err := os.ReadFile("testdata/compaction.json")
	if err != nil {
		t.Fatal(err)
	}
	var g compactionGolden
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func repeatTo(pattern string, n int) string { return strings.Repeat(pattern, n/len(pattern)+1)[:n] }

func TestThresholdsMatchPython(t *testing.T) {
	for _, c := range loadCompactionGolden(t).Thresholds {
		maxTokens := 0
		if c.MaxTokens != nil {
			maxTokens = *c.MaxTokens
		}
		eff := effectiveThresholdPercent(c.Ctx, c.Pct)
		if eff != c.EffectivePct {
			t.Errorf("ctx=%d pct=%v: effective %v, want %v", c.Ctx, c.Pct, eff, c.EffectivePct)
		}
		if got := computeThresholdTokens(c.Ctx, eff, maxTokens); got != c.Threshold {
			t.Errorf("ctx=%d pct=%v max=%d: threshold %d, want %d", c.Ctx, c.Pct, maxTokens, got, c.Threshold)
		}
	}
}

func TestTailBudgetMatchesPython(t *testing.T) {
	for _, c := range loadCompactionGolden(t).TailBudgets {
		if got := tailTokenBudget(c.Ctx); got != c.TailBudget {
			t.Errorf("ctx=%d: %d, want %d", c.Ctx, got, c.TailBudget)
		}
	}
}

func TestSummaryPromptMatchesPython(t *testing.T) {
	for i, c := range loadCompactionGolden(t).Prompts {
		previous := ""
		if c.Previous != nil {
			previous = *c.Previous
		}
		content := "[USER]: do the thing\n\n[ASSISTANT]: done"
		if c.PreviousXLen > 0 {
			previous, content = strings.Repeat("x", c.PreviousXLen), "[USER]: hi"
		}
		focus := ""
		if c.Focus != nil {
			focus = *c.Focus
		}
		got := buildSummaryPrompt(content, c.Budget, focus, previous, c.HasUserTurn, "2026-03-15")
		if c.PromptSHA != "" {
			sum := sha256.Sum256([]byte(got))
			if len([]rune(got)) != c.PromptLen || hex.EncodeToString(sum[:]) != c.PromptSHA {
				t.Errorf("case %d: bounded prompt differs (len %d, want %d)", i, len([]rune(got)), c.PromptLen)
			}
			continue
		}
		if got != c.Prompt {
			t.Errorf("case %d (user=%v focus=%q prev=%v) differs\n got: %q\nwant: %q", i, c.HasUserTurn, focus, previous != "", clip(got), clip(c.Prompt))
		}
	}
}

func TestToolStubsMatchPython(t *testing.T) {
	for _, c := range loadCompactionGolden(t).Stubs {
		if got := summarizeToolResult(c.Name, c.Args, c.Content); got != c.Stub {
			t.Errorf("%s %s\n got: %q\nwant: %q", c.Name, c.Args, got, c.Stub)
		}
	}
}

func TestSerializeForSummaryMatchesPython(t *testing.T) {
	g := loadCompactionGolden(t).Serialize
	if got := serializeForSummary(g.Turns); got != g.Rendered {
		t.Fatalf("differs\n got: %q\nwant: %q", clip(got), clip(g.Rendered))
	}
}

func TestElisionMatchesPython(t *testing.T) {
	for _, c := range loadCompactionGolden(t).Elisions {
		text := repeatTo(c.Pattern, c.N)
		if c.Elided != "" {
			if got := elide(text, c.Limit); got != c.Elided {
				t.Errorf("elide(%d,%d) = %q, want %q", c.N, c.Limit, clip(got), clip(c.Elided))
			}
		} else if got := elideMiddle(text, c.Head, c.Tail); got != c.ElidedMiddle {
			t.Errorf("elideMiddle(%d,%d,%d) = %q, want %q", c.N, c.Head, c.Tail, clip(got), clip(c.ElidedMiddle))
		}
	}
}

func TestEstimateTokensMatchesPython(t *testing.T) {
	for _, c := range loadCompactionGolden(t).Estimates {
		if got := EstimateTokens(c.Text); got != c.Tokens {
			t.Errorf("%q: %d, want %d", c.Text, got, c.Tokens)
		}
	}
}

func TestVerbatimUserSectionMatchesPython(t *testing.T) {
	g := loadCompactionGolden(t).UserSection
	if got := verbatimUserSection(g.Turns); got != g.Section {
		t.Errorf("section differs\n got: %q\nwant: %q", clip(got), clip(g.Section))
	}
	if got := recoveryFooter("20260315_x", 7); got != g.Recovery {
		t.Errorf("recovery %q, want %q", got, g.Recovery)
	}
	if got := recoveryFooter("", 7); got != g.RecoveryEmpty {
		t.Errorf("empty recovery %q", got)
	}
}
