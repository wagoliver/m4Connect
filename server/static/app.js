"use strict";

// ── History page state ────────────────────────────────────────────────────────
let currentPeriod = "1h";
let historyData   = null;

// ── Sparkline history (live dashboard) ───────────────────────────────────────
const HIST = 60;
const cpuHist   = new Array(HIST).fill(0);
const netRxHist = new Array(HIST).fill(0);
const netTxHist = new Array(HIST).fill(0);

// ── Uptime ────────────────────────────────────────────────────────────────────
let uptimeSecs  = 0;
let uptimeTimer = null;

// ── Navigation ────────────────────────────────────────────────────────────────
document.querySelectorAll(".nav-item").forEach(item => {
  item.addEventListener("click", e => {
    e.preventDefault();
    const page = item.dataset.page;
    document.querySelectorAll(".nav-item").forEach(n => n.classList.remove("active"));
    document.querySelectorAll(".page").forEach(p => p.classList.remove("active"));
    item.classList.add("active");
    document.getElementById(`page-${page}`).classList.add("active");
    if (page !== "llm") stopLLMPolling();
    if (page === "dashboard")  redrawCharts();
    if (page === "history")    setTimeout(() => loadHistory(currentPeriod), 30);
    if (page === "llm")        startLLMPolling();
    if (page === "inference")  initInference();
  });
});

// ── Formatters ────────────────────────────────────────────────────────────────
function fmtBytes(bps) {
  if (!bps || bps < 0)         return "0 B/s";
  if (bps < 1024)              return bps.toFixed(0) + " B/s";
  if (bps < 1024 * 1024)      return (bps / 1024).toFixed(1) + " KB/s";
  if (bps < 1024 * 1024 * 1024) return (bps / (1024 * 1024)).toFixed(2) + " MB/s";
  return (bps / (1024 * 1024 * 1024)).toFixed(2) + " GB/s";
}

function fmtGBshort(n) {
  if (n == null || isNaN(n)) return "–";
  return n.toFixed(1);
}

function fmtUptime(s) {
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = Math.floor(s % 60);
  return `${h}:${String(m).padStart(2,"0")}:${String(sec).padStart(2,"0")}`;
}

// ── Canvas helpers ────────────────────────────────────────────────────────────
// Reads size from the parent (chart-outer) to avoid layout feedback loops
function canvasSize(canvas) {
  const p = canvas.parentElement;
  return { W: p.clientWidth || 1, H: p.clientHeight || 1 };
}

function setupCanvas(canvas) {
  const dpr = window.devicePixelRatio || 1;
  const { W, H } = canvasSize(canvas);
  canvas.width  = W * dpr;
  canvas.height = H * dpr;
  const ctx = canvas.getContext("2d");
  ctx.scale(dpr, dpr);
  return { ctx, W, H };
}

// ── Donut (SVG) ───────────────────────────────────────────────────────────────
const DONUT_C = 2 * Math.PI * 46; // circumference for r=46 ≈ 289.03

function setDonut(arcId, pct) {
  const arc = document.getElementById(arcId);
  if (!arc) return;
  const offset = DONUT_C * (1 - Math.min(Math.max(pct, 0), 100) / 100);
  arc.style.strokeDashoffset = offset;
}

// ── Sparkline ─────────────────────────────────────────────────────────────────
function drawSparkline(canvas, data, color, fillColor) {
  if (!canvas || !canvas.parentElement) return;
  const { ctx, W, H } = setupCanvas(canvas);
  if (!W || !H) return;
  ctx.clearRect(0, 0, W, H);

  const maxVal = Math.max(...data, 1);
  const pad = 2;
  const step = (W - pad * 2) / (data.length - 1);

  // Grid
  ctx.strokeStyle = "rgba(255,255,255,0.04)";
  ctx.lineWidth = 1;
  [0.25, 0.5, 0.75].forEach(f => {
    const y = H - pad - f * (H - pad * 2);
    ctx.beginPath(); ctx.moveTo(0, y); ctx.lineTo(W, y); ctx.stroke();
  });

  const grad = ctx.createLinearGradient(0, 0, 0, H);
  grad.addColorStop(0, fillColor || "rgba(48,209,88,0.2)");
  grad.addColorStop(1, "rgba(0,0,0,0)");

  const pts = data.map((v, i) => ({
    x: pad + i * step,
    y: H - pad - (v / maxVal) * (H - pad * 2)
  }));

  ctx.beginPath();
  pts.forEach((p, i) => i === 0 ? ctx.moveTo(p.x, p.y) : ctx.lineTo(p.x, p.y));
  ctx.lineTo(pts[pts.length-1].x, H);
  ctx.lineTo(pts[0].x, H);
  ctx.closePath();
  ctx.fillStyle = grad;
  ctx.fill();

  ctx.beginPath();
  pts.forEach((p, i) => i === 0 ? ctx.moveTo(p.x, p.y) : ctx.lineTo(p.x, p.y));
  ctx.strokeStyle = color || "#30d158";
  ctx.lineWidth   = 1.5;
  ctx.lineJoin    = "round";
  ctx.stroke();
}

// ── Dual sparkline (network) ───────────────────────────────────────────────────
function drawDualSparkline(canvas, dataA, dataB, colorA, colorB, fillA, fillB) {
  if (!canvas || !canvas.parentElement) return;
  const { ctx, W, H } = setupCanvas(canvas);
  if (!W || !H) return;
  ctx.clearRect(0, 0, W, H);

  const maxVal = Math.max(...dataA, ...dataB, 1);
  const pad = 2;
  const step = (W - pad * 2) / (dataA.length - 1);

  ctx.strokeStyle = "rgba(255,255,255,0.04)";
  ctx.lineWidth = 1;
  [0.33, 0.66].forEach(f => {
    const y = H - pad - f * (H - pad * 2);
    ctx.beginPath(); ctx.moveTo(0, y); ctx.lineTo(W, y); ctx.stroke();
  });

  const drawSeries = (data, color, fill) => {
    const grad = ctx.createLinearGradient(0, 0, 0, H);
    grad.addColorStop(0, fill);
    grad.addColorStop(1, "rgba(0,0,0,0)");
    const pts = data.map((v, i) => ({
      x: pad + i * step,
      y: H - pad - (v / maxVal) * (H - pad * 2)
    }));
    ctx.beginPath();
    pts.forEach((p, i) => i === 0 ? ctx.moveTo(p.x, p.y) : ctx.lineTo(p.x, p.y));
    ctx.lineTo(pts[pts.length-1].x, H);
    ctx.lineTo(pts[0].x, H);
    ctx.closePath();
    ctx.fillStyle = grad;
    ctx.fill();
    ctx.beginPath();
    pts.forEach((p, i) => i === 0 ? ctx.moveTo(p.x, p.y) : ctx.lineTo(p.x, p.y));
    ctx.strokeStyle = color;
    ctx.lineWidth = 1.5;
    ctx.lineJoin  = "round";
    ctx.stroke();
  };

  drawSeries(dataB, colorB, fillB || "rgba(74,144,217,0.15)");
  drawSeries(dataA, colorA, fillA || "rgba(48,209,88,0.18)");
}

