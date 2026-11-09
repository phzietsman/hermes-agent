// Package threats is the shared prompt-injection and exfiltration pattern library. It ports
// tools/threat_patterns.py. Scopes are cumulative: "all" runs everywhere, "context" adds promptware, C2 and
// role hijack for content that is not user-authored (context files, memory, tool results), and "strict"
// adds aggressive checks for user-mediated writes (memory, skill installs) where a block is resolvable.
// New patterns must anchor on C2 vocabulary or unambiguous attack behaviour, not bossy English ("you must"
// is common in legitimate AGENTS.md); filler between tokens is the bounded filler.
//
// Go's regexp engine (RE2) differs from Python's re in three ways that matter for a scanner, handled here:
//   - repeat counts are capped at 1000: a longer bound is composed from chunks, with identical matching;
//   - \w and \s are ASCII-only: they are replaced by Unicode classes equal to Python's;
//   - there is no lookahead: the one pattern that needs it (hardcoded_secret) is checked by hand.
//
// \b stays RE2's ASCII word boundary. It can only match more than Python's (next to a non-ASCII letter),
// never less, which is the safe side for a scanner.
package threats

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// MaxScanChars bounds the scanned text: scanners are advisory, so worst-case runtime is capped.
const MaxScanChars = 65_536

// Scope names.
const (
	ScopeAll     = "all"
	ScopeContext = "context"
	ScopeStrict  = "strict"
)

const (
	// w is Python's Unicode \w: letters, digits and underscore.
	w = `[\p{L}\p{N}_]`
	// sInner is the body of Python's Unicode \s, for use inside a character class such as [\s-].
	sInner = `\t\n\v\f\r \x{1c}-\x{1f}\x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}`
	// s is Python's Unicode \s as a class of its own.
	s = `[` + sInner + `]`
)

// bound is class{0,n} for any n: RE2 caps a repeat at 1000, so a larger bound is composed from chunks.
func bound(class string, n int) string {
	var b strings.Builder
	for n > 0 {
		c := min(n, 1000)
		fmt.Fprintf(&b, "%s{0,%d}", class, c)
		n -= c
	}
	return b.String()
}

var (
	// filler is the bounded run of words allowed between key attack words (unbounded repetition backtracks badly).
	filler = `(?:` + w + `+` + s + `+){0,8}`
	// secretVar is an env var reference ending in a secret-ish suffix.
	secretVar = `\$\{?` + w + `*(?:KEY|TOKEN|SECRET|PASSWORD|CREDENTIAL)S?\b`
	// line is any run of up to 2048 non-newline characters.
	line2048 = bound(`[^\n]`, 2048)
	modify   = `(update|modify|edit|write|change|append|add` + s + `+to)` + s + `+` + line2048
)

type pattern struct {
	re    string
	id    string
	scope string
}

// ws rewrites a Python pattern's \s and \w into their Unicode classes. A \s inside a character class
// ([\s-], [\s\-]) must splice in the class body, not a nested class: a nested class would silently stop
// matching what the pattern means.
func ws(re string) string {
	return strings.NewReplacer(
		`[\s\-]`, `[`+sInner+`\-]`,
		`[\s-]`, `[`+sInner+`-]`,
		`\s`, s,
		`\w`, w,
	).Replace(re)
}

