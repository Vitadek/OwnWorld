# --- Stage 1: Builder ---
FROM golang:1.22-alpine AS builder

# Install build tools AND sqlite-dev (Required for linking against system lib)
RUN apk add --no-cache git build-base sqlite-dev

WORKDIR /app

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
# -tags libsqlite3: Tells go-sqlite3 to link against the installed system library 
#                   instead of compiling the incompatible bundled C source.
RUN CGO_ENABLED=1 GOOS=linux go build -tags libsqlite3 -ldflags="-s -w" -o ownworld .

# --- Stage 2: Runner ---
FROM alpine:latest

# Install certificates and sqlite shared libraries
RUN apk add --no-cache ca-certificates sqlite-libs

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
