"""OFFGRID worker SDK: клиент шлюза прорабов и рантайм воркера (ADR-45)."""

from .client import GatewayClient, GatewayError, NotOwnerError, RetryPolicy, UnauthorizedError
from .models import Blank, ChargeLine, ClaimedTask, CommitRequest, TaskState, TaskStatus, Verdict
from .worker import DataRejected, Decline, Outcome, TaskContext, TaskError, Worker, WorkerConfig

__all__ = [
    "Blank",
    "ChargeLine",
    "ClaimedTask",
    "CommitRequest",
    "DataRejected",
    "Decline",
    "GatewayClient",
    "GatewayError",
    "NotOwnerError",
    "Outcome",
    "RetryPolicy",
    "TaskContext",
    "TaskError",
    "TaskState",
    "TaskStatus",
    "UnauthorizedError",
    "Verdict",
    "Worker",
    "WorkerConfig",
]
