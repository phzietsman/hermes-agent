"""Write the three background-review prompts verbatim from the Python source to JSON for the Go port.

usage: python scripts/port/gen_review_prompts.py internal/learning/prompttext/prompts.json
"""
import json
import pathlib
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[2]))

from agent import background_review as br  # noqa: E402


def main(path):
    data = {"memory": br._MEMORY_REVIEW_PROMPT, "skills": br._SKILL_REVIEW_PROMPT, "combined": br._COMBINED_REVIEW_PROMPT}
    pathlib.Path(path).write_text(json.dumps(data, indent=1, ensure_ascii=False, sort_keys=True) + "\n")
    print({k: len(v) for k, v in data.items()})


if __name__ == "__main__":
    main(sys.argv[1])
