# ---------- Etapa de compilación ----------
FROM debian:bookworm-slim AS build-env

# Instala Go 1.24.3 manualmente + libvips y pkg-config
RUN apt-get update && apt-get install -y \
    curl ca-certificates tar libvips libvips-dev pkg-config && \
    curl -LO https://go.dev/dl/go1.24.3.linux-amd64.tar.gz && \
    tar -C /usr/local -xzf go1.24.3.linux-amd64.tar.gz && \
    ln -s /usr/local/go/bin/go /usr/bin/go

ENV PATH="/usr/local/go/bin:$PATH"

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . ./

# Compila con salida visible y límite de memoria
ENV GOMEMLIMIT=256MiB
RUN go build -v -o main ./server.go | tee /dev/stderr

# ---------- Etapa de ejecución ----------
FROM debian:bookworm-slim

# Solo las libs necesarias para tiempo de ejecución
RUN apt-get update && apt-get install -y \
    libvips curl ca-certificates && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=build-env /app/main ./
COPY bin/sh/entrypoint.sh ./

RUN chmod +x /app/entrypoint.sh

USER 1000
ENTRYPOINT ["/bin/sh", "/app/entrypoint.sh"]
