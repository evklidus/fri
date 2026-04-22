# FRI Phase 1

Первая фаза `Football Reputation Index` на `Go + Gin + PostgreSQL`.

Что реализовано:

- backend API на `Gin`
- PostgreSQL schema и встроенные миграции
- импорт игроков и новостей из legacy HTML
- сидирование БД при первом запуске
- фронтенд на основе исходного HTML, подключённый к live API
- локальный запуск через `Docker Compose`

## Структура

Проект организован по мотивам [`golang-standards/project-layout`](https://github.com/golang-standards/project-layout):

- `cmd/api` — точка входа HTTP API
- `cmd/importer` — ручной импорт legacy HTML в БД
- `internal` — приватная бизнес-логика и инфраструктура
- `configs` — пример переменных окружения
- `deployments` — локальный docker compose
- `web` — фронтенд и source legacy HTML

## Roadmap

Полный план фаз и задач вынесен в [docs/project-phases.md](/Users/evklidus/Documents/Codex/2026-04-20-fri-football-reputation-index-1-fri/dev/my_projects/fri/docs/project-phases.md).

## Быстрый старт

```bash
cp configs/.env.example .env
make docker-up
```

После запуска:

- фронтенд и API: `http://localhost:8080`
- healthcheck: `http://localhost:8080/api/health`

Полезные команды:

```bash
make help
make docker-ps
make docker-logs
make docker-down
make check
```
