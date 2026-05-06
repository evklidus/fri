APP_NAME := fri-api
COMPOSE_FILE := deployments/docker-compose.yml

ifneq (,$(wildcard .env))
include .env
export
endif

.PHONY: help docker-up docker-down docker-restart docker-logs docker-ps docker-build build run import sync-media sync-social sync-performance sync-all api-football-status tidy fmt test check

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
	@echo "  make sync-media   - run phase 2 media sync against the live API"
	@echo "  make sync-social  - run phase 2 social sync against the live API"
	@echo "  make sync-performance - run phase 2 performance sync against the live API"
	@echo "  make sync-character - run character keyword scan against recent news"
	@echo "  make sync-all     - run all phase 2/3 sync jobs against the live API"
	@echo "  make api-football-status - show API-Football request limit/status"
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

sync-media:
	curl -s -X POST http://localhost:8080/api/sync/media | jq

sync-social:
	curl -s -X POST http://localhost:8080/api/sync/social | jq

sync-performance:
	curl -s -X POST http://localhost:8080/api/sync/performance | jq

sync-character:
	curl -s -X POST http://localhost:8080/api/sync/character | jq

sync-all:
	curl -s -X POST http://localhost:8080/api/sync/all | jq

api-football-status:
	@test -n "$$API_FOOTBALL_KEY" || (echo "API_FOOTBALL_KEY is not set" && exit 1)
	@curl -s -H "x-apisports-key: $$API_FOOTBALL_KEY" https://v3.football.api-sports.io/status | jq

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
