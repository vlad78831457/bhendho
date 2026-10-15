"""Прораб подразделения: подключается к ядру по ключу (ADR-70) и берёт задачи.

Предметная логика — только в handle(); очередь, попытки, сердцебиение и отчёт делает SDK.
"""

from __future__ import annotations

import asyncio
import os
from pathlib import Path
from typing import Any

from offgrid_worker import GatewayClient, Outcome, TaskContext, Worker, WorkerConfig

MODULE_DIR = Path(os.environ.get("MODULE_DIR", Path(__file__).resolve().parents[3] / "module"))
SERVICE = "example"


async def handle(ctx: TaskContext) -> Outcome:
    """Повторяет имя заметки repeat раз; результат — новая заметка пользователю."""
    values = ctx.values
    name = str(values["note"]["values"]["name"])
    repeat = int(values.get("repeat") or 1)
    text = " ".join([name] * repeat)[:200]
    ctx.charge("chars", len(text))
    ctx.charge("calls", 1)
    ctx.remember("example.spend.v1", {"chars": len(text), "role": ctx.user_marks.get("role", "")})
    return Outcome(result={"name": text})


def estimate(_: Any) -> int:
    return 5


async def run() -> None:
    async with GatewayClient(os.environ["CORE_URL"], os.environ["DEPARTMENT_KEY"]) as client:
        await client.register(MODULE_DIR)
        cfg = WorkerConfig(target_service=SERVICE, schema_versions=[1], concurrency=2)
        await Worker(client, cfg, handle, estimate=estimate).run()


def main() -> None:
    asyncio.run(run())
