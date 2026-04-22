APP_NAME := fri-api
COMPOSE_FILE := deployments/docker-compose.yml

.PHONY: help docker-up docker-down docker-restart docker-logs docker-ps docker-build build run import tidy fmt test check

help:
	@echo "Available commands:"
	@echo "  make docker-up      - build and start app + postgres in Docker"
	@echo "  make docker-down    - stop Docker services"
	@echo "  make docker-restart - restart Docker services"
	@echo "  make docker-logs    - follow Docker logs"
	@echo "  make docker-ps      - show Docker services status"
	@echo "  make docker-build   - rebuild Docker images"
	@echo "  make run          - run API locally without Docker"
	@echo "  make import       - import legacy HTML into database"
	@echo "  make build        - build API binary"
	@echo "  make fmt          - format Go code"
	@echo "  make test         - run Go tests"
	@echo "  make tidy         - tidy Go modules"
	@echo "  make check        - fmt + test + build"

docker-up:
	docker compose -f $(COMPOSE_FILE) up --build -d

docker-down:
	docker compose -f $(COMPOSE_FILE) down

docker-restart: docker-down docker-up

docker-logs:
	docker compose -f $(COMPOSE_FILE) logs -f

docker-ps:
	docker compose -f $(COMPOSE_FILE) ps

docker-build:
	docker compose -f $(COMPOSE_FILE) build

run:
	go run ./cmd/api

import:
	go run ./cmd/importer

build:
	mkdir -p bin
	go build -o bin/$(APP_NAME) ./cmd/api

tidy:
	go mod tidy

fmt:
	gofmt -w ./cmd ./internal

test:
	go test ./...

check: fmt test build