// ── Uptime bars ───────────────────────────────────────────────────────────────
function drawUptimeBars(canvas) {
  if (!canvas || !canvas.parentElement) return;
  const { ctx, W, H } = setupCanvas(canvas);
  if (!W || !H) return;
  ctx.clearRect(0, 0, W, H);

  const bars = 30, gap = 2;
  const barW = Math.max(1, (W - gap * (bars - 1)) / bars);
  for (let i = 0; i < bars; i++) {
    const x    = i * (barW + gap);
    const hPct = 0.15 + Math.random() * 0.65;
    const h    = hPct * H;
    ctx.fillStyle = `rgba(48,209,88,${(0.1 + hPct * 0.4).toFixed(2)})`;
    ctx.fillRect(x, H - h, barW, h);
  }
}

// ── Redraw all charts ─────────────────────────────────────────────────────────
function redrawCharts() {
  requestAnimationFrame(() => {
    drawSparkline(document.getElementById("cpu-chart"), cpuHist, "#30d158");
    drawDualSparkline(document.getElementById("net-chart"), netRxHist, netTxHist, "#30d158", "#4a90d9");
    drawUptimeBars(document.getElementById("uptime-bars"));
    if (historyData) renderHistory(historyData);
  });
}

// ── DOM helpers ───────────────────────────────────────────────────────────────
function setText(id, val) {
  const el = document.getElementById(id);
  if (el && val != null) el.textContent = val;
}
function setWidth(id, pct) {
  const el = document.getElementById(id);
  if (el) el.style.width = Math.min(100, Math.max(0, pct)).toFixed(1) + "%";
}

// ── Update from WebSocket data ────────────────────────────────────────────────
function updateStats(data) {
  const cpu  = data.cpu_percent ?? 0;
  const idle = data.cpu_idle    ?? (100 - cpu);

  // CPU
  cpuHist.push(cpu); cpuHist.shift();
  setText("cpu-val",      cpu.toFixed(0) + "%");
  setText("cpu-idle",     idle.toFixed(0));
  setText("cpu-badge",    cpu.toFixed(0) + "%");
  setText("cpu-temp",     data.temperature != null ? data.temperature.toFixed(0) + "°C" : "–°C");
  setText("la1",          data.load_avg_1 != null ? data.load_avg_1.toFixed(2) : "–");
  setText("la5",          data.load_avg_5 != null ? data.load_avg_5.toFixed(2) : "–");
  setText("stat-procs",   data.process_count ?? "–");
  setText("stat-threads", data.thread_count  ?? "–");
  setText("d-temp-badge", data.temperature != null ? data.temperature.toFixed(0) + "°C" : "–°C");
  setDonut("cpu-arc", cpu);
  drawSparkline(document.getElementById("cpu-chart"), cpuHist, "#30d158");

  // Memory
  const ramUsed  = data.ram_used_gb  ?? 0;
  const ramTotal = data.ram_total_gb ?? 0;
  const ramPct   = data.ram_percent  ?? 0;
  setText("mem-val",    fmtGBshort(ramUsed) + " GB");
  setText("mem-used",   fmtGBshort(ramUsed));
  setText("mem-total",  fmtGBshort(ramTotal));
  setText("mem-badge",  fmtGBshort(ramUsed) + " / " + fmtGBshort(ramTotal) + " GB");
  setText("app-mem",    data.app_mem_gb    != null ? fmtGBshort(data.app_mem_gb)    + " GB" : "–");
  setText("wired-mem",  data.wired_mem_gb  != null ? fmtGBshort(data.wired_mem_gb)  + " GB" : "–");
  setText("comp-mem",   data.comp_mem_gb   != null ? fmtGBshort(data.comp_mem_gb)   + " GB" : "–");
  setText("cached-mem", data.cached_mem_gb != null ? fmtGBshort(data.cached_mem_gb) + " GB" : "–");

  const swapUsed  = data.swap_used_gb  ?? 0;
  const swapTotal = data.swap_total_gb ?? 1;
  setText("swap-used",  fmtGBshort(swapUsed)  + " GB");
  setText("swap-total", fmtGBshort(swapTotal) + " GB");
  setWidth("swap-fill", swapTotal > 0 ? (swapUsed / swapTotal) * 100 : 0);

  const pressure = ramPct > 85 ? "Critical" : ramPct > 70 ? "High" : ramPct > 50 ? "Medium" : "Low";
  setText("mem-pressure", pressure);
  setDonut("mem-arc", ramPct);

  // Network
  const rx = data.net_rx_bps ?? 0;
  const tx = data.net_tx_bps ?? 0;
  netRxHist.push(rx); netRxHist.shift();
  netTxHist.push(tx); netTxHist.shift();
  setText("net-rx", fmtBytes(rx));
  setText("net-tx", fmtBytes(tx));
  drawDualSparkline(document.getElementById("net-chart"), netRxHist, netTxHist, "#30d158", "#4a90d9");

  // Disk
  const diskUsed  = data.disk_used_gb  ?? 0;
  const diskTotal = data.disk_total_gb ?? 1;
  const diskFree  = diskTotal - diskUsed;
  const diskPct   = diskTotal > 0 ? (diskUsed / diskTotal) * 100 : 0;
  setText("disk-val",         Math.round(diskUsed)  + " GB");
  setText("disk-total-label", Math.round(diskTotal) + " GB");
  setText("disk-free",        Math.round(diskFree).toString());
  setText("disk-read",        fmtBytes(data.disk_read_bps  ?? 0));
  setText("disk-write",       fmtBytes(data.disk_write_bps ?? 0));
  setWidth("disk-bar-used", diskPct);
  setDonut("disk-arc", diskPct);

  // System Info
  if (data.os_version)    setText("si-os",      data.os_version);
  if (data.chip_info)     setText("si-chip",    data.chip_info);
  if (data.disk_total_gb) setText("si-storage", Math.round(data.disk_total_gb) + " GB SSD");

  // Uptime
  if (data.uptime_secs != null) {
    uptimeSecs = data.uptime_secs;
    if (!uptimeTimer) {
      uptimeTimer = setInterval(() => {
        uptimeSecs++;
        setText("uptime-clock", fmtUptime(uptimeSecs));
      }, 1000);
    }
    setText("uptime-clock", fmtUptime(uptimeSecs));
  }
  if (data.hostname) {
    setText("uptime-hostname", data.hostname);
    setText("sb-hostname", data.hostname);
  }
  if (data.chip_info) setText("sb-chip", data.chip_info);

  // Sidebar IP
  if (data.mac_ip) setText("d-ip", data.mac_ip);

  // Services
  if (data.services) updateServiceUI(data.services);
}

// ── Services ──────────────────────────────────────────────────────────────────
function updateServiceUI(svc) {
  ["vnc", "ssh"].forEach(name => {
    const on  = svc[name] === true;
    const chk = document.getElementById(`svc-${name}`);
    if (chk) chk.checked = on;
  });
}

