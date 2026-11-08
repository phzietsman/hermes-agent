package filetools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phzietsman/hermes-agent/internal/tools"
)

type goldenFiles struct {
	Files map[string]string `json:"files"`
	Reads []struct {
		Name          string
		Offset, Limit int
		Result        map[string]any
	} `json:"reads"`
	Patches []struct {
		Name       string
		Old, New   string
		ReplaceAll bool `json:"replace_all"`
		Result     map[string]any
		After      *string
	} `json:"patches"`
}

func loadGolden(t *testing.T) goldenFiles {
	t.Helper()
	b, err := os.ReadFile("testdata/files.json")
	if err != nil {
		t.Fatal(err)
	}
	var g goldenFiles
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func materialize(t *testing.T, g goldenFiles) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, b64 := range g.Files {
		data, _ := base64.StdEncoding.DecodeString(b64)
		if err := os.WriteFile(filepath.Join(d, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func fixDir(v any, dir string) any {
	switch t := v.(type) {
	case string:
		return strings.ReplaceAll(t, dir, "<DIR>")
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = fixDir(x, dir)
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for k, x := range t {
			out[k] = fixDir(x, dir)
		}
		return out
	}
	return v
}

func decodeJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("%q: %v", s, err)
	}
	return m
}

// Differential: read_file returns the same JSON, key for key, as Python's read_file on a local environment, over 25
// reads of files with and without trailing newlines, CRLF, a BOM, a 3,000-character line, binary content,
// merge-conflict markers and 2,600 lines.
func TestReadFileMatchesPython(t *testing.T) {
	g := loadGolden(t)
	dir := materialize(t, g)
	env := tools.CallEnv{CWD: dir, SessionID: "read-golden"}
	for _, c := range g.Reads {
		args := tools.Args{"path": filepath.Join(dir, c.Name), "offset": json.Number(itoa(c.Offset)), "limit": json.Number(itoa(c.Limit))}
		got, _ := readFile(env, newGuard(""), args)
		gm := fixDir(decodeJSON(t, got), dir)
		// The tool adds guard-level keys on top of Python's file-operation result (repeat tracking); compare the
		// shared keys.
		want := c.Result
		gmm := gm.(map[string]any)
		for _, k := range []string{"_warning", "conflict_blocks", "_hint"} {
			if _, ok := want[k]; !ok {
				delete(gmm, k)
			}
		}
		// Python's file operation does not report conflicts or hints; the tool layer does. Compare what it reports.
		delete(gmm, "conflict_blocks")
		delete(gmm, "_hint")
		delete(want, "conflict_blocks")
		delete(want, "_hint")
		if !reflect.DeepEqual(gmm, want) {
			gb, _ := json.MarshalIndent(gmm, "", " ")
			wb, _ := json.MarshalIndent(want, "", " ")
			t.Errorf("%s offset=%d limit=%d\n got: %s\nwant: %s", c.Name, c.Offset, c.Limit, clipBytes(gb), clipBytes(wb))
		}
	}
}

func clipBytes(b []byte) string {
	if len(b) > 900 {
		return string(b[:900]) + "…"
	}
	return string(b)
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestPatchReplaceMatchesPython(t *testing.T) {
	g := loadGolden(t)
	for _, c := range g.Patches {
		dir := materialize(t, g)
		env := tools.CallEnv{CWD: dir, SessionID: "patch-golden"}
		res := patchReplace(env, filepath.Join(dir, c.Name), c.Old, c.New, c.ReplaceAll)
		got := decodeJSON(t, obj(patchResultFields(res)...))
		got = fixDir(got, dir).(map[string]any)
		delete(c.Result, "lint") // linters are not ported
		if !reflect.DeepEqual(got, c.Result) {
			gb, _ := json.MarshalIndent(got, "", " ")
			wb, _ := json.MarshalIndent(c.Result, "", " ")
			t.Errorf("%s %q -> %q\n got: %s\nwant: %s", c.Name, c.Old, c.New, clipBytes(gb), clipBytes(wb))
		}
		after, err := os.ReadFile(filepath.Join(dir, c.Name))
		var gotAfter *string
		if err == nil {
			s := base64.StdEncoding.EncodeToString(after)
			gotAfter = &s
		}
		if (gotAfter == nil) != (c.After == nil) || (gotAfter != nil && *gotAfter != *c.After) {
			t.Errorf("%s: file after the patch differs from Python's", c.Name)
		}
	}
}

var _ = context.Background
