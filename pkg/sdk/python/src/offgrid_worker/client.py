"""Асинхронный клиент шлюза прорабов (ADR-45).

Повтор accept/commit безопасен: ядро дедуплицирует по состоянию строки задачи (ADR-20),
поэтому при сетевых сбоях и 5xx клиент повторяет запрос с растущей паузой.
"""

from __future__ import annotations

import asyncio
import random
from collections.abc import Sequence
from dataclasses import dataclass
from typing import Any

import httpx

from .models import ChargeLine, ClaimedTask, CommitRequest, TaskState


class GatewayError(Exception):
    """Шлюз ответил ошибкой, которую повтор не исправит."""

    def __init__(self, status: int, body: str) -> None:
        super().__init__(f"gateway error {status}: {body[:500]}")
        self.status = status
        self.body = body


class UnauthorizedError(GatewayError):
    """Неизвестный или отозванный service-токен."""


class NotOwnerError(GatewayError):
    """Задача больше не у этого прораба (лок истёк, отменена) — работу надо бросить."""

    def __init__(self, state: TaskState | None, body: str) -> None:
        super().__init__(409, body)
        self.state = state


@dataclass(frozen=True)
class RetryPolicy:
    """Повторы при сетевых сбоях и 5xx; все числа — настройка, не константы."""

    attempts: int = 5
    base_delay: float = 0.2
    max_delay: float = 5.0


class GatewayClient:
    """Клиент шлюза одного прораба (один service-токен — один target_service)."""

    def __init__(
        self,
        base_url: str,
        token: str,
        *,
        timeout: float = 30.0,
        retry: RetryPolicy | None = None,
        transport: httpx.AsyncBaseTransport | None = None,
    ) -> None:
        self._http = httpx.AsyncClient(
            base_url=base_url.rstrip("/") + "/gateway/v1",
            headers={"Authorization": f"Bearer {token}"},
            timeout=timeout,
            transport=transport,
        )
        self._retry = retry or RetryPolicy()

    async def __aenter__(self) -> GatewayClient:
        return self

    async def __aexit__(self, *exc: object) -> None:
        await self.aclose()

    async def aclose(self) -> None:
        await self._http.aclose()

    async def claim(self, target_service: str, schema_versions: Sequence[int]) -> ClaimedTask | None:
        """Забрать задачу; None — очередь пуста."""
        resp = await self._post(
            "/claim", {"target_service": target_service, "schema_versions": list(schema_versions)}
        )
        if resp.status_code == 204:
            return None
        return ClaimedTask.model_validate(resp.json())

    async def accept(self, task_id: str, *, eta_seconds: int | None = None) -> TaskState:
        """«Принято» + оценка времени; ожидание отчёта = 3 × ETA (ADR-30)."""
        body: dict[str, Any] = {"accepted": True}
        if eta_seconds:
            body["eta_seconds"] = eta_seconds
        return self._state(await self._post(f"/tasks/{task_id}/accept", body))

    async def decline(self, task_id: str) -> TaskState:
        """Отказ в приёме: задача сразу возвращается в очередь, попытка не тратится."""
        return self._state(await self._post(f"/tasks/{task_id}/accept", {"accepted": False}))

    async def heartbeat(self, task_id: str) -> TaskState:
        return self._state(await self._post(f"/tasks/{task_id}/heartbeat", None))

    async def progress(self, task_id: str, percent: int, phase: str) -> None:
        """Эфемерный прогресс; сбой доставки не критичен — прогресс не хранится (FR-FORM-4)."""
        await self._post(f"/tasks/{task_id}/progress", {"percent": percent, "phase": phase})

    async def commit_success(
        self,
        task_id: str,
        result: dict[str, Any],
        *,
        charge: Sequence[ChargeLine] = (),
        report: dict[str, Any] | None = None,
    ) -> TaskState:
        """Успешный отчёт. charge — счёт в единицах воркера (ADR-47); деньги считает ядро."""
        rep = dict(report or {})
        if charge:
            rep["worker_charge"] = {"lines": [line.model_dump(mode="json") for line in charge]}
        req = CommitRequest(outcome="success", result=result, report=rep)
        return await self._commit(task_id, req)

    async def commit_error(
        self, task_id: str, admin_message: str, *, report: dict[str, Any] | None = None
    ) -> TaskState:
        """Ошибка исполнения → WORKER_ERROR; текст видит админ, не пользователь (ADR-24)."""
        rep = dict(report or {})
        rep["admin_message"] = admin_message[:2000]
        return await self._commit(task_id, CommitRequest(outcome="error", report=rep))

    async def commit_rejected(
        self, task_id: str, admin_message: str, *, report: dict[str, Any] | None = None
    ) -> TaskState:
        """Данные неприменимы по смыслу → DATA_REJECTED, retry не предлагается (ADR-53)."""
        rep = dict(report or {})
        rep["admin_message"] = admin_message[:2000]
        return await self._commit(task_id, CommitRequest(outcome="rejected", report=rep))

    async def _commit(self, task_id: str, req: CommitRequest) -> TaskState:
        return self._state(
            await self._post(f"/tasks/{task_id}/commit", req.model_dump(mode="json", exclude_none=True))
        )

    @staticmethod
    def _state(resp: httpx.Response) -> TaskState:
        return TaskState.model_validate(resp.json())

    async def download_file(self, task_id: str, file_id: str) -> bytes | None:
        """Файл из снимка задачи (ADR-60) — байты; None, если пользователь его уже удалил."""
        resp = await self._request("GET", f"/tasks/{task_id}/files/{file_id}", None, allow_404=True)
        return None if resp.status_code == 404 else resp.content

    async def _post(self, path: str, body: dict[str, Any] | None) -> httpx.Response:
        return await self._request("POST", path, body)

    async def _request(
        self, method: str, path: str, body: dict[str, Any] | None, *, allow_404: bool = False
    ) -> httpx.Response:
        delay = self._retry.base_delay
        for attempt in range(1, self._retry.attempts + 1):
            try:
                resp = await self._http.request(method, path, json=body)
            except httpx.TransportError:
                if attempt == self._retry.attempts:
                    raise
            else:
                if resp.status_code < 500:
                    if allow_404 and resp.status_code == 404:
                        return resp
                    return self._check(resp)
                if attempt == self._retry.attempts:
                    raise GatewayError(resp.status_code, resp.text)
            await asyncio.sleep(delay * (0.5 + random.random()))
            delay = min(delay * 2, self._retry.max_delay)
        raise AssertionError("unreachable")

    @staticmethod
    def _check(resp: httpx.Response) -> httpx.Response:
        if resp.status_code in (200, 202, 204):
            return resp
        if resp.status_code == 401:
            raise UnauthorizedError(401, resp.text)
        if resp.status_code == 409:
            try:
                state: TaskState | None = TaskState.model_validate(resp.json())
            except ValueError:
                state = None
            raise NotOwnerError(state, resp.text)
        raise GatewayError(resp.status_code, resp.text)
