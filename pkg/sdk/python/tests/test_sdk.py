from __future__ import annotations

import asyncio
import json
import os
from pathlib import Path
from typing import Any

import httpx
import jsonschema
import pytest

from offgrid_worker import (
    Blank,
    ChargeLine,
    ClaimedTask,
    Decline,
    GatewayClient,
    NotOwnerError,
    Outcome,
    RetryPolicy,
    TaskContext,
    TaskError,
    UnauthorizedError,
    Worker,
    WorkerConfig,
)

from .fake_gateway import FakeGateway

_env = os.environ.get("CONTRACTS_DIR")
CONTRACTS = Path(_env) if _env else Path(__file__).resolve().parents[4] / "contracts"
FAST = RetryPolicy(attempts=3, base_delay=0.001, max_delay=0.002)


def client(gw: FakeGateway, token: str | None = None) -> GatewayClient:
    return GatewayClient("http://core", token or gw.token, retry=FAST, transport=gw.transport())


def worker(gw: FakeGateway, handler: Any, **kw: Any) -> Worker:
    cfg = WorkerConfig(
        target_service=gw.service,
        schema_versions=[1],
        poll_interval=0.01,
        max_poll_interval=0.02,
        heartbeat_interval=kw.pop("heartbeat", 5.0),
        concurrency=kw.pop("concurrency", 1),
    )
    return Worker(client(gw), cfg, handler, **kw)


# --- контракт -----------------------------------------------------------------


def envelope_schema() -> dict[str, Any]:
    return json.loads((CONTRACTS / "manifests" / "blank_envelope_schema.json").read_text())


def test_example_blank_parses_and_is_frozen() -> None:
    raw = json.loads(
        (CONTRACTS / "manifests" / "examples" / "taleweaver.generate_start.blank.json").read_text()
    )
    blank = Blank.model_validate(raw)
    assert blank.system.form_id == "taleweaver.generate_start.v1"
    with pytest.raises(ValueError):
        blank.system.form_id = "x"  # type: ignore[misc]


def test_charge_serialization_matches_contract() -> None:
    schema = envelope_schema()
    charge = {
        "lines": [
            ChargeLine(unit="tokens", units=12400).model_dump(mode="json"),
            ChargeLine(unit="tts_seconds", units="96.5").model_dump(mode="json"),
        ]
    }
    assert charge["lines"][0] == {"unit": "tokens", "units": 12400}
    validator = jsonschema.Draft202012Validator({"$ref": "#/$defs/workerCharge", "$defs": schema["$defs"]})
    validator.validate(charge)
    with pytest.raises(ValueError):
        ChargeLine(unit="tokens", units=-1)


# --- клиент -------------------------------------------------------------------


async def test_client_retries_server_errors() -> None:
    gw = FakeGateway()
    gw.add({"x": 1})
    gw.fail_next = 2
    async with client(gw) as c:
        claimed = await c.claim("tale", [1])
    assert claimed is not None
    assert [p for _, p in gw.calls].count("/claim") == 3


async def test_client_gives_up_and_maps_errors() -> None:
    gw = FakeGateway()
    gw.fail_next = 10
    async with client(gw) as c:
        with pytest.raises(Exception, match="503"):
            await c.claim("tale", [1])
    async with client(gw, token="sa_wrong") as c:
        with pytest.raises(UnauthorizedError):
            await c.claim("tale", [1])


async def test_client_retries_transport_errors() -> None:
    attempts = 0

    def flaky(req: httpx.Request) -> httpx.Response:
        nonlocal attempts
        attempts += 1
        if attempts < 3:
            raise httpx.ConnectError("boom", request=req)
        return httpx.Response(204)

    async with GatewayClient("http://core", "t", retry=FAST, transport=httpx.MockTransport(flaky)) as c:
        assert await c.claim("tale", [1]) is None
    assert attempts == 3


async def test_not_owner_carries_state() -> None:
    gw = FakeGateway()
    t = gw.add({})
    async with client(gw) as c:
        with pytest.raises(NotOwnerError) as err:
            await c.accept(t.task_id, eta_seconds=5)
    assert err.value.state is not None and err.value.state.status == "pending"


# --- рантайм ------------------------------------------------------------------


async def test_worker_happy_path_with_charge_and_immutable_values() -> None:
    gw = FakeGateway()
    t = gw.add({"hero": {"values": {"name": "Лисёнок"}}})
    seen: list[TaskContext] = []

    async def handler(ctx: TaskContext) -> Outcome:
        seen.append(ctx)
        values = ctx.values
        values["hero"]["values"]["name"] = "мутант"
        await ctx.progress(50, "text_ready")
        ctx.charge("tokens", 12400)
        ctx.charge("images", 4)
        ctx.charge("tokens", 100)
        return Outcome(result={"pages": 4}, report={"explanation": {"formulas": ["x"]}})

    assert await worker(gw, handler).run_once()
    assert seen[0].blank.values["hero"]["values"]["name"] == "Лисёнок"
    assert t.status == "completed"
    assert t.commit == {
        "outcome": "success",
        "result": {"pages": 4},
        "report": {
            "explanation": {"formulas": ["x"]},
            "worker_charge": {"lines": [{"unit": "tokens", "units": 12500}, {"unit": "images", "units": 4}]},
        },
    }
    assert ("POST", f"/tasks/{t.task_id}/progress") in gw.calls


