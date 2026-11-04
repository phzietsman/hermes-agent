from __future__ import annotations

from enum import Enum
from typing import Literal

from pydantic import BaseModel, ConfigDict, Field


class ArmName(str, Enum):
    BASELINE = "baseline"
    LOOPFIX = "loopfix"
    LOOPFIX_SAM = "loopfix_sam"


class Fact(BaseModel):
    model_config = ConfigDict(frozen=True)
    key: str
    statement: str
    question: str
    answer: str


class Turn(BaseModel):
    model_config = ConfigDict(frozen=True)
    session: int = Field(ge=0)
    kind: Literal["tell", "ask"]
    fact_key: str
    text: str


class ScenarioResult(BaseModel):
    model_config = ConfigDict(frozen=True)
    scenario: str
    passed: int
    total: int
    requires_live: bool = False

    @property
    def rate(self) -> float:
        return self.passed / self.total if self.total else 0.0


class Report(BaseModel):
    arm: ArmName
    seed: int
    harness_only: bool
    results: list[ScenarioResult]