async function toggleService(name, state) {
  const action = state ? "on" : "off";
  try {
    await fetch(`/api/services/${name}/${action}`, { method: "POST" });
    addLog(`${name.toUpperCase()} ${state ? "ativado" : "desativado"}`, state ? "ok" : "warn");
  } catch(e) {
    addLog(`Erro ao toggling ${name}: ${e.message}`, "error");
  }
}

// ── Logs ──────────────────────────────────────────────────────────────────────
const logOutput = document.getElementById("log-output");

function addLog(text, type = "info") {
  if (!logOutput) return;
  const line = document.createElement("div");
  const ts   = new Date().toLocaleTimeString("pt-BR");
  line.className  = `log-line log-${type}`;
  line.textContent = `[${ts}] ${text}`;
  logOutput.appendChild(line);
  logOutput.scrollTop = logOutput.scrollHeight;
}

function clearLogs() {
  if (logOutput) logOutput.innerHTML = "";
}

// ── WebSocket ─────────────────────────────────────────────────────────────────
function connectWS() {
  const proto = location.protocol === "https:" ? "wss" : "ws";
  const ws = new WebSocket(`${proto}://${location.host}/ws`);
  ws.onmessage = e => { try { updateStats(JSON.parse(e.data)); } catch(_) {} };
  ws.onclose   = ()  => { setTimeout(connectWS, 3000); };
  ws.onerror   = ()  => { addLog("WebSocket erro", "error"); };
}

// ── History charts ────────────────────────────────────────────────────────────

function fmtHistTime(ts, period) {
  const d = new Date(ts * 1000);
  if (period === "7d") {
    return d.toLocaleDateString("pt-BR", { weekday: "short", day: "numeric" });
  }
  return d.toLocaleTimeString("pt-BR", { hour: "2-digit", minute: "2-digit" });
}

function drawHistoryChart(canvas, points, getValue, color, fillColor) {
  if (!canvas || !canvas.parentElement) return;
  const { ctx, W, H } = setupCanvas(canvas);
  if (!W || !H) return;
  ctx.clearRect(0, 0, W, H);
  if (!points || points.length < 2) return;

  const values = points.map(p => getValue(p));
  const maxVal = Math.max(...values, 1);
  const pad    = 2;
  const step   = (W - pad * 2) / (points.length - 1);

  ctx.strokeStyle = "rgba(255,255,255,0.04)";
  ctx.lineWidth   = 1;
  [0.25, 0.5, 0.75].forEach(f => {
    const y = H - pad - f * (H - pad * 2);
    ctx.beginPath(); ctx.moveTo(0, y); ctx.lineTo(W, y); ctx.stroke();
  });

  const grad = ctx.createLinearGradient(0, 0, 0, H);
  grad.addColorStop(0, fillColor || "rgba(48,209,88,0.2)");
  grad.addColorStop(1, "rgba(0,0,0,0)");

  const pts = values.map((v, i) => ({
    x: pad + i * step,
    y: H - pad - (v / maxVal) * (H - pad * 2)
  }));

  ctx.beginPath();
  pts.forEach((p, i) => i === 0 ? ctx.moveTo(p.x, p.y) : ctx.lineTo(p.x, p.y));
  ctx.lineTo(pts[pts.length - 1].x, H); ctx.lineTo(pts[0].x, H);
  ctx.closePath(); ctx.fillStyle = grad; ctx.fill();

  ctx.beginPath();
  pts.forEach((p, i) => i === 0 ? ctx.moveTo(p.x, p.y) : ctx.lineTo(p.x, p.y));
  ctx.strokeStyle = color || "#30d158";
  ctx.lineWidth   = 1.5;
  ctx.lineJoin    = "round";
  ctx.stroke();
}

function drawHistoryNetChart(canvas, points) {
  if (!canvas || !canvas.parentElement) return;
  const { ctx, W, H } = setupCanvas(canvas);
  if (!W || !H) return;
  ctx.clearRect(0, 0, W, H);
  if (!points || points.length < 2) return;

  const rxVals = points.map(p => p.rx);
  const txVals = points.map(p => p.tx);
  const maxVal = Math.max(...rxVals, ...txVals, 1);
  const pad    = 2;
  const step   = (W - pad * 2) / (points.length - 1);

  ctx.strokeStyle = "rgba(255,255,255,0.04)";
  ctx.lineWidth   = 1;
  [0.33, 0.66].forEach(f => {
    const y = H - pad - f * (H - pad * 2);
    ctx.beginPath(); ctx.moveTo(0, y); ctx.lineTo(W, y); ctx.stroke();
  });

  const drawLine = (vals, color, fill) => {
    const grad = ctx.createLinearGradient(0, 0, 0, H);
    grad.addColorStop(0, fill); grad.addColorStop(1, "rgba(0,0,0,0)");
    const pts = vals.map((v, i) => ({
      x: pad + i * step,
      y: H - pad - (v / maxVal) * (H - pad * 2)
    }));
    ctx.beginPath();
    pts.forEach((p, i) => i === 0 ? ctx.moveTo(p.x, p.y) : ctx.lineTo(p.x, p.y));
    ctx.lineTo(pts[pts.length - 1].x, H); ctx.lineTo(pts[0].x, H);
    ctx.closePath(); ctx.fillStyle = grad; ctx.fill();
    ctx.beginPath();
    pts.forEach((p, i) => i === 0 ? ctx.moveTo(p.x, p.y) : ctx.lineTo(p.x, p.y));
    ctx.strokeStyle = color; ctx.lineWidth = 1.5; ctx.lineJoin = "round"; ctx.stroke();
  };

  drawLine(txVals, "#4a90d9", "rgba(74,144,217,0.15)");
  drawLine(rxVals, "#30d158", "rgba(48,209,88,0.18)");
}

function setHistAxis(id, points, period) {
  const el = document.getElementById(id);
  if (!el || !points || points.length < 2) return;
  const spans = el.querySelectorAll("span");
  if (spans.length >= 2) {
    spans[0].textContent = fmtHistTime(points[0].t, period);
    spans[1].textContent = fmtHistTime(points[points.length - 1].t, period);
  }
}

function renderHistory(data) {
  historyData = data;
  const pts    = data.points || [];
  const period = data.period;
  const avg    = (arr, fn) => arr.length ? (arr.reduce((s, p) => s + fn(p), 0) / arr.length) : null;

  // CPU
  const cpuAvg = avg(pts, p => p.cpu);
  setText("hist-cpu-badge", cpuAvg !== null ? `avg ${cpuAvg.toFixed(0)}%` : "sem dados");
  drawHistoryChart(
    document.getElementById("hist-cpu-chart"),
    pts, p => p.cpu, "#30d158", "rgba(48,209,88,0.18)"
  );
  setHistAxis("hist-cpu-axis", pts, period);

  // RAM
  const ramAvg = avg(pts, p => p.ram);
  setText("hist-ram-badge", ramAvg !== null ? `avg ${ramAvg.toFixed(0)}%` : "sem dados");
  drawHistoryChart(
    document.getElementById("hist-ram-chart"),
    pts, p => p.ram, "#4a90d9", "rgba(74,144,217,0.18)"
  );
  setHistAxis("hist-ram-axis", pts, period);

  // Temperature (filter zeroes — no data points)
  const tempPts = pts.filter(p => p.temp > 0);
  const tempAvg = avg(tempPts, p => p.temp);
  setText("hist-temp-badge", tempAvg !== null ? `avg ${tempAvg.toFixed(0)}°C` : "sem dados");
  drawHistoryChart(
    document.getElementById("hist-temp-chart"),
    tempPts, p => p.temp, "#e67e22", "rgba(230,126,34,0.18)"
  );
  setHistAxis("hist-temp-axis", pts, period);

  // Network
  const maxRx = pts.length ? Math.max(...pts.map(p => p.rx)) : 0;
  setText("hist-net-badge", pts.length ? `↓ max ${fmtBytes(maxRx)}` : "sem dados");
  drawHistoryNetChart(document.getElementById("hist-net-chart"), pts);
  setHistAxis("hist-net-axis", pts, period);
}

