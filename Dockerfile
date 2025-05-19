# ────────────────────────────────
# Etapa de compilación
# ────────────────────────────────
FROM golang:1.23 AS builder

WORKDIR /app

# Instalar dependencias necesarias para compilar bimg con libvips
RUN apt-get update && apt-get install -y \
    libvips libvips-dev pkg-config

# Módulos de Go
COPY go.mod ./
COPY go.sum ./
RUN go mod download

# Código fuente
COPY . ./

# Compilar el binario
RUN go build -o main ./server.go

# ────────────────────────────────
# Etapa de ejecución
# ────────────────────────────────
FROM debian:bookworm-slim

# Instalar solo lo necesario para ejecutar libvips (sin headers de desarrollo)
RUN apt-get update && apt-get install -y \
    libvips curl ca-certificates && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Copiar binario compilado
COPY --from=builder /app/main .

# Entrypoint script
COPY bin/sh/entrypoint.sh .

# Permisos de ejecución
RUN chmod +x /app/entrypoint.sh

# Ejecutar como user no-root (ID 1000)
USER 1000

# Entrypoint
ENTRYPOINT ["/bin/sh", "/app/entrypoint.sh"]
