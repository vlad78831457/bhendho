"""Рантайм прораба: claim → accept(ETA) → обработчик с heartbeat → commit (ADR-30).

Обработчик пишет только предметную логику (ADR-49): получает снимок бланка,
сообщает прогресс, копит счёт в своих единицах и возвращает результат.
"""

from __future__ import annotations

import asyncio
import contextlib
import copy
import logging
from collections import defaultdict
from collections.abc import Callable, Coroutine, Sequence
from dataclasses import dataclass, field
from decimal import Decimal
from typing import Any

from .client import GatewayClient, GatewayError, NotOwnerError, UnauthorizedError
from .models import Blank, ChargeLine, ClaimedTask, TaskStatus

log = logging.getLogger("offgrid_worker")


class Decline(Exception):
    """Поднять из estimate: прораб не берёт задачу — она вернётся в очередь без попытки."""


class DataRejected(Exception):
    """Данные бланка неприменимы по смыслу (направленный граф для TSP, завершённая сказка) →
    DATA_REJECTED: пользователь исправляет данные, повтор той же задачи не предлагается (ADR-53)."""

    def __init__(self, admin_message: str, *, report: dict[str, Any] | None = None) -> None:
        super().__init__(admin_message)
        self.admin_message = admin_message
        self.report = report or {}


class TaskError(Exception):
    """Предметная ошибка исполнения → WORKER_ERROR (retry разрешён пользователю)."""

    def __init__(self, admin_message: str, *, report: dict[str, Any] | None = None) -> None:
        super().__init__(admin_message)
        self.admin_message = admin_message
        self.report = report or {}


@dataclass(frozen=True)
class Outcome:
    """Результат обработчика: values результата и предметные поля report (explanation, evidence…)."""

    result: dict[str, Any]
    report: dict[str, Any] = field(default_factory=dict)


class TaskContext:
    """Всё, что видит обработчик про одну попытку."""

    def __init__(self, client: GatewayClient, claimed: ClaimedTask) -> None:
        self._client = client
        self._claimed = claimed
        self._charge: dict[str, Decimal] = defaultdict(Decimal)
        self._lost = asyncio.Event()

    @property
    def task_id(self) -> str:
        return self._claimed.task_id

    @property
    def attempt(self) -> int:
        return self._claimed.attempt

    @property
    def blank(self) -> Blank:
        """Конверт как пришёл (заморожен)."""
        return self._claimed.blank

    @property
    def form_id(self) -> str:
        return self._claimed.blank.system.form_id

    @property
    def schema_version(self) -> int:
        return self._claimed.blank.system.schema_version

    @property
    def values(self) -> dict[str, Any]:
        """Копия материализованных values: мутации не затрагивают бланк (ADR-10)."""
        return copy.deepcopy(self._claimed.blank.values)

    @property
    def lost(self) -> bool:
        """Задачу у нас забрали (лок истёк, отменена) — дальнейшая работа бесполезна."""
        return self._lost.is_set()

    def _mark_lost(self) -> None:
        self._lost.set()

    async def progress(self, percent: int, phase: str) -> None:
        """Эфемерный прогресс; сбой доставки не прерывает работу."""
        try:
            await self._client.progress(self.task_id, max(0, min(100, percent)), phase)
        except NotOwnerError:
            self._lost.set()
        except (GatewayError, OSError) as exc:
            log.warning("progress not delivered", extra={"task_id": self.task_id, "error": str(exc)})

    async def file(self, file_id: str) -> bytes | None:
        """Байты файла из бланка задачи (ADR-60): фото персонажа и т. п. None — файла больше нет
        или задачу у нас забрали (тогда lost)."""
        try:
            return await self._client.download_file(self.task_id, file_id)
        except NotOwnerError:
            self._lost.set()
            return None

    def charge(self, unit: str, units: int | float | Decimal) -> None:
        """Добавить затраты в единицах воркера (ADR-47): tokens, images, tts_seconds…"""
        value = Decimal(str(units))
        if value < 0:
            raise ValueError("units must be non-negative")
        self._charge[unit] += value

    def charge_lines(self) -> list[ChargeLine]:
        return [ChargeLine(unit=u, units=v) for u, v in self._charge.items()]


Handler = Callable[[TaskContext], Coroutine[Any, Any, dict[str, Any] | Outcome]]
Estimate = Callable[[Blank], int | None]


