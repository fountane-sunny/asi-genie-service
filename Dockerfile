# Build stage
FROM golang:1.24-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates

WORKDIR /app

# Copy go module files first (better layer caching)
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the orchestrator
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-w -s" \
    -o orchestrator \
    ./cmd/orchestrator

# Final stage - minimal image
FROM alpine:3.18

RUN apk --no-cache add ca-certificates tzdata tini

# Create non-root user
RUN addgroup -g 1000 -S appgroup && \
    adduser -u 1000 -S appuser -G appgroup

WORKDIR /app

COPY --from=builder /app/orchestrator .
COPY --from=builder /app/specs ./specs

RUN chown -R appuser:appgroup /app
USER appuser

EXPOSE 8090

LABEL maintainer="ASI Team"
LABEL description="ASI Genie Orchestrator"
LABEL version="${VERSION}"

ENTRYPOINT ["/sbin/tini", "--", "./orchestrator"]
