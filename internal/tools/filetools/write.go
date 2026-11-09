package filetools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/phzietsman/hermes-agent/internal/fuzzy"
	"github.com/phzietsman/hermes-agent/internal/tools"
)

// hermesConfigPath is the Hermes config file: approvals.mode and other security settings live there, so an agent
// that could edit it could silently disable exec approval.
func hermesConfigPath(env tools.CallEnv) string {
	if env.Home == "" {
		return ""
	}
	return realpath(filepath.Join(env.Home, "config.yaml"))
}

// writeGuards runs the shared write and patch guards in order and returns the first refusal: hard denies first,
// then anything that could prompt the user (one prompt covers every path of a multi-file patch).
func writeGuards(ctx context.Context, env tools.CallEnv, g guard, paths []string) string {
	type target struct{ display, real string }
	var ts []target
	for _, p := range paths {
		_, real := resolve(env, p)
		ts = append(ts, target{p, real})
	}
	for _, t := range ts {
		if msg := g.sensitivePath(t.display, t.real, hermesConfigPath(env)); msg != "" {
			return msg
		}
		if msg := g.writeDenied(t.display, t.real); msg != "" {
			return msg
		}
	}
	var protected []string
	for _, t := range ts {
		if name := g.protectedInstructionTarget(t.display, t.real); name != "" {
			protected = append(protected, name)
		}
	}
	if len(protected) > 0 {
		if env.Gate == nil {
			return "BLOCKED: write to protected agent-instruction file(s) (" + strings.Join(protected, ", ") + ") requires approval but the approval subsystem is unavailable. " +
				"The user has NOT consented to this write. Do NOT retry it or attempt the same edit via another path (terminal, execute_code, etc.)."
		}
		if msg := env.Gate.ProtectedWrite(ctx, protected, env.Approval); msg != "" {
			return msg
		}
	}
	var ssh []string
	for _, t := range ts {
		if t.real == filepath.Join(g.home, ".ssh", "config") {
			ssh = append(ssh, t.display)
		}
	}
	if len(ssh) > 0 {
		list := strings.Join(ssh, ", ")
		if env.Gate == nil {
			return "BLOCKED: write to SSH config file(s) (" + list + ") requires approval but the approval subsystem is unavailable. Do NOT retry it via another path (terminal, execute_code) without the user's explicit consent."
		}
		desc := fmt.Sprintf("Write to SSH client config file(s): %s. The SSH config can carry ProxyCommand / Match exec directives that run commands, so writes require your approval.", list)
		if msg := env.Gate.ActionApproval(ctx, "ssh_config_write", desc, "<write to "+list+">", env.Approval); msg != "" {
			return msg
		}
	}
	return ""
}

func errorResult(msg string) (string, error) { return tools.Error(msg), nil }

func writeFile(ctx context.Context, env tools.CallEnv, g guard, args tools.Args) (string, error) {
	path := args.String("path", "")
	if path == "" {
		return errorResult("write_file: missing required field 'path'. Re-emit the tool call with both 'path' and 'content' set.")
	}
	raw, present := args["content"]
	if !present {
		return errorResult("write_file: missing required field 'content'. The tool call included a " +
			"path but no content argument — this is almost always a dropped-arg bug " +
			"under context pressure. Re-emit the tool call with the full content " +
			"payload.")
	}
	content, isString := raw.(string)
	if !isString {
		return errorResult(fmt.Sprintf("write_file: 'content' must be a string, got %T.", raw))
	}
	if msg := writeGuards(ctx, env, g, []string{path}); msg != "" {
		return errorResult(msg)
	}
	if looksLikeReadOutput(content) {
		return errorResult("Refusing to write internal read_file display text as file content. " +
			"Strip read_file line-number prefixes or reconstruct the intended file contents before writing.")
	}
	abs, real := resolve(env, path)
	files := filesFor(env.SessionID)
	if msg := files.staleOverwrite(path, real); msg != "" {
		return obj(f("error", "Refusing to overwrite "+path+": "+msg+" The file was NOT modified. Reload the current contents with read_file "+
			"(every page, for a file that needs offset/limit), merge the requested "+
			"change, then call write_file again. For small edits, prefer patch so "+
			"existing unrelated changes are preserved."), f("stale_write_blocked", true), f("path", path), f("resolved_path", real)), nil
	}
	ext := strings.ToLower(filepath.Ext(abs))
	if ext == ".json" {
		if e := syntaxError(ext, content); e != "" {
			return errorResult(fmt.Sprintf("Refusing to write '%s': candidate content fails %s syntax validation (%s). The file was NOT created or modified. Fix the content and retry.", abs, ext, e))
		}
	}
	warning := files.staleWarning(path, real)
	if err := writeText(abs, content); err != nil {
		return errorResult(err.Error())
	}
	written, _ := os.ReadFile(abs)
	verified := sha(string(written)) == sha(normalizeForCompare(content, string(written)))
	fields := []tools.Field{f("bytes_written", len(written)), f("dirs_created", filepath.Dir(abs) != ""), f("verified", verified)}
	if warning != "" {
		fields = append(fields, f("_warning", warning))
	}
	fields = append(fields, f("resolved_path", real), f("files_modified", []string{real}))
	files.noteWrite(real, string(written), false)
	return obj(fields...), nil
}

