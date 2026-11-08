package cron

import (
	"fmt"
	"regexp"

	"github.com/phzietsman/hermes-agent/internal/threats"
)

// A cron prompt is user-authored and small: it gets the strict set. A directive-shaped prompt has no business
// containing `cat ~/.hermes/.env` or `rm -rf /`; there it is a smoking gun, not prose.
var cronThreats = []struct {
	re *regexp.Regexp
	id string
}{
	{regexp.MustCompile(`(?i)ignore\s+(?:\w+\s+)*(?:previous|all|above|prior)\s+(?:\w+\s+)*instructions`), "prompt_injection"},
	{regexp.MustCompile(`(?i)do\s+not\s+tell\s+the\s+user`), "deception_hide"},
	{regexp.MustCompile(`(?i)system\s+prompt\s+override`), "sys_prompt_override"},
	{regexp.MustCompile(`(?i)disregard\s+(your|all|any)\s+(instructions|rules|guidelines)`), "disregard_rules"},
	{regexp.MustCompile(`(?i)cat\s+[^\n]*(\.env|credentials|\.netrc|\.pgpass|id_rsa|id_ed25519|id_ecdsa)`), "read_secrets"},
	{regexp.MustCompile(`(?i)authorized_keys`), "ssh_backdoor"},
	{regexp.MustCompile(`(?i)/etc/sudoers|visudo`), "sudoers_mod"},
	{regexp.MustCompile(`(?i)rm\s+-rf\s+/`), "destructive_root_rm"},
	{regexp.MustCompile(`(?i)curl\s+[^\n]*https?://[^\s"'` + "`" + `]*\$\{?\w*(?:KEY|TOKEN|SECRET|PASSWORD|CREDENTIAL|API)\w*\}?`), "exfil_curl_url"},
	{regexp.MustCompile(`(?i)wget\s+[^\n]*https?://[^\s"'` + "`" + `]*\$\{?\w*(?:KEY|TOKEN|SECRET|PASSWORD|CREDENTIAL|API)\w*\}?`), "exfil_wget_url"},
	{regexp.MustCompile(`(?i)curl\s+[^\n]*(?:--data(?:-raw|-binary|-urlencode)?|-d|--form|-F)\s+[^\n]*\$\{?\w*(?:KEY|TOKEN|SECRET|PASSWORD|CREDENTIAL|API)\w*\}?`), "exfil_curl_data"},
}

// ScanPrompt returns a refusal message when a cron prompt looks like an injection or exfiltration payload, else "".
func ScanPrompt(prompt string) string {
	prompt = stripEmojiJoiners(prompt)
	if found, _ := threats.Scan(prompt, threats.ScopeAll); len(found) > 0 {
		for _, f := range found {
			if len(f) > 18 && f[:18] == "invisible_unicode_" {
				return fmt.Sprintf("Blocked: prompt contains invisible unicode character %s (possible injection).", f[18:])
			}
		}
	}
	for _, t := range cronThreats {
		if t.re.MatchString(prompt) {
			return fmt.Sprintf("Blocked: prompt matches threat pattern '%s'. Cron prompts run unattended and must not contain injection or exfiltration payloads.", t.id)
		}
	}
	return ""
}

// U+200D (ZWJ) is part of many emoji sequences (family, rainbow flag): blocked between plain text, allowed inside an
// emoji grapheme cluster.
func stripEmojiJoiners(s string) string {
	r := []rune(s)
	out := make([]rune, 0, len(r))
	for i, c := range r {
		if c == 0x200d && emojiBefore(r, i) && emojiAfter(r, i) {
			continue
		}
		out = append(out, c)
	}
	return string(out)
}

func isEmoji(c rune) bool {
	return (c >= 0x1F000 && c <= 0x1FFFF) || (c >= 0x2600 && c <= 0x27BF) || (c >= 0x2300 && c <= 0x23FF) || c == 0x20E3
}

func emojiBefore(r []rune, i int) bool {
	for j := i - 1; j >= 0; j-- {
		if r[j] == 0xFE0F {
			continue
		}
		return isEmoji(r[j])
	}
	return false
}

func emojiAfter(r []rune, i int) bool {
	for j := i + 1; j < len(r); j++ {
		if r[j] == 0xFE0F {
			continue
		}
		return isEmoji(r[j])
	}
	return false
}
