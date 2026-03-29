"use strict";

let _portalUrl   = null;
let _hostnameUrl = null;
let _macIp       = null;
let _macWs       = null;
let _pingIntvl   = null;
let _memHist     = [];   // CPU % samples (max 60 = 2 min at 2s interval)
let _chartRaf    = null;
let _failCount   = 0;

const STEPS = ["cable", "ip", "handshake", "portal", "ready"];

// ── Helpers ───────────────────────────────────────────────────────────────────
function formatUptime(secs) {
  if (!secs) return "–";
  const d = Math.floor(secs / 86400);
  const h = Math.floor((secs % 86400) / 3600);
  const m = Math.floor((secs % 3600) / 60);
  if (d > 0) return `${d}d ${h}h ${m}m`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}

// ── Step rendering ────────────────────────────────────────────────────────────
function setStep(stepId, status, detail) {
  const item = document.getElementById(`step-${stepId}`);
  const icon = document.getElementById(`icon-${stepId}`);
  const det  = document.getElementById(`detail-${stepId}`);
  const line = document.getElementById(`line-${stepId}`);
  if (!item || !icon) return;

  icon.className = `step-icon ${status}`;

  if (status === "waiting") item.classList.add("active-step");
  else item.classList.remove("active-step");

  if (status === "done" || status === "error") item.classList.add("step-done");

  if (line) line.className = `step-line ${status === "done" ? "done" : ""}`;

  if (detail !== undefined && det) det.textContent = detail;
}

// ── Log footer ────────────────────────────────────────────────────────────────
const MAX_LOG_LINES = 4;

function appendLog(line) {
  const footer = document.getElementById("log-footer");
  const el = document.createElement("div");
  el.className = "log-footer-entry";
  el.textContent = line;
  footer.appendChild(el);
  while (footer.children.length > MAX_LOG_LINES) {
    footer.removeChild(footer.firstChild);
  }
}

// ── SSE ───────────────────────────────────────────────────────────────────────
function connectSSE() {
  const es = new EventSource("/events");
  es.onmessage = (e) => {
    const { step, status, detail } = JSON.parse(e.data);
    if (step === "log") { appendLog(detail); return; }
    setStep(step, status, detail);

    if (step === "cable" && status === "done") {
      document.querySelector(".hero-img-cable")?.classList.add("faded");
      document.getElementById("hero-cable-text")?.classList.add("faded");
    }

    if (step === "handshake" && status === "done") {
      // detail = "hostname — ip"
      const parts = detail.split(" — ");
      if (parts.length >= 2) {
        document.getElementById("device-name").textContent = parts[0];
        _macIp = parts[1];
      } else {
        const m = detail.match(/[\d.]+$/);
        if (m) _macIp = m[0];
      }
    }

    if (step === "portal" && status === "done") {
      document.getElementById("app-subtitle").textContent = detail;
    }

    if (step === "ready" && status === "done") {
      _portalUrl = detail;
      // Derive hostname URL from device name + port
      const hostname = document.getElementById("device-name").textContent.replace(/\.local$/, "");
      const portMatch = detail.match(/:(\d+)/);
      if (hostname && portMatch) {
        _hostnameUrl = `http://${hostname}.local:${portMatch[1]}/`;
      }
      showConnected(detail);
    }

    if (status === "waiting") {
      document.getElementById("app-subtitle").textContent = "Connecting…";
    }

    if (status === "error") {
      document.getElementById("app-subtitle").textContent = "Connection failed";
      showError();
    }
  };
}

// ── Views ─────────────────────────────────────────────────────────────────────
function showProgress() {
  if (_macWs) { clearInterval(_macWs); _macWs = null; }
  if (_pingIntvl) { clearInterval(_pingIntvl); _pingIntvl = null; }
  _memHist = [];
  _failCount = 0;
  document.getElementById("state-idle").classList.add("hidden");
  document.getElementById("state-progress").classList.remove("hidden");
  document.getElementById("state-connected").classList.add("hidden");
  document.getElementById("app-subtitle").textContent = "Waiting for cable…";
  STEPS.forEach(s => setStep(s, "pending", "–"));
  document.getElementById("detail-cable").textContent = "Waiting…";
  document.getElementById("device-name").textContent = "Mac Mini M4";
  const bar = document.getElementById("error-bar");
  if (bar) bar.remove();
  document.querySelector(".hero-img-cable")?.classList.remove("faded");
  document.getElementById("hero-cable-text")?.classList.remove("faded");
}

