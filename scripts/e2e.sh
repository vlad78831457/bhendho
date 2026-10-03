#!/bin/sh
# e2e: Python SDK против живого Go-ядра в отдельном compose-проекте.
set -eu
cd "$(dirname "$0")/.."
export COMPOSE_PROJECT_NAME=bhendho-e2e
JWT_SECRET=$(openssl rand -hex 32)
export JWT_SECRET
cleanup() { docker compose --profile e2e down -v --remove-orphans >/dev/null 2>&1 || true; }
trap cleanup EXIT

docker compose --profile e2e up -d --build --wait postgres
docker compose --profile e2e up -d --build core-e2e
# Токен живёт только в переменной окружения этого процесса.
WORKER_TOKEN=""
for _ in $(seq 1 30); do
  if WORKER_TOKEN=$(docker compose exec -T core-e2e /core issue-token --service echo --name e2e 2>/dev/null); then break; fi
  sleep 1
done
[ -n "$WORKER_TOKEN" ] || { echo "core-e2e did not start"; docker compose logs core-e2e; exit 1; }
export WORKER_TOKEN
docker compose --profile e2e run --rm --build sdk-e2e
