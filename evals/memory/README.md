# evals/memory

Typed, seeded memory eval harness. Scenarios 1, 2, 3, 6 are implemented against a `MemoryArm`
Protocol; 4, 5, 7, 8 need a live agent arm (marked `requires_live`).

`baseline.json` is currently produced by `DictArm` and has `harness_only: true`: it validates the
harness, **not** current Hermes memory. A live arm must replace it before ship gates are evaluated.

    uv run python -m evals.memory.run --arm baseline --out evals/memory/baseline.json
