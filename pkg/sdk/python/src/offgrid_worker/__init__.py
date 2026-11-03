"""OFFGRID worker SDK: клиент шлюза прорабов и рантайм воркера (ADR-45)."""

from .client import (
    GATEWAY_CONTRACT,
    GatewayClient,
    GatewayError,
    NotOwnerError,
    RetryPolicy,
    UnauthorizedError,
    VersionMismatchError,
)
from .models import Blank, ChargeLine, ClaimedTask, CommitRequest, TaskState, TaskStatus, Verdict
from .worker import DataRejected, Decline, Outcome, TaskContext, TaskError, Worker, WorkerConfig

__all__ = [
    "GATEWAY_CONTRACT",
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
    "VersionMismatchError",
    "Verdict",
    "Worker",
    "WorkerConfig",
]
