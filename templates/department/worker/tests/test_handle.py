"""Предметная логика без ядра: TaskContext на снимке задачи."""

from __future__ import annotations

from typing import Any

from offgrid_worker import ClaimedTask, Outcome, TaskContext

from department.main import handle


def context(values: dict[str, Any], marks: dict[str, Any]) -> TaskContext:
    claimed = ClaimedTask.model_validate(
        {
            "task_id": "t1",
            "attempt": 1,
            "blank": {
                "system": {
                    "document_id": "t1",
                    "form_id": "example.run.v1",
                    "schema_version": 1,
                    "kind": "task",
                    "status": "processing",
                    "created_at": "2026-10-15T00:00:00Z",
                },
                "meta_ui": {"title": "x", "fields_rules": {}},
                "values": values,
                "user_marks": marks,
            },
        }
    )
    return TaskContext(None, claimed)  # type: ignore[arg-type]  # клиент обработчику не нужен


async def test_handle_repeats_and_counts() -> None:
    ctx = context({"note": {"values": {"name": "эхо"}}, "repeat": 2}, {"role": "tester"})
    out = await handle(ctx)
    assert isinstance(out, Outcome)
    assert out.result == {"name": "эхо эхо"}
    assert {line.unit: int(line.units) for line in ctx.charge_lines()} == {"chars": 7, "calls": 1}
    assert ctx.service_facts() == [{"form_id": "example.spend.v1", "values": {"chars": 7, "role": "tester"}}]
