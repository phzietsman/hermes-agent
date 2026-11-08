"""Golden fixtures for internal/fuzzy: difflib ratios, opcodes and unified diffs, and fuzzy_find_and_replace.

usage: python scripts/port/gen_fuzzy_golden.py internal/fuzzy/testdata
"""
import difflib
import json
import pathlib
import random
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[2]))

from tools import fuzzy_match as fm  # noqa: E402

R = random.Random(20260315)
ALPHA = "abcde fgh\n\t  ij_(){}\"'\\—–“”…"


def rand_text(n, alphabet=ALPHA):
    return "".join(R.choice(alphabet) for _ in range(n))


def matcher_cases():
    out = []
    pairs = [("", ""), ("a", ""), ("", "a"), ("abcd", "abcd"), ("abcd", "bcde"), ("private Thread currentThread;", "private volatile Thread currentThread;"),
             ("the quick brown fox", "the quick brown dog"), ("x" * 300, "x" * 299 + "y"), ("ab" * 150, "ba" * 150)]
    for _ in range(60):
        n, m = R.randint(0, 60), R.randint(0, 60)
        a = rand_text(n, "abc \n")
        b = rand_text(m, "abc \n")
        pairs.append((a, b))
    for _ in range(10):  # long enough to trigger autojunk (b >= 200)
        a = rand_text(R.randint(150, 400), "ab c\n")
        b = rand_text(R.randint(200, 500), "ab c\n")
        pairs.append((a, b))
    for a, b in pairs:
        sm = difflib.SequenceMatcher(None, a, b)
        out.append({"a": a, "b": b, "ratio": sm.ratio(), "opcodes": [list(o) for o in sm.get_opcodes()],
                    "blocks": [list(t) for t in sm.get_matching_blocks()]})
    return out


def diff_cases():
    out = []
    docs = [("", "a\n"), ("a\n", ""), ("a\nb\nc\n", "a\nB\nc\n"), ("a\nb\nc", "a\nb\nc\nd"), ("same\n", "same\n")]
    for _ in range(40):
        a = [R.choice(["x", "y", "", "foo", "bar baz", "}", "{"]) + "\n" for _ in range(R.randint(0, 25))]
        b = list(a)
        for _ in range(R.randint(0, 6)):
            op = R.choice(["ins", "del", "mod"])
            if op == "ins":
                b.insert(R.randint(0, len(b)), R.choice(["new", "other"]) + "\n")
            elif op == "del" and b:
                b.pop(R.randrange(len(b)))
            elif b:
                b[R.randrange(len(b))] = "changed\n"
        docs.append(("".join(a), "".join(b)))
    for old, new in docs:
        d = "".join(difflib.unified_diff(old.splitlines(keepends=True), new.splitlines(keepends=True), fromfile="a/f", tofile="b/f"))
        out.append({"old": old, "new": new, "diff": d})
    return out


CONTENT = """def alpha():
    x = 1
    return x


def beta(a, b):
    total = a + b
    # comment — with “smart” quotes
    return total


class Gamma:
    def run(self):
\tprint("tab indented")
        return None
"""

