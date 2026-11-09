// Package fuzzy ports Python's difflib.SequenceMatcher (with its autojunk heuristic, so ratios are identical)
// and tools/fuzzy_match.py, the nine-strategy find-and-replace that lets a model's edit land despite
// whitespace, indentation, escaping and Unicode drift.
package fuzzy

import (
	"fmt"
	"sort"
	"strings"
)

// Match is a matching block: a[A:A+Size] == b[B:B+Size].
type Match struct{ A, B, Size int }

// Opcode is one edit step, as difflib.get_opcodes reports it.
type Opcode struct {
	Tag            string
	I1, I2, J1, J2 int
}

// SequenceMatcher compares two rune sequences exactly as Python's difflib does with isjunk=None and
// autojunk=True: elements that make up more than 1% of a sequence of 200+ are ignored as match anchors.
type SequenceMatcher[T comparable] struct {
	a, b    []T
	b2j     map[T][]int
	matches []Match
}

// NewSequenceMatcher compares two strings character by character.
func NewSequenceMatcher(a, b string) *SequenceMatcher[rune] {
	m := &SequenceMatcher[rune]{a: []rune(a), b: []rune(b)}
	m.chainB()
	return m
}

// NewLineMatcher compares two line lists.
func NewLineMatcher(a, b []string) *SequenceMatcher[string] {
	m := &SequenceMatcher[string]{a: a, b: b}
	m.chainB()
	return m
}

func (m *SequenceMatcher[T]) chainB() {
	m.b2j = map[T][]int{}
	for i, r := range m.b {
		m.b2j[r] = append(m.b2j[r], i)
	}
	if n := len(m.b); n >= 200 {
		ntest := n/100 + 1
		for r, idxs := range m.b2j {
			if len(idxs) > ntest {
				delete(m.b2j, r)
			}
		}
	}
}

func (m *SequenceMatcher[T]) longestMatch(alo, ahi, blo, bhi int) Match {
	besti, bestj, bestsize := alo, blo, 0
	j2len := map[int]int{}
	for i := alo; i < ahi; i++ {
		newj2len := map[int]int{}
		for _, j := range m.b2j[m.a[i]] {
			if j < blo {
				continue
			}
			if j >= bhi {
				break
			}
			k := j2len[j-1] + 1
			newj2len[j] = k
			if k > bestsize {
				besti, bestj, bestsize = i-k+1, j-k+1, k
			}
		}
		j2len = newj2len
	}
	// Extend the match over equal elements the anchoring ignored (popular ones).
	for besti > alo && bestj > blo && m.a[besti-1] == m.b[bestj-1] {
		besti, bestj, bestsize = besti-1, bestj-1, bestsize+1
	}
	for besti+bestsize < ahi && bestj+bestsize < bhi && m.a[besti+bestsize] == m.b[bestj+bestsize] {
		bestsize++
	}
	return Match{besti, bestj, bestsize}
}

// MatchingBlocks returns the non-adjacent matching blocks, ending with the (len(a), len(b), 0) sentinel.
func (m *SequenceMatcher[T]) MatchingBlocks() []Match {
	if m.matches != nil {
		return m.matches
	}
	la, lb := len(m.a), len(m.b)
	type span struct{ alo, ahi, blo, bhi int }
	queue := []span{{0, la, 0, lb}}
	var blocks []Match
	for len(queue) > 0 {
		q := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		x := m.longestMatch(q.alo, q.ahi, q.blo, q.bhi)
		if x.Size > 0 {
			blocks = append(blocks, x)
			if q.alo < x.A && q.blo < x.B {
				queue = append(queue, span{q.alo, x.A, q.blo, x.B})
			}
			if x.A+x.Size < q.ahi && x.B+x.Size < q.bhi {
				queue = append(queue, span{x.A + x.Size, q.ahi, x.B + x.Size, q.bhi})
			}
		}
	}
	sort.Slice(blocks, func(i, j int) bool {
		if blocks[i].A != blocks[j].A {
			return blocks[i].A < blocks[j].A
		}
		if blocks[i].B != blocks[j].B {
			return blocks[i].B < blocks[j].B
		}
		return blocks[i].Size < blocks[j].Size
	})
	i1, j1, k1 := 0, 0, 0
	var out []Match
	for _, bl := range blocks {
		if i1+k1 == bl.A && j1+k1 == bl.B {
			k1 += bl.Size
		} else {
			if k1 > 0 {
				out = append(out, Match{i1, j1, k1})
			}
			i1, j1, k1 = bl.A, bl.B, bl.Size
		}
	}
	if k1 > 0 {
		out = append(out, Match{i1, j1, k1})
	}
	out = append(out, Match{la, lb, 0})
	m.matches = out
	return out
}

