package approval

import (
	"context"
	"fmt"
	"strings"
)

const noHuman = "requires approval but no interactive user or gateway is present to approve it."

func protectedBlocked(targets, why string) string {
	return fmt.Sprintf("BLOCKED: write to protected agent-instruction file(s) (%s) %s The user has NOT consented to this write. "+
		"Do NOT retry it or attempt the same edit via another path (terminal, execute_code, etc.).", targets, why)
}

// ProtectedWrite asks the user to approve a write to files that steer future agent behaviour (AGENTS.md and the
// like). It is one operation approval every time: not bypassed by yolo, mode off or the allowlist, nothing
// persisted, and it fails closed when there is nobody to ask. It returns "" when approved, else the message for
// the model.
func (g *Gate) ProtectedWrite(ctx context.Context, targets []string, c Check) string {
	seen := map[string]bool{}
	var uniq []string
	for _, t := range targets {
		if !seen[t] {
			seen[t] = true
			uniq = append(uniq, t)
		}
	}
	list := strings.Join(uniq, ", ")
	if c.Prompter == nil {
		return protectedBlocked(list, noHuman)
	}
	description := fmt.Sprintf("Write to protected agent-instruction file(s): %s. "+
		"These files steer future agent behavior; approval is always "+
		"required (not bypassed by auto-approve).", list)
	pctx := ctx
	if g.cfg.Timeout > 0 {
		var cancel context.CancelFunc
		pctx, cancel = context.WithTimeout(ctx, g.cfg.Timeout)
		defer cancel()
	}
	choice, err := c.Prompter.Ask(pctx, Request{Command: "<write to " + list + ">", Description: description, PatternKey: "protected_instruction_file"})
	switch {
	case err != nil && pctx.Err() == context.DeadlineExceeded && ctx.Err() == nil:
		return protectedBlocked(list, "approval prompt timed out without a user response. Silence is not consent.")
	case err != nil:
		return protectedBlocked(list, fmt.Sprintf("approval prompt could not be delivered or was not answered (%v).", err))
	}
	switch choice {
	case Once, Session, Always: // any scope is a one-operation grant
		return ""
	}
	return protectedBlocked(list, "was denied by the user.")
}

// ActionApproval asks about an action that is not a shell command (a write to ~/.ssh/config). It honours yolo and
// mode off, session and permanent approvals under key, and fails closed without a human. It returns "" when
// approved, else the model-facing refusal.
func (g *Gate) ActionApproval(ctx context.Context, key, description, display string, c Check) string {
	if g.yoloActive(c.SessionID) || g.cfg.Mode == "off" || g.IsApproved(c.SessionID, key) {
		return ""
	}
	blocked := func(why string) string {
		return fmt.Sprintf("BLOCKED: %s %s Do NOT retry it via another path (terminal, execute_code) without the user's explicit consent.", display, why)
	}
	if c.Prompter == nil {
		return blocked(noHuman)
	}
	pctx := ctx
	if g.cfg.Timeout > 0 {
		var cancel context.CancelFunc
		pctx, cancel = context.WithTimeout(ctx, g.cfg.Timeout)
		defer cancel()
	}
	choice, err := c.Prompter.Ask(pctx, Request{Command: display, Description: description, PatternKey: key})
	switch {
	case err != nil && pctx.Err() == context.DeadlineExceeded && ctx.Err() == nil:
		return blocked("approval prompt timed out without a user response. Silence is not consent.")
	case err != nil:
		return blocked(fmt.Sprintf("approval prompt could not be delivered or was not answered (%v).", err))
	}
	switch choice {
	case Once, Session, Always:
		g.persist(c.SessionID, choice, key)
		return ""
	}
	return blocked("was denied by the user.")
}
