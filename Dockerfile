# ── Build stage ───────────────────────────────────────────────────────────────
# Use Debian-based image (not Alpine) so the CGO binary links against glibc,
# matching the debian:bookworm-slim runtime stage. sqlite-vec requires CGO.
FROM golang:1.26-bookworm AS builder

RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc \
    libc6-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src

# Download dependencies first so this layer is cached separately from source.
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build both binaries.
# agentos needs CGO for sqlite-vec; migrate does not.
COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/agentos  ./cmd/agentos
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/migrate  ./cmd/migrate

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM debian:bookworm-slim

# ffmpeg: video frame extraction for multimodal LLM analysis.
# ca-certificates: outbound HTTPS (LLM gateway, search API, OAuth).
# tzdata: correct time zone handling in scheduled reminders.
RUN apt-get update && apt-get install -y --no-install-recommends \
    ffmpeg \
    ca-certificates \
    tzdata \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=builder /out/agentos  ./agentos
COPY --from=builder /out/migrate  ./migrate
COPY --from=builder /src/agents   ./agents

# /data holds the SQLite database; mount a named volume here in production.
RUN mkdir -p /data

EXPOSE 9091

ENTRYPOINT ["./agentos"]
