package filetools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phzietsman/hermes-agent/internal/approval"
	"github.com/phzietsman/hermes-agent/internal/llm"
	"github.com/phzietsman/hermes-agent/internal/tools"
)

type rig struct {
	t       *testing.T
	dir     string
	home    string
	tb      *tools.Toolbox
	session string
	check   approval.Check
	gate    *approval.Gate
}

type answer struct {
	choice approval.Choice
	err    error
	asked  *[]approval.Request
}

func (a answer) Ask(_ context.Context, r approval.Request) (approval.Choice, error) {
	*a.asked = append(*a.asked, r)
	return a.choice, a.err
}

func newRig(t *testing.T) *rig {
	t.Helper()
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	home, _ := filepath.EvalSymlinks(t.TempDir())
	reg := tools.NewRegistry()
	Register(reg)
	r := &rig{t: t, dir: dir, home: home, tb: reg.Build(tools.Selection{}), session: t.Name()}
	r.gate = approval.NewGate(approval.Detector{}, approval.Config{})
	t.Cleanup(func() { ForgetSession(r.session) })
	return r
}

func (r *rig) run(name string, args any) map[string]any {
	r.t.Helper()
	b, _ := json.Marshal(args)
	ctx := tools.WithEnv(context.Background(), tools.CallEnv{Home: r.home, SessionID: r.session, CWD: r.dir, Gate: r.gate, Approval: r.check})
	out, _ := r.tb.Run(ctx, llm.ToolCall{Function: llm.ToolCallFunction{Name: name, Arguments: string(b)}})
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		r.t.Fatalf("%s: %q", name, out)
	}
	return m
}

