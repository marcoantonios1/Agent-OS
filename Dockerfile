# ── Build stage ───────────────────────────────────────────────────────────────
FROM golang:1.26-alpine AS builder

WORKDIR /src

# Download dependencies first so this layer is cached separately from source.
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build both binaries.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/agentos  ./cmd/agentos
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/migrate  ./cmd/migrate

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM alpine:3.20

# ca-certificates: needed for outbound HTTPS (LLM gateway, search API, OAuth).
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /out/agentos ./agentos
COPY --from=builder /out/migrate  ./migrate

# /data holds the SQLite database; mount a named volume here in production.
RUN mkdir -p /data

EXPOSE 9091

ENTRYPOINT ["./agentos"]
