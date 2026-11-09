package filetools

import (
	"context"

	"github.com/phzietsman/hermes-agent/internal/tools"
)

const readDescription = "Read a text file with line numbers and pagination. Use this instead of cat/head/tail in terminal. Output format: 'LINE_NUM|CONTENT'. " +
	"Suggests similar filenames if not found. Use offset and limit for large files. Reads exceeding ~100K characters are truncated on a line boundary " +
	"and return a next_offset; continue with offset to read the rest. Cannot read images/binary files."

const writeDescription = "Write content to a file, completely replacing existing content. Use this instead of echo/cat heredoc in terminal. " +
	"Creates parent directories automatically. OVERWRITES the entire file — use 'patch' for targeted edits. For an EXISTING file, call read_file first: " +
	"write_file refuses (file untouched) when this task has no current full read/write of the file or the file changed on disk since; on refusal, " +
	"read_file, merge, then retry. JSON content that does not parse is refused. The result's verified:true means the on-disk content hash was confirmed — " +
	"do NOT re-read the file to check the write landed."

const patchDescription = "Targeted find-and-replace edits in files. Use this instead of sed/awk in terminal. " +
	"Uses fuzzy matching (9 strategies) so minor whitespace/indentation differences won't break it. " +
	"Returns a unified diff. Finds a unique string and replaces it."

func schema(props map[string]any, required ...string) map[string]any {
	req := make([]any, len(required))
	for i, r := range required {
		req[i] = r
	}
	return map[string]any{"type": "object", "properties": props, "required": req}
}

// Register adds the file tools to r.
func Register(r *tools.Registry) {
	r.MustRegister(
		tools.Tool{
			Name: "read_file", Toolset: "file", Description: readDescription, Parallel: true, MaxResultChars: 1_000_000, // self-limited to ~100K characters of content
			Parameters: schema(map[string]any{
				"path":   map[string]any{"type": "string", "description": "Path to the file to read (absolute, relative, or ~/path)"},
				"offset": map[string]any{"type": "integer", "description": "Line number to start reading from (1-indexed, default: 1)", "default": 1, "minimum": 1},
				"limit": map[string]any{"type": "integer", "description": "Maximum number of lines to read (default: 2000, max: 2000). Reads are additionally capped at a ~100K-character budget with a next_offset continuation.",
					"default": defaultReadLimit, "maximum": 2000},
			}, "path"),
			Handler: func(ctx context.Context, a tools.Args) (string, error) {
				env := tools.Env(ctx)
				return readFile(env, newGuard(env.Home), a)
			},
		},
		tools.Tool{
			Name: "write_file", Toolset: "file", Description: writeDescription, MaxResultChars: 100_000,
			Parameters: schema(map[string]any{
				"path":    map[string]any{"type": "string", "description": "Path to the file to write (will be created if it doesn't exist, overwritten if it does)"},
				"content": map[string]any{"type": "string", "description": "Complete content to write to the file"},
			}, "path", "content"),
			Handler: func(ctx context.Context, a tools.Args) (string, error) {
				env := tools.Env(ctx)
				return writeFile(ctx, env, newGuard(env.Home), a)
			},
		},
		tools.Tool{
			Name: "patch", Toolset: "file", Description: patchDescription, MaxResultChars: 100_000,
			Parameters: schema(map[string]any{
				"path":        map[string]any{"type": "string", "description": "File path to edit."},
				"old_string":  map[string]any{"type": "string", "description": "Exact text to find and replace. Must be unique in the file unless replace_all=true. Include surrounding context lines to ensure uniqueness."},
				"new_string":  map[string]any{"type": "string", "description": "Changed replacement text; it must differ from old_string. Pass empty string '' to delete the matched text."},
				"replace_all": map[string]any{"type": "boolean", "description": "Replace all occurrences instead of requiring a unique match (default: false)", "default": false},
			}, "path", "old_string", "new_string"),
			Handler: func(ctx context.Context, a tools.Args) (string, error) {
				env := tools.Env(ctx)
				return patchTool(ctx, env, newGuard(env.Home), a)
			},
		},
	)
}
