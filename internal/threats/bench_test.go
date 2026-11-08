package threats

import (
	"strings"
	"testing"
)

// A realistic AGENTS.md-sized input: prose, code and headings, up to the scan cap.
var benignDoc = strings.Repeat("## Section\n\nRun the tests with `go test ./...` before committing. Keep functions small.\n\n```go\nfunc main() { fmt.Println(\"hi\") }\n```\n\n", 700)

func BenchmarkScanContext64K(b *testing.B) {
	doc := benignDoc[:MaxScanChars]
	b.SetBytes(int64(len(doc)))
	for i := 0; i < b.N; i++ {
		if _, err := Scan(doc, ScopeContext); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkScanStrict64K(b *testing.B) {
	doc := benignDoc[:MaxScanChars]
	b.SetBytes(int64(len(doc)))
	for i := 0; i < b.N; i++ {
		if _, err := Scan(doc, ScopeStrict); err != nil {
			b.Fatal(err)
		}
	}
}
