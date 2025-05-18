FROM golang:1.23 AS builder

WORKDIR /app

COPY go.mod ./
COPY go.sum ./
RUN go mod download

COPY . ./
RUN go build -o main ./server.go

# ────────────────────────────────
FROM debian:bookworm-slim

# Instala dependencias necesarias para bimg/libvips
RUN apt-get update && apt-get install -y \
    libvips libvips-dev curl ca-certificates && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=builder /app/main .
COPY bin/sh/entrypoint.sh .

RUN chmod +x /app/entrypoint.sh

USER 1000

ENTRYPOINT ["/bin/sh", "/app/entrypoint.sh"]
