# Build stage
FROM golang:1.24-alpine AS builder

# Install CA certificates and timezone data for the final image
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary with static linking
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /laplaced ./cmd/bot

# Final stage - scratch image
FROM scratch

LABEL org.opencontainers.image.source="https://github.com/runixer/laplaced"
LABEL org.opencontainers.image.description="Smart Telegram bot with long-term memory"
LABEL org.opencontainers.image.licenses="MIT"

# Copy CA certificates for HTTPS
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy timezone data
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

# Copy the binary
COPY --from=builder /laplaced /laplaced

# Set default timezone (can be overridden with TZ env var)
ENV TZ=UTC

# Expose the default port
EXPOSE 9081

# Run the binary
ENTRYPOINT ["/laplaced"]