async def test_worker_reports_errors() -> None:
    gw = FakeGateway()
    a, b = gw.add({"n": 1}), gw.add({"n": 2})

    async def handler(ctx: TaskContext) -> dict[str, Any]:
        if ctx.values["n"] == 1:
            raise TaskError("provider 503", report={"data_gaps": ["нет сна за 3 дня"]})
        raise KeyError("boom")

    w = worker(gw, handler)
    await w.run_once()
    await w.run_once()
    assert a.status == "failed" and a.commit is not None
    assert a.commit["report"] == {"data_gaps": ["нет сна за 3 дня"], "admin_message": "provider 503"}
    assert b.commit is not None and b.commit["report"]["admin_message"].startswith("KeyError")


async def test_worker_declines_via_estimate() -> None:
    gw = FakeGateway()
    t = gw.add({})
    called = False

    async def handler(ctx: TaskContext) -> dict[str, Any]:
        nonlocal called
        called = True
        return {}

    def estimate(blank: Blank) -> int | None:
        raise Decline

    await worker(gw, handler, estimate=estimate).run_once()
    assert not called
    assert t.status == "pending" and t.attempts == 0


async def test_worker_abandons_lost_task() -> None:
    gw = FakeGateway(lose_on_heartbeat=True)
    t = gw.add({})
    cancelled = asyncio.Event()

    async def handler(ctx: TaskContext) -> dict[str, Any]:
        try:
            await asyncio.sleep(10)
        except asyncio.CancelledError:
            cancelled.set()
            raise
        return {}

    await worker(gw, handler, heartbeat=0.01).run_once()
    assert cancelled.is_set()
    assert t.commit is None
    assert ("POST", f"/tasks/{t.task_id}/commit") not in gw.calls


async def test_worker_slots_run_in_parallel_and_stop() -> None:
    gw = FakeGateway()
    for i in range(6):
        gw.add({"i": i})
    running = 0
    peak = 0

    async def handler(ctx: TaskContext) -> dict[str, Any]:
        nonlocal running, peak
        running += 1
        peak = max(peak, running)
        await asyncio.sleep(0.05)
        running -= 1
        return {"i": ctx.values["i"]}

    w = worker(gw, handler, concurrency=3)
    runner = asyncio.create_task(w.run())
    for _ in range(200):
        if all(t.status == "completed" for t in gw.tasks):
            break
        await asyncio.sleep(0.01)
    w.stop()
    await asyncio.wait_for(runner, timeout=2)
    assert all(t.status == "completed" for t in gw.tasks)
    assert peak == 3


async def test_worker_fails_fast_on_bad_token() -> None:
    gw = FakeGateway()

    async def handler(ctx: TaskContext) -> dict[str, Any]:
        return {}

    cfg = WorkerConfig(target_service="tale", schema_versions=[1], poll_interval=0.01)
    w = Worker(client(gw, token="sa_wrong"), cfg, handler)
    with pytest.raises(ExceptionGroup) as err:
        await asyncio.wait_for(w.run(), timeout=2)
    assert err.group_contains(UnauthorizedError)


def test_verdict_enum_matches_contract() -> None:
    from offgrid_worker import Verdict

    assert {v.value for v in Verdict} == set(envelope_schema()["$defs"]["verdict"]["enum"])


async def test_worker_reports_data_rejection() -> None:
    from offgrid_worker import DataRejected

    gw = FakeGateway()
    t = gw.add({})

    async def handler(ctx: TaskContext) -> dict[str, Any]:
        raise DataRejected("graph is directed", report={"input": "g1"})

    await worker(gw, handler).run_once()
    assert t.status == "failed" and t.commit is not None
    assert t.commit == {
        "outcome": "rejected",
        "report": {"input": "g1", "admin_message": "graph is directed"},
    }


# --- llm: интерфейс внешней модели и защита от выдуманных чисел ----------------------------------


async def test_llm_join_stub_and_number_guard() -> None:
    from offgrid_worker.llm import JoinModel, explain, invented_numbers

    class Liar:
        name = "liar"

        async def complete(self, system: str, facts: list[str]) -> str:
            return "Итого 100 штук"

    class Down:
        name = "down"

        async def complete(self, system: str, facts: list[str]) -> str:
            raise ConnectionError

    facts = ["итого 75 л", "дата 2026-10-25"]
    assert (await explain(JoinModel(), "s", facts)).text == "итого 75 л; дата 2026-10-25"
    assert (await explain(Liar(), "s", facts)).source == "join (fallback: invented numbers ['100'])"
    assert (await explain(Down(), "s", facts)).source == "join (fallback: ConnectionError)"
    assert invented_numbers("100 и 75.0 и 0,50", ["1 и 75 и 0.5"]) == {"100"}


async def test_task_files_download_missing_and_lost() -> None:
    gw = FakeGateway()
    gw.files["photo-1"] = b"\xff\xd8jpeg"
    t = gw.add({"hero": {"values": {"photo": {"file_id": "photo-1", "mime": "image/jpeg"}}}})
    got: dict[str, bytes | None] = {}

    async def handler(ctx: TaskContext) -> dict[str, Any]:
        got["photo"] = await ctx.file(ctx.values["hero"]["values"]["photo"]["file_id"])
        got["deleted"] = await ctx.file("photo-deleted")  # пользователь удалил — None
        return {}

    assert await worker(gw, handler).run_once()
    assert got == {"photo": b"\xff\xd8jpeg", "deleted": None}
    assert ("GET", f"/tasks/{t.task_id}/files/photo-1") in gw.calls

    # Задача уже не у нас (завершена) — файл не отдаётся, контекст помечен lost.
    claimed = ClaimedTask.model_validate({"task_id": t.task_id, "attempt": 1, "blank": gw.blank(t)})
    async with client(gw) as c:
        ctx = TaskContext(c, claimed)
        assert await ctx.file("photo-1") is None
        assert ctx.lost
