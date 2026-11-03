# bhendho — ядро OFFGRID. Всё в Docker.
.PHONY: help build up down test e2e logs people-types

help: ## список команд
	@grep -E '^[a-z-]+:.*## ' Makefile | sed 's/:.*## /\t/'

build: ## собрать образы
	docker compose build

up: ## ядро и Postgres в фоне (нужен JWT_SECRET в .env)
	docker compose up -d

down: ## остановить
	docker compose down

test: ## gofmt, go vet, go test против Postgres + тесты Python SDK + фронт (tsc, vitest) + заготовка подразделения
	docker compose run --rm --build test
	docker compose run --rm --build sdk-test
	docker compose run --rm --build web-test
	docker compose run --rm --build ts-sdk-test
	docker compose run --rm --build template-test

e2e: ## Python SDK против живого ядра (отдельный compose-проект)
	./scripts/e2e.sh

logs: ## логи
	docker compose logs -f

people-types: ## типы API людей для SDK фронта из договора: pkg/sdk/ts/src/people.gen.ts (ADR-81)
	docker compose build -q ts-sdk-test
	docker compose run --rm --no-deps --user "$$(id -u):$$(id -g)" -e HOME=/tmp -v "$$PWD/pkg/sdk/ts/src:/out" ts-sdk-test \
	  sh -c 'npx openapi-typescript /contracts/people.openapi.yaml -o /out/people.gen.ts'