FR_CASES = [
    (CONTENT, "    x = 1\n    return x", "    x = 2\n    return x", False),
    (CONTENT, "x = 1\nreturn x", "x = 2\nreturn x", False),
    (CONTENT, "def alpha():\n  x = 1\n  return x", "def alpha():\n  x = 9\n  return x", False),
    (CONTENT, "total = a + b", "total = a - b", False),
    (CONTENT, "total   =   a  +  b", "total = a * b", False),
    (CONTENT, "return", "yield", False),
    (CONTENT, "return", "yield", True),
    (CONTENT, "missing text", "x", False),
    (CONTENT, "", "x", False),
    (CONTENT, "   ", "x", False),
    (CONTENT, "same", "same", False),
    (CONTENT, "# comment - with \"smart\" quotes", "# note - with \"plain\" quotes", False),
    (CONTENT, "# comment — with “smart” quotes", "# remark — with “smart” quotes", False),
    (CONTENT, "print(\"tab indented\")", "print('x')", False),
    (CONTENT, "def alpha():\\n    x = 1", "def alpha():\\n    x = 5", False),
    (CONTENT, "def beta(a, b):\n    totl = a + b\n    # comment — with “smart” quotes\n    return total", "def beta(a, b):\n    total = a * b\n    return total", False),
    (CONTENT, "def beta(a, b):\n    XXXXX\n    return total", "def beta(a, b):\n    return 0", False),
    (CONTENT, "    def run(self):\n        print(\"tab indented\")\n        return None", "    def run(self):\n        return 1", False),
    ("say \u201chello\u201d now\nnext\n", "say \"hello\" now", "say \"bye\" now", False),
    ("say \u201chello\u201d now\n  indented\n", "say \"hello\" now\nindented", "say bye\nindented", False),
    ("em\u2014dash and\u00a0nbsp\n", "em--dash and nbsp", "plain", False),
    (CONTENT, "def alpha():\\n    x = 1", "def alpha():\n    x = 5", False),
    ("a\na\na\n", "a", "b", False),
    ("a\na\na\n", "a", "b", True),
    ("it's here\n", "it\\'s here", "it\\'s there", False),
    ("path C:\\dir\\file\n", "path C:\\\\dir\\\\file", "path C:\\\\x", False),
    ("x = \"a\\nb\"\n", "x = \"a\\nb\"", "x = \"a\\nc\"", False),
    ("line1\nline2\n", "line1\\nline2", "line1\\nlineX", False),
    ("a\tb\n", "a\tb", "a\\tc", False),
    ("héllo wörld\n", "héllo wörld", "héllo there", False),
    ("emoji 😀 line\nnext\n", "emoji 😀 line\nnext", "emoji 😀 line\nchanged", False),
    ("  indented\n    more\n", "indented\n  more", "indented\n  less", False),
    ("foo(  1,2 )\n", "foo( 1,2 )", "foo(3)", False),
    ("start\nmid1\nmid2\nend\n", "start\nmid1\nXid2\nend", "start\nnew\nend", False),
]


def fr_cases():
    out = []
    for content, old, new, all_ in FR_CASES:
        c, n, strat, err = fm.fuzzy_find_and_replace(content, old, new, replace_all=all_)
        out.append({"content": content, "old": old, "new": new, "replace_all": all_, "result": c, "count": n, "strategy": strat, "error": err,
                    "hint": fm.format_no_match_hint(err, n, old, content)})
    for _ in range(80):  # random edits of a shared document
        lines = CONTENT.split("\n")
        i = R.randrange(len(lines) - 2)
        k = R.randint(1, 3)
        old = "\n".join(lines[i:i + k])
        mode = R.choice(["exact", "indent", "trail", "typo"])
        if mode == "indent":
            old = "\n".join("  " + l.lstrip() for l in old.split("\n"))
        elif mode == "trail":
            old = "\n".join(l + "  " for l in old.split("\n"))
        elif mode == "typo" and len(old) > 6:
            p = R.randrange(len(old))
            old = old[:p] + "~" + old[p + 1:]
        new = old.replace("a", "A") + R.choice(["", " # new"])
        ra = R.random() < 0.3
        c, n, strat, err = fm.fuzzy_find_and_replace(CONTENT, old, new, replace_all=ra)
        out.append({"content": CONTENT, "old": old, "new": new, "replace_all": ra, "result": c, "count": n, "strategy": strat, "error": err,
                    "hint": fm.format_no_match_hint(err, n, old, CONTENT)})
    return out


def closest_cases():
    out = []
    for old in ["return totl", "def betaa(a, b):", "x = 1", "    x = 1", "\tx = 1", "nothing similar at all zzz", ""]:
        out.append({"old": old, "content": CONTENT, "closest": fm.find_closest_lines(old, CONTENT)})
    return out


def main(outdir):
    out = pathlib.Path(outdir)
    out.mkdir(parents=True, exist_ok=True)
    cases = fr_cases()
    (out / "matcher.json").write_text(json.dumps(matcher_cases(), ensure_ascii=True) + "\n")
    (out / "diff.json").write_text(json.dumps(diff_cases(), ensure_ascii=True) + "\n")
    (out / "closest.json").write_text(json.dumps(closest_cases(), ensure_ascii=True) + "\n")
    (out / "replace.json").write_text(json.dumps(cases, ensure_ascii=True) + "\n")
    print(f"matcher/diff/closest/replace: {len(cases)} replace cases")


if __name__ == "__main__":
    main(sys.argv[1])