function showConnected(url) {
  setTimeout(() => {
    document.getElementById("state-progress").classList.add("hidden");
    document.getElementById("state-connected").classList.remove("hidden");
    document.getElementById("btn-disconnect").classList.remove("hidden");
    document.getElementById("btn-reconnect").classList.add("hidden");
    document.getElementById("app-subtitle").textContent = "Connected";
    document.getElementById("cc-hostname").textContent =
      document.getElementById("device-name").textContent;
    hideLostConnection();
    connectMacStats();
    startPingPolling();
  }, 500);
}

// ── Lost connection ───────────────────────────────────────────────────────────
function showLostConnection() {
  document.getElementById("cc-lost")?.classList.remove("hidden");
}

function hideLostConnection() {
  document.getElementById("cc-lost")?.classList.add("hidden");
}

// ── Disconnect / Reconnect ────────────────────────────────────────────────────
async function doDisconnect() {
  if (_macWs)    { clearInterval(_macWs);    _macWs    = null; }
  if (_pingIntvl){ clearInterval(_pingIntvl);_pingIntvl = null; }
  await fetch("/api/disconnect", { method: "POST" }).catch(() => {});
  _portalUrl   = null;
  _hostnameUrl = null;
  _macIp       = null;
  // Show Reconectar button instead of going back to cable-waiting
  document.getElementById("btn-disconnect").classList.add("hidden");
  document.getElementById("btn-reconnect").classList.remove("hidden");
  document.getElementById("cc-lost")?.classList.add("hidden");
  document.getElementById("app-subtitle").textContent = "Disconnected";
}

async function doReconnectSaved() {
  document.getElementById("btn-reconnect").classList.add("hidden");
  document.getElementById("btn-disconnect").classList.remove("hidden");
  startConnection();
}

function doReconnect() {
  startConnection();
}

// ── Ping polling ──────────────────────────────────────────────────────────────
function startPingPolling() {
  if (_pingIntvl) clearInterval(_pingIntvl);
  const poll = async () => {
    try {
      const d = await fetch("/api/ping").then(r => r.json());
      const el = document.getElementById("cc-ping");
      if (el) el.textContent = d.ms != null ? `${d.ms}ms` : "–";
    } catch(_) {}
  };
  poll();
  _pingIntvl = setInterval(poll, 5000);
}

// ── Mac Mini live stats (polling proxy) ───────────────────────────────────────
function connectMacStats() {
  if (_macWs) { clearInterval(_macWs); _macWs = null; }
  _failCount = 0;
  _macWs = setInterval(async () => {
    try {
      const d = await fetch("/api/mac-stats").then(r => r.json());
      if (d && d.cpu_percent != null) {
        _failCount = 0;
        hideLostConnection();
        updateConnectedCard(d);
      } else {
        _failCount++;
      }
    } catch(_) {
      _failCount++;
    }
    if (_failCount >= 3) showLostConnection();
  }, 2000);
  // fetch immediately on connect
  fetch("/api/mac-stats").then(r => r.json()).then(d => {
    if (d && d.cpu_percent != null) updateConnectedCard(d);
  }).catch(() => {});
}

