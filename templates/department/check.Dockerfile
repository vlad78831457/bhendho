# syntax=docker/dockerfile:1
# Проверка паспорта и форм теми же правилами, что у ядра: `core check-module` из bhendho (BHENDHO_REF).
FROM golang:1.25-alpine AS core
ARG BHENDHO_REF=main
ADD https://github.com/vlad78831457/bhendho/archive/${BHENDHO_REF}.tar.gz /tmp/core.tgz
RUN mkdir /src && tar -xzf /tmp/core.tgz -C /src --strip-components=1 && cd /src && CGO_ENABLED=0 go build -o /core ./cmd/core

FROM alpine:3.22
COPY --from=core /core /core
COPY module /module
CMD ["/core", "check-module", "/module"]