// Ratio is 2*M/T, the similarity of the two sequences in [0, 1].
func (m *SequenceMatcher[T]) Ratio() float64 {
	total := len(m.a) + len(m.b)
	if total == 0 {
		return 1
	}
	matched := 0
	for _, b := range m.MatchingBlocks() {
		matched += b.Size
	}
	return 2 * float64(matched) / float64(total)
}

// Opcodes lists the edits that turn a into b.
func (m *SequenceMatcher[T]) Opcodes() []Opcode {
	var out []Opcode
	i, j := 0, 0
	for _, b := range m.MatchingBlocks() {
		tag := ""
		switch {
		case i < b.A && j < b.B:
			tag = "replace"
		case i < b.A:
			tag = "delete"
		case j < b.B:
			tag = "insert"
		}
		if tag != "" {
			out = append(out, Opcode{tag, i, b.A, j, b.B})
		}
		i, j = b.A+b.Size, b.B+b.Size
		if b.Size > 0 {
			out = append(out, Opcode{"equal", b.A, i, b.B, j})
		}
	}
	return out
}

// GroupedOpcodes groups opcodes into hunks with n lines of context, as difflib.get_grouped_opcodes does.
func (m *SequenceMatcher[T]) GroupedOpcodes(n int) [][]Opcode {
	codes := m.Opcodes()
	if len(codes) == 0 {
		codes = []Opcode{{"equal", 0, 1, 0, 1}}
	}
	if codes[0].Tag == "equal" {
		c := codes[0]
		codes[0] = Opcode{c.Tag, max(c.I1, c.I2-n), c.I2, max(c.J1, c.J2-n), c.J2}
	}
	if last := codes[len(codes)-1]; last.Tag == "equal" {
		codes[len(codes)-1] = Opcode{last.Tag, last.I1, min(last.I2, last.I1+n), last.J1, min(last.J2, last.J1+n)}
	}
	nn := n + n
	var groups [][]Opcode
	var group []Opcode
	for _, c := range codes {
		if c.Tag == "equal" && c.I2-c.I1 > nn {
			group = append(group, Opcode{c.Tag, c.I1, min(c.I2, c.I1+n), c.J1, min(c.J2, c.J1+n)})
			groups = append(groups, group)
			group = nil
			c = Opcode{c.Tag, max(c.I1, c.I2-n), c.I2, max(c.J1, c.J2-n), c.J2}
		}
		group = append(group, c)
	}
	if len(group) > 0 && !(len(group) == 1 && group[0].Tag == "equal") {
		groups = append(groups, group)
	}
	return groups
}

// UnifiedDiff renders a unified diff of two line lists (each element keeps its line ending), with three lines of
// context, matching difflib.unified_diff with its default arguments.
func UnifiedDiff(a, b []string, fromFile, toFile string) string {
	sm := NewLineMatcher(a, b)
	var out strings.Builder
	started := false
	for _, group := range sm.GroupedOpcodes(3) {
		if !started {
			started = true
			fmt.Fprintf(&out, "--- %s\n+++ %s\n", fromFile, toFile)
		}
		first, last := group[0], group[len(group)-1]
		fmt.Fprintf(&out, "@@ -%s +%s @@\n", formatRange(first.I1, last.I2), formatRange(first.J1, last.J2))
		for _, c := range group {
			if c.Tag == "equal" {
				for _, line := range a[c.I1:c.I2] {
					out.WriteString(" " + line)
				}
				continue
			}
			if c.Tag == "replace" || c.Tag == "delete" {
				for _, line := range a[c.I1:c.I2] {
					out.WriteString("-" + line)
				}
			}
			if c.Tag == "replace" || c.Tag == "insert" {
				for _, line := range b[c.J1:c.J2] {
					out.WriteString("+" + line)
				}
			}
		}
	}
	return out.String()
}

func formatRange(start, stop int) string {
	beginning := start + 1
	length := stop - start
	if length == 1 {
		return fmt.Sprint(beginning)
	}
	if length == 0 {
		beginning--
	}
	return fmt.Sprintf("%d,%d", beginning, length)
}