var patterns = []pattern{
	// Classic prompt injection (applies everywhere).
	{ws(`ignore\s+`) + filler + `(previous|all|above|prior)` + s + `+` + filler + `instructions`, "prompt_injection", ScopeAll},
	{ws(`system\s+prompt\s+override`), "sys_prompt_override", ScopeAll},
	{ws(`disregard\s+`) + filler + `(your|all|any)` + s + `+` + filler + `(instructions|rules|guidelines)`, "disregard_rules", ScopeAll},
	{ws(`act\s+as\s+(if|though)\s+`) + filler + `you` + s + `+` + filler + `(have` + s + `+no|don't` + s + `+have)` + s + `+` + filler + `(restrictions|limits|rules)`, "bypass_restrictions", ScopeAll},
	{`<!--[^>]{0,512}(?:ignore|override|system|secret|hidden)[^>]{0,512}-->`, "html_comment_injection", ScopeAll},
	{ws(`<\s*div\s+style\s*=\s*["'][^>]`) + `{0,1000}[^>]{0,1000}[^>]{0,48}` + ws(`display\s*:\s*none`), "hidden_div", ScopeAll},
	{ws(`translate\s+[^\n]{0,512}\s+into\s+\w+(?:[\s-]+\w+){0,2}\s+and\s+(execute|run|eval)\b`), "translate_execute", ScopeAll},
	{ws(`do\s+not\s+`) + filler + ws(`tell\s+`) + filler + ws(`the\s+user`), "deception_hide", ScopeAll},

	// Role-play and identity hijack (scraped web content, poisoned context files).
	{ws(`you\s+are\s+`) + filler + ws(`now\s+(?:a|an|the)\s+`), "role_hijack", ScopeContext},
	{ws(`pretend\s+`) + filler + ws(`(you\s+are|to\s+be)\s+`), "role_pretend", ScopeContext},
	{ws(`output\s+`) + filler + ws(`(system|initial)\s+prompt`), "leak_system_prompt", ScopeContext},
	{ws(`(respond|answer|reply)\s+without\s+`) + filler + `(restrictions|limitations|filters|safety)`, "remove_filters", ScopeContext},
	{ws(`you\s+have\s+been\s+`) + filler + `(updated|upgraded|patched)` + s + `+to`, "fake_update", ScopeContext},
	// Brainworm tell: identity override via spec. Verb pair anchored so "name your variables" is safe.
	{ws(`\bname\s+yourself\s+\w+`), "identity_override", ScopeContext},

	// C2 and Brainworm-style promptware (context scope). "register as a node" appears in legitimate
	// distributed-systems docs, so this is a warning, not a block.
	{ws(`register\s+(as\s+)?a?\s*node`), "c2_node_registration", ScopeContext},
	{ws(`(heartbeat|beacon|check[\s\-]?in)\s+(to|with)\s+`), "c2_heartbeat", ScopeContext},
	{ws(`pull\s+(down\s+)?(?:new\s+)?task(?:ing|s)?\b`), "c2_task_pull", ScopeContext},
	{ws(`connect\s+to\s+the\s+network\b`), "c2_network_connect", ScopeContext},
	// C2-specific verbs avoid the broader "you must X" false positive.
	{ws(`you\s+must\s+(?:\w+\s+){0,3}(register|connect|report|beacon)\b`), "forced_action", ScopeContext},
	// Anti-forensic instructions: near-zero false positives in legitimate content.
	{ws(`only\s+use\s+one[\s\-]?liners?\b`), "anti_forensic_oneliner", ScopeContext},
	{ws(`never\s+`) + filler + ws(`(?:create|write)\s+`) + filler + ws(`(?:script|file)\s+`) + filler + `disk`, "anti_forensic_disk", ScopeContext},
	// Unsetting agent-runtime env vars is pure attack behaviour (Brainworm sub-session bypass).
	{ws(`unset\s+\w*(?:CLAUDE|CODEX|HERMES|AGENT|OPENAI|ANTHROPIC)\w*`), "env_var_unset_agent", ScopeContext},

	// Known C2 and red-team framework names (warn-only). Every token must be a distinctive
	// offensive-security brand: a common English word here false-positives whole AGENTS.md files.
	{ws(`\b(?:cobalt\s*strike|sliver|havoc|mythic|metasploit|brainworm)\b`), "known_c2_framework", ScopeContext},
	{ws(`\bc2\s+(?:server|channel|infrastructure|beacon)\b`), "c2_explicit", ScopeContext},
	{ws(`\bcommand\s+and\s+control\b`), "c2_explicit_long", ScopeContext},

	// Exfiltration via curl, wget or cat with secrets (applies everywhere). The var name ends with \b so
	// benign names containing KEY or TOKEN as substrings pass.
	{ws(`curl\s+`) + line2048 + secretVar, "exfil_curl", ScopeAll},
	{ws(`wget\s+`) + line2048 + secretVar, "exfil_wget", ScopeAll},
	{ws(`cat\s+`) + line2048 + `(\.env|credentials|\.netrc|\.pgpass|\.npmrc|\.pypirc)`, "read_secrets", ScopeAll},
	{`(send|post|upload|transmit)` + s + `+` + line2048 + s + `+(to|at)` + s + `+https?://`, "send_to_url", ScopeStrict},
	{`(include|output|print|share)` + s + `+` + filler + `(conversation|chat` + s + `+history|previous` + s + `+messages|full` + s + `+context|entire` + s + `+context)`, "context_exfil", ScopeStrict},

	// Persistence and SSH backdoor (strict scope: memory and skills).
	{`authorized_keys`, "ssh_backdoor", ScopeStrict},
	{`(?:\b(?:echo|cat|cp|mv|dd|tee|install|printf|rsync|scp|ln|append|add|write|sed|chmod|chown|truncate|rm|touch|curl|wget|git)\b|\bopen` + s + `*\(|>>?)` +
		bound(`[^\n]`, 512) + `(?:\$HOME/\.ssh|~/\.ssh)`, "ssh_access", ScopeStrict},
	{`\$HOME/\.hermes/\.env|~/\.hermes/\.env`, "hermes_env", ScopeStrict},
	{modify + `(?:AGENTS\.md|CLAUDE\.md|\.cursorrules|\.clinerules)`, "agent_config_mod", ScopeStrict},
	{modify + `\.hermes/(config\.yaml|SOUL\.md)`, "hermes_config_mod", ScopeStrict},
}