function updateConnectedCard(d) {
  const cpu = d.cpu_percent;
  const mem = d.ram_percent;
  const tmp = d.power_w;

  document.getElementById("cc-cpu").textContent  = cpu != null ? `${Math.round(cpu)}%` : "–";
  document.getElementById("cc-mem").textContent  = mem != null ? `${Math.round(mem)}%` : "–";
  document.getElementById("cc-temp").textContent = tmp != null  ? `${tmp.toFixed(1)}W`  : "–";

  // Uptime
  const uptimeEl = document.getElementById("cc-uptime");
  if (uptimeEl) uptimeEl.textContent = formatUptime(d.uptime_secs);

  // Mac IP
  if (d.mac_ip) document.getElementById("cc-ip").textContent = d.mac_ip;

  // Chip info → hero badge
  if (d.chip_info) {
    const label = document.getElementById("hero-badge-label");
    if (label) label.textContent = `${d.chip_info} · P2P`;
  }

  // VNC button — disable if service is off
  const vncBtn = document.querySelector(".cc-btn-vnc");
  if (vncBtn && d.services) {
    const vncOn = d.services.vnc === true;
    vncBtn.disabled = !vncOn;
    vncBtn.title    = vncOn ? "Open VNC" : "VNC not running";
  }

  if (cpu != null) {
    _memHist.push(cpu);
    if (_memHist.length > 60) _memHist.shift();
  }
  scheduleCCChart();
}

function scheduleCCChart() {
  if (_chartRaf) return;
  _chartRaf = requestAnimationFrame(() => {
    _chartRaf = null;
    drawCCChart();
  });
}

function drawCCChart() {
  const canvas = document.getElementById("cc-chart");
  if (!canvas) return;
  const p = canvas.parentElement;
  const W = p.clientWidth;
  const H = p.clientHeight;
  if (!W || !H) return;
  if (canvas.width !== W || canvas.height !== H) {
    canvas.width  = W;
    canvas.height = H;
  }

  const ctx = canvas.getContext("2d");
  ctx.clearRect(0, 0, W, H);

  const data = _memHist;
  if (data.length < 2) return;

  // Adaptive color: green < 50%, yellow 50–80%, red > 80%
  const latest = data[data.length - 1];
  const color   = latest >= 80 ? "#ff453a" : latest >= 50 ? "#ffd60a" : "#30d158";
  const colorR  = latest >= 80 ? "255,69,58"  : latest >= 50 ? "255,214,10" : "48,209,88";

  const pad   = 4;
  const total = 60;
  const step  = W / (total - 1);

  const pts = data.map((v, i) => {
    const x = W - (data.length - 1 - i) * step;
    const y = H - pad - (v / 100) * (H - pad * 2);
    return [x, y];
  });

  // Gradient fill
  const grad = ctx.createLinearGradient(0, 0, 0, H);
  grad.addColorStop(0, `rgba(${colorR},0.18)`);
  grad.addColorStop(1, `rgba(${colorR},0)`);

  ctx.beginPath();
  ctx.moveTo(pts[0][0], pts[0][1]);
  for (let i = 1; i < pts.length; i++) ctx.lineTo(pts[i][0], pts[i][1]);
  ctx.lineTo(pts[pts.length - 1][0], H);
  ctx.lineTo(pts[0][0], H);
  ctx.closePath();
  ctx.fillStyle = grad;
  ctx.fill();

  // Line
  ctx.beginPath();
  ctx.moveTo(pts[0][0], pts[0][1]);
  for (let i = 1; i < pts.length; i++) ctx.lineTo(pts[i][0], pts[i][1]);
  ctx.strokeStyle = color;
  ctx.lineWidth   = 1.5;
  ctx.lineJoin    = "round";
  ctx.stroke();
}

function showError() {
  if (document.getElementById("error-bar")) return;
  const bar = document.createElement("div");
  bar.id = "error-bar";
  bar.className = "error-bar";
  bar.innerHTML = `
    <span>Check the cable and the server on Mac Mini.</span>
    <button onclick="retryConnection()">Retry</button>
  `;
  document.getElementById("state-progress").appendChild(bar);
}

function retryConnection() {
  startConnection();
}

// ── Actions ───────────────────────────────────────────────────────────────────
async function startConnection() {
  showProgress();
  setStep("cable", "waiting", "Waiting for Ethernet cable…");
  await fetch("/api/connect", { method: "POST" }).catch(() => {});
}

