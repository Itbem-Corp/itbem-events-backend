# ---------- Etapa de compilación ----------
FROM debian:bookworm-slim AS build-env

RUN apt-get update && apt-get install -y \
    curl ca-certificates tar libvips libvips-dev pkg-config && \
    curl -LO https://go.dev/dl/go1.24.3.linux-amd64.tar.gz && \
    tar -C /usr/local -xzf go1.24.3.linux-amd64.tar.gz && \
    ln -s /usr/local/go/bin/go /usr/bin/go

ENV PATH="/usr/local/go/bin:$PATH"
ENV GOMAXPROCS=1
ENV GOMEMLIMIT=256MiB

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . ./

RUN go build -v -trimpath -ldflags="-s -w" -o main ./server.go | tee /dev/stderr

# ---------- Etapa de ejecución ----------
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y \
    libvips curl ca-certificates && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=build-env /app/main ./
COPY bin/sh/entrypoint.sh ./

RUN chmod +x /app/entrypoint.sh

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD curl -sf http://localhost:8080/health || exit 1

RUN useradd -u 1000 -m appuser
USER appuser

ENTRYPOINT ["/bin/sh", "/app/entrypoint.sh"]