func (r *rig) file(name, content string) string {
	r.t.Helper()
	p := filepath.Join(r.dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
	return p
}

func (r *rig) read(name string) string {
	r.t.Helper()
	b, err := os.ReadFile(filepath.Join(r.dir, name))
	if err != nil {
		r.t.Fatal(err)
	}
	return string(b)
}

func errOf(m map[string]any) string { s, _ := m["error"].(string); return s }

func TestWriteCreatesFilesAndParents(t *testing.T) {
	r := newRig(t)
	m := r.run("write_file", map[string]any{"path": "new/deep/a.txt", "content": "hello\n"})
	if m["verified"] != true || m["bytes_written"] != float64(6) || r.read("new/deep/a.txt") != "hello\n" {
		t.Fatalf("%v", m)
	}
	if m["resolved_path"] != filepath.Join(r.dir, "new/deep/a.txt") {
		t.Fatalf("resolved_path %v", m["resolved_path"])
	}
}

func TestWriteRefusesToOverwriteWhatWasNeverRead(t *testing.T) {
	r := newRig(t)
	r.file("a.txt", "precious\n")
	m := r.run("write_file", map[string]any{"path": "a.txt", "content": "clobber\n"})
	if m["stale_write_blocked"] != true || !strings.Contains(errOf(m), "has not seen its full current content") || r.read("a.txt") != "precious\n" {
		t.Fatalf("%v", m)
	}
	// A full read unlocks the overwrite, and the write itself is a fresh baseline for the next one.
	r.run("read_file", map[string]any{"path": "a.txt"})
	if m := r.run("write_file", map[string]any{"path": "a.txt", "content": "one\n"}); errOf(m) != "" {
		t.Fatalf("%v", m)
	}
	if m := r.run("write_file", map[string]any{"path": "a.txt", "content": "two\n"}); errOf(m) != "" || r.read("a.txt") != "two\n" {
		t.Fatalf("consecutive writes by the same session must stay unblocked: %v", m)
	}
}

func TestWriteRefusesAfterExternalChange(t *testing.T) {
	r := newRig(t)
	p := r.file("a.txt", "v1\n")
	r.run("read_file", map[string]any{"path": "a.txt"})
	if err := os.WriteFile(p, []byte("v2 from elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := r.run("write_file", map[string]any{"path": "a.txt", "content": "mine\n"})
	if m["stale_write_blocked"] != true || !strings.Contains(errOf(m), "was modified since you last read it") || r.read("a.txt") != "v2 from elsewhere\n" {
		t.Fatalf("%v", m)
	}
}

func TestPartialReadIsNotAFullView(t *testing.T) {
	r := newRig(t)
	var sb strings.Builder
	for i := 1; i <= 50; i++ {
		sb.WriteString("line\n")
	}
	r.file("big.txt", sb.String())
	r.run("read_file", map[string]any{"path": "big.txt", "offset": 1, "limit": 10})
	if m := r.run("write_file", map[string]any{"path": "big.txt", "content": "x"}); m["stale_write_blocked"] != true {
		t.Fatalf("a partial read must not unlock an overwrite: %v", m)
	}
	// Paging contiguously to the last line does.
	r.run("read_file", map[string]any{"path": "big.txt", "offset": 11, "limit": 40})
	if m := r.run("write_file", map[string]any{"path": "big.txt", "content": "x\n"}); errOf(m) != "" {
		t.Fatalf("%v", m)
	}
}

func TestWriteRefusesReadOutputAndBadJSON(t *testing.T) {
	r := newRig(t)
	m := r.run("write_file", map[string]any{"path": "x.txt", "content": "1|alpha\n2|beta\n3|gamma\n"})
	if !strings.Contains(errOf(m), "internal read_file display text") {
		t.Fatalf("%v", m)
	}
	m = r.run("write_file", map[string]any{"path": "x.json", "content": "{not json"})
	if !strings.Contains(errOf(m), "fails .json syntax validation") {
		t.Fatalf("%v", m)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "x.json")); err == nil {
		t.Fatal("a refused write created the file")
	}
	if m := r.run("write_file", map[string]any{"path": "y.json", "content": `{"ok": true}`}); errOf(m) != "" {
		t.Fatalf("%v", m)
	}
	if !strings.Contains(errOf(r.run("write_file", map[string]any{"content": "x"})), "missing required field 'path'") {
		t.Fatal("missing path message")
	}
	if !strings.Contains(errOf(r.run("write_file", map[string]any{"path": "x"})), "missing required field 'content'") {
		t.Fatal("missing content message")
	}
}

func TestWriteKeepsBOMAndCRLF(t *testing.T) {
	r := newRig(t)
	r.file("w.txt", "\ufeffa\r\nb\r\n")
	r.run("read_file", map[string]any{"path": "w.txt"})
	r.run("write_file", map[string]any{"path": "w.txt", "content": "a\nB\n"})
	if got := r.read("w.txt"); got != "\ufeffa\r\nB\r\n" {
		t.Fatalf("%q", got)
	}
}

func TestSensitiveAndCredentialPathsAreRefused(t *testing.T) {
	r := newRig(t)
	home, _ := os.UserHomeDir()
	for _, p := range []string{"/etc/hosts", filepath.Join(home, ".ssh", "id_rsa"), filepath.Join(home, ".aws", "credentials"),
		filepath.Join(r.home, ".env"), filepath.Join(r.home, "state.db"), filepath.Join(r.home, "sessions", "x.json"), filepath.Join(r.home, "config.yaml")} {
		m := r.run("write_file", map[string]any{"path": p, "content": "x"})
		if e := errOf(m); !(strings.Contains(e, "Refusing to write") || strings.Contains(e, "Write denied")) {
			t.Errorf("%s: %v", p, m)
		}
		if m := r.run("patch", map[string]any{"path": p, "old_string": "a", "new_string": "b"}); errOf(m) == "" {
			t.Errorf("patch %s was not refused", p)
		}
	}
}

func TestProtectedInstructionFilesAlwaysAsk(t *testing.T) {
	r := newRig(t)
	var asked []approval.Request
	// Nobody to ask: fail closed, even with yolo on.
	r.gate.SetSessionYolo(r.session, true)
	m := r.run("write_file", map[string]any{"path": "AGENTS.md", "content": "new rules\n"})
	if !strings.Contains(errOf(m), "no interactive user or gateway") {
		t.Fatalf("%v", m)
	}
	// A human who says no.
	r.check = approval.Check{SessionID: r.session, Prompter: answer{choice: approval.Deny, asked: &asked}}
	if m := r.run("write_file", map[string]any{"path": "CLAUDE.md", "content": "x"}); !strings.Contains(errOf(m), "was denied by the user") {
		t.Fatalf("%v", m)
	}
	// A human who says yes once: the write lands, and the next one asks again.
	r.check = approval.Check{SessionID: r.session, Prompter: answer{choice: approval.Always, asked: &asked}}
	if m := r.run("write_file", map[string]any{"path": "AGENTS.md", "content": "approved\n"}); errOf(m) != "" || r.read("AGENTS.md") != "approved\n" {
		t.Fatalf("%v", m)
	}
	before := len(asked)
	r.run("write_file", map[string]any{"path": "AGENTS.md", "content": "again\n"})
	if len(asked) != before+1 {
		t.Fatalf("an always answer must not persist for protected files (asked %d -> %d)", before, len(asked))
	}
	// An unrelated file never asks.
	before = len(asked)
	r.run("write_file", map[string]any{"path": "notes.txt", "content": "n\n"})
	if len(asked) != before {
		t.Fatal("asked about an ordinary file")
	}
	// A prompt that fails is not consent.
	r.check = approval.Check{SessionID: r.session, Prompter: answer{err: errors.New("no tty"), asked: &asked}}
	if m := r.run("write_file", map[string]any{"path": ".cursorrules", "content": "x"}); !strings.Contains(errOf(m), "no tty") {
		t.Fatalf("%v", m)
	}
}

func TestPatchReplaceThroughTheTool(t *testing.T) {
	r := newRig(t)
	r.file("c.go", "package c\n\nfunc a() int {\n\treturn 1\n}\n")
	m := r.run("patch", map[string]any{"path": "c.go", "old_string": "return 1", "new_string": "return 2"})
	if m["success"] != true || !strings.Contains(m["diff"].(string), "+\treturn 2") || r.read("c.go") != "package c\n\nfunc a() int {\n\treturn 2\n}\n" {
		t.Fatalf("%v", m)
	}
	if m["files_modified"].([]any)[0] != filepath.Join(r.dir, "c.go") || m["resolved_path"] == nil {
		t.Fatalf("%v", m)
	}
}

func TestPatchMissAndEscalatingHint(t *testing.T) {
	r := newRig(t)
	r.file("c.txt", "alpha\nbeta\n")
	for i := 1; i <= 3; i++ {
		m := r.run("patch", map[string]any{"path": "c.txt", "old_string": "gamma", "new_string": "delta"})
		hint, _ := m["_hint"].(string)
		// The error already carries a "Did you mean" snippet, so the generic hint stays out until the third miss.
		if i < 3 && (hint != "" || !strings.Contains(errOf(m), "Did you mean one of these sections?")) {
			t.Fatalf("attempt %d: %v", i, m)
		}
		if i == 3 && !strings.Contains(hint, "failure #3") {
			t.Fatalf("attempt 3 should escalate: %v", m)
		}
	}
	if m := r.run("patch", map[string]any{"path": "c.txt", "old_string": "alpha"}); !strings.Contains(errOf(m), "old_string and new_string required") {
		t.Fatalf("%v", m)
	}
	if m := r.run("patch", map[string]any{"mode": "bogus"}); !strings.Contains(errOf(m), "Unknown mode") {
		t.Fatalf("%v", m)
	}
}

func TestPatchWarnsWhenFileChangedSinceRead(t *testing.T) {
	r := newRig(t)
	p := r.file("c.txt", "alpha\nbeta\n")
	r.run("read_file", map[string]any{"path": "c.txt"})
	if err := os.WriteFile(p, []byte("alpha\nbeta\nextra\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := r.run("patch", map[string]any{"path": "c.txt", "old_string": "beta", "new_string": "BETA"})
	if m["success"] != true || !strings.Contains(m["_warning"].(string), "was modified since you last read it") {
		t.Fatalf("%v", m)
	}
}

func TestPatchV4AThroughTheTool(t *testing.T) {
	r := newRig(t)
	r.file("a.txt", "one\ntwo\n")
	patch := "*** Begin Patch\n*** Update File: a.txt\n@@\n-two\n+2\n*** Add File: b.txt\n+new\n*** End Patch"
	m := r.run("patch", map[string]any{"mode": "patch", "patch": patch})
	if m["success"] != true || r.read("a.txt") != "one\n2\n" || r.read("b.txt") != "new" {
		t.Fatalf("%v", m)
	}
	// Headers cannot climb out with "..", and protected paths in a header gate the whole patch.
	bad := "*** Begin Patch\n*** Update File: ../../etc/passwd\n@@\n-a\n+b\n*** End Patch"
	if m := r.run("patch", map[string]any{"mode": "patch", "patch": bad}); !strings.Contains(errOf(m), "'..' traversal") {
		t.Fatalf("%v", m)
	}
	prot := "*** Begin Patch\n*** Add File: AGENTS.md\n+x\n*** End Patch"
	if m := r.run("patch", map[string]any{"mode": "patch", "patch": prot}); !strings.Contains(errOf(m), "protected agent-instruction") {
		t.Fatalf("%v", m)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "AGENTS.md")); err == nil {
		t.Fatal("the protected file was created")
	}
}

func TestReadGuards(t *testing.T) {
	r := newRig(t)
	r.file(".env", "SECRET=1\n")
	if e := errOf(r.run("read_file", map[string]any{"path": ".env"})); !strings.Contains(e, "secret-bearing environment file") {
		t.Fatalf("%q", e)
	}
	if err := os.WriteFile(filepath.Join(r.home, "auth.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if e := errOf(r.run("read_file", map[string]any{"path": filepath.Join(r.home, "auth.json")})); !strings.Contains(e, "credential store") {
		t.Fatalf("%q", e)
	}
	if e := errOf(r.run("read_file", map[string]any{"path": "/dev/zero"})); !strings.Contains(e, "device file") {
		t.Fatalf("%q", e)
	}
	if e := errOf(r.run("read_file", map[string]any{"path": "/proc/self/environ"})); !strings.Contains(e, "device file") {
		t.Fatalf("%q", e)
	}
	r.file("img.png", "x")
	if e := errOf(r.run("read_file", map[string]any{"path": "img.png"})); !strings.Contains(e, "Cannot read binary file") {
		t.Fatalf("%q", e)
	}
	m := r.run("read_file", map[string]any{"path": "."})
	if m["success"] != false || !strings.Contains(m["note"].(string), "a directory") {
		t.Fatalf("%v", m)
	}
}

func TestMissingFileSuggestsSimilarNames(t *testing.T) {
	r := newRig(t)
	r.file("AGENTS.md", "x")
	r.file("config.yaml", "x")
	m := r.run("read_file", map[string]any{"path": "AGENT.md"})
	sim, _ := m["similar_files"].([]any)
	if m["not_found"] != true || len(sim) == 0 || sim[0] != filepath.Join(r.dir, "AGENTS.md") {
		t.Fatalf("%v", m)
	}
	m = r.run("read_file", map[string]any{"path": "config.yml"})
	if sim, _ := m["similar_files"].([]any); len(sim) == 0 || sim[0] != filepath.Join(r.dir, "config.yaml") {
		t.Fatalf("%v", m)
	}
}

func TestRepeatedIdenticalReadsAreCalledOut(t *testing.T) {
	r := newRig(t)
	r.file("a.txt", "x\n")
	for i := 1; i <= 2; i++ {
		if m := r.run("read_file", map[string]any{"path": "a.txt"}); m["_warning"] != nil || errOf(m) != "" {
			t.Fatalf("read %d: %v", i, m)
		}
	}
	if m := r.run("read_file", map[string]any{"path": "a.txt"}); !strings.Contains(m["_warning"].(string), "3 times consecutively") {
		t.Fatalf("%v", m)
	}
	if m := r.run("read_file", map[string]any{"path": "a.txt"}); !strings.Contains(errOf(m), "BLOCKED: You have read this exact file region 4 times") {
		t.Fatalf("%v", m)
	}
	// A different region, or another file in between, resets the count.
	r.file("b.txt", "y\n")
	r.run("read_file", map[string]any{"path": "b.txt"})
	if m := r.run("read_file", map[string]any{"path": "a.txt"}); m["_warning"] != nil || errOf(m) != "" {
		t.Fatalf("%v", m)
	}
}

func TestReadCharBudgetGivesNextOffset(t *testing.T) {
	r := newRig(t)
	var sb strings.Builder
	for i := 0; i < 1500; i++ {
		sb.WriteString(strings.Repeat("y", 99) + "\n")
	}
	r.file("big.txt", sb.String())
	m := r.run("read_file", map[string]any{"path": "big.txt"})
	next, _ := m["next_offset"].(float64)
	if m["truncated_by"] != "bytes" || next < 900 || next > 1100 || !strings.Contains(m["hint"].(string), "read budget") {
		t.Fatalf("%v", m["hint"])
	}
	m2 := r.run("read_file", map[string]any{"path": "big.txt", "offset": int(next)})
	if !strings.HasPrefix(m2["content"].(string), itoaf(next)+"|") {
		t.Fatalf("continuation starts at %q", m2["content"].(string)[:20])
	}
}

func itoaf(f float64) string {
	return strings.TrimSuffix(strings.TrimSuffix(json.Number(strings.TrimRight(strings.TrimRight(formatFloat(f), "0"), ".")).String(), ".0"), ".")
}
func formatFloat(f float64) string { b, _ := json.Marshal(f); return string(b) }

func TestSymlinkedTargetIsWrittenThrough(t *testing.T) {
	r := newRig(t)
	target := r.file("real.txt", "old\n")
	if err := os.Symlink(target, filepath.Join(r.dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	r.run("read_file", map[string]any{"path": "link.txt"})
	r.run("write_file", map[string]any{"path": "link.txt", "content": "new\n"})
	if fi, _ := os.Lstat(filepath.Join(r.dir, "link.txt")); fi.Mode()&os.ModeSymlink == 0 || r.read("real.txt") != "new\n" {
		t.Fatal("the symlink was replaced instead of written through")
	}
}
