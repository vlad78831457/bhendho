# bhendho

**bhendho** — ядро экосистемы OFFGRID (проект [orb-bundam](https://github.com/vlad78831457/orb-bundam)).
Имя — от праиндоевропейского *bʰendʰ- «связывать»: ядро связывает пользователей, деньги, данные и прорабов — внешние службы, которые выполняют задачи.

Ядро не знает предметной области. Сферы услуг (сказки, алгоритмы, закупки, тьюторы…) подключаются модулями: манифест `module.yaml` с бланками и прораб, который берёт задачи через шлюз.

## Что внутри

| Каталог | Что |
|---|---|
| `cmd/core` | точка входа ядра |
| `internal/core` | ядро: auth (пароль, JWT, OAuth/OIDC, VK ID, кабинет), engine (задачи, факты, кошельки, промокоды, файлы), очередь и шлюз прорабов, realtime (WebSocket), миграции Postgres |
| `contracts/` | языконезависимые контракты: манифест модуля, конверт бланка, события realtime, OpenAPI шлюза прорабов и эмитента промокодов |
| `pkg/sdk/python` | Python SDK прораба (`offgrid_worker`) |
| `docs/` | спецификация ядра, история решений ADR-01…ADR-60, вход через OAuth, промокоды |

Стек: Go 1.25, Postgres 17, pgx, JWT, coder/websocket; SDK — Python 3, Pydantic v2, httpx.

## Команды

```
make test     # gofmt, go vet, go test против Postgres + тесты SDK (Docker)
make e2e      # Python SDK против живого ядра
make up       # ядро и Postgres; сначала: cp .env.example .env и задать JWT_SECRET
```

## Откуда код

Перенесён 2026-10-03 из `taleweaver-app-vr-ab` (main `0912a91`): это ядро платформы `r-aaa-core-a` v0.1.0 вместе с улучшениями, сделанными для Taleweaver (промокоды ADR-57, OAuth ADR-58, кабинет ADR-59, файлы ADR-60). Модули и фронт сюда не входят — решение P-002 в `orb-bundam`.
