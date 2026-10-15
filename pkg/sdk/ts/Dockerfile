# syntax=docker/dockerfile:1
# SDK фронта (ADR-72): tsc и vitest — только в Docker.
FROM node:22-alpine
WORKDIR /sdk
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY tsconfig.json vitest.config.ts ./
COPY src ./src
COPY tests ./tests
CMD ["sh", "-c", "npx tsc --noEmit && npx vitest run"]
