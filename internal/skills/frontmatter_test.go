package skills

import (
	"reflect"
	"testing"
)

func TestParseFrontmatter(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		wantName string // frontmatter "name", "" when absent
		wantBody string
		wantKeys int
	}{
		{"valid", "---\nname: foo\ndescription: d\n---\nBody\n", "foo", "Body\n", 2},
		{"no fence", "name: foo\n", "", "name: foo\n", 0},
		{"no closing fence", "---\nname: foo\nbody", "", "---\nname: foo\nbody", 0},
		{"closing fence must be its own line", "---\nname: foo---\nbody", "", "---\nname: foo---\nbody", 0},
		{"BOM before the fence", "\ufeff---\nname: foo\n---\nBody", "foo", "Body", 1},
		{"crlf line endings", "---\r\nname: foo\r\n---\r\nBody", "foo", "Body", 1},
		{"empty frontmatter", "---\n---\nBody", "", "Body", 0},
		{"trailing spaces after closing fence", "---\nname: foo\n---   \nBody", "foo", "Body", 1},
		{"body keeps later fences", "---\nname: foo\n---\nA\n---\nB\n", "foo", "A\n---\nB\n", 1},
		{"yaml list is not a mapping", "---\n- a\n- b\n---\nBody", "", "Body", 0},
		{"yaml scalar is not a mapping", "---\njust text\n---\nBody", "", "Body", 0},
		{"empty input", "", "", "", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fm, body := ParseFrontmatter(tc.in)
			if body != tc.wantBody {
				t.Errorf("body = %q, want %q", body, tc.wantBody)
			}
			if got, _ := fm["name"].(string); got != tc.wantName {
				t.Errorf("name = %q, want %q", got, tc.wantName)
			}
			if len(fm) != tc.wantKeys {
				t.Errorf("keys = %d (%v), want %d", len(fm), fm, tc.wantKeys)
			}
		})
	}
}

func TestParseFrontmatterBOMMatchesPlain(t *testing.T) {
	plain, pb := ParseFrontmatter("---\nname: x\nplatforms: [macos]\n---\nBody")
	bom, bb := ParseFrontmatter("\ufeff---\nname: x\nplatforms: [macos]\n---\nBody")
	if !reflect.DeepEqual(plain, bom) || pb != bb {
		t.Fatalf("BOM changed the result: %v vs %v", plain, bom)
	}
	if !HostFilterFor("darwin").MatchesPlatform(bom) {
		t.Fatal("a BOM must not defeat platform gating")
	}
}

func TestParseFrontmatterMalformedYAMLFallsBackToLines(t *testing.T) {
	in := "---\nname: kept\ndescription: has: a colon and [unclosed\nextra: [oops\n---\nBody"
	fm, body := ParseFrontmatter(in)
	if body != "Body" {
		t.Fatalf("body = %q", body)
	}
	if fm["name"] != "kept" {
		t.Fatalf("name lost in the fallback: %v", fm)
	}
	if fm["description"] != "has: a colon and [unclosed" {
		t.Fatalf("description = %q (split at the first colon only)", fm["description"])
	}
}

func TestParseFrontmatterNestedAndTypes(t *testing.T) {
	fm, _ := ParseFrontmatter("---\nname: n\nplatforms: [macos, linux]\nmetadata:\n  hermes:\n    requires_tools: [terminal]\nversion: 3\nflag: true\n---\n")
	if !reflect.DeepEqual(stringList(fm["platforms"]), []string{"macos", "linux"}) {
		t.Errorf("platforms = %v", fm["platforms"])
	}
	if got := ExtractConditions(fm).RequiresTools; !reflect.DeepEqual(got, []string{"terminal"}) {
		t.Errorf("requires_tools = %v", got)
	}
}

func TestExtractConditions(t *testing.T) {
	if c := ExtractConditions(map[string]any{}); !reflect.DeepEqual(c, Conditions{}) {
		t.Errorf("absent = %+v", c)
	}
	if c := ExtractConditions(map[string]any{"metadata": "not a map"}); !reflect.DeepEqual(c, Conditions{}) {
		t.Errorf("malformed metadata = %+v", c)
	}
	fm, _ := ParseFrontmatter("---\nmetadata:\n  hermes:\n    fallback_for_toolsets: [web]\n    requires_toolsets: [terminal, file]\n    fallback_for_tools: [web_search]\n    requires_tools: lone\n    session_platforms: [telegram]\n---\n")
	want := Conditions{
		FallbackForToolsets: []string{"web"},
		RequiresToolsets:    []string{"terminal", "file"},
		FallbackForTools:    []string{"web_search"},
		RequiresTools:       []string{"lone"}, // a scalar is a one-item list
		SessionPlatforms:    []string{"telegram"},
	}
	if got := ExtractConditions(fm); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestExtractConfigVars(t *testing.T) {
	list, _ := ParseFrontmatter(`---
metadata:
  hermes:
    config:
      - key: wiki.path
        description: Where the wiki lives
        default: ~/wiki
        prompt: Wiki path?
      - key: wiki.path
        description: duplicate, ignored
      - key: no.description
      - description: no key
      - key: model
        description: Model to use
        prompt: "   "
      - not a mapping
---
`)
	want := []ConfigVar{
		{Key: "wiki.path", Description: "Where the wiki lives", Default: "~/wiki", Prompt: "Wiki path?"},
		{Key: "model", Description: "Model to use", Prompt: "Model to use"}, // blank prompt falls back to the description
	}
	if got := ExtractConfigVars(list); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}

	single, _ := ParseFrontmatter("---\nmetadata:\n  hermes:\n    config:\n      key: k\n      description: d\n---\n")
	if got := ExtractConfigVars(single); len(got) != 1 || got[0].Key != "k" {
		t.Errorf("single mapping = %+v", got)
	}
	if got := ExtractConfigVars(map[string]any{}); got != nil {
		t.Errorf("absent = %+v", got)
	}
}

func FuzzParseFrontmatter(f *testing.F) {
	for _, s := range []string{"---\nname: a\n---\nb", "\ufeff---\n---\n", "---", "---\n", "---\n:\n---\n", "---\n[\n---\n", "---\n\x00\n---\n", "----\n---\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		fm, body := ParseFrontmatter(in)
		if fm == nil {
			t.Fatal("frontmatter must never be nil")
		}
		if len(body) > len(in) {
			t.Fatalf("body longer than the input: %d > %d", len(body), len(in))
		}
		// None of the extractors may panic on whatever the parser produced.
		_ = ExtractConditions(fm)
		_ = ExtractConfigVars(fm)
		_ = HostFilter().Visible(fm)
	})
}
