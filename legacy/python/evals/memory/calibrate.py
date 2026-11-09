from __future__ import annotations

from .calibration import CASES, Case
from .judge import Judge, JevJudge, verdict


def score(judge: Judge, case: Case) -> float:
    if case.prop == "current":
        return judge.states_current_value(case.question, case.value, case.reply)
    if case.prop == "stale":
        return judge.states_stale_value(case.question, case.value, case.reply)
    if case.prop == "abstain":
        return judge.abstains(case.reply)
    return judge.follows_preference(case.preference, case.reply)


def main() -> int:
    judge = JevJudge()
    correct = uncertain = 0
    for case in CASES:
        p = score(judge, case)
        v = verdict(p)
        ok = v is case.label
        correct += ok
        uncertain += v is None
        if not ok:
            print(f"MISS {case.prop:10} p={p:.2f} label={case.label}  {case.reply[:70]!r}")
    n = len(CASES)
    print(f"accuracy {correct}/{n} ({correct / n:.0%}); uncertain {uncertain}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
