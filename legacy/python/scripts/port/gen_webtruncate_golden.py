"""Golden fixtures for the Go web_extract truncation and base64-image cleaning, from the real Python helpers.

usage: python scripts/port/gen_webtruncate_golden.py internal/tools/webtools/testdata
The stored-path step is pinned to a fixed path so the footer text is comparable; it is a separate Go concern.
"""
import json
import pathlib
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[2]))

from tools import web_tools_truncate as t  # noqa: E402

STORED = "/home/u/.hermes/cache/web/example.com-0123456789.md"
B64 = "A" * 40 + "+/=="


def lines(n, w=30):
    return "\n".join(f"line {i:04d} " + "x" * w for i in range(n))


TRUNC = {
    "under_limit": (lines(5), 2000),
    "exactly_limit": ("a" * 2000, 2000),
    "over_by_one": ("a" * 2001, 2000),
    "many_lines": (lines(400), 5000),
    "no_newlines": ("b" * 9000, 3000),
    "long_head_line": ("h" * 2500 + "\n" + lines(300), 3000),
    "unicode": ("日本語のテキスト\n" * 900, 2000),
    "emoji_astral": ("😀 line\n" * 1200, 2000),
    "tail_newline_early": (lines(200) + "\n" + "t" * 40, 2500),
}
BASE64 = {
    "md_image_alt": f"see ![a diagram](data:image/png;base64,{B64}) done",
    "md_image_no_alt": f"![](data:image/jpeg;base64,{B64})",
    "md_image_whitespace_alt": f"![   ](data:image/gif;base64,{B64})",
    "paren_blob": f"x (data:image/png;base64,{B64}) y",
    "bare_blob": f"src=data:image/webp;base64,{B64} end",
    "http_image_kept": "![logo](https://example.com/a.png) text",
    "multiple": f"![a](data:image/png;base64,{B64}) and ![b](data:image/png;base64,{B64})",
    "multiline_blob": "![m](data:image/png;base64,AAAA\nBBBB\nCCCC==)",
    "not_base64": "data:text/plain;base64,AAAA",
}


def main(outdir):
    out = pathlib.Path(outdir)
    out.mkdir(parents=True, exist_ok=True)
    t._store_full_text = lambda url, content: STORED
    import tools.credential_files as cf
    cf.to_agent_visible_cache_path = lambda p: p
    trunc = []
    for name, (content, limit) in TRUNC.items():
        text, was = t._truncate_with_footer(content, "https://example.com/x", limit)
        trunc.append({"name": name, "content": content, "limit": limit, "stored": STORED, "text": text, "truncated": was})
    # the unstored footer variant
    t._store_full_text = lambda url, content: None
    text, was = t._truncate_with_footer(lines(400), "https://example.com/x", 5000)
    trunc.append({"name": "unstored_footer", "content": lines(400), "limit": 5000, "stored": "", "text": text, "truncated": was})
    b64 = [{"name": n, "in": s, "out": t.convert_base64_images_to_links(s)} for n, s in BASE64.items()]
    clamp = [{"in": v, "out": t._clamp_or_default(v)} for v in (None, 2000, 1, 499999, 500001, 15000, "7000", "x", 2000.9)]
    (out / "truncate.json").write_text(json.dumps({"truncate": trunc, "base64": b64, "clamp": clamp}, indent=1, ensure_ascii=False, sort_keys=True) + "\n")
    print(f"wrote {len(trunc)} truncate, {len(b64)} base64, {len(clamp)} clamp cases")


if __name__ == "__main__":
    main(sys.argv[1])
