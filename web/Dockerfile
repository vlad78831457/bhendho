# syntax=docker/dockerfile:1
# Фронт движка (ADR-64): фронт по бланкам и панель админа. Проверки и сборка — только в Docker.
# Контекст сборки — корень bhendho: клиент API людей и его типы — исходники SDK pkg/sdk/ts/src (ADR-81), в src/offgrid.
FROM node:22-alpine AS deps
WORKDIR /app
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/tsconfig.json web/vite.config.ts web/index.html ./
COPY web/public ./public
COPY web/src ./src
COPY pkg/sdk/ts/src ./src/offgrid

FROM deps AS test
COPY web/tests/unit ./tests/unit
CMD ["sh", "-c", "npx tsc --noEmit && npx vitest run"]

FROM deps AS build
RUN npx tsc --noEmit && npx vite build
