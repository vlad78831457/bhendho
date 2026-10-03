# syntax=docker/dockerfile:1
FROM golang:1.25-alpine AS base
ENV CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

# Проверка: vet + тесты (docker compose run --rm test)
FROM base AS test
CMD ["sh", "-c", "test -z \"$(gofmt -l .)\" || { gofmt -l .; echo gofmt required; exit 1; }; go vet ./... && go test -count=1 ./..."]

FROM base AS build
RUN go build -trimpath -ldflags="-s -w" -o /out/core ./cmd/core

FROM gcr.io/distroless/static-debian12:nonroot AS runtime
COPY --from=build /out/core /core
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/core"]
