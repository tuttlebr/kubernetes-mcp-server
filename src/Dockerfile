# Stage 1: Build the Go binary
FROM --platform=$BUILDPLATFORM golang:alpine AS builder

# Install git (required for go modules) and update all packages
RUN apk --no-cache upgrade && apk --no-cache add git ca-certificates

# Set the working directory inside the container
WORKDIR /app

# Copy go mod and sum files
COPY go.mod go.sum ./

# Set GOTOOLCHAIN to auto to allow automatic toolchain upgrades
ENV GOTOOLCHAIN=auto

# Download dependencies
RUN go mod download

# Copy the source code
COPY . .

# Build arguments for cross-compilation
ARG TARGETOS
ARG TARGETARCH

# Build the Go app with cross-compilation support
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -ldflags="-w -s" -o k8s-mcp-server main.go

# Stage 2: Install opencode for agent debugging support
FROM oven/bun:1-alpine AS opencode-installer
RUN bun install -g opencode-ai

# Stage 3: Final runtime image
FROM alpine:latest

# Install ca-certificates, curl for health checks, tzdata for timezone,
# and gcompat + libstdc++ for Bun/opencode runtime
RUN apk --no-cache add ca-certificates curl tzdata gcompat libstdc++

# Set timezone
ENV TZ=America/New_York

# Create a non-root user
RUN addgroup -g 1001 -S appgroup && \
    adduser -u 1001 -S appuser -G appgroup

# Copy the binary from the builder stage
COPY --from=builder /app/k8s-mcp-server /usr/local/bin/k8s-mcp-server
RUN chmod +x /usr/local/bin/k8s-mcp-server

# Copy Bun runtime and opencode for agent debugging
COPY --from=opencode-installer /usr/local/bin/bun /usr/local/bin/bun
COPY --from=opencode-installer --chown=1001:1001 /root/.bun/install/global /home/appuser/.bun/install/global
RUN ln -s /home/appuser/.bun/install/global/node_modules/.bin/opencode /usr/local/bin/opencode

# Switch to non-root user
USER appuser

# Expose the port the app runs on
EXPOSE 8080

# Set default environment variables
ENV SERVER_MODE=streamable-http
ENV SERVER_PORT=8080

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=30s --retries=5 \
    CMD curl -f http://localhost:8080/healthz || exit 1

# Command to run the executable
ENTRYPOINT ["/usr/local/bin/k8s-mcp-server"]
