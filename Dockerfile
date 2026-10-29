# ---------- Etapa de compilación ----------
FROM golang:1.26.10-bookworm@sha256:23ce71337b726608afb1bc00986e630a05034a87a87ea1fc2cd1ae5cf33387eb AS build-env

RUN apt-get -o Acquire::Retries=3 update && apt-get -o Acquire::Retries=3 install -y --no-install-recommends \
    libvips-dev pkg-config && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

ENV CGO_ENABLED=1

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . ./

RUN go build -v -trimpath -ldflags="-s -w" -o main ./cmd/api

# ---------- Etapa de ejecución ----------
FROM debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818

ARG SOURCE_REVISION
LABEL org.opencontainers.image.revision=$SOURCE_REVISION

RUN apt-get -o Acquire::Retries=3 update && apt-get -o Acquire::Retries=3 install -y --no-install-recommends \
    libvips curl ca-certificates git util-linux && \
    apt-get -o Acquire::Retries=3 upgrade -y && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=build-env /app/main ./
COPY bin/sh/entrypoint.sh ./

# Windows checkouts can materialize shell scripts with CRLF. Normalize the
# runtime copy so the image remains reproducible regardless of git autocrlf.
RUN sed -i 's/\r$//' /app/entrypoint.sh && chmod +x /app/entrypoint.sh

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD curl -sf "http://localhost:${PORT:-8080}/health" || exit 1

RUN useradd -u 1000 -m appuser
USER appuser

ENTRYPOINT ["/bin/sh", "/app/entrypoint.sh"]
