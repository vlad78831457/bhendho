# Подразделение на ядре OFFGRID. Всё в Docker.
.PHONY: help test check

help: ## список команд
	@grep -E '^[a-z-]+:.*## ' Makefile | sed 's/:.*## /\t/'

check: ## паспорт и формы — правилами ядра
	docker compose run --rm --build check

test: check ## паспорт и формы; прораб: ruff, mypy, pytest
	docker compose run --rm --build worker-test
