// Package redact removes credentials from text that is about to be persisted or shown to a model. It is the
// strict subset of agent/redact.py that compaction summaries need: the provider key prefixes (verbatim from
// Python), secret-named assignments, JSON secret fields, authorization headers, private-key blocks, JWTs and
// credentials embedded in URLs and connection strings. It always redacts; there is no off switch.
//
// It errs towards over-redaction. Python's context-sensitive rules (deciding whether a value "looks like" a
// credential) are not ported, so a few harmless values that Python would keep are masked here.
package redact

import (
	"regexp"
	"strings"
)

// Mask replaces a redacted value.
const Mask = "[REDACTED]"

var prefixPatterns = []string{
	`sk-[A-Za-z0-9_-](?:\.?[A-Za-z0-9_-]){9,}`, `ghp_[A-Za-z0-9]{10,}`, `github_pat_[A-Za-z0-9_]{10,}`,
	`gho_[A-Za-z0-9]{10,}`, `ghu_[A-Za-z0-9]{10,}`, `ghs_[A-Za-z0-9]{10,}`, `ghr_[A-Za-z0-9]{10,}`,
	`xapp-\d+-[A-Za-z0-9-]{10,}`, `xox[baprs]-[A-Za-z0-9-]{10,}`, `AIza[A-Za-z0-9_-]{30,}`, `pplx-[A-Za-z0-9]{10,}`,
	`fal_[A-Za-z0-9_-]{10,}`, `fc-[A-Za-z0-9]{10,}`, `bb_live_[A-Za-z0-9_-]{10,}`, `gAAAA[A-Za-z0-9_=-]{20,}`,
	`AKIA[A-Z0-9]{16}`, `sk_live_[A-Za-z0-9]{10,}`, `sk_test_[A-Za-z0-9]{10,}`, `rk_live_[A-Za-z0-9]{10,}`,
	`SG\.[A-Za-z0-9_-]{10,}`, `hf_[A-Za-z0-9]{10,}`, `r8_[A-Za-z0-9]{10,}`, `npm_[A-Za-z0-9]{10,}`,
	`pypi-[A-Za-z0-9_-]{10,}`, `dop_v1_[A-Za-z0-9]{10,}`, `doo_v1_[A-Za-z0-9]{10,}`, `am_(?:org_)?[A-Za-z0-9]{20,}`,
	`sk_[A-Za-z0-9_]{10,}`, `tvly-[A-Za-z0-9]{10,}`, `exa_[A-Za-z0-9]{10,}`, `gsk_[A-Za-z0-9]{10,}`,
	`syt_[A-Za-z0-9]{10,}`, `retaindb_[A-Za-z0-9]{10,}`, `hsk-[A-Za-z0-9]{10,}`, `mem0_[A-Za-z0-9]{10,}`,
	`brv_[A-Za-z0-9]{10,}`, `xai-[A-Za-z0-9]{30,}`, `ntn_[A-Za-z0-9]{10,}`, `fw-[A-Za-z0-9]{30,}`,
	`fw_[A-Za-z0-9]{30,}`, `fpk_[A-Za-z0-9]{30,}`, `glpat-[A-Za-z0-9_\-]{10,}`, `gloas-[A-Za-z0-9_\-]{10,}`,
	`gldt-[A-Za-z0-9_\-]{10,}`, `glrt-[A-Za-z0-9_.\-]{10,}`, `glrtr-[A-Za-z0-9_.\-]{10,}`, `glcbt-[A-Za-z0-9_\-]{10,}`,
	`glptt-[A-Za-z0-9_\-]{10,}`, `glft-[A-Za-z0-9_\-]{10,}`, `glimt-[A-Za-z0-9_\-]{10,}`, `glagent-[A-Za-z0-9_\-]{10,}`,
	`glsoat-[A-Za-z0-9_\-]{10,}`, `glffct-[A-Za-z0-9_\-]{10,}`, `glwt-[A-Za-z0-9_\-]{10,}`, `GR1348941[A-Za-z0-9_\-]{10,}`,
	`pk-lf-[A-Za-z0-9\-]{8,}`,
}

// Go's RE2 has no lookbehind, so a prefix pattern is anchored by a leading boundary group that is put back.
var prefixRe = regexp.MustCompile(`(^|[^A-Za-z0-9_])(` + strings.Join(prefixPatterns, "|") + `)`)

const secretNames = `(?:API_?KEY|KEY|TOKEN|SECRET|PASSWORD|PASSWD|PASS|PW|CREDENTIAL|AUTH)`

