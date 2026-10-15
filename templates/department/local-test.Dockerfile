# syntax=docker/dockerfile:1
# Только для make test ядра: прораб заготовки против SDK из этого же репозитория (контекст — корень bhendho).
FROM python:3.12-slim
ENV PYTHONDONTWRITEBYTECODE=1 PIP_DISABLE_PIP_VERSION_CHECK=1
WORKDIR /app
COPY pkg/sdk/python /sdk
RUN pip install --no-cache-dir /sdk
COPY templates/department/worker/pyproject.toml ./
COPY templates/department/worker/src ./src
RUN pip install --no-cache-dir '.[test]'
COPY templates/department/worker/tests ./tests
CMD ["sh", "-c", "ruff format --check src tests && ruff check src tests && mypy src && pytest -q"]
