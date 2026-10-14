# syntax=docker/dockerfile:1
# Фронт движка (ADR-64): фронт по бланкам и панель админа. Проверки и сборка — только в Docker.
FROM node:22-alpine AS deps
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY tsconfig.json vite.config.ts index.html ./
COPY public ./public
COPY src ./src

FROM deps AS test
COPY tests/unit ./tests/unit
CMD ["sh", "-c", "npx tsc --noEmit && npx vitest run"]

FROM deps AS build
RUN npx tsc --noEmit && npx vite build