@dataclass
class WorkerConfig:
    """Параметры рантайма; все числа — настройка."""

    target_service: str
    schema_versions: Sequence[int]
    concurrency: int = 1
    poll_interval: float = 1.0
    max_poll_interval: float = 10.0
    heartbeat_interval: float = 10.0


class Worker:
    """Прораб одного сервиса с N параллельными слотами."""

    def __init__(
        self,
        client: GatewayClient,
        config: WorkerConfig,
        handler: Handler,
        *,
        estimate: Estimate | None = None,
    ) -> None:
        self._client = client
        self._cfg = config
        self._handler = handler
        self._estimate = estimate
        self._stop = asyncio.Event()

    def stop(self) -> None:
        """Мягкая остановка: слоты доделывают текущие задачи и не берут новые."""
        self._stop.set()

    async def run(self) -> None:
        """Крутится до stop(); неверный токен — фатально."""
        async with asyncio.TaskGroup() as tg:
            for slot in range(self._cfg.concurrency):
                tg.create_task(self._slot(slot))

    async def run_once(self) -> bool:
        """Одна итерация: взять и обработать одну задачу. False — очередь пуста."""
        claimed = await self._client.claim(self._cfg.target_service, self._cfg.schema_versions)
        if claimed is None:
            return False
        await self._process(claimed)
        return True

    async def _slot(self, slot: int) -> None:
        idle = self._cfg.poll_interval
        while not self._stop.is_set():
            try:
                busy = await self.run_once()
            except UnauthorizedError:
                raise
            except (GatewayError, OSError) as exc:
                log.warning("gateway unavailable", extra={"slot": slot, "error": str(exc)})
                busy = False
            if busy:
                idle = self._cfg.poll_interval
                continue
            with contextlib.suppress(TimeoutError):
                await asyncio.wait_for(self._stop.wait(), timeout=idle)
            idle = min(idle * 2, self._cfg.max_poll_interval)

    async def _process(self, claimed: ClaimedTask) -> None:
        task_id = claimed.task_id
        try:
            eta = self._estimate(claimed.blank) if self._estimate else None
        except Decline:
            await self._client.decline(task_id)
            return
        try:
            await self._client.accept(task_id, eta_seconds=eta)
        except NotOwnerError:
            return  # лок на приём истёк — задача уже у другого

        ctx = TaskContext(self._client, claimed)
        work = asyncio.create_task(self._handler(ctx))
        watch = asyncio.create_task(self._watch(ctx))
        try:
            await asyncio.wait({work, watch}, return_when=asyncio.FIRST_COMPLETED)
        finally:
            watch.cancel()
        if not work.done():
            work.cancel()  # задачу забрали — результат никому не нужен
            await asyncio.gather(work, return_exceptions=True)
            log.info("task lost, work abandoned", extra={"task_id": task_id})
            return
        await self._report(ctx, work)

    async def _watch(self, ctx: TaskContext) -> None:
        """Heartbeat: как только задача не наша или не в работе — сигнал бросить."""
        while not ctx.lost:
            await asyncio.sleep(self._cfg.heartbeat_interval)
            try:
                state = await self._client.heartbeat(ctx.task_id)
            except NotOwnerError:
                ctx._mark_lost()
                return
            except (GatewayError, OSError):
                continue  # шлюз временно недоступен — лок решит сам
            if state.status != TaskStatus.PROCESSING:
                ctx._mark_lost()
                return

    async def _report(self, ctx: TaskContext, work: asyncio.Task[dict[str, Any] | Outcome]) -> None:
        exc = work.exception()
        if isinstance(exc, DataRejected):
            await self._client.commit_rejected(ctx.task_id, exc.admin_message, report=exc.report)
            return
        if isinstance(exc, TaskError):
            await self._client.commit_error(ctx.task_id, exc.admin_message, report=exc.report)
            return
        if exc is not None:
            log.error("handler failed", extra={"task_id": ctx.task_id}, exc_info=exc)
            await self._client.commit_error(ctx.task_id, f"{type(exc).__name__}: {exc}")
            return
        out = work.result()
        outcome = out if isinstance(out, Outcome) else Outcome(result=out)
        await self._client.commit_success(
            ctx.task_id, outcome.result, charge=ctx.charge_lines(), report=outcome.report
        )
