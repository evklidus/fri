# FRI Project Phases

FRI (Football Reputation Index) — платформа для автоматического расчёта репутационного рейтинга футболистов на основе агрегации данных из множества источников. Текущая версия — статичный HTML-файл с захардкоженными данными. Задача — превратить его в живую систему с автообновлением.

Этот файл фиксирует согласованный roadmap проекта, чтобы фазы и scope не потерялись в переписке.

Стек:

- backend: `Go + Gin`
- database: `PostgreSQL`
- local infra: `Docker Compose`
- frontend: текущий `HTML/CSS/JS` с постепенным переводом на API

## Фаза 1. Основа системы и живой MVP

Статус:

- реализована

Цель:

- убрать захардкоженные данные из HTML и перевести проект на backend + database

Что входит:

- структура проекта на `Go` по мотивам `golang-standards/project-layout`
- backend API на `Gin`
- `PostgreSQL` schema и миграции
- импорт текущих игроков и новостей из legacy HTML
- таблицы:
  - `players`
  - `fri_scores`
  - `fri_history`
  - `news_items`
  - `fan_votes`
- базовые API:
  - `GET /api/players`
  - `GET /api/players/:id`
  - `GET /api/players/:id/history`
  - `GET /api/players/:id/news`
  - `GET /api/leaderboard`
  - `GET /api/news/feed`
  - `POST /api/players/:id/vote`
- перевод текущего фронтенда с локальных JS-массивов на `fetch` к API
- локальный запуск через `Docker Compose`

Результат фазы:

- сайт работает от живой БД и backend API
- данные legacy HTML больше не являются runtime-источником истины
- голосование фанатов записывается в БД и влияет на `fan` и `FRI`

Что сознательно не входит:

- внешние парсеры `SofaScore`, `Transfermarkt`, соцсети
- `Character` moderation workflow
- production deploy
- DNS, SSL, домен
- `Redis`, очереди, `k8s`

## Фаза 2. Data ingestion и автоматический пересчёт FRI

Статус:

- реализована для локального/demo-контура

Что реализовано:

- ingestion infrastructure на backend
- таблица `component_updates`
- дополнительные timestamps обновления компонент в `fri_scores`
- live media sync через `Google News RSS`
- эвристический sentiment scoring и расчёт `Media Score`
- social sync через локальный `demo-social-provider`
- performance sync через `api-football`, если задан `API_FOOTBALL_KEY`
- fallback на локальный `demo-performance-provider`, если ключа нет или игрок не найден
- таблица `social_snapshots`
- таблица `performance_snapshots`
- автоматический пересчёт общего `FRI` после изменения `media`, `social`, `performance`
- фоновый scheduler в API-процессе
- ручной запуск sync через `POST /api/sync/media`
- ручной запуск sync через `POST /api/sync/social`
- ручной запуск sync через `POST /api/sync/performance`
- общий запуск через `POST /api/sync/all`
- просмотр статусов sync через `GET /api/sync/updates`
- базовые retries/backoff для внешнего media RSS provider

Текущее состояние API-Football:

- ключ подключается через `.env` как `API_FOOTBALL_KEY`, в код ключ не пишется
- Pro-план активен
- актуальный сезон по команде определяется автоматически через `/leagues?team={id}&current=true` с in-memory кешем на 24 часа
- если auto-lookup упал, используется эвристика по календарному месяцу (июль+ → текущий год, иначе предыдущий)

Что остаётся на последующие production-итерации:

- внешние `Social` providers с реальными API/ключами
- повышение качества `Performance` provider после перехода с бюджетного API-Football на Sportmonks/STATSCORE/Opta при необходимости
- нормализация по полной исторической базе игроков
- proxy strategy для сложных источников

Что остаётся за рамками текущего scope (требует бюджета или скрапинга, отложено до monetization):

- Instagram Graph API / RapidAPI Instagram (~$50/мес) — закрывает 40% Social
- Twitter/X API Basic ($200/мес) — закрывает 30% Social
- TikTok view counts — нет официального API
- WhoScored / SofaScore fan ratings — нет публичного API, риск блокировки парсингом
- FBref / Transfermarkt продвинутая статистика и форма — без API, blocking-риск

Сделано:

- переход с API-Football Free на Pro
- auto-current-season lookup вместо фиксированного `API_FOOTBALL_SEASON`
- generic-таблица `player_external_ids (player_id, provider, external_id, external_team_id)` под мульти-провайдерность; на `(provider, external_id)` повешен `UNIQUE`
- mapping заполняется автоматически после первого успешного matching через текстовый поиск
- sanity-check перед сохранением mapping: position group, age window `±3`, минимум `1 матч` или `90 минут` активности
- при mapping path → прямой запрос `/players?id={id}&season={auto}`, экономит ~60% запросов на sync
- transfer-aware: при смене команды у игрока `external_team_id` обновляется автоматически
- Performance: добавлены **last-5 form** (`/fixtures` + `/fixtures/players`) и **topN rank в лиге по позиции** (`/players/topscorers` + `/players/topassists`) с in-memory кешем (form 1h, topN 6h)
- веса Performance перебалансированы: rating 0.30, GA/90 0.18, xGxA 0.18, posRank 0.14, minutes 0.10, form 0.10
- Media: переход с `Google News RSS` на `GDELT DOC 2.0` (EN + RU)
- Sentiment: `jonreiter/govader` (EN) + ручной RU-лексикон + футбольные доменные boosters; cyrillic-ratio detection
- Social: `YouTube Data API` (search.list + videos.list) с factory-fallback на demo при пустом ключе

