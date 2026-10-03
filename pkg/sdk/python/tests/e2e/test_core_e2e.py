"""SDK против живого ядра: пользователь ставит задачу по HTTP, воркер на SDK её исполняет.

Запуск: make e2e (поднимает отдельный compose-проект, выпускает токен прораба).
"""

from __future__ import annotations

import asyncio
import os
import uuid
from decimal import Decimal
from typing import Any

import httpx
import pytest

from offgrid_worker import GatewayClient, TaskContext, TaskError, Worker, WorkerConfig

pytestmark = pytest.mark.e2e

CORE_URL = os.environ.get("CORE_URL", "http://localhost:8080")


@pytest.fixture
async def api() -> Any:
    async with httpx.AsyncClient(base_url=CORE_URL, timeout=10) as http:
        email = f"e2e-{uuid.uuid4().hex[:8]}@example.org"
        r = await http.post("/api/v1/auth/register", json={"email": email, "password": "longpassword"})
        assert r.status_code == 201, r.text
        http.headers["Authorization"] = "Bearer " + r.json()["tokens"]["access_token"]
        r = await http.post("/api/v1/deposits/demo", json={"amount": 100})
        assert r.status_code == 200, r.text
        yield http


async def echo(ctx: TaskContext) -> dict[str, Any]:
    values = ctx.values
    text, repeat = values["text"], values.get("repeat", 1)
    if text == "fail":
        raise TaskError("echo refused on purpose", report={"data_gaps": ["text"]})
    await ctx.progress(50, "echoing")
    ctx.charge("chars", len(text) * repeat)
    ctx.charge("calls", 1)
    return {
        "echo": text * repeat,
        "note": values["note"]["values"]["name"],
        "note_version": values["note"]["version"],
    }


def make_worker() -> Worker:
    client = GatewayClient(CORE_URL, os.environ["WORKER_TOKEN"])
    cfg = WorkerConfig(target_service="echo", schema_versions=[1], heartbeat_interval=1)
    return Worker(client, cfg, echo, estimate=lambda blank: 5)


async def create_task(api: httpx.AsyncClient, text: str) -> str:
    r = await api.post("/api/v1/facts", json={"form_id": "echo.note.v1", "values": {"name": "заметка"}})
    assert r.status_code == 201, r.text
    fact_id = r.json()["fact_id"]
    assert (await api.post(f"/api/v1/facts/{fact_id}/publish")).status_code == 200
    r = await api.post(
        "/api/v1/tasks",
        json={
            "form_id": "echo.run.v1",
            "idempotency_key": str(uuid.uuid4()),
            "values": {"note": fact_id, "text": text, "repeat": 3},
        },
    )
    assert r.status_code == 201, r.text
    return str(r.json()["system"]["document_id"])


async def process_until(api: httpx.AsyncClient, task_id: str, status: str) -> dict[str, Any]:
    worker = make_worker()
    for _ in range(50):
        await worker.run_once()
        task = (await api.get(f"/api/v1/tasks/{task_id}")).json()
        if task["system"]["status"] == status:
            return dict(task)
        await asyncio.sleep(0.1)
    raise AssertionError(f"task {task_id} did not reach {status}")


async def wallet(api: httpx.AsyncClient) -> tuple[Decimal, Decimal]:
    w = (await api.get("/api/v1/wallets")).json()["items"][0]
    return Decimal(w["balance"]), Decimal(w["reserved"])


async def test_success_is_settled_in_worker_units(api: httpx.AsyncClient) -> None:
    task_id = await create_task(api, "ab")
    assert await wallet(api) == (Decimal("100"), Decimal("10"))

    task = await process_until(api, task_id, "completed")
    assert task["result"] == {"echo": "ababab", "note": "заметка", "note_version": 1}
    report = task["report"]
    assert report["verdict"] == "SUCCESS"
    # 6 chars + 1 call × 100 = 106 базовых единиц × 0.01 = 1.06 GC
    assert report["worker_charge"]["base_units"] == 106
    assert Decimal(str(report["worker_charge"]["total_gc"])) == Decimal("1.06")
    assert await wallet(api) == (Decimal("98.94"), Decimal("0"))


async def test_handler_error_releases_reserve_and_allows_retry(api: httpx.AsyncClient) -> None:
    task_id = await create_task(api, "fail")
    task = await process_until(api, task_id, "failed")
    assert task["report"]["verdict"] == "WORKER_ERROR"
    assert task["report"]["data_gaps"] == ["text"]
    assert "echo refused" not in task["report"]["user_message"]  # текст воркера — только админу
    assert await wallet(api) == (Decimal("100"), Decimal("0"))
    assert task["commands"]["retry"]["status"] == "todo"
