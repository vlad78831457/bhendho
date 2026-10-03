"""Модели контракта шлюза (contracts/interfaces/worker_gateway.openapi.yaml).

Бланк приходит как JSON-конверт (contracts/manifests/blank_envelope_schema.json).
Модели заморожены: обработчик не может мутировать полученный бланк (ADR-10).
"""

from __future__ import annotations

from decimal import Decimal
from enum import StrEnum
from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field, field_serializer


class TaskStatus(StrEnum):
    """Статусы задачи (ADR-48)."""

    DRAFT = "draft"
    TEMPLATE = "template"
    PENDING = "pending"
    PROCESSING = "processing"
    COMPLETED = "completed"
    FAILED = "failed"
    CANCELLED = "cancelled"


class Verdict(StrEnum):
    """SUCCESS либо код единого реестра 5.10 (ADR-24, ADR-48)."""

    SUCCESS = "SUCCESS"
    INSUFFICIENT_FUNDS = "INSUFFICIENT_FUNDS"
    VALIDATION_ERROR = "VALIDATION_ERROR"
    FACT_NOT_PUBLISHED = "FACT_NOT_PUBLISHED"
    FACT_MISSING = "FACT_MISSING"
    FILE_MISSING = "FILE_MISSING"
    WORKER_TIMEOUT = "WORKER_TIMEOUT"
    LIMIT_EXHAUSTED = "LIMIT_EXHAUSTED"
    WORKER_ERROR = "WORKER_ERROR"
    REPORT_INVALID = "REPORT_INVALID"
    COMMAND_REFUSED = "COMMAND_REFUSED"
    DATA_REJECTED = "DATA_REJECTED"


class _Frozen(BaseModel):
    model_config = ConfigDict(frozen=True, extra="allow")


class BlankSystem(_Frozen):
    """Секция system конверта."""

    document_id: str
    form_id: str
    schema_version: int
    kind: Literal["fact", "task"]
    status: str
    created_at: str
    owner_id: str | None = None
    target_service: str | None = None
    attempts: int = 0


class Blank(_Frozen):
    """Конверт бланка задачи; values — материализованный снимок (ADR-14)."""

    system: BlankSystem
    meta_ui: dict[str, Any]
    commands: dict[str, Any] = Field(default_factory=dict)
    values: dict[str, Any]
    report: dict[str, Any] | None = None


class ClaimedTask(_Frozen):
    """Задача, выданная прорабу."""

    task_id: str
    attempt: int
    blank: Blank


class TaskState(_Frozen):
    """Состояние задачи в ответе шлюза."""

    task_id: str
    status: TaskStatus
    verdict: Verdict | None = None
    locked_until: str | None = None
    attempts: int = 0


class ChargeLine(BaseModel):
    """Строка счёта воркера в его единицах (ADR-47)."""

    model_config = ConfigDict(frozen=True)

    unit: str = Field(min_length=1)
    units: Decimal = Field(ge=0)

    @field_serializer("units")
    def _units_as_number(self, v: Decimal) -> int | float:
        # В контракте units — число; целые отдаём целыми, без потери точности.
        return int(v) if v == v.to_integral_value() else float(v)


class CommitRequest(BaseModel):
    """Отчёт попытки: результат или ошибка."""

    outcome: Literal["success", "error", "rejected"]
    result: dict[str, Any] | None = None
    report: dict[str, Any] = Field(default_factory=dict)