var (
	envAssignRe   = regexp.MustCompile(`(?i)([A-Z0-9_]{0,50}` + secretNames + `[A-Z0-9_]{0,50})(\s*=\s*)(['"]?)([^\s'"&]+)(['"]?)`)
	jsonFieldRe   = regexp.MustCompile(`(?i)("(?:api_?key|token|secret|password|access_token|refresh_token|auth_token|bearer|secret_value|raw_secret|secret_input|key_material)")(\s*:\s*)"([^"]+)"`)
	authHeaderRe  = regexp.MustCompile(`(?i)((?:Proxy-)?Authorization:\s*)([A-Za-z][\w.+-]*\s+)?([^\s"']+)`)
	secretHdrRe   = regexp.MustCompile(`(?i)((?:x-api-key|api-key|x-auth-token|x-access-token|cookie|set-cookie)\s*:\s*)(\S+)`)
	privateKeyRe  = regexp.MustCompile(`-----BEGIN[A-Z ]*PRIVATE KEY-----[\s\S]*?-----END[A-Z ]*PRIVATE KEY-----`)
	jwtRe         = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}(?:\.[A-Za-z0-9_=-]{4,}){0,2}`)
	urlUserinfoRe = regexp.MustCompile(`((?:https?|wss?|ftp)://)([^/\s:@]+):([^/\s@]+)@`)
	dbConnRe      = regexp.MustCompile(`(?i)((?:postgres(?:ql)?|mysql|mongodb(?:\+srv)?|redis|amqp)://)([^:/\s@]+):([^@\s]+)@`)
	telegramRe    = regexp.MustCompile(`(^|[^0-9])(bot)?([0-9]{8,}):([-A-Za-z0-9_]{30,})`)
	urlSecretRe   = regexp.MustCompile(`(?i)([?&](?:api_?key|key|token|access_token|auth|secret|password|signature|sig)=)([^&#\s"'<>]+)`)
)

// prefixLiterals are the fixed starts of the provider key patterns; a text with none of them skips that regex.
var prefixLiterals = []string{
	"sk-", "ghp_", "github_pat_", "gho_", "ghu_", "ghs_", "ghr_", "xapp-", "xox", "AIza", "pplx-", "fal_", "fc-", "bb_live_",
	"gAAAA", "AKIA", "sk_", "rk_live_", "SG.", "hf_", "r8_", "npm_", "pypi-", "dop_v1_", "doo_v1_", "am_", "tvly-", "exa_",
	"gsk_", "syt_", "retaindb_", "hsk-", "mem0_", "brv_", "xai-", "ntn_", "fw-", "fw_", "fpk_", "gl", "GR1348941", "pk-lf-",
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

var secretHeaderNames = []string{"x-api-key", "api-key", "x-auth-token", "x-access-token", "cookie"}

// Text redacts every credential it recognises in s. Each pattern is guarded by a cheap substring test, so text
// that cannot contain that kind of secret (most of it) costs a few memchr passes, not a regex scan.
func Text(s string) string {
	if s == "" {
		return s
	}
	lower := strings.ToLower(s)
	if strings.Contains(s, "PRIVATE KEY") {
		s = privateKeyRe.ReplaceAllString(s, Mask)
	}
	if strings.Contains(s, "://") {
		s = dbConnRe.ReplaceAllString(s, "${1}"+Mask+"@")
		s = urlUserinfoRe.ReplaceAllString(s, "${1}"+Mask+"@")
	}
	if strings.Contains(lower, "authorization:") {
		s = authHeaderRe.ReplaceAllString(s, "${1}${2}"+Mask)
	}
	if strings.Contains(lower, ":") && containsAny(lower, secretHeaderNames) {
		s = secretHdrRe.ReplaceAllString(s, "${1}"+Mask)
	}
	if strings.Contains(s, `"`) && strings.Contains(s, ":") {
		s = jsonFieldRe.ReplaceAllString(s, `${1}${2}"`+Mask+`"`)
	}
	if strings.Contains(s, "=") {
		s = envAssignRe.ReplaceAllStringFunc(s, redactAssignment)
		if strings.ContainsAny(s, "?&") {
			s = urlSecretRe.ReplaceAllString(s, "${1}"+Mask)
		}
	}
	if strings.Contains(s, ":") && strings.ContainsAny(s, "0123456789") {
		s = telegramRe.ReplaceAllString(s, "${1}${2}"+Mask)
	}
	if containsAny(s, prefixLiterals) {
		s = prefixRe.ReplaceAllString(s, "${1}"+Mask)
	}
	if strings.Contains(s, "eyJ") {
		s = jwtRe.ReplaceAllString(s, Mask)
	}
	return s
}

// redactAssignment masks the value of KEY=value when the key names a secret as a whole word or underscore
// segment (so KEYBOARD= and PASSAGE= are left alone).
func redactAssignment(m string) string {
	sub := envAssignRe.FindStringSubmatch(m)
	if sub == nil || !keyNamesSecret(sub[1]) || strings.Contains(sub[4], Mask) {
		return m
	}
	return sub[1] + sub[2] + sub[3] + Mask + sub[5]
}

var secretWordRe = regexp.MustCompile(`(?i)(^|_)(?:API_?KEY|KEY|TOKEN|SECRET|PASSWORD|PASSWD|PASS|PW|CREDENTIAL|AUTH)(S|_|$)`)

func keyNamesSecret(key string) bool { return secretWordRe.MatchString(key) }
