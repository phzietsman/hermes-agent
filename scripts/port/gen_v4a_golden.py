"""Golden fixtures for the Go V4A patch engine, from the real Python parser and applier over an in-memory store.

usage: python scripts/port/gen_v4a_golden.py internal/tools/filetools/testdata
"""
import json
import pathlib
import sys
from types import SimpleNamespace

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[2]))

from tools.patch_parser import apply_v4a_operations, parse_v4a_patch  # noqa: E402

PY = "def a():\n    return 1\n\n\ndef b():\n    return 2\n"
TXT = "line one\nline two\nline three\n"


class MemFS:
    def __init__(self, files):
        self.files = dict(files)

    def read_file_raw(self, path):
        if path in self.files:
            return SimpleNamespace(error=None, content=self.files[path], not_found=False)
        return SimpleNamespace(error=f"File not found: {path}", content="", not_found=True)

    def write_file(self, path, content, pre_content=None):
        self.files[path] = content
        return SimpleNamespace(error=None)

    def delete_file(self, path):
        self.files.pop(path, None)
        return SimpleNamespace(error=None)

    def move_file(self, src, dst):
        self.files[dst] = self.files.pop(src)
        return SimpleNamespace(error=None)


CASES = [
    ("update_simple", {"a.py": PY}, "*** Begin Patch\n*** Update File: a.py\n@@ def a @@\n def a():\n-    return 1\n+    return 10\n*** End Patch"),
    ("update_two_hunks", {"a.py": PY}, "*** Begin Patch\n*** Update File: a.py\n@@\n-    return 1\n+    return 10\n@@\n-    return 2\n+    return 20\n*** End Patch"),
    ("update_hunk_not_found", {"a.py": PY}, "*** Begin Patch\n*** Update File: a.py\n@@\n-    return 99\n+    return 1\n*** End Patch"),
    ("update_missing_file", {}, "*** Begin Patch\n*** Update File: nope.py\n@@\n-x\n+y\n*** End Patch"),
    ("update_no_hunks", {"a.py": PY}, "*** Begin Patch\n*** Update File: a.py\n*** End Patch"),
    ("update_context_only", {"a.py": PY}, "*** Begin Patch\n*** Update File: a.py\n@@\n def a():\n     return 1\n*** End Patch"),
    ("update_already_applied", {"a.py": PY}, "*** Begin Patch\n*** Update File: a.py\n@@\n-    return 99999\n+    return 1\n*** End Patch"),
    ("update_addition_only_with_hint", {"a.py": PY}, "*** Begin Patch\n*** Update File: a.py\n@@ def a():\n+    # added\n*** End Patch"),
    ("update_addition_only_no_hint", {"a.py": PY}, "*** Begin Patch\n*** Update File: a.py\n@@\n+# trailing\n*** End Patch"),
    ("update_addition_ambiguous_hint", {"a.py": "x\nx\n"}, "*** Begin Patch\n*** Update File: a.py\n@@ x @@\n+y\n*** End Patch"),
    ("update_hint_missing", {"a.py": PY}, "*** Begin Patch\n*** Update File: a.py\n@@ nothing here @@\n+y\n*** End Patch"),
    ("update_fuzzy_indent", {"a.py": PY}, "*** Begin Patch\n*** Update File: a.py\n@@\n-  return 1\n+  return 11\n*** End Patch"),
    ("update_crlf_patch", {"a.py": PY}, "*** Begin Patch\r\n*** Update File: a.py\r\n@@\r\n-    return 1\r\n+    return 12\r\n*** End Patch\r\n"),
    ("add_file", {}, "*** Begin Patch\n*** Add File: new.txt\n+hello\n+world\n*** End Patch"),
    ("add_existing", {"new.txt": "x"}, "*** Begin Patch\n*** Add File: new.txt\n+hello\n*** End Patch"),
    ("delete_file", {"t.txt": TXT}, "*** Begin Patch\n*** Delete File: t.txt\n*** End Patch"),
    ("delete_missing", {}, "*** Begin Patch\n*** Delete File: t.txt\n*** End Patch"),
    ("move_file", {"t.txt": TXT}, "*** Begin Patch\n*** Move File: t.txt -> u.txt\n*** End Patch"),
    ("move_onto_existing", {"t.txt": TXT, "u.txt": "x"}, "*** Begin Patch\n*** Move File: t.txt -> u.txt\n*** End Patch"),
    ("move_missing_src", {}, "*** Begin Patch\n*** Move File: t.txt -> u.txt\n*** End Patch"),
    ("move_then_update", {"t.txt": TXT}, "*** Begin Patch\n*** Move File: t.txt -> u.txt\n*** Update File: u.txt\n@@\n-line two\n+line 2\n*** End Patch"),
    ("delete_then_add", {"t.txt": TXT}, "*** Begin Patch\n*** Delete File: t.txt\n*** Add File: t.txt\n+fresh\n*** End Patch"),
    ("multi_file", {"a.py": PY, "t.txt": TXT}, "*** Begin Patch\n*** Update File: a.py\n@@\n-    return 2\n+    return 22\n*** Update File: t.txt\n@@\n-line one\n+line 1\n*** Add File: z.txt\n+z\n*** End Patch"),
    ("multi_file_one_bad", {"a.py": PY, "t.txt": TXT}, "*** Begin Patch\n*** Update File: a.py\n@@\n-    return 2\n+    return 22\n*** Update File: t.txt\n@@\n-absent\n+x\n*** End Patch"),
    ("empty_patch", {"a.py": PY}, "*** Begin Patch\n*** End Patch"),
    ("no_markers", {"a.py": PY}, "*** Update File: a.py\n@@\n-    return 1\n+    return 5\n"),
    ("move_missing_dest", {"t.txt": TXT}, "*** Begin Patch\n*** Move File: t.txt ->\n*** End Patch"),
    ("implicit_context_line", {"a.py": PY}, "*** Begin Patch\n*** Update File: a.py\n@@\ndef a():\n-    return 1\n+    return 3\n*** End Patch"),
    ("no_newline_marker", {"a.py": PY}, "*** Begin Patch\n*** Update File: a.py\n@@\n-    return 1\n\\ No newline at end of file\n+    return 4\n*** End Patch"),
    ("content_mentions_markers", {"a.txt": "x\n"}, "*** Begin Patch\n*** Update File: a.txt\n@@\n-x\n+*** End Patch\n*** End Patch"),
    ("unicode_update", {"u.txt": "héllo — wörld\nsecond\n"}, "*** Begin Patch\n*** Update File: u.txt\n@@\n-héllo — wörld\n+hello world\n*** End Patch"),
]


def main(outdir):
    out = pathlib.Path(outdir)
    out.mkdir(parents=True, exist_ok=True)
    cases = []
    for name, files, patch in CASES:
        ops, perr = parse_v4a_patch(patch)
        if perr:
            res = {"success": False, "error": f"Failed to parse patch: {perr}"}
            state = dict(files)
        else:
            fs = MemFS(files)
            res = apply_v4a_operations(ops, fs).to_dict()
            state = fs.files
        cases.append({"name": name, "files": files, "patch": patch, "result": res, "state": state})
    (out / "v4a.json").write_text(json.dumps(cases, indent=1, ensure_ascii=True) + "\n")
    print(f"{len(cases)} v4a cases")


if __name__ == "__main__":
    main(sys.argv[1])