// normalizeForCompare maps content to what writeText produced for it (BOM and CRLF are restored from the file).
func normalizeForCompare(content, onDisk string) string {
	if strings.HasPrefix(onDisk, "\ufeff") && !strings.HasPrefix(content, "\ufeff") {
		content = "\ufeff" + content
	}
	if detectLineEnding(onDisk[:min(len(onDisk), 4096)]) == "\r\n" {
		content = normalizeLineEndings(content, "\r\n")
	}
	return content
}

var (
	v4aSingleHeader = regexp.MustCompile(`(?m)^\*\*\*\s*(Update|Add|Delete)\s+File:\s*(.+)`)
	v4aMoveHeader   = regexp.MustCompile(`(?m)^\*\*\*\s*Move\s+File:\s*(.+?)\s*->\s*(.+)`)
)

func hasTraversal(p string) bool {
	for _, part := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return true
		}
	}
	return false
}

// v4aHeaderPaths lists every path a V4A patch names, rejecting ".." traversal: header paths come from patch
// content, which is more attacker-influenceable than path=.
func v4aHeaderPaths(patch string) ([]string, string) {
	var paths []string
	for _, m := range v4aSingleHeader.FindAllStringSubmatch(patch, -1) {
		paths = append(paths, strings.TrimSpace(m[2]))
	}
	for _, m := range v4aMoveHeader.FindAllStringSubmatch(patch, -1) {
		paths = append(paths, strings.TrimSpace(m[1]), strings.TrimSpace(m[2]))
	}
	for _, p := range paths {
		if hasTraversal(p) {
			return nil, fmt.Sprintf("V4A patch header contains '..' traversal: %s. Use the agent's cwd-relative path (no '..') or an absolute "+
				"path in '*** Update File:' / '*** Add File:' / '*** Delete File:' / '*** Move File:' headers.", pyRepr(p))
		}
	}
	return paths, ""
}

func patchResultFields(r PatchResult) []tools.Field {
	fields := []tools.Field{f("success", r.Success)}
	if r.NoChange {
		fields = append(fields, f("no_change", true))
	}
	if r.Note != "" {
		fields = append(fields, f("note", r.Note))
	}
	if r.Diff != "" {
		fields = append(fields, f("diff", r.Diff))
	}
	if len(r.FilesModified) > 0 {
		fields = append(fields, f("files_modified", r.FilesModified))
	}
	if len(r.FilesCreated) > 0 {
		fields = append(fields, f("files_created", r.FilesCreated))
	}
	if len(r.FilesDeleted) > 0 {
		fields = append(fields, f("files_deleted", r.FilesDeleted))
	}
	if r.Error != "" {
		fields = append(fields, f("error", r.Error))
	}
	return fields
}

