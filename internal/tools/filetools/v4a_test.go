package filetools

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

type memFS struct{ files map[string]string }

func (m *memFS) ReadRaw(p string) (string, error) {
	if c, ok := m.files[p]; ok {
		return c, nil
	}
	return "", &NotFoundError{p}
}
func (m *memFS) Write(p, c string) error { m.files[p] = c; return nil }
func (m *memFS) Delete(p string) error   { delete(m.files, p); return nil }
func (m *memFS) Move(s, d string) error  { m.files[d] = m.files[s]; delete(m.files, s); return nil }

// resultDict renders a PatchResult as Python's PatchResult.to_dict does: success first, then the optional flags and
// the non-empty fields in a fixed order.
func resultDict(r PatchResult) map[string]any {
	out := map[string]any{"success": r.Success}
	if r.NoChange {
		out["no_change"] = true
	}
	if r.Note != "" {
		out["note"] = r.Note
	}
	if r.Diff != "" {
		out["diff"] = r.Diff
	}
	for k, v := range map[string][]string{"files_modified": r.FilesModified, "files_created": r.FilesCreated, "files_deleted": r.FilesDeleted} {
		if len(v) > 0 {
			arr := make([]any, len(v))
			for i, s := range v {
				arr[i] = s
			}
			out[k] = arr
		}
	}
	if r.Error != "" {
		out["error"] = r.Error
	}
	return out
}

// Differential: parsing, validation, application, diffs and final file contents equal Python's on 31 patches.
func TestV4AMatchesPython(t *testing.T) {
	b, err := os.ReadFile("testdata/v4a.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name   string
		Files  map[string]string
		Patch  string
		Result map[string]any
		State  map[string]string
	}
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		fs := &memFS{files: map[string]string{}}
		for k, v := range c.Files {
			fs.files[k] = v
		}
		got := resultDict(ApplyV4A(c.Patch, fs, nil))
		if !reflect.DeepEqual(got, c.Result) {
			gb, _ := json.MarshalIndent(got, "", " ")
			wb, _ := json.MarshalIndent(c.Result, "", " ")
			t.Errorf("%s\n got: %s\nwant: %s", c.Name, gb, wb)
		}
		if !reflect.DeepEqual(fs.files, c.State) {
			t.Errorf("%s: files\n got: %q\nwant: %q", c.Name, fs.files, c.State)
		}
	}
}

func FuzzParseV4A(f *testing.F) {
	f.Add("*** Begin Patch\n*** Update File: a\n@@\n-x\n+y\n*** End Patch")
	f.Add("*** Add File: n\n+z")
	f.Add("@@@@\n\n+-+- \n*** Move File: a -> b")
	f.Fuzz(func(t *testing.T, patch string) {
		ops, err := parseV4A(patch)
		if err != nil && ops != nil {
			t.Fatal("operations returned with an error")
		}
		// Applying a parsed patch never panics and never leaves the store half-written when validation fails.
		fs := &memFS{files: map[string]string{"a": "x\n", "b": "b\n"}}
		before := map[string]string{"a": "x\n", "b": "b\n"}
		r := ApplyV4A(patch, fs, nil)
		if !r.Success && r.Error != "" && len(r.Error) > 0 && r.Error[:5] != "Apply" && !reflect.DeepEqual(fs.files, before) {
			t.Fatalf("a rejected patch changed files: %q", fs.files)
		}
	})
}