// hardcoded_secret needs a negative lookahead, so it is not in the table: see hardcodedSecret.
var (
	secretPrefixRe = regexp.MustCompile(`(?i)(?:api[_-]?key|token|secret|password)` + s + `*[=:]` + s + `*["']`)
	secretValueRe  = regexp.MustCompile(`^[A-Za-z0-9+/=_-]{20,}`)
	// envNameRe is a value that is itself an environment-variable NAME (SHOUTY_SNAKE, at least two segments)
	// followed by its closing quote. ENV_PASSWORD = "MYPLUGIN_APP_PASSWORD" says where the credential lives; it
	// embeds none. Case-sensitive on purpose: a lowercase snake value is the passphrase shape, and requiring
	// an underscore segment keeps underscore-free all-caps credentials (AWS AKIA..., base32) matched.
	envNameRe = regexp.MustCompile(`^[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+["']`)
)

// hardcodedSecret reports a quoted credential-looking value after a key, token, secret or password name.
func hardcodedSecret(text string) bool {
	for off := 0; off < len(text); {
		loc := secretPrefixRe.FindStringIndex(text[off:])
		if loc == nil {
			return false
		}
		start, end := off+loc[0], off+loc[1]
		rest := text[end:]
		if secretValueRe.MatchString(rest) && !envNameRe.MatchString(rest) {
			return true
		}
		off = start + 1 // a later prefix may start inside this one
	}
	return false
}

type compiledPattern struct {
	re *regexp.Regexp
	id string
}

var compiled = func() map[string][]compiledPattern {
	cumulative := map[string][]string{ScopeAll: {ScopeAll, ScopeContext, ScopeStrict}, ScopeContext: {ScopeContext, ScopeStrict}, ScopeStrict: {ScopeStrict}}
	out := map[string][]compiledPattern{ScopeAll: nil, ScopeContext: nil, ScopeStrict: nil}
	for _, p := range patterns {
		re := regexp.MustCompile(`(?i)` + p.re)
		for _, sc := range cumulative[p.scope] {
			out[sc] = append(out[sc], compiledPattern{re, p.id})
		}
	}
	return out
}()

// invisible codepoints used in injection attacks: zero-width space, non-joiner and joiner, word joiner,
// invisible times, separator and plus, BOM, LTR/RTL embedding, pop and overrides, and the isolates.
var invisible = map[rune]bool{
	0x200b: true, 0x200c: true, 0x200d: true, 0x2060: true, 0x2062: true, 0x2063: true, 0x2064: true, 0xfeff: true,
	0x202a: true, 0x202b: true, 0x202c: true, 0x202d: true, 0x202e: true, 0x2066: true, 0x2067: true, 0x2068: true, 0x2069: true,
}

// Scan returns the matched pattern ids in content for scope. Invisible codepoints are reported as
// invisible_unicode_U+XXXX, in codepoint order, ahead of the pattern ids. An unknown scope is an error.
func Scan(content, scope string) ([]string, error) {
	pats, ok := compiled[scope]
	if !ok {
		return nil, fmt.Errorf("threats: unknown scope %q", scope)
	}
	if content == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(content) > MaxScanChars {
		content = string([]rune(content)[:MaxScanChars])
	}
	// Invisible unicode is checked on the RAW content: NFKC below can strip these codepoints.
	seen := map[rune]bool{}
	var found []rune
	for _, r := range content {
		if invisible[r] && !seen[r] {
			seen[r] = true
			found = append(found, r)
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i] < found[j] })
	var out []string
	for _, r := range found {
		out = append(out, fmt.Sprintf("invisible_unicode_U+%04X", r))
	}
	// NFKC folds full-width and compatibility variants (ｃａｔ becomes cat) against homograph bypass. It does
	// NOT fold cross-script confusables (Cyrillic а): that needs a TR#39 database.
	normalised := norm.NFKC.String(content)
	for _, p := range pats {
		if p.re.MatchString(normalised) {
			out = append(out, p.id)
		}
	}
	if scope == ScopeStrict && hardcodedSecret(normalised) {
		out = append(out, "hardcoded_secret")
	}
	return out, nil
}

// FirstThreatMessage is the user-facing error for the first threat found in content, or "" when there is
// none. It is for block-on-first-hit paths.
func FirstThreatMessage(content, scope string) (string, error) {
	findings, err := Scan(content, scope)
	if err != nil || len(findings) == 0 {
		return "", err
	}
	if cp, ok := strings.CutPrefix(findings[0], "invisible_unicode_"); ok {
		return fmt.Sprintf("Blocked: content contains invisible unicode character %s (possible injection).", cp), nil
	}
	return fmt.Sprintf("Blocked: content matches threat pattern '%s'. Content is injected into the system prompt and must not contain injection or exfiltration payloads.", findings[0]), nil
}
