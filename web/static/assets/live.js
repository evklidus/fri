(function () {
  const state = {
    players: [],
    news: [],
    currentPollPlayerId: null,
  };

  function round1(value) {
    return Math.round(Number(value || 0) * 10) / 10;
  }

  function toLegacyPlayer(item, index) {
    const trendValue = Math.abs(Number(item.trend_value || 0));
    return {
      id: item.id,
      // Server-side flag: this article is about a player whose leaderboard
      // place is withheld, and arrives with its text and links stripped.
      locked: item.locked === true,
      rank: index + 1,
      emoji: item.emoji || "⚽",
      name: item.name,
      club: item.club,
      league: item.league || "",
      pos: item.position,
      age: item.age,
      fri: round1(item.fri),
      perf: round1(item.performance),
      social: round1(item.social),
      fan: round1(item.fan),
      media: round1(item.media),
      char: round1(item.character),
      trend: trendValue.toFixed(1),
      dir: item.trend_direction || "stable",
      bg: item.theme_background || "linear-gradient(135deg,#1a3a6a,#2a5a9a)",
      // Prefer the CDN URL: photo_data is a base64 data URI that made
      // /api/players an 867KB response for 22 players. Fall back to it so
      // players the sync hasn't reached yet still show a face.
      photo: item.photo_url || item.photo_data || "",
      sum_en: item.summary_en || "",
      sum_ru: item.summary_ru || "",
      // Rows the API withheld from anonymous visitors: rank only, no
      // identifying fields. The UI renders a placeholder for these.
      locked: item.locked === true,
      // "Recently added to the index", used for the NEW badge on the cards.
      isNew: isRecentlyAdded(item.created_at),
    };
  }

  // NEW_PLAYER_WINDOW_DAYS is how long a player stays marked NEW after being
  // added. Two weeks is long enough that a visitor who checks in weekly sees
  // the badge at least once, short enough that it still means something.
  const NEW_PLAYER_WINDOW_DAYS = 14;

  function isRecentlyAdded(createdAt) {
    if (!createdAt) return false;
    const added = new Date(createdAt).getTime();
    if (Number.isNaN(added)) return false;
    return Date.now() - added < NEW_PLAYER_WINDOW_DAYS * 24 * 60 * 60 * 1000;
  }

  function toLegacyNews(item) {
    const delta = Number(item.impact_delta || 0);
    const sign = delta > 0 ? "+" : "";
    return {
      id: item.id,
      // Carry the id, not a resolved photo. Players and news load
      // concurrently, so a lookup here races the roster and loses: every
      // card rendered the fallback ball instead of a face. The photo is
      // resolved at render time, when the roster is definitely populated.
      playerId: item.player_id,
      player: item.player_name,
      impact: item.impact_type,
      delta: `${sign}${round1(delta).toFixed(1)}`,
      time: item.relative_time || "",
      t_en: item.title_en || "",
      t_ru: item.title_ru || "",
      s_en: item.summary_en || "",
      s_ru: item.summary_ru || "",
      url: item.source_url || "",
      domain: extractDomain(item.source_url || ""),
    };
  }

  // extractDomain pulls the human-readable host name out of an article URL
  // ("https://www.bbc.com/sport/12345" → "bbc.com") for display next to the
  // headline so readers see who reported the story before they click.
  function extractDomain(url) {
    if (!url) return "";
    try {
      const host = new URL(url).hostname;
      return host.replace(/^www\./, "");
    } catch (_) {
      return "";
    }
  }

  async function fetchJSON(url, options) {
    const response = await fetch(url, options);
    if (!response.ok) {
      const payload = await response.json().catch(() => ({}));
      throw new Error(payload.error || `Request failed: ${response.status}`);
    }
    return response.json();
  }

  // NEW_BADGE_MAX_SHARE: above this fraction of the roster, "recently added"
  // stops distinguishing anyone and the badge is dropped. This is not
  // hypothetical — the database was rebuilt on 2026-08-17, so every player
  // was two days old and every card wore a NEW tag, which tells a visitor
  // nothing except that the label is decorative.
  const NEW_BADGE_MAX_SHARE = 0.5;

  // How many faces the showcase holds. Six fills the grid at every breakpoint
  // without the row of stragglers ten produced on a narrow screen.
  const SHOWCASE_SIZE = 6;

  // The showcase answers "who is worth looking at right now", which is not the
  // same question as the leaderboard's "who is best". Ranking it by FRI would
  // make it a second, unlocked copy of the table.
  //
  // Three signals, each already in the payloads the main page loads:
  //   buzz     — how much press the player is getting, from the news feed,
  //              weighted by how much each article moved their score
  //   momentum — how far their FRI just moved, either direction; a collapse is
  //              as interesting as a surge
  //   fresh    — recently added to the index
  //
  // Deliberately not a quality measure: a player having a terrible week
  // belongs here. Note this rewards controversy, since impact magnitude counts
  // regardless of sign — which is what "hyped" means.
  function computeShowcase(mapped, newsItems) {
    const articleWeight = new Map();
    (newsItems || []).forEach((item) => {
      const owner = item.player_id;
      if (!owner) return;
      const impact = Math.abs(Number(item.impact_delta || 0));
      // Every article counts for something; a big score move counts for more.
      articleWeight.set(owner, (articleWeight.get(owner) || 0) + 1 + impact);
    });

    const maxBuzz = Math.max(1, ...articleWeight.values());

    const scored = mapped
      // Withheld players never appear here. The showcase is unblurred by
      // design, so including them would hand over the top five the
      // leaderboard withholds.
      .filter((p) => !p.locked)
      .map((p) => {
        const buzz = (articleWeight.get(p.id) || 0) / maxBuzz;
        const momentum = Math.min(1, Math.abs(Number(p.trend || 0)) / 5);
        const fresh = p.isNew ? 1 : 0;
        return { player: p, hype: buzz * 0.6 + momentum * 0.3 + fresh * 0.1 };
      })
      .sort((a, b) => b.hype - a.hype);

    // On a quiet news day every buzz is 0 and the ordering collapses onto
    // momentum alone, which can leave near-ties in an arbitrary order. That is
    // acceptable — the grid still shows real players — but it does mean the
    // showcase is not stable minute to minute by design.
    return scored.slice(0, SHOWCASE_SIZE).map((entry) => entry.player);
  }

  async function loadPlayers() {
    const payload = await fetchJSON("/api/players");
    state.players = Array.isArray(payload.data) ? payload.data : [];
    const mapped = state.players.map(toLegacyPlayer);

    const freshCount = mapped.filter((p) => p.isNew).length;
    if (mapped.length && freshCount / mapped.length > NEW_BADGE_MAX_SHARE) {
      mapped.forEach((p) => {
        p.isNew = false;
      });
    }

    players.splice(0, players.length, ...mapped);
    refreshShowcase();
  }

  // The showcase joins players against news, so it can only be built once both
  // have landed. Both loaders call this; whichever finishes second wins.
  function refreshShowcase() {
    const mapped = players.slice();
    if (!mapped.length) return;
    const showcase = computeShowcase(mapped, state.news);
    showcasePlayers.splice(0, showcasePlayers.length, ...showcase);
  }

  async function loadNews() {
    const payload = await fetchJSON("/api/news/feed");
    state.news = Array.isArray(payload.data) ? payload.data : [];
    news.splice(0, news.length, ...state.news.map(toLegacyNews));
    refreshShowcase();
  }

  // Modal-only data: history + per-player news. Returns plain arrays so the
  // caller can render via Chart.js / DOM without coupling to live.js state.
  let historyChart = null;
  window.fetchPlayerHistory = async function fetchPlayerHistory(playerID) {
    const payload = await fetchJSON(`/api/players/${playerID}/history`);
    return Array.isArray(payload.data) ? payload.data : [];
  };
  window.fetchPlayerNews = async function fetchPlayerNews(playerID) {
    const payload = await fetchJSON(`/api/players/${playerID}/news`);
    return Array.isArray(payload.data) ? payload.data : [];
  };

  // showHistorySkeleton swaps the chart canvas for a pulsing placeholder
  // while the history fetch is in flight. Caller hides it again by invoking
  // renderHistoryChart with real data.
  window.showHistorySkeleton = function showHistorySkeleton() {
    const wrap = document.querySelector(".modal-chart-wrap");
    const canvas = document.getElementById("modal-history-chart");
    const empty = document.getElementById("modal-history-empty");
    if (!wrap) return;
    if (canvas) canvas.style.display = "none";
    if (empty) empty.style.display = "none";
    wrap.querySelector(".skeleton-chart")?.remove();
    const sk = document.createElement("div");
    sk.className = "skeleton-chart";
    wrap.appendChild(sk);
  };

  window.showNewsSkeleton = function showNewsSkeleton() {
    const list = document.getElementById("modal-news-list");
    const empty = document.getElementById("modal-news-empty");
    if (!list) return;
    if (empty) empty.style.display = "none";
    list.innerHTML = "";
    for (let i = 0; i < 3; i++) {
      const sk = document.createElement("div");
      sk.className = "skeleton-news-item";
      list.appendChild(sk);
    }
  };

  // Renders the FRI history line chart inside the modal canvas. Destroys any
  // previous chart instance so reopening the modal for another player doesn't
  // leak Chart.js state.
  window.renderHistoryChart = function renderHistoryChart(history) {
    const canvas = document.getElementById("modal-history-chart");
    const empty = document.getElementById("modal-history-empty");
    if (!canvas || !empty) return;
    document.querySelector(".modal-chart-wrap")?.querySelector(".skeleton-chart")?.remove();

    if (historyChart) {
      historyChart.destroy();
      historyChart = null;
    }

    if (!history.length) {
      canvas.style.display = "none";
      empty.style.display = "block";
      return;
    }
    canvas.style.display = "";
    empty.style.display = "none";

    const points = history.map((p) => ({
      x: new Date(p.calculated_at),
      y: round1(p.fri),
    }));

    historyChart = new Chart(canvas.getContext("2d"), {
      type: "line",
      data: {
        datasets: [
          {
            label: "FRI",
            data: points,
            borderColor: "#F5C842",
            backgroundColor: "rgba(245,200,66,0.10)",
            borderWidth: 2,
            pointRadius: 3,
            pointBackgroundColor: "#F5C842",
            tension: 0.35,
            fill: true,
          },
        ],
      },
      options: {
        responsive: true,
        maintainAspectRatio: false,
        plugins: {
          legend: { display: false },
          tooltip: {
            backgroundColor: "rgba(8,10,13,0.95)",
            titleColor: "#F5C842",
            bodyColor: "#E8EDF5",
            borderColor: "rgba(255,255,255,0.08)",
            borderWidth: 1,
          },
        },
        scales: {
          x: {
            type: "time",
            time: { unit: "day", displayFormats: { day: "MMM d" } },
            ticks: { color: "#5A6B82", font: { size: 11 } },
            grid: { color: "rgba(255,255,255,0.03)" },
          },
          y: {
            min: 0,
            max: 100,
            ticks: { color: "#5A6B82", font: { size: 11 }, stepSize: 20 },
            grid: { color: "rgba(255,255,255,0.05)" },
          },
        },
      },
    });
  };

  window.renderModalNews = function renderModalNews(items) {
    const list = document.getElementById("modal-news-list");
    const empty = document.getElementById("modal-news-empty");
    if (!list || !empty) return;
    list.innerHTML = ""; // also clears any skeleton items
    if (!items.length) {
      empty.style.display = "block";
      return;
    }
    empty.style.display = "none";
    const langKey = typeof lang === "string" ? lang : "en";
    items.slice(0, 8).forEach((item) => {
      const impact = item.impact_type || "neu";
      const delta = Number(item.impact_delta || 0);
      const deltaSign = delta > 0 ? "+" : "";
      const title = (langKey === "ru" ? item.title_ru : item.title_en) || item.title_en || "";
      const url = item.source_url || "";
      const domain = extractDomain(url) || item.source || "";

      // Use <a> when we have a URL so the reader can open the original
      // article in a new tab. Fall back to <div> for legacy items (older
      // syncs that didn't store source_url).
      const card = document.createElement(url ? "a" : "div");
      card.className = `modal-news-item ${impact}`;
      if (url) {
        card.href = url;
        card.target = "_blank";
        card.rel = "noopener noreferrer";
      }
      const arrow = url ? `<span class="modal-news-arrow">↗</span>` : "";
      card.innerHTML = `
        <div class="modal-news-item-top">
          <span class="modal-news-item-delta ${impact}">${deltaSign}${round1(delta).toFixed(1)} FRI</span>
          <span class="modal-news-item-time">${item.relative_time || ""}</span>
        </div>
        <div class="modal-news-item-title">${title}</div>
        <div class="modal-news-item-source">${domain}${arrow}</div>`;
      list.appendChild(card);
    });
  };

  function renderLiveData() {
    // Sections live on separate routes now, so any of these may be absent
    // from the document. Each renderer checks for its own container.
    const newsGrid = document.getElementById("news-grid");
    if (newsGrid) {
      newsGrid.innerHTML = "";
    }

    if (typeof window.populateLeagueFilter === "function") {
      window.populateLeagueFilter();
    }
    renderTable();
    if (typeof window.renderPlayerCards === "function") {
      window.renderPlayerCards();
    }
    renderNews();
    updateHeroCard();
    populatePollPlayers(); // legacy — no-op now that the poll widget is gone
    loadEventsFeed();      // Phase 5: load pending events for voting
  }

  // ── Phase 5: Events Feed ─────────────────────────────────────────────
  // Loads pending-vote events from the API and renders one card per event
  // with a slider for the fan to suggest a different delta. Submits via
  // POST /api/events/{id}/vote.

  async function loadEventsFeed() {
    const container = document.getElementById("events-feed");
    if (!container) return;
    try {
      const payload = await fetchJSON("/api/events/pending?limit=20");
      const events = Array.isArray(payload.data) ? payload.data : [];
      renderEventsFeed(events);
    } catch (err) {
      console.warn("events feed fetch failed:", err);
      container.innerHTML = `<div class="events-empty">Couldn't load events: ${err.message}</div>`;
    }
  }

  function renderEventsFeed(events) {
    const container = document.getElementById("events-feed");
    if (!container) return;
    if (!events.length) {
      const langKey = typeof lang === "string" ? lang : "en";
      const msg = langKey === "ru"
        ? "Сейчас нет событий, ожидающих голосования. Загляните после следующего sync."
        : "No events pending votes right now. Check back after the next sync.";
      container.innerHTML = `<div class="events-empty">${msg}</div>`;
      return;
    }
    container.innerHTML = "";
    events.forEach((event) => container.appendChild(buildEventCard(event)));
  }

  function buildEventCard(event) {
    const card = document.createElement("div");
    card.className = "event-card";
    card.dataset.eventId = event.id;

    const componentTag = event.target_component === "performance" ? "performance" : "character";
    const timeLeft = humanTimeLeft(event.voting_closes_at);
    const proposed = Number(event.proposed_delta || 0);
    const proposedSign = proposed > 0 ? "+" : "";
    const median = event.votes_median != null ? Number(event.votes_median) : null;
    const medianRow = median != null
      ? `<div class="event-card-row">
           <span class="lbl">Community vote</span>
           <span class="val community">${median > 0 ? "+" : ""}${median.toFixed(1)}</span>
           <span style="font-size:11px;color:var(--muted)">${event.votes_count} ${event.votes_count === 1 ? "vote" : "votes"}</span>
         </div>`
      : `<div class="event-card-row">
           <span class="lbl">Community vote</span>
           <span style="color:var(--muted);font-size:13px">No votes yet — be the first</span>
         </div>`;

    const sourceLine = event.news_title
      ? `<div class="event-card-source">News: ${escapeHtml(event.news_title)}</div>`
      : `<div class="event-card-source">Stats-derived event</div>`;

    // Show whose score this event moves. The event feed only carries a
    // player_id and name, so the portrait comes from the roster we already
    // loaded — no extra request, and it falls back to the emoji when the
    // player has no photo yet.
    const eventOwner = state.players.find((p) => p.id === event.player_id);
    const eventPhoto = eventOwner ? eventOwner.photo_url || eventOwner.photo_data || "" : "";
    const eventFace = eventPhoto
      ? `<img class="event-face" src="${escapeHtml(eventPhoto)}" alt="" loading="lazy" />`
      : `<div class="event-face" style="display:flex;align-items:center;justify-content:center">${(eventOwner && eventOwner.emoji) || "⚽"}</div>`;

    card.innerHTML = `
      <div class="event-card-top">
        <div class="event-card-head">
          ${eventFace}
          <div>
            <div class="event-card-player">${escapeHtml(event.player_name)}</div>
            <div class="event-card-trigger">Trigger: <strong>${escapeHtml(event.trigger_word.replace(/_/g, " "))}</strong></div>
          </div>
        </div>
        <div class="event-card-meta">
          <span class="event-card-tag ${componentTag}">${componentTag}</span>
        </div>
      </div>
      ${sourceLine}
      <div class="event-card-row">
        <span class="lbl">FRI proposes</span>
        <span class="val proposed">${proposedSign}${proposed.toFixed(1)}</span>
      </div>
      ${medianRow}
      <div class="event-card-vote">
        <input type="range" min="-5" max="5" step="0.5" value="${proposed}" />
        <div class="vote-val">${proposedSign}${proposed.toFixed(1)}</div>
        <button class="vote-btn">Vote</button>
      </div>
      <div class="event-card-time">Voting ends in ${timeLeft}</div>
    `;

    // Wire the slider + button
    const slider = card.querySelector("input[type=range]");
    const valDisplay = card.querySelector(".vote-val");
    const btn = card.querySelector(".vote-btn");

    slider.addEventListener("input", () => {
      const v = Number(slider.value);
      valDisplay.textContent = (v > 0 ? "+" : "") + v.toFixed(1);
    });

    btn.addEventListener("click", async () => {
      const v = Number(slider.value);
      btn.disabled = true;
      btn.textContent = "...";
      try {
        const resp = await fetch(`/api/events/${event.id}/vote`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ suggested_delta: v }),
        });
        if (resp.status === 410) {
          btn.textContent = "Closed";
          return;
        }
        if (!resp.ok) {
          const payload = await resp.json().catch(() => ({}));
          throw new Error(payload.error || `HTTP ${resp.status}`);
        }
        btn.textContent = "✓ Voted";
        btn.style.background = "#22C55E";
        btn.style.borderColor = "#22C55E";
        btn.style.color = "#fff";
        // Refresh the feed shortly so the user sees their vote in the median
        setTimeout(loadEventsFeed, 600);
      } catch (err) {
        btn.textContent = "Try again";
        btn.disabled = false;
        console.warn("vote submit failed:", err);
      }
    });

    return card;
  }

  function humanTimeLeft(isoString) {
    const target = new Date(isoString);
    const diffMs = target.getTime() - Date.now();
    if (diffMs <= 0) return "any moment";
    const hours = Math.floor(diffMs / 3_600_000);
    const minutes = Math.floor((diffMs % 3_600_000) / 60_000);
    if (hours > 0) return `${hours}h ${minutes}m`;
    return `${minutes}m`;
  }

  // Refresh the events feed every 60s so live votes and new events surface
  // without a page reload. loadEventsFeed returns early when #events-feed is
  // absent, which is now most of the time — the events section lives on its
  // own route — so this costs one GET a minute only while that page is open.
  setInterval(loadEventsFeed, 60_000);

  function getPollSelect() {
    return document.querySelector(".poll-player-select select");
  }

  function populatePollPlayers() {
    const select = getPollSelect();
    if (!select || !state.players.length) {
      return;
    }

    const currentSelected = state.currentPollPlayerId || state.players[0].id;
    select.innerHTML = state.players
      .map((player) => `<option value="${player.id}">${player.name}</option>`)
      .join("");

    state.currentPollPlayerId = currentSelected;
    select.value = String(currentSelected);
    updatePollPlayer(select.value);
  }

  function selectedTierValue() {
    const selected = document.querySelector(".poll-question:nth-of-type(3) .opinion-btn.selected");
    if (!selected) return 80;
    const key = selected.getAttribute("data-i18n");
    const map = {
      op_goat: 100,
      op_wc: 90,
      op_elite: 80,
      op_improv: 65,
      op_over: 40,
      op_below: 25,
    };
    return map[key] || 80;
  }

  function selectedBehaviorValue() {
    const selected = document.querySelector(".poll-question:nth-of-type(4) .opinion-btn.selected");
    if (!selected) return 70;
    const key = selected.getAttribute("data-i18n");
    const map = {
      beh_role: 95,
      beh_neu: 70,
      beh_con: 40,
      beh_prob: 20,
    };
    return map[key] || 70;
  }

  function selectedOverallStars() {
    return document.querySelectorAll(".rating-stars .star.active").length || 3;
  }

  function getSessionID() {
    const key = "fri_session_id";
    const existing = localStorage.getItem(key);
    if (existing) return existing;
    const generated = `sess-${crypto.randomUUID()}`;
    localStorage.setItem(key, generated);
    return generated;
  }

  window.updatePollPlayer = function updatePollPlayer(playerID) {
    if (!state.players.length) {
      return;
    }

    const numericID = Number(playerID);
    const player = state.players.find((item) => item.id === numericID) || state.players[0];
    state.currentPollPlayerId = player.id;

    const label = document.getElementById("poll-rate-label");
    if (label) {
      label.textContent = (lang === "ru" ? "Оценить " : "Rate ") + player.name;
    }
  };

  window.submitPoll = async function submitPoll() {
    if (!state.currentPollPlayerId) {
      return;
    }

    const button = document.getElementById("poll-submit-btn");
    const originalText = button.textContent;

    try {
      button.disabled = true;
      button.textContent = lang === "ru" ? "Отправка..." : "Submitting...";

      await fetchJSON(`/api/players/${state.currentPollPlayerId}/vote`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
        },
        body: JSON.stringify({
          session_id: getSessionID(),
          rating_overall: selectedOverallStars(),
          rating_hype: Number(document.getElementById("engagement-slider").value || 7),
          rating_tier: selectedTierValue(),
          behavior_score: selectedBehaviorValue(),
        }),
      });

      await loadPlayers();
      renderLiveData();

      button.textContent = lang === "ru" ? "Голос учтён" : "Vote Counted";
      button.style.background = "linear-gradient(135deg,#22C55E,#16A34A)";
      setTimeout(() => {
        button.textContent = originalText;
        button.style.background = "linear-gradient(135deg,var(--gold),var(--gold2))";
      }, 2500);
    } catch (error) {
      button.textContent = error.message;
      button.style.background = "linear-gradient(135deg,#EF4444,#DC2626)";
      setTimeout(() => {
        button.textContent = originalText;
        button.style.background = "linear-gradient(135deg,var(--gold),var(--gold2))";
      }, 2500);
    } finally {
      button.disabled = false;
    }
  };

  // Dev-only: kick off a full backend sync (Performance + Social + Media +
  // Character) and refresh the UI when it finishes. The production build
  // drops the `.dev-tools` footer block, so this handler is harmless to ship.
  window.triggerFullSync = async function triggerFullSync() {
    const btn = document.getElementById("dev-sync-btn");
    const text = document.getElementById("dev-sync-text");
    const status = document.getElementById("dev-sync-status");
    if (!btn || !text || !status) return;

    btn.disabled = true;
    status.classList.remove("success", "error");
    text.textContent = "Syncing…";
    status.textContent = "Hitting external APIs (60–90s)…";

    const startedAt = Date.now();
    try {
      const r = await fetch("/api/sync/all", { method: "POST" });
      const payload = await r.json().catch(() => ({}));
      if (!r.ok) {
        throw new Error(payload?.error || `HTTP ${r.status}`);
      }
      const elapsed = ((Date.now() - startedAt) / 1000).toFixed(1);
      const summary = (payload.data || [])
        .map((c) => `${c.component}: ${c.records_seen}`)
        .join(" · ");
      status.classList.add("success");
      status.textContent = `done in ${elapsed}s · ${summary || "no records"}`;

      // Refresh leaderboard + news so the impact of the sync is visible.
      await Promise.all([loadPlayers(), loadNews()]);
      renderLiveData();
    } catch (err) {
      status.classList.add("error");
      status.textContent = `failed: ${err.message}`;
    } finally {
      btn.disabled = false;
      text.textContent = "Trigger full sync";
      // Auto-clear status after 12s so the footer doesn't carry stale text.
      setTimeout(() => {
        if (!btn.disabled) {
          status.textContent = "";
          status.classList.remove("success", "error");
        }
      }, 12000);
    }
  };

  // ── ACCOUNTS ─────────────────────────────────────────────────────────
  // The session lives in an HttpOnly cookie, so the page can't read it and
  // has to ask the server who it is. window.friIsAdmin gates the admin-only
  // controls in the UI; the endpoints behind them check for themselves.
  window.friUser = null;
  window.friIsAdmin = false;
  let authMode = "login";

  async function loadCurrentUser() {
    try {
      const payload = await fetchJSON("/api/auth/me");
      window.friUser = payload.data || null;
    } catch (_) {
      window.friUser = null;
    }
    window.friIsAdmin = !!(window.friUser && window.friUser.is_admin);
    renderAuthControls();
  }

  function renderAuthControls() {
    const host = document.getElementById("auth-controls");
    if (!host) return;
    const t = (window.T && window.T[window.lang]) || {};
    const user = window.friUser;

    if (!user) {
      host.innerHTML =
        '<button class="auth-btn" onclick="openAuth(\'login\')">' + (t.auth_signin || "Sign in") + "</button>" +
        '<button class="auth-btn primary" onclick="openAuth(\'register\')">' + (t.auth_signup || "Sign up") + "</button>";
    } else {
      const adminTag = user.is_admin ? '<span class="auth-admin-tag">admin</span>' : "";
      host.innerHTML =
        '<div class="auth-user">' + adminTag +
        '<span class="auth-email">' + escapeAttr(user.email) + "</span>" +
        '<button class="auth-btn" onclick="signOut()">' + (t.auth_signout || "Sign out") + "</button></div>";
    }

    const adminTools = document.getElementById("admin-tools");
    if (adminTools) {
      adminTools.style.display = window.friIsAdmin ? "" : "none";
    }
    renderAdminNav();
    // Signing in or out changes what the dashboard is allowed to show, so a
    // page already on screen is refetched rather than left stale.
    const adminBody = document.getElementById("admin-body");
    const onAdminPage = document.querySelector('.page[data-route="/admin"]');
    if (adminBody && onAdminPage && !onAdminPage.hidden) {
      loadAdminStats(true);
    } else if (adminBody) {
      adminBody.removeAttribute("data-loaded");
    }
  }
  window.renderAuthControls = renderAuthControls;

  function escapeAttr(value) {
    return String(value == null ? "" : value).replace(/[&<>"']/g, (c) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c])
    );
  }

  window.openAuth = function openAuth(mode) {
    authMode = mode === "register" ? "register" : "login";
    const modal = document.getElementById("auth-modal");
    if (!modal) return;
    applyAuthMode();
    document.getElementById("auth-error").textContent = "";
    modal.classList.add("open");
    const email = document.getElementById("auth-email");
    if (email) setTimeout(() => email.focus(), 50);
  };

  window.closeAuth = function closeAuth() {
    const modal = document.getElementById("auth-modal");
    if (modal) modal.classList.remove("open");
  };

  window.toggleAuthMode = function toggleAuthMode() {
    authMode = authMode === "login" ? "register" : "login";
    applyAuthMode();
  };

  function applyAuthMode() {
    const t = (window.T && window.T[window.lang]) || {};
    const signup = authMode === "register";
    const set = (id, value) => {
      const el = document.getElementById(id);
      if (el && value) el.textContent = value;
    };
    set("auth-title", signup ? t.auth_signup_title : t.auth_signin_title);
    set("auth-sub", signup ? t.auth_signup_sub : t.auth_signin_sub);
    set("auth-submit", signup ? t.auth_submit_signup : t.auth_submit_signin);
    set("auth-switch-text", signup ? t.auth_have_account : t.auth_no_account);
    set("auth-switch-btn", signup ? t.auth_switch_signin : t.auth_switch_signup);
    const pwd = document.getElementById("auth-password");
    if (pwd) pwd.setAttribute("autocomplete", signup ? "new-password" : "current-password");
  }

  window.submitAuth = function submitAuth(event) {
    event.preventDefault();
    const t = (window.T && window.T[window.lang]) || {};
    const emailEl = document.getElementById("auth-email");
    const pwdEl = document.getElementById("auth-password");
    const errEl = document.getElementById("auth-error");
    const btn = document.getElementById("auth-submit");
    if (!emailEl || !pwdEl || !errEl || !btn) return false;

    errEl.textContent = "";
    btn.disabled = true;

    const path = authMode === "register" ? "/api/auth/register" : "/api/auth/login";
    fetch(path, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ email: emailEl.value.trim(), password: pwdEl.value }),
    })
      .then(async (r) => {
        const payload = await r.json().catch(() => ({}));
        if (!r.ok) {
          // The server's message is the useful one — it distinguishes a
          // taken address from a weak password. Fall back to a generic line
          // only when there's nothing to show.
          throw new Error(payload.error || t.auth_err_generic || "Failed");
        }
        return payload;
      })
      .then(async () => {
        window.closeAuth();
        pwdEl.value = "";
        // Reload the leaderboard: the top five arrive unmasked now.
        await loadCurrentUser();
        await Promise.all([loadPlayers(), loadNews()]);
        renderLiveData();
      })
      .catch((err) => {
        errEl.textContent = err.message;
      })
      .finally(() => {
        btn.disabled = false;
      });
    return false;
  };

  window.signOut = async function signOut() {
    try {
      await fetch("/api/auth/logout", { method: "POST" });
    } catch (_) {
      /* clearing local state below matters more than the response */
    }
    await loadCurrentUser();
    await Promise.all([loadPlayers(), loadNews()]);
    renderLiveData();
  };

  // Admin-only: drop an article the filters let through. The server
  // remembers the deletion so the next sync doesn't bring the article back,
  // and rescores the player from what remains — then tells us what moved,
  // so the moderator sees the number change rather than taking it on trust.
  window.deleteNewsItem = async function deleteNewsItem(newsID, cardEl) {
    if (!newsID || !window.friIsAdmin) return;
    const label = (window.T && window.T[window.lang] && window.T[window.lang].news_delete) || "Delete";
    if (!window.confirm(label + "?")) return;
    try {
      const r = await fetch("/api/news/" + encodeURIComponent(newsID), { method: "DELETE" });
      if (!r.ok) {
        const payload = await r.json().catch(() => ({}));
        throw new Error(payload.error || "HTTP " + r.status);
      }
      const payload = await r.json().catch(() => ({}));
      const d = payload.data || {};
      if (cardEl && cardEl.parentNode) cardEl.parentNode.removeChild(cardEl);
      // The article fed a player's Media score, so refresh the table too.
      await Promise.all([loadPlayers(), loadNews()]);
      renderLiveData();
      if (d.player_name && typeof d.new_fri === "number") {
        window.alert(d.player_name + ": Media " + d.old_media + " → " + d.new_media +
          ", FRI " + d.old_fri + " → " + d.new_fri +
          (d.remaining_articles === 0 ? " (no articles left — neutral)" : ""));
      }
    } catch (err) {
      console.error("delete news failed", err);
      window.alert("Delete failed: " + err.message);
    }
  };

  // ── ADMIN DASHBOARD ───────────────────────────────────────
  // Accounts, traffic and sync health. The endpoint is admin-gated, so this
  // renders a polite refusal rather than an error for everyone else.
  let adminStatsPending = false;

  function adminText(key, fallback) {
    const t = (window.T && window.T[window.lang]) || {};
    return t[key] || fallback;
  }

  function adminKpi(value, labelKey, labelFallback) {
    return '<div class="admin-kpi"><div class="admin-kpi-value">' +
      Number(value || 0).toLocaleString() +
      '</div><div class="admin-kpi-label">' + escapeHtml(adminText(labelKey, labelFallback)) +
      "</div></div>";
  }

  function adminPanel(titleKey, titleFallback, subKey, subFallback, body) {
    return '<div class="admin-panel"><h3>' + escapeHtml(adminText(titleKey, titleFallback)) + "</h3>" +
      '<div class="admin-panel-sub">' + escapeHtml(adminText(subKey, subFallback)) + "</div>" +
      body + "</div>";
  }

  // Two bars per day: visitors and documents served. Scaled to whichever is
  // largest across the window so a quiet week still shows shape.
  function adminTrafficChart(days) {
    const rows = Array.isArray(days) ? days : [];
    if (!rows.length || !rows.some((d) => d.visitors || d.page_loads)) {
      return '<div class="admin-empty">' +
        escapeHtml(adminText("admin_no_traffic", "No traffic recorded yet.")) + "</div>";
    }
    const peak = Math.max.apply(null, rows.map((d) => Math.max(d.visitors || 0, d.page_loads || 0))) || 1;
    const height = (value) => Math.max(2, Math.round(((value || 0) / peak) * 110));
    const cols = rows.map((d) => {
      const label = String(d.day || "").slice(5); // MM-DD
      const title = label + " · " + adminText("admin_visitors", "Visitors") + ": " + (d.visitors || 0) +
        " · " + adminText("admin_loads", "Page loads") + ": " + (d.page_loads || 0);
      return '<div class="admin-chart-col" title="' + escapeAttr(title) + '">' +
        '<div class="admin-chart-stack">' +
        '<div class="admin-chart-bar visitors" style="height:' + height(d.visitors) + 'px"></div>' +
        '<div class="admin-chart-bar loads" style="height:' + height(d.page_loads) + 'px"></div>' +
        "</div>" +
        '<div class="admin-chart-day">' + escapeHtml(label) + "</div></div>";
    }).join("");
    const totals = rows.reduce((acc, d) => {
      acc.visitors += d.visitors || 0;
      acc.loads += d.page_loads || 0;
      acc.api += d.api_calls || 0;
      return acc;
    }, { visitors: 0, loads: 0, api: 0 });
    return '<div class="admin-chart">' + cols + "</div>" +
      '<div class="admin-legend">' +
      '<span class="k-visitors">' + escapeHtml(adminText("admin_visitors", "Visitors")) + ": " + totals.visitors.toLocaleString() + "</span>" +
      '<span class="k-loads">' + escapeHtml(adminText("admin_loads", "Page loads")) + ": " + totals.loads.toLocaleString() + "</span>" +
      "<span>" + escapeHtml(adminText("admin_api", "API calls")) + ": " + totals.api.toLocaleString() + "</span>" +
      "</div>";
  }

  function adminTable(headers, rows) {
    if (!rows.length) {
      return '<div class="admin-empty">—</div>';
    }
    return '<table class="admin-table"><thead><tr>' +
      headers.map((h) => "<th>" + escapeHtml(h) + "</th>").join("") +
      "</tr></thead><tbody>" +
      rows.map((cells) => "<tr>" + cells.join("") + "</tr>").join("") +
      "</tbody></table>";
  }

  function adminRenderStats(stats) {
    const users = stats.users || {};
    const content = stats.content || {};

    const kpis = '<div class="admin-kpis">' +
      adminKpi(users.total, "admin_total", "Total") +
      adminKpi(users.new_last_7_days, "admin_new7", "New, 7d") +
      adminKpi(users.new_last_30_days, "admin_new30", "New, 30d") +
      adminKpi(users.active_last_7_days, "admin_active7", "Signed in, 7d") +
      adminKpi(users.admins, "admin_admins", "Admins") +
      "</div>";

    const contentKpis = '<div class="admin-kpis">' +
      adminKpi(content.players, "admin_players", "Players") +
      adminKpi(content.news_items, "admin_news", "News items") +
      adminKpi(content.pending_events, "admin_events", "Open events") +
      adminKpi(content.votes_all_time, "admin_votes", "Votes") +
      "</div>";

    const entryRows = (stats.entry_points || []).map((e) => [
      "<td>" + escapeHtml(e.section || "—") + "</td>",
      '<td class="num">' + Number(e.views || 0).toLocaleString() + "</td>",
    ]);

    const signupRows = (stats.signups || []).filter((d) => d.users > 0).map((d) => [
      "<td>" + escapeHtml(d.day || "") + "</td>",
      '<td class="num">' + Number(d.users || 0).toLocaleString() + "</td>",
    ]);

    const syncRows = (stats.syncs || []).map((sync) => {
      const ok = String(sync.status || "").toLowerCase() === "completed";
      const when = sync.finished_at || sync.started_at || "";
      return [
        "<td>" + escapeHtml(sync.component || "") + "</td>",
        '<td class="' + (ok ? "admin-ok" : "admin-bad") + '">' + escapeHtml(sync.status || "") + "</td>",
        "<td>" + escapeHtml(String(when).replace("T", " ").slice(0, 16)) + "</td>",
        '<td class="num">' + Number(sync.records_seen || 0).toLocaleString() + "</td>",
        "<td>" + escapeHtml(String(sync.message || "").slice(0, 70)) + "</td>",
      ];
    });

    return '<div class="admin-note">' + escapeHtml(adminText("admin_traffic_sub", "")) + "</div>" +
      kpis +
      adminPanel("admin_traffic", "Traffic", "admin_traffic_short", "Per day, last 14 days.", adminTrafficChart(stats.days)) +
      adminPanel("admin_entry", "Entry points", "admin_entry_sub", "",
        adminTable([adminText("admin_section", "Section"), adminText("admin_loads", "Page loads")], entryRows)) +
      adminPanel("admin_signups", "Registrations", "admin_signups_sub", "",
        adminTable([adminText("admin_day", "Day"), adminText("admin_users", "Accounts")], signupRows)) +
      contentKpis +
      adminPanel("admin_syncs", "Data syncs", "admin_syncs_sub", "",
        adminTable([
          adminText("admin_component", "Component"), adminText("admin_status", "Status"),
          adminText("admin_when", "When"), adminText("admin_records", "Records"),
          adminText("admin_message", "Message"),
        ], syncRows));
  }

  async function loadAdminStats(force) {
    const host = document.getElementById("admin-body");
    if (!host || adminStatsPending) return;
    if (!force && host.getAttribute("data-loaded") === "1") return;

    adminStatsPending = true;
    host.innerHTML = '<div class="admin-empty">' + escapeHtml(adminText("admin_loading", "Loading…")) + "</div>";
    try {
      const payload = await fetchJSON("/api/stats");
      host.innerHTML = adminRenderStats(payload.data || {});
      host.setAttribute("data-loaded", "1");
    } catch (err) {
      // 401 and 403 are the ordinary answer for a visitor who opened the
      // address without an admin account, not a fault worth a stack trace.
      const denied = /401|403|sign in|admin/i.test(String(err.message || ""));
      host.innerHTML = '<div class="admin-empty">' +
        escapeHtml(adminText(denied ? "admin_denied" : "admin_failed", err.message)) + "</div>";
      host.removeAttribute("data-loaded");
      if (!denied) console.error("admin stats failed", err);
    } finally {
      adminStatsPending = false;
    }
  }
  window.loadAdminStats = loadAdminStats;

  // The nav entry only appears for admins; the data behind it is gated
  // server-side regardless.
  function renderAdminNav() {
    const link = document.getElementById("nav-admin");
    if (link) link.hidden = !window.friIsAdmin;
  }
  window.renderAdminNav = renderAdminNav;

  async function hydrate() {
    try {
      // Identity first: it decides whether the leaderboard comes back masked.
      await loadCurrentUser();
      await Promise.all([loadPlayers(), loadNews()]);
      renderLiveData();
    } catch (error) {
      console.error("FRI hydrate failed", error);
    }
  }

  hydrate();
})();
