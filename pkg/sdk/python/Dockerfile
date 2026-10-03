# syntax=docker/dockerfile:1
# Контекст сборки — корень репозитория: тестам нужны contracts/.
FROM python:3.12-slim AS test
ENV PYTHONDONTWRITEBYTECODE=1 PIP_DISABLE_PIP_VERSION_CHECK=1 CONTRACTS_DIR=/contracts
WORKDIR /sdk
COPY pkg/sdk/python/pyproject.toml ./
COPY pkg/sdk/python/src ./src
RUN pip install --no-cache-dir -e '.[test]'
COPY pkg/sdk/python/tests ./tests
COPY contracts /contracts
CMD ["sh", "-c", "ruff format --check src tests && ruff check src tests && mypy src && pytest -q"]