async function loadHistory(period) {
  currentPeriod = period;
  try {
    const res  = await fetch(`/api/history?period=${period}`);
    const data = await res.json();
    renderHistory(data);
  } catch(e) {
    addLog(`Histórico: erro ao carregar (${e.message})`, "error");
  }
}

document.querySelectorAll(".hist-period-btn").forEach(btn => {
  btn.addEventListener("click", () => {
    document.querySelectorAll(".hist-period-btn").forEach(b => b.classList.remove("active"));
    btn.classList.add("active");
    loadHistory(btn.dataset.period);
  });
});

// ── Resize ────────────────────────────────────────────────────────────────────
let resizeTimer = null;
window.addEventListener("resize", () => {
  clearTimeout(resizeTimer);
  resizeTimer = setTimeout(redrawCharts, 100);
});

// ── Init ──────────────────────────────────────────────────────────────────────
window.addEventListener("load", () => {
  requestAnimationFrame(() => {
    drawUptimeBars(document.getElementById("uptime-bars"));
  });

  connectWS();

  fetch("/api/status")
    .then(r => r.json())
    .then(updateStats)
    .catch(() => {});

  fetch("/api/services")
    .then(r => r.json())
    .then(updateServiceUI)
    .catch(() => {});

  loadHistory("1h");

  fetch("/api/version")
    .then(r => r.json())
    .then(v => setText("sb-version", `v${v.version} · ${v.build}`))
    .catch(() => {});
});

// ── Inference page ────────────────────────────────────────────────────────────

let infMessages    = [];   // conversation history [{role, content}]
let infAbortCtrl   = null; // AbortController for active stream
let infMsgId       = 0;    // monotonic ID per assistant message
let infCurrent     = {};   // state for the streaming assistant message
let infRAFPending  = false; // requestAnimationFrame render queued

function initInference() {
  fetch("/api/ollama")
    .then(r => r.json())
    .then(d => {
      const sel = document.getElementById("inf-model-select");
      const models = d.models || [];
      sel.innerHTML = models.length === 0
        ? '<option value="">Nenhum modelo instalado</option>'
        : models.map(m => `<option value="${m.name}">${m.name}</option>`).join("");
      // pre-select a running model if any
      const running = d.running_models || [];
      if (running.length > 0) sel.value = running[0].name;
      infUpdateChip();
    })
    .catch(() => {
      document.getElementById("inf-model-select").innerHTML = '<option value="">Ollama offline</option>';
      infUpdateChip();
    });
}

function infUpdateChip() {
  const sel = document.getElementById("inf-model-select");
  document.getElementById("inf-footer-chip").textContent = sel && sel.value ? sel.value : "–";
}

function infKeydown(e) {
  if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); sendInference(); }
}

function infAutoResize(el) {
  el.style.height = "auto";
  el.style.height = Math.min(el.scrollHeight, 120) + "px";
}

async function sendInference() {
  // Stop active stream
  if (infAbortCtrl) { infAbortCtrl.abort(); return; }

  const ta  = document.getElementById("inf-textarea");
  const sel = document.getElementById("inf-model-select");
  const text = ta.value.trim();
  if (!text || !sel.value) return;

  // Hide empty state, reset textarea
  const emptyEl = document.getElementById("inf-empty");
  if (emptyEl) emptyEl.style.display = "none";
  ta.value = "";
  infAutoResize(ta);

  // Add to history and render user bubble
  infMessages.push({ role: "user", content: text });
  infRenderUser(text);

  // Prepare assistant container
  const id = ++infMsgId;
  infCurrent = { id, thinkText: "", responseText: "", thinkDone: false };
  infRenderAssistant(id, sel.value);
  infScrollChat();

  infSetStop(true);
  infAbortCtrl = new AbortController();

  try {
    const resp = await fetch("/api/inference", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        model:    sel.value,
        messages: infMessages,
        options:  infGetOptions(),
        system:   infGetSystem(),
      }),
      signal: infAbortCtrl.signal,
    });
    if (!resp.ok) throw new Error("HTTP " + resp.status);

    const reader  = resp.body.getReader();
    const decoder = new TextDecoder();
    let sseBuf = "", curEvent = "";

    while (true) {
      const { value, done } = await reader.read();
      if (done) break;
      sseBuf += decoder.decode(value, { stream: true });
      const lines = sseBuf.split("\n");
      sseBuf = lines.pop();
      for (const line of lines) {
        if (line.startsWith("event: ")) {
          curEvent = line.slice(7).trim();
        } else if (line.startsWith("data: ")) {
          try { infHandleSSE(curEvent, JSON.parse(line.slice(6)), id); } catch (_) {}
          curEvent = "";
        }
      }
    }
  } catch (err) {
    if (err.name !== "AbortError") infRenderError(id, "Erro ao conectar com Ollama.");
  } finally {
    infAbortCtrl = null;
    infSetStop(false);
    infFinishMsg(id);
  }
}

function infHandleSSE(event, data, id) {
  switch (event) {
    case "think_start":
      infShowThink(id, true);
      // Show placeholder in response while thinking
      const rePlaceholder = document.getElementById(`inf-re-${id}`);
      if (rePlaceholder) rePlaceholder.setAttribute("data-thinking", "1");
      break;

    case "think": {
      infCurrent.thinkText += data.token || "";
      const tc = document.getElementById(`inf-tc-${id}`);
      if (tc) tc.textContent = infCurrent.thinkText;
      // Update header counter
      const th = document.getElementById(`inf-th-label-${id}`);
      const chars = infCurrent.thinkText.length;
      if (th) th.textContent = `Pensando… (${chars} chars)`;
      infScrollChat();
      break;
    }

    case "think_end":
      infCurrent.thinkDone = true;
      infMarkThinkDone(id);
      // Clear thinking placeholder
      const reAfterThink = document.getElementById(`inf-re-${id}`);
      if (reAfterThink) reAfterThink.removeAttribute("data-thinking");
      break;

    case "token": {
      infCurrent.responseText += data.token || "";
      infScheduleRender();
      break;
    }

    case "stats":
      infRenderStats(id, data);
      infMessages.push({ role: "assistant", content: infCurrent.responseText });
      break;

    case "done": {
      const rel = document.getElementById(`inf-re-${id}`);
      if (rel) {
        rel.classList.remove("streaming");
        rel.innerHTML = infMarkdown(infCurrent.responseText);
      }
      const tsEl = document.getElementById(`inf-ts-time-${id}`);
      if (tsEl) tsEl.textContent = infNow();
      break;
    }

    case "error":
      infRenderError(id, data.msg || "Erro desconhecido.");
      break;
  }
}