func patchReplace(env tools.CallEnv, path, oldString, newString string, replaceAll bool) PatchResult {
	fs := &osFS{env: env}
	abs := fs.abs(path)
	content, err := fs.ReadRaw(path)
	if err != nil {
		if isNotFound(err) {
			return PatchResult{Error: fmt.Sprintf("Failed to read file: %s", abs)}
		}
		return PatchResult{Error: err.Error()}
	}
	r := fuzzy.FindAndReplace(content, oldString, newString, replaceAll)
	if r.Error != "" || r.Count == 0 {
		if fuzzy.IsAlreadyApplied(content, oldString, newString) {
			return PatchResult{Success: true, NoChange: true, Note: fmt.Sprintf("File already contains the target text — the edit "+
				"appears to be already applied to %s. No write performed; do not re-send this patch.", abs)}
		}
		msg := r.Error
		if msg == "" {
			msg = "Could not find match for old_string in " + abs
		}
		return PatchResult{Error: msg + fuzzy.NoMatchHint(msg, r.Count, oldString, content)}
	}
	newContent := r.Content
	raw, _ := os.ReadFile(abs)
	if ending := detectLineEnding(content); ending != "" {
		newContent = normalizeLineEndings(newContent, ending)
	} else if detectLineEnding(string(raw[:min(len(raw), 4096)])) == "\r\n" {
		newContent = normalizeLineEndings(newContent, "\r\n")
	}
	if err := writeText(abs, newContent); err != nil {
		return PatchResult{Error: "Failed to write changes: " + err.Error()}
	}
	back, err := os.ReadFile(abs)
	if err != nil {
		return PatchResult{Error: "Post-write verification failed: could not re-read " + abs}
	}
	onDisk := strings.ReplaceAll(strings.ReplaceAll(strings.TrimPrefix(string(back), "\ufeff"), "\r\n", "\n"), "\r", "\n")
	intended := strings.ReplaceAll(strings.ReplaceAll(newContent, "\r\n", "\n"), "\r", "\n")
	if onDisk != intended {
		return PatchResult{Error: fmt.Sprintf("Post-write verification failed for %s: on-disk content differs from intended write (wrote %d chars, read back %d chars after normalizing line endings). "+
			"The patch did not persist. Re-read the file and try again.", abs, len([]rune(intended)), len([]rune(onDisk)))}
	}
	return PatchResult{Success: true, Diff: unifiedDiff(abs, content, &newContent), FilesModified: []string{abs},
		Writes: []FileWrite{{abs, sha(string(raw)), sha(string(back))}}}
}

func patchTool(ctx context.Context, env tools.CallEnv, g guard, args tools.Args) (string, error) {
	mode := args.String("mode", "replace")
	path := args.String("path", "")
	patchText := args.String("patch", "")
	var paths []string
	if path != "" {
		paths = append(paths, path)
	}
	if mode == "patch" && patchText != "" {
		hp, bad := v4aHeaderPaths(patchText)
		if bad != "" {
			return errorResult(bad)
		}
		paths = append(paths, hp...)
	}
	if msg := writeGuards(ctx, env, g, paths); msg != "" {
		return errorResult(msg)
	}
	files := filesFor(env.SessionID)
	var warnings []string
	resolved := map[string]string{}
	for _, p := range paths {
		_, real := resolve(env, p)
		resolved[p] = real
		if w := files.staleWarning(p, real); w != "" {
			warnings = append(warnings, w)
		}
	}
	var res PatchResult
	switch mode {
	case "replace":
		if path == "" {
			return errorResult("path required")
		}
		oldS, hasOld := args["old_string"].(string)
		newS, hasNew := args["new_string"].(string)
		if !hasOld || !hasNew {
			return errorResult("old_string and new_string required")
		}
		if msg := g.writeDenied(path, resolved[path]); msg != "" {
			return obj(patchResultFields(PatchResult{Error: msg})...), nil
		}
		res = patchReplace(env, path, oldS, newS, args.Bool("replace_all", false))
	case "patch":
		if patchText == "" {
			return errorResult("patch content required")
		}
		res = ApplyV4A(patchText, &osFS{env: env}, sha)
	default:
		return errorResult("Unknown mode: " + mode)
	}
	fields := patchResultFields(res)
	if len(warnings) > 0 {
		fields = append(fields, f("_warning", strings.Join(warnings, " | ")))
	}
	if res.Error == "" {
		var modified []string
		for _, p := range paths {
			modified = append(modified, resolved[p])
		}
		fields = append(fields, f("files_modified", modified))
		if len(modified) == 1 {
			fields = append(fields, f("resolved_path", modified[0]))
		}
		for _, p := range paths {
			if data, err := os.ReadFile(resolved[p]); err == nil {
				// A patch applied on top of a full view keeps the baseline; otherwise the next whole-file
				// overwrite needs a fresh read.
				known := files.hasBaseline(resolved[p])
				files.noteWrite(resolved[p], string(data), !known)
				files.resetPatchFailures(resolved[p])
			}
		}
	} else if strings.Contains(res.Error, "Could not find") {
		count := 0
		if mode == "replace" && path != "" {
			count = files.recordPatchFailure(resolved[path])
		}
		switch {
		case count >= 3:
			fields = append(fields, f("_hint", fmt.Sprintf("This is failure #%d patching %s. Stop retrying with variations of the same old_string. "+
				"Either: (1) re-read the file fresh to verify current content, (2) use a longer / more unique old_string with "+
				"surrounding context lines, or (3) re-read the file in full, then use write_file to replace it if the targeted "+
				"region is hard to anchor.", count, pyRepr(path))))
		case !strings.Contains(res.Error, "Did you mean one of these sections?"):
			fields = append(fields, f("_hint", "old_string not found. Use read_file to verify the current content, or search_files to locate the text."))
		}
	}
	return obj(fields...), nil
}
