package approval

import (
	"regexp"
	"strings"
)

type wordSpan struct {
	start, end int
	word       string
}

var (
	envAssignmentRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=.*$`)
	redirectionRe   = regexp.MustCompile(`^(?:[0-9]+)?(?:>>|<<|<>|>&|<&|>\||[<>])`)
)

var commandWrapperWords = map[string]bool{"sudo": true, "env": true, "exec": true, "nohup": true, "setsid": true, "time": true,
	"command": true, "builtin": true, "nice": true, "timeout": true, "stdbuf": true, "ionice": true, "chrt": true, "taskset": true, "chroot": true}

var sudoOptionsWithArg = set("-c", "--close-from", "-g", "--group", "-h", "--host", "-p", "--prompt", "-u", "--user")

var wrapperOptionsWithArg = map[string]map[string]bool{
	"chroot":  set("--groups", "--userspec"),
	"sudo":    sudoOptionsWithArg,
	"env":     set("-a", "--argv0", "-C", "--chdir", "-S", "--split-string", "-u", "--unset"),
	"exec":    set("-a"),
	"nice":    set("-n", "--adjustment"),
	"time":    set("-f", "--format", "-o", "--output"),
	"timeout": set("-k", "--kill-after", "-s", "--signal"),
	"stdbuf":  set("-e", "--error", "-i", "--input", "-o", "--output"),
	"ionice":  set("-c", "--class", "-n", "--classdata"),
}

var wrapperNonExecutingOptions = map[string]map[string]bool{
	"command": set("-v", "-V"), "chrt": set("-p", "--pid"),
	"ionice": set("-p", "--pid", "--pgid", "--uid"), "taskset": set("-p", "--pid"),
}

var wrapperPositionalArgs = map[string]int{"chroot": 1, "chrt": 1, "taskset": 1, "timeout": 1}

func set(xs ...string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// commandWordSpans yields the command-position words that may be executable names, skipping redirections,
// wrapper options and their operands, and environment assignments.
func commandWordSpans(t []rune) []wordSpan {
	var out []wordSpan
	for _, pos := range commandStarts(t) {
		wrapper := ""
		positionals := 0
		options, skipArg := true, false
		for pos < len(t) {
			from := skipSpace(t, pos)
			if m := redirectionRe.FindStringIndex(string(t[from:min(from+16, len(t))])); m != nil {
				_, np, _ := readShellWord(t, from+m[1]) // the match is ASCII: bytes are runes
				pos = np
				continue
			}
			ws, we, word := readShellWord(t, pos)
			if ws == we {
				break
			}
			pos = we
			deob := deobfuscateWord(word)
			name := baseLower(deob)
			if skipArg {
				skipArg = false
				continue
			}
			if wrapper != "" && options && deob == "--" {
				options = false
				continue
			}
			if wrapper != "" && options && strings.HasPrefix(deob, "-") {
				option := strings.SplitN(deob, "=", 2)[0]
				if wrapper == "env" && (option == "--split-string" || strings.HasPrefix(deob, "-S")) {
					break
				}
				queries := wrapperNonExecutingOptions[wrapper]
				if queries[option] || (wrapper == "command" && !strings.HasPrefix(option, "--") && strings.ContainsAny(option[1:], "vV")) {
					break
				}
				skipArg = !strings.Contains(deob, "=") && wrapperOptionsWithArg[wrapper][option]
				continue
			}
			if positionals > 0 {
				positionals--
				continue
			}
			if envAssignmentRe.MatchString(word) {
				continue
			}
			out = append(out, wordSpan{ws, we, word})
			if !commandWrapperWords[name] {
				break
			}
			wrapper, options = name, true
			positionals = wrapperPositionalArgs[name]
		}
	}
	return out
}

// splitEnvString projects GNU env -S's literal argv (not POSIX shell words). Dynamic ${NAME} is not evaluated.
func splitEnvString(payload string) ([]string, bool) {
	escapes := map[rune]rune{'f': '\f', 'n': '\n', 'r': '\r', 't': '\t', 'v': '\v', '#': '#', '$': '$', '"': '"', '\'': '\'', '\\': '\\'}
	var args []string
	var word []rune
	var quote rune
	started := false
	rs := []rune(payload)
	index := 0
	for index < len(rs) {
		char := rs[index]
		index++
		if char == '\\' {
			if index == len(rs) {
				return nil, false
			}
			escaped := rs[index]
			if quote == '\'' && escaped != '\'' && escaped != '\\' {
				word = append(word, char)
				started = true
				continue
			}
			index++
			if escaped == 'c' {
				if quote != 0 {
					return nil, false
				}
				break
			}
			if escaped == '_' && quote == 0 {
				if started {
					args = append(args, string(word))
				}
				word, started = nil, false
				continue
			}
			rep, ok := escapes[escaped]
			if !ok && escaped != '_' {
				return nil, false
			}
			if escaped == '_' {
				rep = ' '
			}
			word = append(word, rep)
			started = true
			continue
		}
		if (char == '\'' || char == '"') && (quote == 0 || char == quote) {
			if quote == 0 {
				quote = char
			} else {
				quote = 0
			}
			started = true
			continue
		}
		if quote == 0 && strings.ContainsRune(" \t\n\r\v\f", char) {
			if started {
				args = append(args, string(word))
			}
			word, started = nil, false
			continue
		}
		if quote == 0 && char == '#' && !started {
			break
		}
		if char == '$' && quote != '\'' {
			return nil, false
		}
		word = append(word, char)
		started = true
	}
	if quote != 0 {
		return nil, false
	}
	if started {
		args = append(args, string(word))
	}
	return args, true
}

// envSplitPayload returns the command an `env -S` string spells, rejoined so literal separators stay protected.
func envSplitPayload(tokens []string) (string, bool) {
	index := 1
	for index < len(tokens) {
		token := tokens[index]
		if token == "--" || !strings.HasPrefix(token, "-") {
			return "", false
		}
		option, value, hasEq := strings.Cut(token, "=")
		if option == "--split-string" || strings.HasPrefix(token, "-S") {
			attached := len(token) > 2
			if option == "--split-string" {
				attached = hasEq
			}
			if !attached {
				index++
			}
			var payload string
			switch {
			case attached && option == "--split-string":
				payload = value
			case attached:
				payload = token[2:]
			case index < len(tokens):
				payload = tokens[index]
			}
			args, ok := splitEnvString(payload)
			if !ok {
				return "", false
			}
			return shlexJoin(append(args, tokens[min(index+1, len(tokens)):]...)), true
		}
		if !hasEq && wrapperOptionsWithArg["env"][option] {
			index += 2
		} else {
			index++
		}
	}
	return "", false
}
