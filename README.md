# FRI

FRI (Football Reputation Index) — платформа для автоматического расчёта репутационного рейтинга футболистов на основе агрегации данных из множества источников. Текущая версия — статичный HTML-файл с захардкоженными данными. Задача — превратить его в живую систему с автообновлением.

`Football Reputation Index` на `Go + Gin + PostgreSQL`.

Статус:

- Фаза 1 завершена
- Фаза 2 завершена (production источники подключены)
- Фаза 3 в работе: антиабьюз голосования, страница игрока с графиком истории, фильтр по лиге, skeleton loaders

Все фазы:

- [docs/project-phases.md](/Users/evklidus/Desktop/Dev/my%20projects/fri/docs/project-phases.md)

Что реализовано:

- backend API на `Gin`
- PostgreSQL schema и встроенные миграции
- импорт игроков и новостей из legacy HTML
- сидирование БД при первом запуске
- фронтенд на основе исходного HTML, подключённый к live API
- локальный запуск через `Docker Compose`
- media ingestion через `GDELT DOC 2.0` (EN + RU), без API-ключа, ≤1 RPS rate-limit
- VADER-based sentiment (EN) + ручной RU-лексикон + футбольные доменные boosters (hat-trick, racism, red card …)
- social ingestion через `YouTube Data API` (если задан `YOUTUBE_API_KEY`); followers/engagement остаются на demo до подключения Instagram/Twitter
- performance ingestion через `API-Football Pro`: сезонные статы + last-5 form + topN rank по позиции в лиге
- fallback на локальный demo-provider для performance, если API-Football не нашёл игрока/клуб
- external mapping `api_football` ID игрока/команды в `player_external_ids` — после первого успешного matching последующие sync идут напрямую через `/players?id=...`
- sanity-check (position group, age window ±3, минимальная активность) защищает от сохранения mapping на тёзках
- фоновый scheduler для `media`, `social`, `performance`
- tracking таблица `component_updates`
- raw snapshots для `social` и `performance`
- автоматический пересчёт `FRI` и запись в `fri_history` после sync-компонентов

## Структура

Проект организован по мотивам [`golang-standards/project-layout`](https://github.com/golang-standards/project-layout):

- `cmd/api` — точка входа HTTP API
- `cmd/importer` — ручной импорт legacy HTML в БД
- `internal` — приватная бизнес-логика и инфраструктура
- `configs` — пример переменных окружения
- `deployments` — локальный docker compose
- `web` — фронтенд и source legacy HTML

## Roadmap

Полный план фаз и задач вынесен в [docs/project-phases.md](/Users/evklidus/Desktop/Dev/my%20projects/fri/docs/project-phases.md).

## Быстрый старт

```bash
cp configs/.env.example .env
make docker-up
```

Для реальных компонентов добавь ключи в `.env`:

```bash
API_FOOTBALL_KEY=your_api_football_key  # Pro план для Performance
YOUTUBE_API_KEY=your_youtube_key         # для Social (YouTube Views 7d)
```

Актуальный сезон API-Football определяется автоматически: для каждой команды backend дёргает `/leagues?team={id}&current=true` и кеширует результат на сутки. Если запрос упал, используется эвристика по календарному месяцу.

Media ingestion работает без ключа — GDELT public API, ограничен ≤1 RPS внутренним rate-limit'ом.

После запуска:

- фронтенд и API: `http://localhost:8080`
- healthcheck: `http://localhost:8080/api/health`

Полезные команды:

```bash
make help
make docker-ps
make docker-logs
make docker-down
make sync-media
make sync-social
make sync-performance
make sync-all
make api-football-status
make check
```

## Current Handoff

Текущее состояние для продолжения в новом чате:

- проект лежит в `/Users/evklidus/Desktop/Dev/my projects/fri`
- Docker daemon сейчас не был запущен, поэтому live Docker-проверка не выполнена
- `go test ./...`, `go build ./...`, `go vet ./...` проходят
- локальный `.env` создан и содержит `API_FOOTBALL_KEY`; `.env` в `.gitignore`
- API-Football Pro активен
- `make api-football-status` проверяет лимит и статус ключа
- `make sync-performance` использует API-Football provider, если ключ задан
- актуальный сезон по команде определяется через `/leagues?current=true` с in-memory кешем на сутки

Следующие технические шаги:

- сохранять использованный сезон и source provider в `performance_snapshots`
- заменить Google News RSS на GDELT для бюджетного Media API
- подключить YouTube Data API для части Social