Цель:

- подключить реальные внешние источники и сделать автоматическое обновление компонентов

Что входит:

- ingestion-слой для загрузки данных по компонентам `Performance`, `Social`, `Media`
- отдельные jobs/workers по расписанию
- retries, timeout, логирование ошибок, stale-data flags
- нормализация сырых метрик в шкалу `0–100`
- автоматический пересчёт:
  - `FRI`
  - `delta_7d`
  - `delta_30d`
  - `trend`
- техтаблица статусов обновлений, например `component_updates`
- хранение raw snapshots по источникам, где это нужно

Приоритет источников:

- сначала наиболее стабильные
- `Media`: `NewsAPI`, `Google News RSS`, sentiment pipeline
- `Social`: официальные API там, где реально получить доступ
- `Performance`: отдельными адаптерами, с учётом рисков блокировок

Результат фазы:

- рейтинг обновляется автоматически
- появляется операционный контур обновлений и ошибок
- можно постепенно расширять базу игроков без ручного редактирования HTML

Риски:

- блокировки парсеров
- rate limits
- нестабильность неофициальных источников

## Фаза 3. Продуктовая интеграция: фронтенд, карточка игрока, история, фильтры

Статус:

- в работе

Сделано:

- антиабьюз голосования: 1 голос на (player, IP) за 24 часа; HTTP 429 при превышении (миграция не нужна — уже храним `ip_hash` и `created_at` в `fan_votes`)
- модалка игрока с графиком истории FRI через Chart.js + time-axis (`/api/players/:id/history`)
- лента новостей по игроку в модалке (`/api/players/:id/news`)
- skeleton loaders в модалке пока запросы в полёте
- поле `players.league`, миграция `006_players_league.sql` с backfill для существующих игроков по club→league маппингу
- dropdown фильтр по лиге на leaderboard (client-side фильтрация)
- минимальный Character pipeline: keyword-scan по `news_items` (doping/racism/red card/scandal/fair play/charity и т.п.) с negator-защитой (`victim of`, `condemns`, …), cap ±15 на sync, audit-trail в `character_events`. Это покрытие 10% веса FRI на реальных данных без модерации; полноценный workflow с админкой — фаза 4.

Цель:

- довести продукт до полноценного пользовательского интерфейса поверх backend

Что входит:

- страница игрока с полной детализацией
- график истории `FRI`
- компонентные breakdown-блоки `P/S/F/M/C`
- фильтры:
  - по позиции
  - по клубу
  - по лиге
- улучшение leaderboard
- полноценная интеграция `Fan Poll`
- лента новостей по игроку и общая лента
- skeleton loaders и состояние загрузки/ошибок

Технические задачи:

- подключить `Chart.js` или другой лёгкий графический слой
- стабилизировать API-контракты под фронтенд
- добавить пагинацию и query filters там, где потребуется
- усилить антиабьюз для голосования:
  - rate limiting
  - `ip_hash`
  - session/fingerprint
  - базовые аномальные детекторы

Результат фазы:

- текущая landing-витрина превращается в живое приложение
- пользователь видит динамику рейтинга, новости и влияние голосования

## Фаза 4. Character Index, админка и production hardening

Статус:

- запланирована

Цель:

- закрыть самый сложный компонент модели и подготовить систему к реальной эксплуатации

Что входит:

- полуавтоматический `Character Index`
- pipeline обнаружения событий:
  - ключевые слова в новостях
  - дисциплинарные сигналы
  - спорные и репутационные инциденты
- workflow модерации:
  - `detected`
  - `pending_review`
  - `confirmed`
  - `rejected`
- админские эндпоинты и простая админка/moderation UI
- audit trail всех ручных решений
- автоматический пересчёт `Character` и общего `FRI` после подтверждения события
- production hardening:
  - structured logs
  - metrics
  - health checks
  - alerting
  - backup strategy
  - deploy target

Дополнительно на этом этапе:

- `Redis` для кэша leaderboard/news при необходимости
- расширение до `200+` игроков
- оптимизация тяжёлых запросов и джоб
- подготовка к продовому домену, SSL и деплою

Результат фазы:

- появляется контролируемый `Character Index`
- система становится ближе к production и масштабированию

## Общие правила по scope

- Не прыгать сразу к парсерам, пока не стабилизирован backend-контур и API.
- Не тащить `k8s`, очереди и лишнюю инфраструктуру раньше фактической необходимости.
- На каждом этапе сначала делать рабочий вертикальный slice, потом усложнять.
- Legacy HTML хранить как reference/source для миграции, но не как постоянный runtime backend.