function infRenderUser(text) {
  const chat = document.getElementById("inf-chat");
  const el = document.createElement("div");
  el.className = "inf-msg inf-msg-user";
  el.innerHTML = `
    <div class="inf-msg-user-inner">
      <div class="inf-msg-content">${infEscape(text)}</div>
      <span class="inf-timestamp">${infNow()}</span>
    </div>
    <div class="inf-avatar-user-icon">
      <svg width="14" height="14" viewBox="0 0 14 14" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round">
        <circle cx="7" cy="5" r="2.5"/>
        <path d="M2.5 13c0-2.5 2-4.5 4.5-4.5s4.5 2 4.5 4.5"/>
      </svg>
    </div>`;
  chat.appendChild(el);
}

function infRenderAssistant(id, modelName) {
  const chat = document.getElementById("inf-chat");
  const el = document.createElement("div");
  el.className = "inf-msg inf-msg-assistant";
  el.id = `inf-msg-${id}`;
  const family = infModelFamily(modelName);
  const shortName = infModelShort(modelName);
  el.innerHTML = `
    <div class="inf-avatar-col">
      <div class="inf-avatar-icon">${familyIcon(family)}</div>
      <span class="inf-avatar-model-label">${shortName}</span>
    </div>
    <div class="inf-msg-body">
      <div class="inf-think-block" id="inf-tb-${id}" style="display:none">
        <div class="inf-think-header" onclick="infToggleThink(${id})">
          <div class="inf-think-spinner" id="inf-ts-${id}"></div>
          <span id="inf-th-label-${id}">Pensando…</span>
          <span class="inf-think-toggle">▾</span>
        </div>
        <div class="inf-think-content" id="inf-tc-${id}"></div>
      </div>
      <div class="inf-response streaming" id="inf-re-${id}"></div>
      <div class="inf-stats-row" id="inf-sr-${id}" style="display:none"></div>
      <span class="inf-timestamp" id="inf-ts-time-${id}"></span>
    </div>`;
  chat.appendChild(el);
}

function infShowThink(id, show) {
  const el = document.getElementById(`inf-tb-${id}`);
  if (el) el.style.display = show ? "" : "none";
}

function infMarkThinkDone(id) {
  const spinner = document.getElementById(`inf-ts-${id}`);
  if (spinner) spinner.classList.add("done");
  const label = document.getElementById(`inf-th-label-${id}`);
  if (label) {
    const chars = infCurrent.thinkText.length;
    label.textContent = `Pensamento (${chars} chars) — clique para expandir`;
  }
  const block = document.getElementById(`inf-tb-${id}`);
  if (block) block.classList.add("collapsed");
}

function infRenderStats(id, d) {
  const el = document.getElementById(`inf-sr-${id}`);
  if (!el) return;
  const tps  = (d.tps  || 0).toFixed(1);
  const ptps = (d.prompt_tps || 0).toFixed(1);
  const sec  = ((d.total_ms || 0) / 1000).toFixed(2);
  el.style.display = "flex";
  el.innerHTML = `
    <span class="inf-stat-hi">⚡ <span class="inf-stat-val">${tps} t/s</span></span>
    <span class="inf-stat-sep">·</span>
    <span><span class="inf-stat-val">${d.eval_count || 0}</span> tokens</span>
    <span class="inf-stat-sep">·</span>
    <span><span class="inf-stat-val">${sec}s</span> total</span>
    <span class="inf-stat-sep">·</span>
    <span>prompt <span class="inf-stat-val">${ptps} t/s</span></span>
    <span class="inf-stat-sep">·</span>
    <span>load <span class="inf-stat-val">${d.load_ms || 0}ms</span></span>`;
}

function infRenderError(id, msg) {
  const el = document.getElementById(`inf-re-${id}`);
  if (el) { el.classList.remove("streaming"); el.className += " inf-error"; el.textContent = msg; }
}

function infFinishMsg(id) {
  const el = document.getElementById(`inf-re-${id}`);
  if (el) el.classList.remove("streaming");
  const sp = document.getElementById(`inf-ts-${id}`);
  if (sp) sp.classList.add("done");
}

function infToggleThink(id) {
  const b = document.getElementById(`inf-tb-${id}`);
  if (b) b.classList.toggle("collapsed");
}

function infSetStop(isStop) {
  const btn  = document.getElementById("inf-send-btn");
  const send = document.getElementById("inf-send-icon");
  const stop = document.getElementById("inf-stop-icon");
  btn.classList.toggle("stop", isStop);
  if (send) send.style.display = isStop ? "none" : "";
  if (stop) stop.style.display = isStop ? ""     : "none";
}

function infScrollChat() {
  const c = document.getElementById("inf-chat");
  if (c) c.scrollTop = c.scrollHeight;
}

function clearInference() {
  if (infAbortCtrl) infAbortCtrl.abort();
  infMessages = []; infMsgId = 0; infCurrent = {};
  const chat = document.getElementById("inf-chat");
  if (!chat) return;
  chat.querySelectorAll(".inf-msg").forEach(el => el.remove());
  const empty = document.getElementById("inf-empty");
  if (empty) empty.style.display = "";
}

