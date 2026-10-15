# syntax=docker/dockerfile:1
# Прораб подразделения. SDK — из ядра bhendho по ссылке (BHENDHO_REF): закрепите на коммите или теге.
FROM python:3.12-slim AS base
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 PIP_DISABLE_PIP_VERSION_CHECK=1
ARG BHENDHO_REF=main
RUN pip install --no-cache-dir "offgrid-worker @ https://github.com/vlad78831457/bhendho/archive/${BHENDHO_REF}.tar.gz#subdirectory=pkg/sdk/python"
WORKDIR /app
COPY worker/pyproject.toml ./
COPY worker/src ./src
RUN pip install --no-cache-dir .
COPY module /module
ENV MODULE_DIR=/module

FROM base AS test
RUN pip install --no-cache-dir '.[test]'
COPY worker/tests ./tests
CMD ["sh", "-c", "ruff format --check src tests && ruff check src tests && mypy src && pytest -q"]

FROM base AS runtime
RUN useradd --uid 10001 --no-create-home worker
USER worker
CMD ["department-worker"]
