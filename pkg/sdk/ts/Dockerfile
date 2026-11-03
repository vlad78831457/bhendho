# syntax=docker/dockerfile:1
# SDK фронта (ADR-72, ADR-81): tsc и vitest — только в Docker. Типы API людей (src/people.gen.ts) собираются из
# договора contracts/interfaces/people.openapi.yaml (контекст contracts в compose); устаревший файл — провал проверки.
FROM node:22-alpine
WORKDIR /sdk
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY tsconfig.json vitest.config.ts ./
COPY src ./src
COPY tests ./tests
COPY --from=contracts interfaces/people.openapi.yaml /contracts/people.openapi.yaml
CMD ["sh", "-c", "npx openapi-typescript /contracts/people.openapi.yaml -o /tmp/people.gen.ts >/dev/null && { cmp -s /tmp/people.gen.ts src/people.gen.ts || { echo 'src/people.gen.ts устарел — make people-types'; exit 1; }; } && npx tsc --noEmit && npx vitest run"]
