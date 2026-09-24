// Player card generator.
//
// Draws a 1080×1920 portrait card — the story/reel format — so the team can
// drop a player's FRI straight into a video. Everything is painted onto a
// canvas in the browser: the photos come from media.api-sports.io, which
// answers with Access-Control-Allow-Origin: *, so the canvas stays untainted
// and can be exported.
//
// Layout, top to bottom: the FRI mark and wordmark, the portrait in a gold
// ring, name and club, the FRI score with its tier, four glass tiles with the
// components and their bars, and the address at the foot.
(function () {
  const W = 1080;
  const H = 1920;
  const GOLD = "#F5C842";
  const GOLD2 = "#E8A820";
  const INK = "#07080A";

  const labels = {
    en: {
      perf: "PERFORMANCE", social: "SOCIAL", media: "MEDIA", char: "CHARACTER",
      score: "FRI SCORE", index: "FOOTBALL REPUTATION INDEX",
      share: "Share", download: "Download", close: "Close", making: "Rendering card…",
      failed: "Could not render the card.",
    },
    ru: {
      perf: "ИГРА", social: "СОЦСЕТИ", media: "МЕДИА", char: "ХАРАКТЕР",
      score: "ИНДЕКС FRI", index: "FOOTBALL REPUTATION INDEX",
      share: "Поделиться", download: "Скачать", close: "Закрыть", making: "Рисуем карточку…",
      failed: "Не удалось нарисовать карточку.",
    },
  };

  function lang() {
    return window.lang === "ru" ? "ru" : "en";
  }

  function loadImage(src) {
    return new Promise((resolve) => {
      if (!src) return resolve(null);
      const img = new Image();
      img.crossOrigin = "anonymous";
      img.onload = () => resolve(img);
      img.onerror = () => resolve(null);
      img.src = src;
    });
  }

  async function ensureFonts() {
    if (!document.fonts || !document.fonts.load) return;
    try {
      await Promise.all([
        document.fonts.load('120px "Bebas Neue"'),
        document.fonts.load('28px "Space Mono"'),
        document.fonts.load('600 36px "DM Sans"'),
      ]);
    } catch (_) {
      /* fall back to system fonts rather than failing the card */
    }
  }

  function roundRect(ctx, x, y, w, h, r) {
    ctx.beginPath();
    ctx.moveTo(x + r, y);
    ctx.arcTo(x + w, y, x + w, y + h, r);
    ctx.arcTo(x + w, y + h, x, y + h, r);
    ctx.arcTo(x, y + h, x, y, r);
    ctx.arcTo(x, y, x + w, y, r);
    ctx.closePath();
  }

  function glass(ctx, x, y, w, h, r, opts) {
    const o = opts || {};
    ctx.save();
    roundRect(ctx, x, y, w, h, r);
    const fill = ctx.createLinearGradient(0, y, 0, y + h);
    fill.addColorStop(0, o.top || "rgba(255,255,255,0.075)");
    fill.addColorStop(1, o.bottom || "rgba(255,255,255,0.025)");
    ctx.fillStyle = fill;
    ctx.fill();
    ctx.lineWidth = 2;
    ctx.strokeStyle = o.stroke || "rgba(255,255,255,0.12)";
    ctx.stroke();
    // A thin highlight along the top edge is what reads as glass.
    ctx.beginPath();
    ctx.moveTo(x + r, y + 1.5);
    ctx.lineTo(x + w - r, y + 1.5);
    ctx.lineWidth = 2;
    ctx.strokeStyle = "rgba(255,255,255,0.18)";
    ctx.stroke();
    ctx.restore();
  }

  function goldGradient(ctx, x0, y0, x1, y1) {
    const g = ctx.createLinearGradient(x0, y0, x1, y1);
    g.addColorStop(0, "#FFE38A");
    g.addColorStop(0.5, GOLD);
    g.addColorStop(1, GOLD2);
    return g;
  }

  function fitText(ctx, text, maxWidth, size, weight, family) {
    let s = size;
    do {
      ctx.font = `${weight} ${s}px ${family}`;
      if (ctx.measureText(text).width <= maxWidth) break;
      s -= 4;
    } while (s > 24);
    return s;
  }

  async function renderCard(p) {
    const L = labels[lang()];
    await ensureFonts();
    const [photo, logo] = await Promise.all([loadImage(p.photo), loadImage("/assets/logo@2x.png")]);

    const canvas = document.createElement("canvas");
    canvas.width = W;
    canvas.height = H;
    const ctx = canvas.getContext("2d");

    // Ground: near-black with a gold bloom behind the portrait.
    ctx.fillStyle = INK;
    ctx.fillRect(0, 0, W, H);
    let bloom = ctx.createRadialGradient(W / 2, 640, 40, W / 2, 640, 900);
    bloom.addColorStop(0, "rgba(245,200,66,0.22)");
    bloom.addColorStop(0.45, "rgba(245,200,66,0.06)");
    bloom.addColorStop(1, "rgba(245,200,66,0)");
    ctx.fillStyle = bloom;
    ctx.fillRect(0, 0, W, H);
    bloom = ctx.createRadialGradient(W / 2, H + 200, 60, W / 2, H + 200, 900);
    bloom.addColorStop(0, "rgba(245,200,66,0.10)");
    bloom.addColorStop(1, "rgba(245,200,66,0)");
    ctx.fillStyle = bloom;
    ctx.fillRect(0, 0, W, H);
    // Faint pitch grid, the same texture as the site.
    ctx.strokeStyle = "rgba(255,255,255,0.025)";
    ctx.lineWidth = 1;
    for (let x = 0; x <= W; x += 60) { ctx.beginPath(); ctx.moveTo(x, 0); ctx.lineTo(x, H); ctx.stroke(); }
    for (let y = 0; y <= H; y += 60) { ctx.beginPath(); ctx.moveTo(0, y); ctx.lineTo(W, y); ctx.stroke(); }

    // Header: mark + wordmark.
    ctx.textAlign = "center";
    ctx.textBaseline = "alphabetic";
    const markH = 96;
    let markW = 0;
    if (logo) markW = (logo.width / logo.height) * markH;
    ctx.font = '120px "Bebas Neue", Impact, sans-serif';
    const wordW = ctx.measureText("FRI").width;
    const gap = logo ? 28 : 0;
    const headX = (W - (markW + gap + wordW)) / 2;
    if (logo) ctx.drawImage(logo, headX, 108, markW, markH);
    ctx.textAlign = "left";
    ctx.fillStyle = goldGradient(ctx, 0, 110, 0, 210);
    ctx.fillText("FRI", headX + markW + gap, 200);
    ctx.textAlign = "center";
    ctx.font = '700 22px "Space Mono", monospace';
    ctx.fillStyle = "rgba(255,255,255,0.45)";
    ctx.fillText(L.index.split("").join(String.fromCharCode(8202)), W / 2, 252);

    // Portrait in a gold ring.
    const cx = W / 2;
    const cy = 600;
    const r = 250;
    ctx.save();
    ctx.shadowColor = "rgba(245,200,66,0.45)";
    ctx.shadowBlur = 60;
    ctx.beginPath();
    ctx.arc(cx, cy, r + 14, 0, Math.PI * 2);
    ctx.fillStyle = goldGradient(ctx, cx - r, cy - r, cx + r, cy + r);
    ctx.fill();
    ctx.restore();
    ctx.beginPath();
    ctx.arc(cx, cy, r + 4, 0, Math.PI * 2);
    ctx.fillStyle = INK;
    ctx.fill();
    ctx.save();
    ctx.beginPath();
    ctx.arc(cx, cy, r, 0, Math.PI * 2);
    ctx.clip();
    const face = ctx.createLinearGradient(0, cy - r, 0, cy + r);
    face.addColorStop(0, "#1B1E24");
    face.addColorStop(1, "#0E1014");
    ctx.fillStyle = face;
    ctx.fillRect(cx - r, cy - r, r * 2, r * 2);
    if (photo) {
      // Cover-fit, anchored near the top so the face is never cropped away.
      const scale = Math.max((r * 2) / photo.width, (r * 2) / photo.height);
      const pw = photo.width * scale;
      const ph = photo.height * scale;
      ctx.drawImage(photo, cx - pw / 2, cy - r - (ph - r * 2) * 0.15, pw, ph);
    } else {
      ctx.font = '200px "Bebas Neue", sans-serif';
      ctx.fillStyle = "rgba(245,200,66,0.6)";
      ctx.fillText((p.name || "?").replace(/^[A-Z]\.\s*/, "").slice(0, 1), cx, cy + 70);
    }
    ctx.restore();

    // Name and meta.
    const name = String(p.name || "").toUpperCase();
    const nameSize = fitText(ctx, name, W - 140, 132, "", '"Bebas Neue", Impact, sans-serif');
    ctx.font = `${nameSize}px "Bebas Neue", Impact, sans-serif`;
    ctx.fillStyle = "#FFFFFF";
    ctx.fillText(name, W / 2, 1000);
    const meta = [p.pos, p.club, p.age ? (lang() === "ru" ? `${p.age} лет` : `Age ${p.age}`) : ""].filter(Boolean).join("  ·  ");
    ctx.font = '500 34px "DM Sans", sans-serif';
    ctx.fillStyle = "rgba(255,255,255,0.55)";
    ctx.fillText(meta, W / 2, 1056);

    // Score panel.
    const px = 90;
    const pw = W - 180;
    const py = 1110;
    const ph = 300;
    glass(ctx, px, py, pw, ph, 36, {
      top: "rgba(245,200,66,0.16)", bottom: "rgba(245,200,66,0.04)", stroke: "rgba(245,200,66,0.38)",
    });
    ctx.font = '210px "Bebas Neue", Impact, sans-serif';
    ctx.fillStyle = goldGradient(ctx, 0, py + 40, 0, py + 240);
    const score = Number(p.fri || 0).toFixed(1);
    ctx.fillText(score, W / 2, py + 222);
    ctx.font = '700 24px "Space Mono", monospace';
    ctx.fillStyle = "rgba(255,255,255,0.5)";
    const tier = typeof window.friTier === "function" ? window.friTier(p.fri, lang()) : "";
    ctx.fillText(`${L.score}${tier ? "  ·  " + String(tier).toUpperCase() : ""}`, W / 2, py + 270);

    // Component tiles, two by two.
    const tiles = [
      [L.perf, p.perf], [L.social, p.social],
      [L.media, p.media], [L.char, p.char],
    ];
    const tw = (pw - 30) / 2;
    const th = 176;
    tiles.forEach(([label, value], i) => {
      const tx = px + (i % 2) * (tw + 30);
      const ty = 1440 + Math.floor(i / 2) * (th + 26);
      glass(ctx, tx, ty, tw, th, 28);
      ctx.textAlign = "left";
      ctx.font = '700 22px "Space Mono", monospace';
      ctx.fillStyle = "rgba(245,200,66,0.85)";
      ctx.fillText(label, tx + 32, ty + 50);
      ctx.font = '84px "Bebas Neue", Impact, sans-serif';
      ctx.fillStyle = "#FFFFFF";
      ctx.fillText(Number(value || 0).toFixed(1).replace(/\.0$/, ""), tx + 32, ty + 124);
      // Bar.
      const bx = tx + 32;
      const by = ty + 146;
      const bw = tw - 64;
      roundRect(ctx, bx, by, bw, 10, 5);
      ctx.fillStyle = "rgba(255,255,255,0.08)";
      ctx.fill();
      const filled = Math.max(10, (bw * Math.max(0, Math.min(100, Number(value || 0)))) / 100);
      roundRect(ctx, bx, by, filled, 10, 5);
      ctx.fillStyle = goldGradient(ctx, bx, 0, bx + bw, 0);
      ctx.save();
      ctx.shadowColor = "rgba(245,200,66,0.6)";
      ctx.shadowBlur = 14;
      ctx.fill();
      ctx.restore();
      ctx.textAlign = "center";
    });

    // Foot.
    ctx.font = '700 26px "Space Mono", monospace';
    ctx.fillStyle = "rgba(255,255,255,0.4)";
    ctx.fillText("footballreputationindex.online", W / 2, 1868);

    return canvas;
  }

  function canvasToBlob(canvas) {
    return new Promise((resolve) => canvas.toBlob((b) => resolve(b), "image/png"));
  }

  function slug(name) {
    return String(name || "player").normalize("NFKD").replace(/[̀-ͯ]/g, "")
      .replace(/[^a-zA-Z0-9]+/g, "-").replace(/^-|-$/g, "").toLowerCase() || "player";
  }

  let current = null; // { url, file }

  function closePreview() {
    const el = document.getElementById("card-preview");
    if (el) el.classList.remove("open");
    if (current && current.url) URL.revokeObjectURL(current.url);
    current = null;
  }

  async function openCardPreview(p) {
    const L = labels[lang()];
    const el = document.getElementById("card-preview");
    if (!el || !p) return;
    const img = el.querySelector("img");
    const status = el.querySelector(".card-preview-status");
    const shareBtn = el.querySelector("[data-card-share]");
    const dlBtn = el.querySelector("[data-card-download]");
    const closeBtn = el.querySelector("[data-card-close]");
    shareBtn.textContent = L.share;
    dlBtn.textContent = L.download;
    closeBtn.textContent = L.close;
    img.removeAttribute("src");
    img.hidden = true;
    status.textContent = L.making;
    status.hidden = false;
    shareBtn.disabled = true;
    dlBtn.disabled = true;
    el.classList.add("open");

    try {
      const canvas = await renderCard(p);
      const blob = await canvasToBlob(canvas);
      if (!blob) throw new Error("empty image");
      const fileName = `fri-${slug(p.name)}.png`;
      const file = new File([blob], fileName, { type: "image/png" });
      current = { url: URL.createObjectURL(blob), file };
      img.src = current.url;
      img.hidden = false;
      status.hidden = true;
      dlBtn.disabled = false;
      // Web Share with files is what puts the card straight into Telegram,
      // Instagram or the camera roll on a phone. Desktop browsers mostly
      // can't, so the button hides and Download does the job.
      const canShare = !!(navigator.canShare && navigator.canShare({ files: [file] }));
      shareBtn.hidden = !canShare;
      shareBtn.disabled = !canShare;
    } catch (err) {
      console.error("card render failed", err);
      status.textContent = L.failed;
    }
  }

  async function shareCurrent() {
    if (!current) return;
    try {
      await navigator.share({ files: [current.file], title: "FRI — Football Reputation Index" });
    } catch (err) {
      if (err && err.name !== "AbortError") console.error("share failed", err);
    }
  }

  function downloadCurrent() {
    if (!current) return;
    const a = document.createElement("a");
    a.href = current.url;
    a.download = current.file.name;
    document.body.appendChild(a);
    a.click();
    a.remove();
  }

  document.addEventListener("click", (ev) => {
    const t = ev.target;
    if (!(t instanceof Element)) return;
    if (t.closest("[data-card-share]")) shareCurrent();
    else if (t.closest("[data-card-download]")) downloadCurrent();
    else if (t.closest("[data-card-close]") || t.id === "card-preview") closePreview();
  });

  window.renderPlayerCard = renderCard;
  window.openCardPreview = openCardPreview;
})();
