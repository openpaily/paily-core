# ── Build stage ───────────────────────────────────────────────────────────────
# Build context must be the parent directory (pailes/) so the replace directive
# github.com/openpaily/paily-fetch => ../fetcher/paily-fetch resolves correctly.
#
#   docker build -f paily-core/Dockerfile -t paily-core ..
#
FROM golang:1.24-alpine AS builder

RUN apk add --no-cache gcc musl-dev

WORKDIR /src

COPY paily-core          ./paily-core

WORKDIR /src/paily-core

RUN go mod download

RUN CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags="-s -w" --tags="noclash" \
    -o /bin/paily-core ./cmd/server

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /bin/paily-core .

# Data directory for the default SQLite DSN (./data/data.db)
RUN mkdir -p /app/data

EXPOSE 21380

ENTRYPOINT ["/app/paily-core"]
