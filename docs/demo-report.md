# FRI — Live Demo Report

Snapshot системы по состоянию на **2026-05-06**.

> Это hand-off документ для инвестора: показывает, что система работает на реальных данных, реагирует на события и пересчитывает рейтинги в реальном времени. Все цифры ниже — настоящие, полученные из живой БД через `/api/...`.

---

## 1. Общая картина

При открытии `http://localhost:8080` на главной видна hero-карточка с топ-1 игроком и его компонентами. Сейчас это **H. Kane (Bayern Munich) с FRI 78.4** — карточка обновляется автоматически после каждого sync.

- 22 игрока в активной базе
- 5 компонентов FRI: Performance / Social / Fan Poll / Media / Character
- Hero показывает **топ-1 игрока** автоматически — обновляется после каждого sync.

---

## 2. Топ-10 рейтинга (live)

| # | Игрок | Клуб (лига) | FRI | Perf | Social | Fan | Media | Char |
|---|---|---|---|---|---|---|---|---|
| 1 | **H. Kane** | Bayern Munich (Bundesliga) | **78.4** | 81.2 | 64.1 | 87.0 | 67.6 | 96 |
| 2 | L. Yamal | FC Barcelona (La Liga) | 75.9 | 78.8 | 49.9 | 91.0 | 74.6 | 89 |
| 3 | K. Mbappé | Real Madrid (La Liga) | 74.4 | 72.3 | 55.4 | 91.4 | 72.4 | 89 |
| 4 | E. Haaland | Man City (Premier League) | 71.8 | 69.1 | 47.2 | 91.0 | 69.6 | 95 |
| 5 | F. Valverde | Real Madrid (La Liga) | 71.3 | 60.8 | 67.7 | 82.0 | 73.0 | 91 |
| 6 | M. Olise | Bayern Munich (Bundesliga) | 69.5 | 78.3 | 27.8 | 83.7 | 71.4 | 91 |
| 7 | Vitinha | PSG (Ligue 1) | 69.2 | 56.6 | 64.3 | 84.0 | 71.2 | 90 |
| 8 | J. Bellingham | Real Madrid (La Liga) | 69.0 | 53.1 | 61.0 | 95.0 | 69.2 | 88 |
| 9 | Rayan Cherki | Manchester City (Premier League) | 68.9 | 70.8 | 50.4 | 77.0 | 67.5 | 85 |
| 10 | Pedri | FC Barcelona (La Liga) | 66.7 | 64.3 | 32.2 | 90.0 | 69.4 | 93 |

**Эта картина соответствует реальности.** Kane — топ-скорер Бундеслиги. Yamal — открытие сезона. Olise — лидер ассистов. Это получено автоматически из API-Football, без ручной правки.

---

## 3. Карточка игрока — M. Olise

При клике на любую строку открывается модалка с детализацией.

### Текущий FRI и компоненты

| FRI | Performance | Social | Fan Poll | Media | Character |
|---|---|---|---|---|---|
| **69.5** | 78.3 | 27.8 | 83.7 | 71.4 | 91 |

### График истории FRI (Chart.js)

История из таблицы `fri_history`. **11 точек** за период 2026-03-23 → 2026-05-06:

```
Mar 23  → FRI 75.0
...
May 06  → FRI 69.5
```

Каждая точка — фактическое изменение FRI после sync или голоса. На графике в браузере это интерактивная line-chart с tooltip.

### Лента новостей про игрока

Реальные статьи из MediaStack за 30 дней, sentiment проанализирован VADER:

| Заголовок | Δ FRI | Тональность |
|---|---|---|
| "From Crystal Palace to Europe's creator-in-chief: How Michael Olise became 'the best winger in the world' at Bayern Munich" | +1.2 | Позитив |
| "'He could break his leg!' — Didier Deschamps rages over 'American football' tackle on Olise" | +0.6 | Позитив (Olise — пострадавшая сторона) |
| "Bayern's 'undeserved' yellow cards for Kimmich and Olise should be a wake-up call for UEFA" | +0.6 | Позитив |

> **Важная деталь:** заголовок "He could break his leg" касается Olise как жертвы — наш **negator-фильтр в Character pipeline** не пропускает такие статьи как "красная карточка" против самого игрока. Это работает.

