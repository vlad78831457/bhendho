"""Фейковый шлюз в памяти — та же машина состояний, что у ядра (ADR-20/30/45)."""

from __future__ import annotations

import json
import uuid
from dataclasses import dataclass, field
from typing import Any

import httpx


@dataclass
class FakeTask:
    task_id: str
    values: dict[str, Any]
    status: str = "pending"
    accepted: bool = False
    attempts: int = 0
    commit: dict[str, Any] | None = None


@dataclass
class FakeGateway:
    service: str = "tale"
    token: str = "sa_test"
    tasks: list[FakeTask] = field(default_factory=list)
    calls: list[tuple[str, str]] = field(default_factory=list)
    fail_next: int = 0  # сколько следующих запросов ответить 503
    lose_on_heartbeat: bool = False
    files: dict[str, bytes] = field(default_factory=dict)  # файлы из снимков задач (ADR-60)

    def add(self, values: dict[str, Any]) -> FakeTask:
        t = FakeTask(task_id=str(uuid.uuid4()), values=values)
        self.tasks.append(t)
        return t

    def transport(self) -> httpx.MockTransport:
        return httpx.MockTransport(self._handle)

    def _find(self, task_id: str) -> FakeTask:
        return next(t for t in self.tasks if t.task_id == task_id)

    @staticmethod
    def _state(t: FakeTask) -> dict[str, Any]:
        state: dict[str, Any] = {
            "task_id": t.task_id,
            "status": t.status,
            "attempts": t.attempts,
            "locked_until": None,
        }
        if t.status == "completed":
            state["verdict"] = "SUCCESS"
        elif t.status == "failed":
            state["verdict"] = "WORKER_ERROR"
        return state

    def blank(self, t: FakeTask) -> dict[str, Any]:
        return {
            "system": {
                "document_id": t.task_id,
                "form_id": "tale.start.v1",
                "schema_version": 1,
                "kind": "task",
                "status": "processing",
                "created_at": "2026-09-28T10:00:00Z",
                "target_service": self.service,
                "attempts": t.attempts,
            },
            "meta_ui": {"title": "Сказка", "fields_rules": {}},
            "commands": {"cancel": {"status": "disabled"}, "retry": {"status": "disabled"}},
            "values": t.values,
            "report": None,
        }

    def _handle(self, req: httpx.Request) -> httpx.Response:
        path = req.url.path.removeprefix("/gateway/v1")
        self.calls.append((req.method, path))
        if req.headers.get("Authorization") != f"Bearer {self.token}":
            return httpx.Response(401, json={"error": {"code": "unauthorized"}})
        if self.fail_next > 0:
            self.fail_next -= 1
            return httpx.Response(503, text="unavailable")
        body = json.loads(req.content) if req.content else {}

        if path == "/claim":
            for t in self.tasks:
                if t.status == "pending":
                    t.status, t.accepted = "processing", False
                    return httpx.Response(
                        200, json={"task_id": t.task_id, "attempt": t.attempts + 1, "blank": self.blank(t)}
                    )
            return httpx.Response(204)

        parts = path.split("/")
        if len(parts) == 5 and parts[3] == "files":  # /tasks/{id}/files/{file_id}
            ft = self._find(parts[2])
            if ft.status != "processing":
                return httpx.Response(409, json=self._state(ft))
            data = self.files.get(parts[4])
            return httpx.Response(200, content=data) if data is not None else httpx.Response(404)
        _, _, task_id, action = path.split("/")
        t = self._find(task_id)
        owned = t.status == "processing"
        if action == "accept":
            if not owned:
                return httpx.Response(409, json=self._state(t))
            if not body["accepted"]:
                t.status = "pending"
            elif not t.accepted:
                t.accepted, t.attempts = True, t.attempts + 1
            return httpx.Response(200, json=self._state(t))
        if action == "heartbeat":
            if self.lose_on_heartbeat or not owned:
                return httpx.Response(409, json=self._state(t))
            return httpx.Response(200, json=self._state(t))
        if action == "progress":
            return httpx.Response(202 if owned else 409)
        if action == "commit":
            if owned and t.accepted:  # дедуп по состоянию строки
                t.commit = body
                t.status = "completed" if body["outcome"] == "success" else "failed"
            return httpx.Response(200, json=self._state(t))
        return httpx.Response(404)
