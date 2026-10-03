# offgrid-worker — Python SDK прораба

Клиент шлюза ядра OFFGRID (ADR-45) и рантайм воркера. Воркер пишет только предметную логику (ADR-49); очередь, попытки, heartbeat, повторы и отчёт делает SDK.

## Воркер за 20 строк

```python
import asyncio, os
from offgrid_worker import GatewayClient, TaskContext, TaskError, Worker, WorkerConfig

async def generate(ctx: TaskContext) -> dict:
    values = ctx.values                         # копия материализованного снимка (ADR-10, ADR-14)
    hero = values["hero"]["values"]["name"]     # fact_ref уже развёрнут ядром
    await ctx.progress(30, "text_ready")        # эфемерный прогресс → task.progress у пользователя
    ctx.charge("tokens", 12400)                 # счёт в единицах воркера (ADR-47)
    ctx.charge("images", 4)
    if not hero:
        raise TaskError("empty hero", report={"data_gaps": ["hero"]})  # → WORKER_ERROR
    return {"pages": 4}

async def main() -> None:
    client = GatewayClient(os.environ["CORE_URL"], os.environ["WORKER_TOKEN"])
    cfg = WorkerConfig(target_service="taleweaver", schema_versions=[1], concurrency=4)
    await Worker(client, cfg, generate, estimate=lambda blank: 120).run()

asyncio.run(main())
```

## Что делает рантайм

| Шаг | Поведение |
|---|---|
| claim | опрашивает очередь своего `target_service` и версий; пусто — пауза растёт до `max_poll_interval` |
| accept | `estimate(blank)` → ETA в секундах (ядро ждёт 3 × ETA); `raise Decline` — вернуть задачу без траты попытки |
| работа | обработчик и heartbeat параллельно; задачу забрали (истёк лок, отмена) — обработчик отменяется, отчёт не шлётся |
| commit | результат + `worker_charge` из `ctx.charge`; `DataRejected` → `DATA_REJECTED` (данные неприменимы, retry нет); `TaskError` → `WORKER_ERROR` с предметными полями report; прочее исключение → `WORKER_ERROR` с текстом для админа |
| сеть | повтор с растущей паузой на сетевых сбоях и 5xx — безопасно, ядро дедуплицирует по состоянию строки (ADR-20) |
| файлы | в снимке файл — манифест `{file_id, filename, size, mime}`; байты — `await ctx.file(file_id)` (только файлы бланка этой задачи, пока она у прораба; удалённый пользователем — `None`), ADR-60 |

Возвращайте `Outcome(result=…, report={"explanation": …, "evidence": …})`, чтобы передать предметные поля отчёта. Вердикт и сообщение пользователю ставит ядро по реестру 5.10; текст воркера пользователю не показывается (ADR-24).

## Проверка

```bash
make test   # ruff, mypy --strict, pytest (фейковый шлюз)
make e2e    # SDK против живого ядра: echo-модуль, списание в двух единицах
```
