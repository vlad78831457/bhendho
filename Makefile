# bhendho — ядро OFFGRID. Всё в Docker.
.PHONY: help build up down test e2e logs

help: ## список команд
	@grep -E '^[a-z-]+:.*## ' Makefile | sed 's/:.*## /\t/'

build: ## собрать образы
	docker compose build

up: ## ядро и Postgres в фоне (нужен JWT_SECRET в .env)
	docker compose up -d

down: ## остановить
	docker compose down

test: ## gofmt, go vet, go test против Postgres + тесты Python SDK
	docker compose run --rm --build test
	docker compose run --rm --build sdk-test

e2e: ## Python SDK против живого ядра (отдельный compose-проект)
	./scripts/e2e.sh

logs: ## логи
	docker compose logs -f
