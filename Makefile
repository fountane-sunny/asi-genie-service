# Makefile for ASI Genie Orchestrator

# Variables
DOCKER_REGISTRY ?= your-registry.com
VERSION ?= latest
IMAGE := $(DOCKER_REGISTRY)/asi-genie:$(VERSION)
DOCKER_COMPOSE := docker compose
PORT ?= 8090

# Go variables
GOCMD := go
GOBUILD := $(GOCMD) build
GOTEST := $(GOCMD) test
GOMOD := $(GOCMD) mod
GORUN := $(GOCMD) run

# Paths
APP_PATH := ./cmd/orchestrator
BIN := bin/orchestrator

.PHONY: all help build run run-air run-bin test test-cover vet fmt lint deps clean \
	gen gen-registry gen-registry-local gen-intents \
	db-up db-down db-logs db-ps dev dev-air docker-build docker-push test-health

# Default target
all: test build

help:
	@echo "ASI Genie Orchestrator - Makefile"
	@echo ""
	@echo "Development targets:"
	@echo "  make run                 - Run orchestrator locally with .env"
	@echo "  make run-air             - Run orchestrator with Air live reload"
	@echo "  make dev                 - Start Postgres, then run orchestrator"
	@echo "  make dev-air             - Start Postgres, then run with Air live reload"
	@echo "  make build               - Build orchestrator binary into bin/"
	@echo "  make run-bin             - Build and run the binary with .env"
	@echo "  make test                - Run tests"
	@echo "  make test-cover          - Run tests with coverage report"
	@echo ""
	@echo "Code generation:"
	@echo "  make gen                 - Run all generators, then build and vet"
	@echo "  make gen-registry        - ../asi-*-service specs -> toolregistry/generated/"
	@echo "  make gen-registry-local  - specs/ -> toolregistry/generated/"
	@echo "  make gen-intents         - Scenarios.csv -> internal/intents/catalog_gen.go"
	@echo ""
	@echo "Database (docker compose):"
	@echo "  make db-up / db-down / db-logs / db-ps"
	@echo ""
	@echo "Docker targets:"
	@echo "  make docker-build        - Build Docker image"
	@echo "  make docker-push         - Push Docker image"
	@echo ""
	@echo "Utility targets:"
	@echo "  make deps / fmt / vet / lint / clean / test-health"

# Build / run
build:
	@echo "Building orchestrator..."
	$(GOBUILD) -o $(BIN) $(APP_PATH)

run:
	set -a; . ./.env; set +a; $(GORUN) $(APP_PATH)

run-air:
	@command -v air >/dev/null || { echo "Air is not installed. Run: go install github.com/air-verse/air@latest"; exit 1; }
	set -a; . ./.env; set +a; air

run-bin: build
	set -a; . ./.env; set +a; ./$(BIN)

dev: db-up run

dev-air: db-up run-air

# Tests
test:
	$(GOTEST) -v -race ./...

test-cover:
	$(GOTEST) -count=1 -coverprofile=coverage.out ./...
	$(GOCMD) tool cover -func=coverage.out | tail -1

# Code generation
gen: gen-registry gen-intents
	$(GOBUILD) ./...
	$(GOCMD) vet ./...

gen-registry:
	$(GORUN) ./scripts/gen-registry

gen-registry-local:
	$(GORUN) ./scripts/gen-registry -specs-dir specs

gen-intents:
	$(GORUN) ./scripts/gen-intents

# Database
db-up:
	$(DOCKER_COMPOSE) up -d

db-down:
	$(DOCKER_COMPOSE) down

db-logs:
	$(DOCKER_COMPOSE) logs -f

db-ps:
	$(DOCKER_COMPOSE) ps

# Docker image
docker-build:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE) .

docker-push:
	docker push $(IMAGE)

# Utilities
deps:
	$(GOMOD) download
	$(GOMOD) tidy

fmt:
	$(GOCMD) fmt ./...

vet:
	$(GOCMD) vet ./...

lint:
	golangci-lint run

clean:
	rm -rf bin/ coverage.out

test-health:
	@curl -s http://localhost:$(PORT)/health || echo "Genie not running"