function doOpenPortal() {
  const url = _hostnameUrl || _portalUrl;
  if (url) window.open(url, "_blank");
}

function doOpenVnc() {
  if (_macIp) window.open(`vnc://${_macIp}`, "_blank");
}

function showPortal(url) {
  document.getElementById("portal-device-label").textContent =
    document.getElementById("device-name").textContent;
  document.getElementById("portal-frame").src = url;
  document.getElementById("state-portal").classList.remove("hidden");
}

// ── Settings ──────────────────────────────────────────────────────────────────
async function openSettings() {
  document.getElementById("settings-overlay").classList.remove("hidden");
  const cfg = await fetch("/api/config").then(r => r.json()).catch(() => ({}));
  document.getElementById("input-token").value = cfg.token || "";
  document.getElementById("input-subnet").value = cfg.default_subnet || "10.10.10";
  updateSubnetPreview();
}

function updateSubnetPreview() {
  const val = document.getElementById("input-subnet").value.trim();
  const box  = document.getElementById("subnet-preview");

  const ok = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.test(val) &&
    val.split(".").every(n => parseInt(n) <= 255);

  if (!val) { box.innerHTML = ""; return; }

  if (!ok) {
    box.className = "subnet-preview invalid";
    box.innerHTML = `
      <div class="subnet-preview-row">
        <span class="subnet-preview-label">Invalid prefix — use format x.x.x (e.g. 10.10.10)</span>
      </div>`;
    return;
  }

  box.className = "subnet-preview";
  box.innerHTML = `
    <div class="subnet-preview-row">
      <span class="subnet-preview-label">
        <img src="icon/Ionic-Ionicons-Logo-apple.16.png" width="13" height="13" alt=""/>
        Mac Mini
      </span>
      <span class="subnet-preview-ip">${val}.1</span>
      <span class="subnet-preview-badge">/30</span>
    </div>
    <div class="subnet-preview-row">
      <span class="subnet-preview-label">
        <svg width="13" height="13" viewBox="0 0 24 24" fill="currentColor" style="opacity:.7">
          <path d="M0 3.449L9.75 2.1v9.451H0m10.949-9.602L24 0v11.4H10.949M0 12.6h9.75v9.451L0 20.699M10.949 12.6H24V24l-12.9-1.801"/>
        </svg>
        Your PC
      </span>
      <span class="subnet-preview-ip">${val}.2</span>
      <span class="subnet-preview-badge">/30</span>
    </div>`;
}

function closeSettings() {
  document.getElementById("settings-overlay").classList.add("hidden");
}

async function saveSettings() {
  const token = document.getElementById("input-token").value.trim();
  const default_subnet = document.getElementById("input-subnet").value.trim() || "10.10.10";
  await fetch("/api/config", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ token, default_subnet, client_suffix: "2", handshake_port: 54321 }),
  });
  closeSettings();
}

document.getElementById("settings-overlay").addEventListener("click", function(e) {
  if (e.target === this) closeSettings();
});

// ── Init ──────────────────────────────────────────────────────────────────────
window.addEventListener("load", async () => {
  setTimeout(() => window.resizeTo(900, 640), 100);
  connectSSE();

  try {
    const s = await fetch("/api/status").then(r => r.json());
    if (s.connected) {
      _portalUrl   = s.portal_url;
      _hostnameUrl = s.hostname_url;
      _macIp       = s.mac_ip;
      document.getElementById("device-name").textContent = s.hostname || "Mac Mini M4";
      document.getElementById("cc-hostname").textContent = s.hostname || "Mac Mini M4";
      STEPS.forEach(id => setStep(id, "done", ""));
      setStep("handshake", "done", `${s.hostname} — ${s.mac_ip}`);
      document.getElementById("app-subtitle").textContent = "Connected";
      document.getElementById("state-progress").classList.add("hidden");
      document.getElementById("state-connected").classList.remove("hidden");
      document.getElementById("cable-hint")?.classList.add("hidden");
      connectMacStats();
      startPingPolling();
      return;
    }
  } catch (_) {}

  setTimeout(startConnection, 400);
});
