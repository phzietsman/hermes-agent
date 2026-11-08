"""Per-file isolated coverage for every owned test file; combine per group.

usage: run_all.py [jobs] [per_file_timeout_s] [group ...]
Interpreter: scratch testenv python, PYTHONPATH must include the scratch coverage install."""
import json
import os
import subprocess
import sys
from concurrent.futures import ThreadPoolExecutor

B = os.path.dirname(os.path.abspath(__file__))
ROOT = "/Users/paulz/sourcery/phzietsman/hermes-agent"
jobs = int(sys.argv[1]) if len(sys.argv) > 1 else 8
tmo = int(sys.argv[2]) if len(sys.argv) > 2 else 180
only = sys.argv[3:]
mapping = json.load(open(f"{B}/mapping.json"))
ledger = json.load(open(f"{B}/ledger.json"))
groups = [g for g in ledger if not only or g in only]
os.chdir(ROOT)

tasks = [(g, t) for g in groups for t in ledger[g]]
results: dict[str, dict[str, int]] = {g: {"ok": 0, "fail": 0, "timeout": 0} for g in groups}


def run(task: tuple[str, str]) -> None:
    g, test = task
    d = f"{B}/data/{g}"
    os.makedirs(d, exist_ok=True)
    safe = test.replace("/", "__")
    include = ",".join(os.path.join(ROOT, s) for s in mapping[g]["sources"])
    env = dict(
        os.environ, TZ="UTC", LANG="C.UTF-8", PYTHONHASHSEED="0",
        COVERAGE_FILE=f"{d}/.cov.{safe}", COVERAGE_CORE="sysmon",
    )
    cmd = [
        sys.executable, "-m", "coverage", "run", "--branch", f"--include={include}",
        "-m", "pytest", test, "-q", "-p", "no:cacheprovider", "--no-header",
    ]
    try:
        r = subprocess.run(cmd, env=env, capture_output=True, timeout=tmo)
        results[g]["ok" if r.returncode in (0, 5) else "fail"] += 1
    except subprocess.TimeoutExpired:
        results[g]["timeout"] += 1


with ThreadPoolExecutor(jobs) as ex:
    list(ex.map(run, tasks))

for g in groups:
    d = f"{B}/data/{g}"
    env = dict(os.environ, COVERAGE_FILE=f"{d}/.cov")
    files = sorted(os.path.join(d, f) for f in os.listdir(d) if f.startswith(".cov.")) if os.path.isdir(d) else []
    out: dict = {"group": g, "tests": len(ledger[g]), **results[g]}
    if files:
        subprocess.run([sys.executable, "-m", "coverage", "combine", "--keep", *files], env=env, capture_output=True)
        rep = subprocess.run(
            [sys.executable, "-m", "coverage", "json", "-o", f"{d}/cov.json"], env=env, capture_output=True, text=True
        )
        if rep.returncode == 0:
            t = json.load(open(f"{d}/cov.json"))["totals"]
            out.update(
                lines=t["num_statements"], covered_lines=t["covered_lines"], branches=t["num_branches"],
                covered_branches=t["covered_branches"], percent=round(t["percent_covered"], 1),
                line_percent=round(100 * t["covered_lines"] / max(t["num_statements"], 1), 1),
            )
    print(json.dumps(out), flush=True)