---

## 4. Источники данных по компонентам

| Компонент | Источник | Что приходит | Частота обновления |
|---|---|---|---|
| **Performance** (35%) | API-Football Pro | Сезонные статы + форма последних 5 матчей + позиционный rank в лиге | каждые 12 ч |
| **Social** (20%) | YouTube Data API v3 | Просмотры за 7 дней (часть от Social Score) | каждые 24 ч |
| **Media** (15%) | MediaStack Pro + VADER + RU-лексикон | Статьи EN+RU за 30 дней с tone-анализом | каждые 6 ч |
| **Character** (10%) | Keyword-scan по `news_items` | События репутации (red card, scandal, fair play, racism, charity) с negator-защитой | каждые 12 ч |
| **Fan Poll** (20%) | Внутреннее голосование | 4 параметра × вес → instant FRI update | мгновенно при голосе |

---

## 5. Антиабьюз голосования

Проверено на endpoint `POST /api/players/13/vote`:

```bash
# Первый голос — успех
curl -X POST .../vote → 201 Created
{ "data": { "fri": 69.5, "fan": 83.7 } }

# Второй голос с того же IP в течение 24h
curl -X POST .../vote → 429 Too Many Requests
{ "error": "vote rate limit: already voted for this player in the last 24h" }
```

IP хешируется через SHA-256 — raw IP в БД не сохраняется. PII compliant.

---

## 6. Журнал sync-операций

Из `/api/sync/updates` (последние 8):

| Время | Компонент | Провайдер | Статус | Сообщение |
|---|---|---|---|---|
| 2026-05-06 17:27 | performance | api-football | completed | 22 игрока |
| 2026-05-06 17:00 | performance | api-football | completed | 22 игрока |
| 2026-05-06 15:38 | performance | api-football | completed | 22 игрока |
| 2026-05-06 14:43 | character | news-keyword-scan | completed | 84 articles → 3 events → 2 players |
| 2026-05-06 14:42 | media | mediastack | completed | 22 игрока |
| 2026-05-06 14:42 | social | youtube-data-api | completed | 22 игрока |
| 2026-05-06 14:42 | performance | api-football | completed | 22 игрока |

Каждая операция трекается в БД (таблица `component_updates`) — это audit-trail для production-режима.

---

## 7. Фильтры

| Фильтр | Значения | Реакция |
|---|---|---|
| Поиск по имени | text input | мгновенный фильтр по name + club |
| Позиция | All / GK / DEF / MID / FWD | 9 нападающих, 8 полузащитников, 4 защитника, 1 вратарь |
| Лига | All / Premier League / La Liga / Bundesliga / Ligue 1 / Süper Lig | La Liga = 12, Premier League = 6, Bundesliga = 2, Ligue 1 = 1, Süper Lig = 1 |

Distinct лиги вычисляются на клиенте из `players[].league` — поле приходит из БД, заполняется автоматически по club.

---

## 8. Технологический стек

- **Backend:** Go 1.22 + Gin
- **DB:** PostgreSQL 17 (8 миграций, embed.FS)
- **Внешние API:** API-Football Pro, MediaStack Pro, YouTube Data API v3
- **Sentiment:** VADER (EN) + ручной RU-лексикон + футбольные boosters
- **Frontend:** static HTML/CSS/JS + Chart.js (без фреймворков)
- **Тесты:** 100+ unit/integration, race-detector clean
- **Запуск:** `make docker-up` → http://localhost:8080

Кодовая база ~5k строк Go + ~1k строк HTML/JS.

---

## 9. Что готово, что отложено

**Production-ready сейчас:**
- Все 5 компонентов на реальных данных
- Антиабьюз голосования
- Self-heal для несинхронизированных mappings
- Sanity-check от false-positive matching (тёзки, women-teams, U23)
- Графики истории, фильтры, skeleton loaders
- Idempotent миграции

**Отложено до next итераций:**
- Instagram / Twitter API (требуют $50+/мес или скрапинг с риском блокировки)
- Полноценный Character с moderation UI (фаза 4 по roadmap)
- Расширение базы до 200+ игроков (этап 5 по партнёрскому ТЗ)
- Production deploy: VPS + домен + SSL
- WebSocket / SSE для live-update без перезагрузки страницы
