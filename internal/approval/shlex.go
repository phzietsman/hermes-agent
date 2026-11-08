package approval

import (
	"errors"
	"strings"
)

// shlexSplit splits s the way Python's shlex does in POSIX mode with whitespace_split on and no comments.
// With punct true it also splits runs of '<' and '>' into their own tokens (punctuation_chars="<>").
func shlexSplit(s string, punct bool) ([]string, error) {
	const (
		stSpace = ' '
		stWord  = 'a'
		stPunct = 'c'
		stEsc   = '\\'
	)
	rs := []rune(s)
	var tokens []string
	var pushback []rune
	pos := 0
	next := func() (rune, bool) {
		if n := len(pushback); n > 0 {
			r := pushback[n-1]
			pushback = pushback[:n-1]
			return r, true
		}
		if pos >= len(rs) {
			return 0, false
		}
		r := rs[pos]
		pos++
		return r, true
	}
	isWhite := func(r rune) bool { return r == ' ' || r == '\t' || r == '\r' || r == '\n' }
	isPunct := func(r rune) bool { return punct && (r == '<' || r == '>') }
	for {
		var tok strings.Builder
		state := rune(stSpace)
		escapedState := rune(0)
		quoted := false
		done := false
		eof := false
		for !done {
			r, ok := next()
			switch {
			case state == stSpace:
				switch {
				case !ok:
					eof, done = true, true
				case isWhite(r):
					// skip
				case isWordChar(r, punct):
					tok.WriteRune(r)
					state = stWord
				case isPunct(r):
					tok.WriteRune(r)
					state = stPunct
				case r == '\'' || r == '"':
					state, quoted = r, true
				case r == '\\':
					escapedState, state = stWord, stEsc
				default:
					tok.WriteRune(r)
					state = stWord
				}
			case state == '\'' || state == '"':
				quoted = true
				switch {
				case !ok:
					return nil, errors.New("no closing quotation")
				case r == state:
					state = stWord
				case state == '"' && r == '\\':
					escapedState, state = '"', stEsc
				default:
					tok.WriteRune(r)
				}
			case state == stEsc:
				if !ok {
					return nil, errors.New("no escaped character")
				}
				if (escapedState == '\'' || escapedState == '"') && r != state && r != escapedState {
					tok.WriteRune(state)
				}
				tok.WriteRune(r)
				state = escapedState
			default: // word or punctuation run
				switch {
				case !ok:
					eof, done = true, true
				case isWhite(r):
					state = stSpace
					if tok.Len() > 0 || quoted {
						done = true
					}
				case state == stPunct:
					if isPunct(r) {
						tok.WriteRune(r)
					} else {
						pushback = append(pushback, r)
						state = stSpace
						done = true
					}
				case r == '\'' || r == '"':
					state, quoted = r, true
				case r == '\\':
					escapedState, state = stWord, stEsc
				case isPunct(r):
					pushback = append(pushback, r)
					state = stSpace
					if tok.Len() > 0 || quoted {
						done = true
					}
				default:
					tok.WriteRune(r)
				}
			}
		}
		if tok.Len() > 0 || quoted {
			tokens = append(tokens, tok.String())
		}
		if eof {
			return tokens, nil
		}
	}
}

// isWordChar mirrors shlex's wordchars (letters, digits, underscore and, with punctuation_chars, "~-./*?=").
func isWordChar(r rune, punct bool) bool {
	switch {
	case r == '_', r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		return true
	case r >= 0xc0 && r <= 0xff && r != 0xd7 && r != 0xf7:
		return true // shlex adds the Latin-1 letters
	}
	return punct && strings.ContainsRune("~-./*?=", r)
}

// shlexJoin quotes tokens the way shlex.join does, so the result splits back into the same tokens.
func shlexJoin(tokens []string) string {
	out := make([]string, len(tokens))
	for i, t := range tokens {
		out[i] = shlexQuote(t)
	}
	return strings.Join(out, " ")
}

func shlexQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		if !(r < 128 && (r == '@' || r == '%' || r == '+' || r == '=' || r == ':' || r == ',' || r == '.' || r == '/' || r == '-' ||
			r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'))) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
