"""Golden fixtures for the Go read_file and patch(replace) tools, from the real Python ShellFileOperations on a local
environment (native read path).

usage: python scripts/port/gen_files_golden.py internal/tools/filetools/testdata
"""
import base64
import json
import os
import pathlib
import shutil
import sys
import tempfile

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[2]))

from tools.environments.local import LocalEnvironment  # noqa: E402
from tools.file_operations import ShellFileOperations  # noqa: E402

FILES = {
    "empty.txt": b"",
    "one.txt": b"only line\n",
    "nonl.txt": b"a\nb\nc",
    "three.txt": b"one\ntwo\nthree\n",
    "crlf.txt": b"a\r\nb\r\nc\r\n",
    "bom.txt": "﻿bom first\nsecond\n".encode(),
    "blank.txt": b"a\n\n\nb\n\n",
    "unicode.txt": "héllo wörld\n日本語\nemoji 😀\n".encode(),
    "long.txt": ("x" * 3000 + "\nshort\n").encode(),
    "many.txt": "".join(f"line {i}\n" for i in range(1, 2601)).encode(),
    "nul.raw": b"abc\x00def",
    "badutf8.txt": b"ok line\n\xff\xfe broken\n",
    "png.raw": b"\x89PNG\r\n\x1a\n" + b"\x00" * 50,
    "conflict.txt": b"a\n<<<<<<< HEAD\nx\n=======\ny\n>>>>>>> branch\nb\n",
    "cfg.yaml": b"key: value\n",
    "config.yml": b"key: value\n",
}
READS = [("empty.txt", 1, 2000), ("one.txt", 1, 2000), ("nonl.txt", 1, 2000), ("three.txt", 1, 2), ("three.txt", 2, 5), ("three.txt", 3, 1), ("three.txt", 4, 10),
         ("three.txt", 9, 10), ("crlf.txt", 1, 2000), ("bom.txt", 1, 2000), ("bom.txt", 2, 5), ("blank.txt", 1, 2000), ("unicode.txt", 1, 2000),
         ("long.txt", 1, 2000), ("many.txt", 1, 2000), ("many.txt", 2001, 2000), ("many.txt", 2590, 100), ("many.txt", 1, 5), ("nul.raw", 1, 10),
         ("badutf8.txt", 1, 10), ("png.raw", 1, 10), ("conflict.txt", 1, 2000), ("missing.txt", 1, 10), ("config.yaml", 1, 10), ("cfg.yml", 1, 10)]

PATCHES = [
    ("three.txt", "two", "TWO", False), ("three.txt", "nothing", "x", False), ("three.txt", "one\ntwo", "1\n2", False),
    ("crlf.txt", "b", "B", False), ("crlf.txt", "a\nb", "A\nB", False), ("bom.txt", "bom first", "BOM first", False),
    ("blank.txt", "a", "A", False), ("blank.txt", "b", "B", True), ("unicode.txt", "héllo wörld", "hello world", False),
    ("three.txt", "TWO", "two", False), ("three.txt", "two", "two", False), ("three.txt", "t", "T", False), ("three.txt", "t", "T", True),
    ("nonl.txt", "c", "C", False), ("many.txt", "line 500", "LINE 500", False), ("missing.txt", "a", "b", False),
    ("three.txt", "  two", "  2", False), ("three.txt", "one\n  two", "1\n  2", False),
]


def main(outdir):
    out = pathlib.Path(outdir)
    out.mkdir(parents=True, exist_ok=True)
    d = tempfile.mkdtemp()
    real = os.path.realpath(d)
    try:
        for name, data in FILES.items():
            (pathlib.Path(d) / name).write_bytes(data)
        env = LocalEnvironment(cwd=d, timeout=30)
        ops = ShellFileOperations(env, cwd=d)
        fix = lambda s: s.replace(real, "<DIR>").replace(d, "<DIR>")  # noqa: E731
        reads = []
        for name, offset, limit in READS:
            r = ops.read_file(os.path.join(d, name), offset, limit).to_dict()
            reads.append({"name": name, "offset": offset, "limit": limit, "result": json.loads(fix(json.dumps(r, ensure_ascii=True)))})
        patches = []
        for name, old, new, all_ in PATCHES:
            path = os.path.join(d, name)
            before = pathlib.Path(path).read_bytes() if os.path.exists(path) else None
            res = ops.patch_replace(path, old, new, all_).to_dict()
            after = pathlib.Path(path).read_bytes() if os.path.exists(path) else None
            patches.append({"name": name, "old": old, "new": new, "replace_all": all_,
                            "result": json.loads(fix(json.dumps(res, ensure_ascii=True))),
                            "after": base64.b64encode(after).decode() if after is not None else None})
            if before is not None:
                pathlib.Path(path).write_bytes(before)  # each case starts from the pristine file
        files = {k: base64.b64encode(v).decode() for k, v in FILES.items()}
        (out / "files.json").write_text(json.dumps({"files": files, "reads": reads, "patches": patches}, ensure_ascii=True, indent=0) + "\n")
        print(f"{len(reads)} reads, {len(patches)} patches")
    finally:
        shutil.rmtree(d, ignore_errors=True)


if __name__ == "__main__":
    main(sys.argv[1])
