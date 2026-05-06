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
      photo: item.photo_data || "",
      sum_en: item.summary_en || "",
      sum_ru: item.summary_ru || "",
    };
  }

  function toLegacyNews(item) {
    const delta = Number(item.impact_delta || 0);
    const sign = delta > 0 ? "+" : "";
    return {
      player: item.player_name,
      impact: item.impact_type,
      delta: `${sign}${round1(delta).toFixed(1)}`,
      time: item.relative_time || "",
      t_en: item.title_en || "",
      t_ru: item.title_ru || "",
      s_en: item.summary_en || "",
      s_ru: item.summary_ru || "",
    };
  }

  async function fetchJSON(url, options) {
    const response = await fetch(url, options);
    if (!response.ok) {
      const payload = await response.json().catch(() => ({}));
      throw new Error(payload.error || `Request failed: ${response.status}`);
    }
    return response.json();
  }

  async function loadPlayers() {
    const payload = await fetchJSON("/api/players");
    state.players = Array.isArray(payload.data) ? payload.data : [];
    players.splice(0, players.length, ...state.players.map(toLegacyPlayer));
  }

  async function loadNews() {
    const payload = await fetchJSON("/api/news/feed");
    state.news = Array.isArray(payload.data) ? payload.data : [];
    news.splice(0, news.length, ...state.news.map(toLegacyNews));
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
      const source = item.source || "";
      const card = document.createElement("div");
      card.className = `modal-news-item ${impact}`;
      card.innerHTML = `
        <div class="modal-news-item-top">
          <span class="modal-news-item-delta ${impact}">${deltaSign}${round1(delta).toFixed(1)} FRI</span>
          <span class="modal-news-item-time">${item.relative_time || ""}</span>
        </div>
        <div class="modal-news-item-title">${title}</div>
        <div class="modal-news-item-source">${source}</div>`;
      list.appendChild(card);
    });
  };

  function renderLiveData() {
    const newsGrid = document.getElementById("news-grid");
    if (newsGrid) {
      newsGrid.innerHTML = "";
    }

    if (typeof window.populateLeagueFilter === "function") {
      window.populateLeagueFilter();
    }
    renderTable();
    renderNews();
    updateHeroCard();
    populatePollPlayers();
  }

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

  async function hydrate() {
    try {
      await Promise.all([loadPlayers(), loadNews()]);
      renderLiveData();
    } catch (error) {
      console.error("FRI hydrate failed", error);
    }
  }

  hydrate();
})();
