# ── Stage 1: builder ────────────────────────────────────────────────────────
FROM golang:1.25-bookworm AS builder

WORKDIR /src
COPY . .

# Install templ and generate _templ.go files
RUN go install github.com/a-h/templ/cmd/templ@v0.3.1001
RUN templ generate

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go build -o /app/server .

# ── Stage 2: runtime ────────────────────────────────────────────────────────
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates \
      openssl \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /app/server /app/server
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]
