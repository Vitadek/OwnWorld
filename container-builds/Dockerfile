# --- Stage 1: Builder ---
FROM golang:1.22-alpine AS builder

# Install build tools
RUN apk add --no-cache git build-base

WORKDIR /app

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary (Statically linked for scratch/alpine compatibility)
# -ldflags="-s -w" strips debug info to reduce size
RUN CGO_ENABLED=1 GOOS=linux go build -ldflags="-s -w" -o ownworld .

# --- Stage 2: Runner ---
FROM alpine:latest

# Install certificates for HTTPS/Federation
RUN apk add --no-cache ca-certificates sqlite

WORKDIR /app

# Copy binary from builder
COPY --from=builder /app/ownworld .

# Create data directory
RUN mkdir -p /app/data
VOLUME ["/app/data"]

# Expose the Game Port
EXPOSE 8080

# Set default environment variables
ENV OWNWORLD_COMMAND_CONTROL="true"
ENV OWNWORLD_PEERING_MODE="promiscuous"

# Run
CMD ["./ownworld"]