function infEscape(s) {
  return s.replace(/&/g,"&amp;").replace(/</g,"&lt;").replace(/>/g,"&gt;").replace(/"/g,"&quot;");
}

// ── Params panel ─────────────────────────────────────────────────────────────

function infToggleParams() {
  const panel = document.getElementById("inf-params-panel");
  const btn   = document.getElementById("inf-gear-btn");
  const open  = panel.classList.toggle("open");
  btn.classList.toggle("active", open);
}

function infParamVal(sliderId, valId) {
  const v = document.getElementById(sliderId)?.value;
  const el = document.getElementById(valId);
  if (el && v != null) el.textContent = v;
}

function infGetOptions() {
  const temp   = parseFloat(document.getElementById("inf-temp")?.value   ?? "0.8");
  const topp   = parseFloat(document.getElementById("inf-topp")?.value   ?? "0.9");
  const ctx    = parseInt(document.getElementById("inf-ctx")?.value       ?? "4096");
  const maxtok = parseInt(document.getElementById("inf-maxtok")?.value    ?? "-1");
  const opts = { temperature: temp, top_p: topp, num_ctx: ctx };
  if (maxtok > 0) opts.num_predict = maxtok;
  return opts;
}

function infGetSystem() {
  return document.getElementById("inf-system")?.value?.trim() || "";
}

// ── Progressive rendering (requestAnimationFrame batched) ─────────────────────

function infScheduleRender() {
  if (infRAFPending) return;
  infRAFPending = true;
  requestAnimationFrame(() => {
    infRAFPending = false;
    const re = document.getElementById(`inf-re-${infCurrent.id}`);
    if (re) {
      re.innerHTML = infMarkdownStream(infCurrent.responseText);
      infScrollChat();
    }
  });
}

// Renders complete lines as markdown; current partial line as plain text.
// Holds back unclosed code blocks to avoid janky half-rendered fences.
function infMarkdownStream(raw) {
  if (!raw) return "";
  const lastNl = raw.lastIndexOf("\n");
  if (lastNl <= 0) return '<span style="white-space:pre-wrap">' + infEscape(raw) + "</span>";

  const complete = raw.slice(0, lastNl);
  const partial  = raw.slice(lastNl + 1);

  // Count ``` fences in the complete portion
  const fences = (complete.match(/^```/gm) || []).length;
  if (fences % 2 !== 0) {
    // Inside an unclosed code block — show as plain text until block closes
    return '<span style="white-space:pre-wrap">' + infEscape(raw) + "</span>";
  }

  const rendered = infMarkdown(complete);
  return rendered + (partial
    ? '<span style="white-space:pre-wrap">' + infEscape(partial) + "</span>"
    : "");
}

// ── Helpers ───────────────────────────────────────────────────────────────────

function infNow() {
  return new Date().toLocaleTimeString("pt-BR", { hour: "2-digit", minute: "2-digit" });
}

function infModelFamily(name) {
  const n = (name || "").toLowerCase();
  if (n.includes("codellama") || n.includes("llama")) return "llama";
  if (n.includes("mixtral") || n.includes("mistral"))  return "mistral";
  if (n.includes("gemma"))     return "gemma";
  if (n.includes("phi"))       return "phi";
  if (n.includes("deepseek"))  return "deepseek";
  if (n.includes("qwen"))      return "qwen";
  if (n.includes("starcoder")) return "starcoder";
  return "";
}

function infModelShort(name) {
  if (!name) return "–";
  const s = name.split(":")[0];
  return s.length > 12 ? s.slice(0, 11) + "…" : s;
}

function infCopyCode(btn) {
  const code = btn.closest(".inf-code-block").querySelector("code");
  if (!code) return;
  navigator.clipboard.writeText(code.textContent).then(() => {
    btn.textContent = "Copiado ✓";
    btn.classList.add("copied");
    setTimeout(() => { btn.textContent = "Copiar"; btn.classList.remove("copied"); }, 2000);
  }).catch(() => {});
}

// ── Markdown renderer (streaming-safe: call on full text after done) ──────────
function infMarkdown(raw) {
  const lines = raw.split("\n");
  let out = "", inCode = false, codeLang = "", codeBuf = [];

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];

    if (!inCode && line.startsWith("```")) {
      inCode = true; codeLang = line.slice(3).trim() || "code"; codeBuf = []; continue;
    }
    if (inCode && line.trimEnd() === "```") {
      out += `<div class="inf-code-block">
        <div class="inf-code-header">
          <span class="inf-code-lang">${infEscape(codeLang)}</span>
          <button class="inf-code-copy" onclick="infCopyCode(this)">Copiar</button>
        </div>
        <pre><code>${codeBuf.map(infEscape).join("\n")}</code></pre>
      </div>`;
      inCode = false; continue;
    }
    if (inCode) { codeBuf.push(line); continue; }

    let l = infEscape(line);
    l = l.replace(/`([^`]+)`/g, "<code>$1</code>");
    l = l.replace(/\*\*(.+?)\*\*/g, "<strong>$1</strong>");
    l = l.replace(/(?<!\*)\*([^*\n]+)\*(?!\*)/g, "<em>$1</em>");
    if (l.startsWith("### "))      { out += `<h4>${l.slice(4)}</h4>`; continue; }
    else if (l.startsWith("## ")) { out += `<h3>${l.slice(3)}</h3>`; continue; }
    else if (l.startsWith("# "))  { out += `<h2>${l.slice(2)}</h2>`; continue; }
    if (/^[-*] /.test(l)) { out += `<li>${l.slice(2)}</li>`; continue; }
    if (/^\d+\. /.test(l)) { out += `<li>${l.replace(/^\d+\. /, "")}</li>`; continue; }
    if (l.trim() === "") { out += "<br>"; continue; }
    out += l + "<br>";
  }
  if (inCode) out += `<div class="inf-code-block">
    <div class="inf-code-header">
      <span class="inf-code-lang">${infEscape(codeLang)}</span>
      <button class="inf-code-copy" onclick="infCopyCode(this)">Copiar</button>
    </div>
    <pre><code>${codeBuf.map(infEscape).join("\n")}</code></pre>
  </div>`;

  out = out.replace(/((?:<li>[\s\S]*?<\/li>)+)/g, "<ul>$1</ul>");
  out = out.replace(/<br>(<(?:h[234]|ul|div|br))/g, "$1");
  return out;
}

// ── Benchmark page ────────────────────────────────────────────────────────────

const BENCH_DASH    = 283;   // π × 90 — arc path length for r=90 semicircle
const BENCH_MAX     = 1000;  // 1 Gbps ceiling on the gauge

let benchRunning = false;

function benchSetGauge(mbps, phase) {
  const fraction = Math.min(mbps / BENCH_MAX, 1);
  const fill = document.getElementById("bench-fill");
  if (fill) {
    fill.style.strokeDashoffset = BENCH_DASH * (1 - fraction);
    fill.setAttribute("class", "bench-gauge-fill" + (phase ? " phase-" + phase : ""));
  }
  const valEl   = document.getElementById("bench-speed-val");
  const unitEl  = document.getElementById("bench-speed-unit");
  const phaseEl = document.getElementById("bench-speed-phase");
  if (valEl)   valEl.textContent   = mbps >= 100 ? mbps.toFixed(0) : mbps.toFixed(1);
  if (unitEl)  unitEl.textContent  = "Mbps";
  if (phaseEl) phaseEl.textContent = phase === "download" ? "↓ Download" : "↑ Upload";
}

function benchSetPingDisplay(ms) {
  const valEl   = document.getElementById("bench-speed-val");
  const unitEl  = document.getElementById("bench-speed-unit");
  const phaseEl = document.getElementById("bench-speed-phase");
  if (valEl)   valEl.textContent   = ms.toFixed(1);
  if (unitEl)  unitEl.textContent  = "ms";
  if (phaseEl) phaseEl.textContent = "Ping RTT";
  const fill = document.getElementById("bench-fill");
  if (fill) {
    fill.style.strokeDashoffset = BENCH_DASH;
    fill.setAttribute("class", "bench-gauge-fill phase-ping");
  }
}

function benchSetPhase(phase) {
  ["ping","download","upload"].forEach(p => {
    const el = document.getElementById(`bph-${p}`);
    if (!el) return;
    el.className = "bench-phase-item" + (p === phase ? " active" : "");
  });
}

function benchMarkDone(phase) {
  const el = document.getElementById(`bph-${phase}`);
  if (el) el.className = "bench-phase-item done";
}

async function benchTestPing() {
  benchSetPhase("ping");
  document.getElementById("bench-speed-phase").textContent = "Ping";
  const N = 10;
  const rtts = [];
  for (let i = 0; i < N; i++) {
    const t0 = performance.now();
    await fetch("/api/speedtest/ping", { cache: "no-store" });
    const rtt = performance.now() - t0;
    rtts.push(rtt);
    benchSetPingDisplay(rtt);
    await new Promise(r => setTimeout(r, 40));
  }
  rtts.sort((a, b) => a - b);
  const median = rtts[Math.floor(N / 2)];
  benchSetPingDisplay(median);
  benchMarkDone("ping");
  return median;
}

async function benchTestDownload() {
  benchSetPhase("download");
  const SIZE = 100 * 1024 * 1024; // 100 MB
  const start = performance.now();
  let received = 0;

  const resp = await fetch(`/api/speedtest/download?size=${SIZE}`, { cache: "no-store" });
  const reader = resp.body.getReader();
  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    received += value.byteLength;
    const elapsed = (performance.now() - start) / 1000;
    if (elapsed > 0.05) benchSetGauge((received * 8) / (elapsed * 1e6), "download");
  }

  const elapsed = (performance.now() - start) / 1000;
  const mbps = elapsed > 0 ? (received * 8) / (elapsed * 1e6) : 0;
  benchMarkDone("download");
  return mbps;
}

async function benchTestUpload() {
  benchSetPhase("upload");
  const SIZE = 50 * 1024 * 1024; // 50 MB
  return new Promise((resolve) => {
    const data = new Blob([new ArrayBuffer(SIZE)]);
    const xhr  = new XMLHttpRequest();
    const start = performance.now();

    xhr.upload.onprogress = (e) => {
      const elapsed = (performance.now() - start) / 1000;
      if (elapsed > 0.1 && e.loaded > 0) {
        benchSetGauge((e.loaded * 8) / (elapsed * 1e6), "upload");
      }
    };

    xhr.onload = () => {
      const elapsed = (performance.now() - start) / 1000;
      benchMarkDone("upload");
      resolve(elapsed > 0 ? (SIZE * 8) / (elapsed * 1e6) : 0);
    };

    xhr.onerror = () => { benchMarkDone("upload"); resolve(0); };
    xhr.open("POST", "/api/speedtest/upload");
    xhr.send(data);
  });
}

async function runBenchmark() {
  if (benchRunning) return;
  benchRunning = true;

  const btn = document.getElementById("bench-start-btn");
  btn.disabled = true;
  btn.textContent = "Testando…";

  // Reset UI
  ["ping","download","upload"].forEach(p => {
    const ph = document.getElementById(`bph-${p}`);
    if (ph) ph.className = "bench-phase-item";
    const card = document.getElementById(`bench-card-${p === "ping" ? "ping" : p === "download" ? "dl" : "ul"}`);
    if (card) card.classList.remove("done");
  });
  document.getElementById("bench-res-ping").textContent = "–";
  document.getElementById("bench-res-dl").textContent   = "–";
  document.getElementById("bench-res-ul").textContent   = "–";
  const fill = document.getElementById("bench-fill");
  if (fill) { fill.style.strokeDashoffset = BENCH_DASH; fill.setAttribute("class", "bench-gauge-fill"); }
  document.getElementById("bench-speed-val").textContent   = "0";
  document.getElementById("bench-speed-unit").textContent  = "Mbps";
  document.getElementById("bench-speed-phase").textContent = "Iniciando…";

  try {
    const pingMs = await benchTestPing();
    document.getElementById("bench-res-ping").textContent = pingMs.toFixed(2);
    document.getElementById("bench-card-ping").classList.add("done");

    await new Promise(r => setTimeout(r, 300));

    const dlMbps = await benchTestDownload();
    document.getElementById("bench-res-dl").textContent = dlMbps >= 100 ? dlMbps.toFixed(0) : dlMbps.toFixed(1);
    document.getElementById("bench-card-dl").classList.add("done");

    await new Promise(r => setTimeout(r, 300));

    const ulMbps = await benchTestUpload();
    document.getElementById("bench-res-ul").textContent = ulMbps >= 100 ? ulMbps.toFixed(0) : ulMbps.toFixed(1);
    document.getElementById("bench-card-ul").classList.add("done");

    // Show download result on gauge
    benchSetGauge(dlMbps, "download");
    document.getElementById("bench-speed-phase").textContent = "Concluído";
  } catch (err) {
    document.getElementById("bench-speed-phase").textContent = "Erro";
    addLog("Benchmark: " + (err.message || err), "error");
  }

  benchRunning = false;
  btn.disabled = false;
  btn.textContent = "Repetir Teste";
}

// ── LLM page ──────────────────────────────────────────────────────────────────

let llmTimer = null;

function startLLMPolling() {
  loadLLM();
  if (!llmTimer) llmTimer = setInterval(loadLLM, 5000);
}

function stopLLMPolling() {
  if (llmTimer) { clearInterval(llmTimer); llmTimer = null; }
}

function loadLLM() {
  fetch("/api/ollama")
    .then(r => r.json())
    .then(renderLLM)
    .catch(() => renderLLMOffline());
}

function renderLLMOffline() {
  document.getElementById("llm-dot").className = "llm-dot offline";
  document.getElementById("llm-status-text").textContent = "Offline";
  document.getElementById("llm-top-grid").style.display = "none";
  document.getElementById("llm-models-card") && (document.getElementById("llm-models-card").style.display = "none");
  document.getElementById("llm-offline-wrap").style.display = "flex";
}

function renderLLM(d) {
  if (!d) { renderLLMOffline(); return; }
  const offlineEl = document.getElementById("llm-offline-wrap");
  const topGrid   = document.getElementById("llm-top-grid");
  const modCard   = document.getElementById("llm-models-card");

  if (!d.online) {
    renderLLMOffline();
    return;
  }

  offlineEl.style.display = "none";
  topGrid.style.display   = "";
  if (modCard) modCard.style.display = "";

  // Status bar
  document.getElementById("llm-dot").className = "llm-dot online";
  document.getElementById("llm-status-text").textContent = "Ollama Online";
  document.getElementById("llm-version-chip").textContent = d.version || "–";
  document.getElementById("llm-ping-chip").textContent    = `${d.ping_ms} ms`;

  // ── Active model card ──
  const running = d.running_models || [];
  const hasRunning = running.length > 0;
  const activeModel = hasRunning ? running[0] : null;

  const runBadge = document.getElementById("llm-run-badge");
  const activeBody = document.getElementById("llm-active-body");
  const expiresRow = document.getElementById("llm-expires-row");

  if (activeModel) {
    runBadge.textContent = "Running";
    runBadge.className   = "llm-badge-run";

    document.getElementById("llm-active-icon").innerHTML = familyIcon(activeModel.details?.family || "");
    document.getElementById("llm-active-name").textContent   = activeModel.name || "–";
    document.getElementById("llm-active-family").textContent = familyLabel(activeModel.details?.family || "");
    document.getElementById("llm-active-params").textContent = activeModel.details?.parameter_size || "–";
    document.getElementById("llm-active-quant").textContent  = activeModel.details?.quantization_level || "–";
    document.getElementById("llm-active-fmt").textContent    = activeModel.details?.format?.toUpperCase() || "–";

    expiresRow.style.display = "";
    const exp = new Date(activeModel.expires_at);
    const diff = Math.round((exp - Date.now()) / 60000);
    document.getElementById("llm-expires").textContent = diff > 0 ? `${diff} min` : "em breve";
  } else {
    runBadge.textContent = "Idle";
    runBadge.className   = "llm-badge-run idle";
    document.getElementById("llm-active-icon").innerHTML = familyIcon("");
    document.getElementById("llm-active-name").textContent   = "Nenhum modelo ativo";
    document.getElementById("llm-active-family").textContent = "–";
    document.getElementById("llm-active-params").textContent = "–";
    document.getElementById("llm-active-quant").textContent  = "–";
    document.getElementById("llm-active-fmt").textContent    = "–";
    expiresRow.style.display = "none";
  }

  // ── Memory donut ──
  const totalRAM = 24; // typical M4 — we'll show loaded GB vs totalRAM
  const memGB = d.mem_used_bytes ? d.mem_used_bytes / 1e9 : 0;
  const memPct = Math.min((memGB / totalRAM) * 100, 100);
  setDonut("llm-mem-arc", memPct);
  document.getElementById("llm-mem-val").textContent = memGB.toFixed(1);
  document.getElementById("llm-mem-badge").textContent = `${memGB.toFixed(1)} GB`;
  document.getElementById("llm-mem-sub").textContent = "GB loaded";

  // ── Runtime card ──
  const modCount = (d.models || []).length;
  document.getElementById("llm-rt-host").textContent    = "Ollama " + (d.version || "–");
  document.getElementById("llm-rt-models").textContent  = `${modCount} modelo${modCount !== 1 ? "s" : ""} instalado${modCount !== 1 ? "s" : ""}`;

  const pingMs = d.ping_ms || 0;
  const pingPct = Math.min((pingMs / 500) * 100, 100);
  document.getElementById("llm-ping-fill").style.width = pingPct + "%";
  document.getElementById("llm-ping-val").textContent  = `${pingMs} ms`;

  // ── Models list ──
  const list = document.getElementById("llm-models-list");
  const models = d.models || [];
  const runningNames = new Set((d.running_models || []).map(m => m.name));

  document.getElementById("llm-models-count").textContent = `${modCount} total`;

  if (models.length === 0) {
    list.innerHTML = `<div class="llm-no-model"><span>Nenhum modelo instalado</span><code style="font-size:11px;color:var(--text3)">ollama pull llama3</code></div>`;
    return;
  }

  let html = `<div class="llm-models-header">
    <span></span>
    <span>Model</span>
    <span style="text-align:right">Size</span>
    <span style="text-align:center">Quant</span>
    <span style="text-align:center">Params</span>
    <span style="text-align:right">Status</span>
  </div>`;

  models.forEach(m => {
    const isLoaded = runningNames.has(m.name);
    const sizeGB   = m.size ? (m.size / 1e9).toFixed(1) + " GB" : "–";
    const quant    = m.details?.quantization_level || "–";
    const params   = m.details?.parameter_size || "–";
    const family   = m.details?.family || "";
    const statusPill = isLoaded
      ? `<span class="llm-status-pill loaded">loaded</span>`
      : `<span class="llm-status-pill">idle</span>`;

    html += `<div class="llm-model-row">
      <div class="llm-model-row-icon">${familyIcon(family)}</div>
      <div>
        <div class="llm-model-row-name">${m.name}</div>
        <div class="llm-model-row-fam">${familyLabel(family)}</div>
      </div>
      <div class="llm-model-row-size">${sizeGB}</div>
      <div class="llm-model-row-quant">${quant}</div>
      <div class="llm-model-row-params">${params}</div>
      <div class="llm-model-row-status">${statusPill}</div>
    </div>`;
  });

  list.innerHTML = html;
}

// ── Family icons (inline SVG) ─────────────────────────────────────────────────

function familyLabel(family) {
  const labels = {
    llama: "Meta · LLaMA",
    mistral: "Mistral AI",
    gemma: "Google · Gemma",
    phi: "Microsoft · Phi",
    qwen: "Alibaba · Qwen",
    deepseek: "DeepSeek",
    "nomic-bert": "Nomic",
    mxbai: "MixedBread",
    starcoder: "BigCode · StarCoder",
    codellama: "Meta · Code Llama",
    wizard: "WizardLM",
  };
  return labels[family?.toLowerCase()] || (family ? family.charAt(0).toUpperCase() + family.slice(1) : "Unknown");
}

function familyIcon(family) {
  const f = (family || "").toLowerCase();

  // Llama / Meta
  if (f === "llama" || f === "codellama") return `<svg viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg">
    <path d="M12 3C9.5 3 7.5 4.5 7 7c-.6 2.8.4 5 2 6.5V18a1 1 0 0 0 2 0v-1h2v1a1 1 0 0 0 2 0v-4.5c1.6-1.5 2.6-3.7 2-6.5C16.5 4.5 14.5 3 12 3Z" stroke="var(--orange)" stroke-width="1.4" stroke-linejoin="round"/>
    <circle cx="10" cy="9" r="1" fill="var(--orange)"/>
    <circle cx="14" cy="9" r="1" fill="var(--orange)"/>
  </svg>`;

  // Mistral
  if (f === "mistral") return `<svg viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg">
    <path d="M4 7h16M4 12h16M4 17h10" stroke="var(--blue)" stroke-width="1.8" stroke-linecap="round"/>
    <path d="M18 15l3 2-3 2" stroke="var(--blue)" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
  </svg>`;

  // Gemma / Google
  if (f === "gemma") return `<svg viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg">
    <path d="M12 4l2.5 4.5h-5L12 4Z" fill="var(--blue)" opacity=".9"/>
    <path d="M12 4l4.5 2.5-2 4-2.5-4.5Z" fill="var(--green)" opacity=".8"/>
    <path d="M7.5 6.5L12 4l-2.5 4.5-2-4Z" fill="var(--yellow)" opacity=".8"/>
    <path d="M9.5 8.5h5l2 5H7.5l2-5Z" fill="var(--text2)" opacity=".3"/>
    <path d="M7.5 13.5h9l-2 4h-5l-2-4Z" fill="var(--text2)" opacity=".2"/>
  </svg>`;

  // Phi / Microsoft
  if (f === "phi") return `<svg viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg">
    <text x="4" y="18" font-size="16" font-family="serif" font-style="italic" fill="var(--blue)" font-weight="700">φ</text>
  </svg>`;

  // Qwen / DeepSeek
  if (f === "qwen" || f === "deepseek") return `<svg viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg">
    <circle cx="12" cy="12" r="6" stroke="var(--text2)" stroke-width="1.4"/>
    <path d="M9 12h6M12 9v6" stroke="var(--text2)" stroke-width="1.4" stroke-linecap="round"/>
  </svg>`;

  // Default
  return `<svg viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg">
    <rect x="4" y="4" width="16" height="16" rx="4" stroke="var(--text3)" stroke-width="1.4"/>
    <path d="M8 12h8M8 8.5h8M8 15.5h5" stroke="var(--text3)" stroke-width="1.3" stroke-linecap="round"/>
  </svg>`;
}
