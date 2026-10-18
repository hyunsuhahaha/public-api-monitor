"use strict";
// pamon dashboard: /api/v1/overview for the fleet, /api/v1/series for one API (drawer),
// /api/v1/incidents for the timeline. State lives in the URL hash (#range=24h&t=id).

const C = { ok: "#73bf69", deg: "#fade2a", warn: "#ff9830", out: "#f2495c", blue: "#5794f2", purple: "#b877d9",
  none: "#2b2e36", grid: "#22252b", axis: "#8b8f98", panel: "#16171c" };
const LOC_COLORS = [C.blue, C.purple, "#ff7383", "#5ab1ef", "#8ab8ff", "#ffb357"];
const LABEL = { ok: "정상", slow: "느림", stale: "갱신 멈춤", error: "오류", down: "장애", client: "확인 불가", unknown: "측정 전" };
const RANGES = ["1h", "6h", "24h", "7d", "30d"];
const RANGE_LABEL = { "1h": "1시간", "6h": "6시간", "24h": "24시간", "7d": "7일", "30d": "30일" };

const $ = (id) => document.getElementById(id);
const el = (tag, cls, text) => { const e = document.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; };
const pct = (v, d = 3) => (v == null || v < 0 ? "–" : v >= 1 ? "100%" : (v * 100).toFixed(d) + "%");
const ms = (v) => (v ? (v >= 1000 ? (v / 1000).toFixed(2) + "s" : v + "ms") : "–");
// error budget left: overspent shows how far ("−22%")
const budgetText = (v) => (v == null ? "–" : v < 0 ? (v < -9 ? "소진" : "−" + Math.round(-v * 100) + "%") : Math.round(v * 100) + "%");
const sev = (s) => (s === "down" || s === "error" ? 2 : s === "slow" || s === "stale" ? 1 : s === "ok" ? 0 : -1);
const kst = (d, o) => new Date(d).toLocaleString("ko-KR", Object.assign({ timeZone: "Asia/Seoul", hour12: false }, o));
const dpr = () => window.devicePixelRatio || 1;

const state = { range: "24h", target: null, cat: "전체", q: "", sort: null, data: null, incidents: [], schema: [], loadedAt: 0 };

function readHash() {
  const h = new URLSearchParams(location.hash.slice(1));
  if (RANGES.includes(h.get("range"))) state.range = h.get("range");
  state.target = h.get("t") || null;
}
function writeHash() {
  const h = new URLSearchParams();
  h.set("range", state.range);
  if (state.target) h.set("t", state.target);
  history.replaceState(null, "", "#" + h.toString());
}

// ── canvas helpers ────────────────────────────────────────────────────────────
function sizeCanvas(cv, w, h) {
  cv.width = Math.round(w * dpr()); cv.height = Math.round(h * dpr());
  cv.style.width = w + "px"; cv.style.height = h + "px";
  const g = cv.getContext("2d"); g.setTransform(dpr(), 0, 0, dpr(), 0, 0); return g;
}
const stateColor = (s) => (s === 2 ? C.out : s === 1 ? C.deg : s === 0 ? C.ok : C.none);

function spark(values, color) {
  const cv = el("canvas", "spark"); const W = 120, H = 24; const g = sizeCanvas(cv, W, H);
  const max = Math.max(1, ...values.filter((x) => x != null));
  const step = W / Math.max(1, values.length - 1);
  g.strokeStyle = color; g.lineWidth = 1.2;
  let started = false; g.beginPath();
  values.forEach((x, i) => { if (x == null) return; const X = i * step, Y = H - 2 - (x / max) * (H - 4); if (started) g.lineTo(X, Y); else { g.moveTo(X, Y); started = true; } });
  g.stroke();
  return cv;
}

function strip(states, w = 180, h = 14) {
  const cv = el("canvas", "strip"); const g = sizeCanvas(cv, w, h);
  const bw = w / states.length;
  states.forEach((s, i) => { g.fillStyle = stateColor(s); g.fillRect(i * bw + 0.5, 0, Math.max(1, bw - 1), h); });
  return cv;
}

// ── stat panels ───────────────────────────────────────────────────────────────
function statPanel(k, v, sub, cls, barPct, barColor) {
  const p = el("div", "stat");
  p.append(el("div", "k", k), el("div", "v " + (cls || ""), v));
  if (sub) p.append(el("div", "s", sub));
  if (barPct != null) { const b = el("div", "bar"); b.style.width = Math.max(0, Math.min(100, barPct)) + "%"; b.style.background = barColor || C.ok; p.append(b); }
  return p;
}

function renderStats(d) {
  const f = d.fleet, c = d.counts, box = $("stats"); box.replaceChildren();
  const avCls = f.availability < 0 ? "" : f.availability >= f.slo ? "c-ok-t" : f.availability >= f.slo - 0.01 ? "c-deg-t" : "c-out-t";
  const budget = f.budgetLeft;
  const bCls = budget >= 0.5 ? "c-ok-t" : budget >= 0 ? "c-deg-t" : "c-out-t";
  box.append(
    statPanel("가용률 · " + RANGE_LABEL[d.range], pct(f.availability), "목표(SLO) " + pct(f.slo, 1), avCls),
    statPanel("에러 버짓 잔여", f.availability < 0 ? "–" : budgetText(budget), budget < 0 ? "목표를 넘겨 실패함 (버짓 초과)" : "목표 대비 남은 실패 허용치", bCls, Math.max(0, budget) * 100, budget >= 0.5 ? C.ok : budget >= 0 ? C.deg : C.out),
    statPanel("장애", String(c.down || 0), "진행 중 장애 " + f.openIncidents + "건", c.down ? "c-out-t" : "c-ok-t"),
    statPanel("성능 저하", String(c.degraded || 0), "느림·갱신 멈춤", c.degraded ? "c-deg-t" : "c-ok-t"),
    statPanel("응답 시간 p95", ms(f.p95), "p50 " + ms(f.p50) + " · p99 " + ms(f.p99), "c-blue-t"),
    statPanel("확인 / 분", f.checksPerMin.toFixed(1), `정상 ${c.up || 0} · 확인 불가 ${c.unknown || 0} / ${d.targets.length}개 API`, ""),
  );
  const pill = $("fleet-pill");
  pill.className = "pill " + (c.down ? "out" : c.degraded ? "deg" : "ok");
  pill.textContent = c.down ? `● ${c.down}개 장애` : c.degraded ? `● ${c.degraded}개 저하` : "● 모두 정상";
}

// ── service table ─────────────────────────────────────────────────────────────
function budgetCell(t) {
  const w = el("div", "budget"); const tr = el("div", "track"); const f = el("div", "fill");
  const v = t.availability < 0 ? null : t.budgetLeft;
  f.style.width = (v == null ? 0 : Math.max(0, Math.min(1, v)) * 100) + "%";
  f.style.background = v == null ? C.none : v >= 0.5 ? C.ok : v >= 0 ? C.deg : C.out;
  tr.append(f); w.append(tr, el("span", "n", budgetText(v)));
  w.title = `SLO ${pct(t.slo, 1)} 기준 남은 에러 버짓`;
  return w;
}

// ── correlated failures ───────────────────────────────────────────────────────
// Several APIs of one agency failing together is one agency-side problem, not N separate
// ones; many agencies at once points at the shared data.go.kr gateway (or our network).
function correlate(targets) {
  const by = {};
  for (const t of targets) if (sev(t.status) >= 0) (by[t.provider] = by[t.provider] || []).push(t);
  const groups = [];
  for (const [provider, ts] of Object.entries(by)) {
    const bad = ts.filter((t) => sev(t.status) >= 1);
    if (bad.length >= 2 && bad.length * 2 >= ts.length) groups.push({ provider, bad, total: ts.length });
  }
  groups.sort((a, b) => b.bad.length - a.bad.length);
  const badProviders = Object.values(by).filter((ts) => ts.some((t) => sev(t.status) >= 1)).length;
  return { groups, wide: badProviders >= 4 && badProviders * 2 >= Object.keys(by).length };
}

function renderCorrel() {
  const { groups, wide } = correlate(state.data.targets), box = $("correl");
  box.replaceChildren(); box.hidden = !groups.length && !wide;
  const openBy = {};
  for (const i of state.incidents) if (isOpen(i)) openBy[i.target] = i;
  if (wide) box.append(el("div", "cg wide-out", "⚠ 여러 기관 API가 동시에 이상 — 공공데이터포털 게이트웨이 또는 이 모니터 쪽 네트워크 문제일 가능성"));
  for (const g of groups) {
    const r = el("div", "cg"), worst = Math.max(...g.bad.map((t) => sev(t.status)));
    r.append(el("span", "dot " + (worst >= 2 ? "down" : "slow")),
      el("b", "", g.provider), el("span", "cg-n", ` API ${g.bad.length}/${g.total}개 동시 이상`),
      el("span", "cg-why", "개별 API가 아니라 기관 쪽 문제일 가능성 — 하나의 원인으로 보세요"));
    const chips = el("span", "cg-chips");
    for (const t of g.bad) {
      const c = el("button", "chip " + (sev(t.status) >= 2 ? "out" : "deg"), t.name + (openBy[t.id] ? ` #${openBy[t.id].id}` : ""));
      c.title = `${LABEL[t.status]} — ${t.reason || ""}`; c.onclick = () => openDrawer(t.id); chips.append(c);
    }
    const only = el("button", "chip", "이 기관만 보기");
    only.onclick = () => { $("q").value = state.q = g.provider; renderTable(); renderHeat(); };
    r.append(chips, only); box.append(r);
  }
}

function visibleRows() {
  const rows = state.data.targets.filter((t) =>
    (state.cat === "전체" || t.category === state.cat) &&
    (!state.q || (t.name + t.provider + t.id).toLowerCase().includes(state.q.toLowerCase())));
  const key = state.sort;
  const av = (x) => (x < 0 ? 9 : x);
  return rows.slice().sort((a, b) => {
    if (key === "p50" || key === "p95") return (b[key] || 0) - (a[key] || 0);
    if (key === "availability" || key === "budgetLeft") return av(a[key]) - av(b[key]);
    // default: worst first, then lowest availability
    return sev(b.status) - sev(a.status) || av(a.availability) - av(b.availability) || a.name.localeCompare(b.name);
  });
}

function renderTable() {
  const body = $("rows"); body.replaceChildren();
  for (const t of visibleRows()) {
    const tr = el("tr"); tr.dataset.id = t.id; tr.onclick = () => openDrawer(t.id);
    const td = (child, cls) => { const c = el("td", cls); if (child instanceof Node) c.append(child); else if (child != null) c.textContent = child; tr.append(c); return c; };
    td(el("span", "dot " + t.status)).title = LABEL[t.status] || t.status;
    const nm = el("div"); const prov = el("div", "prov", t.provider);
    if (t.reason) { prov.append(" · "); const r = el("span", "reason", t.reason); r.title = t.reason; prov.append(r); }
    nm.append(el("div", "name", t.name), prov);
    td(nm);
    const av = td(pct(t.availability), "num");
    av.style.color = t.availability < 0 ? C.axis : t.availability >= t.slo ? C.ok : t.availability >= t.slo - 0.01 ? C.deg : C.out;
    td(budgetCell(t));
    td(ms(t.p50), "num"); td(ms(t.p95), "num");
    td(spark(t.spark, t.status === "ok" ? C.blue : stateColor(sev(t.status))));
    td(strip(t.states));
    const lc = el("div", "locs");
    for (const l of t.locations) { const d = el("span", "dot " + l.status); d.title = `${l.location}: ${LABEL[l.status] || l.status} ${ms(l.latencyMs)}`; lc.append(d); }
    td(lc);
    body.append(tr);
  }
  if (!body.children.length) { const tr = el("tr"); const c = el("td", "empty", "조건에 맞는 API가 없습니다"); c.colSpan = 9; tr.append(c); body.append(tr); }
  document.querySelectorAll(".svc th[data-sort]").forEach((th) => th.classList.toggle("sorted", th.dataset.sort === state.sort));
}

function renderCats() {
  const cats = ["전체", ...new Set(state.data.targets.map((t) => t.category).filter(Boolean))];
  const box = $("cats"); box.replaceChildren();
  for (const c of cats) {
    const b = el("button", c === state.cat ? "on" : "", c);
    b.onclick = () => { state.cat = c; renderCats(); renderTable(); renderHeat(); };
    box.append(b);
  }
}

// ── heatmap ───────────────────────────────────────────────────────────────────
function renderHeat() {
  const cv = $("heat"), rows = visibleRows(), n = (state.data.targets[0] && state.data.targets[0].states.length) || 1;
  const labelW = 150, rowH = 14, W = Math.max(300, cv.parentElement.clientWidth - 24), H = rows.length * rowH + 18;
  const g = sizeCanvas(cv, W, H);
  const cw = (W - labelW) / n;
  g.font = "11px Inter, 'Malgun Gothic', sans-serif"; g.textBaseline = "middle";
  rows.forEach((t, r) => {
    g.fillStyle = C.axis; g.fillText(t.name.length > 18 ? t.name.slice(0, 17) + "…" : t.name, 0, r * rowH + rowH / 2);
    t.states.forEach((s, i) => { g.fillStyle = stateColor(s); g.fillRect(labelW + i * cw + 0.5, r * rowH + 1, Math.max(1, cw - 1), rowH - 2); });
  });
  g.fillStyle = "#5d616b"; g.textBaseline = "top";
  const from = state.data.from * 1000, to = state.data.to * 1000;
  const long = state.range === "7d" || state.range === "30d";
  for (let k = 0; k <= 4; k++) {
    const t = from + (to - from) * k / 4, x = labelW + (W - labelW) * k / 4;
    g.textAlign = k === 0 ? "left" : k === 4 ? "right" : "center";
    g.fillText(kst(t, long ? { month: "numeric", day: "numeric" } : { hour: "2-digit", minute: "2-digit" }), x, H - 14);
  }
  g.textAlign = "left";
  $("heat-hint").textContent = `${RANGE_LABEL[state.range]} · 칸당 ${Math.round(state.data.stepSec / 60)}분`;
  cv.onmousemove = (e) => {
    const r = Math.floor(e.offsetY / rowH), i = Math.floor((e.offsetX - labelW) / cw);
    const t = rows[r]; if (!t || i < 0 || i >= n) { cv.title = ""; return; }
    const at = from + i * state.data.stepSec * 1000;
    cv.title = `${t.name} · ${kst(at, { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" })} · ${["기록 없음", "정상", "저하", "장애"][t.states[i] + 1]}`;
  };
  cv.onclick = (e) => { const t = rows[Math.floor(e.offsetY / rowH)]; if (t) openDrawer(t.id); };
}

// ── incidents ─────────────────────────────────────────────────────────────────
const isOpen = (i) => !i.ended || i.ended.startsWith("0001");
const short = (d) => kst(d, { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" });
const dur = (a, b) => { const m = Math.round((Date.parse(b) - Date.parse(a)) / 60000); return m < 60 ? m + "분" : Math.floor(m / 60) + "시간 " + (m % 60) + "분"; };

function incRow(i, title) {
  const r = el("div", "inc"); r.append(el("span", "dot " + (isOpen(i) ? "down" : "ok")));
  const m = el("div"), h = el("div", "", title);
  if (i.flaps) { const f = el("span", "flap", "불안정 ×" + i.flaps); f.title = `복구된 것처럼 보였다가 다시 실패 ${i.flaps}회 — 한 장애로 묶음`; h.append(" ", f); }
  m.append(h, el("div", "t", short(i.started) + (isOpen(i) ? " ~ 진행 중" : " ~ " + kst(i.ended, { hour: "2-digit", minute: "2-digit" }))));
  r.append(m, el("span", "t", isOpen(i) ? "진행 중" : dur(i.started, i.ended)));
  return r;
}

function renderIncidents() {
  const from = state.data.from * 1000, to = state.data.to * 1000;
  const names = Object.fromEntries(state.data.targets.map((t) => [t.id, t.name]));
  const inRange = state.incidents.filter((i) => (isOpen(i) ? to : Date.parse(i.ended)) >= from);
  $("inc-count").textContent = `${RANGE_LABEL[state.range]} 동안 ${inRange.length}건`;
  const gantt = $("gantt"); gantt.replaceChildren();
  const byT = {};
  inRange.forEach((i) => (byT[i.target] = byT[i.target] || []).push(i));
  const ids = Object.keys(byT).slice(0, 12);
  if (!ids.length) gantt.append(el("div", "empty", "이 기간에는 장애가 없습니다"));
  for (const id of ids) {
    const row = el("div", "g-row"); const nm = el("div", "g-name", names[id] || id); nm.title = names[id] || id; row.append(nm);
    const track = el("div", "g-track");
    for (const i of byT[id]) {
      const s = Math.max(from, Date.parse(i.started)), e = isOpen(i) ? to : Math.min(to, Date.parse(i.ended));
      const b = el("div", "g-bar");
      b.style.left = ((s - from) / (to - from) * 100) + "%"; b.style.width = Math.max(0.3, (e - s) / (to - from) * 100) + "%";
      b.style.background = i.severity >= 2 ? C.out : C.deg;
      if (i.flaps) b.classList.add("flapping");
      b.title = `${names[id] || id}: ${i.reason}${i.flaps ? ` · 불안정 ×${i.flaps}` : ""} (${short(i.started)}${isOpen(i) ? " ~ 진행 중" : ""}) — 눌러서 상세`;
      b.onclick = (ev) => { ev.stopPropagation(); openIncident(i.id); };
      track.append(b);
    }
    row.append(track); row.style.cursor = "pointer"; row.onclick = () => openDrawer(id); gantt.append(row);
  }
  if (ids.length) {
    const ax = el("div", "g-axis"); ax.append(el("div"));
    const t = el("div"); [0, 1, 2].forEach((k) => t.append(el("span", "", short(from + (to - from) * k / 2))));
    ax.append(t); gantt.append(ax);
  }
  const list = $("inc-list"); list.replaceChildren();
  for (const i of inRange.slice(0, 8)) {
    const r = incRow(i, (names[i.target] || i.target) + " — " + i.reason);
    r.style.cursor = "pointer"; r.title = "장애 상세 보기"; r.onclick = () => openIncident(i.id); list.append(r);
  }
}

// ── schema changes ────────────────────────────────────────────────────────────
function schemaRow(e, withName) {
  const names = Object.fromEntries(state.data.targets.map((t) => [t.id, t.name]));
  const r = el("div", "schema");
  r.append(el("span", "kind " + e.kind, e.kind === "added" ? "필드 추가" : "필드 사라짐"));
  const body = el("div");
  if (withName) { const n = el("div", "", names[e.target] || e.target); n.style.marginBottom = "3px"; body.append(n); }
  for (const p of e.paths.slice(0, 12)) body.append(el("code", "", p));
  if (e.paths.length > 12) body.append(el("span", "t", `외 ${e.paths.length - 12}개`));
  r.append(body, el("span", "t", short(e.at)));
  if (withName) { r.style.cursor = "pointer"; r.onclick = () => openDrawer(e.target); }
  return r;
}

function renderSchema() {
  const box = $("schema-list"); box.replaceChildren();
  if (!state.schema.length) { box.append(el("div", "empty", "최근 90일 동안 응답 형식이 바뀐 API가 없습니다 (처음 응답이 기준)")); return; }
  for (const e of state.schema.slice(0, 15)) box.append(schemaRow(e, true));
}

// ── drawer ────────────────────────────────────────────────────────────────────
let plots = [];
function closeDrawer() { $("drawer").hidden = true; $("scrim").hidden = true; state.target = null; writeHash(); plots.forEach((p) => p.destroy()); plots = []; }

function axes() {
  const base = { stroke: C.axis, grid: { stroke: C.grid, width: 1 }, ticks: { stroke: C.grid, width: 1 }, font: "11px Consolas, monospace" };
  return [Object.assign({}, base), Object.assign({}, base, { size: 56, values: (u, v) => v.map((x) => (x == null ? "" : x >= 1000 ? (x / 1000).toFixed(1) + "s" : x + "ms")) })];
}

// shades the chart where verdicts were degraded/outage, and draws the state band under it
function statesPlugin(states, stepSec) {
  return { hooks: { drawClear: [(u) => {
    const g = u.ctx, top = u.bbox.top, height = u.bbox.height, xs = u.data[0];
    states.forEach((s, i) => {
      const x0 = u.valToPos(xs[i], "x", true), x1 = u.valToPos(xs[i] + stepSec, "x", true);
      if (s >= 1) { g.fillStyle = s === 2 ? "rgba(242,73,92,.13)" : "rgba(250,222,42,.07)"; g.fillRect(x0, top, Math.max(1, x1 - x0), height); }
      g.fillStyle = stateColor(s); g.fillRect(x0, top + height + 2 * dpr(), Math.max(1, x1 - x0 - dpr()), 3 * dpr());
    });
  }] } };
}

// vertical dashed lines at the given unix times (schema changes)
function markersPlugin(times) {
  return { hooks: { draw: [(u) => {
    const g = u.ctx; g.save(); g.strokeStyle = C.blue; g.setLineDash([4 * dpr(), 3 * dpr()]); g.lineWidth = dpr();
    for (const t of times) {
      const x = u.valToPos(t, "x", true);
      if (x < u.bbox.left || x > u.bbox.left + u.bbox.width) continue;
      g.beginPath(); g.moveTo(x, u.bbox.top); g.lineTo(x, u.bbox.top + u.bbox.height); g.stroke();
    }
    g.restore();
  }] } };
}

async function openDrawer(id) {
  state.target = id; writeHash();
  const t = state.data.targets.find((x) => x.id === id); if (!t) return;
  $("drawer").hidden = false; $("scrim").hidden = false;
  $("d-title").replaceChildren(el("span", "dot " + t.status), document.createTextNode(t.name));
  $("d-sub").textContent = `${t.provider} · ${t.category || ""} · ${LABEL[t.status] || t.status}${t.reason ? " — " + t.reason : ""}`;
  $("d-stats").replaceChildren(
    statPanel("가용률 · " + RANGE_LABEL[state.range], pct(t.availability), "SLO " + pct(t.slo, 1), t.availability < 0 ? "" : t.availability >= t.slo ? "c-ok-t" : "c-out-t"),
    statPanel("에러 버짓 잔여", t.availability < 0 ? "–" : budgetText(t.budgetLeft), "", t.budgetLeft >= 0.5 ? "c-ok-t" : t.budgetLeft >= 0 ? "c-deg-t" : "c-out-t"),
    statPanel("p50", ms(t.p50), "", "c-blue-t"), statPanel("p95", ms(t.p95), "", "c-blue-t"),
    statPanel("판정 수", String(t.checks), "지역 " + t.locations.length + "곳", ""));
  const s = await fetch(`/api/v1/series?target=${encodeURIComponent(id)}&range=${state.range}`).then((r) => r.json());
  if (state.target !== id) return;
  plots.forEach((p) => p.destroy()); plots = [];
  $("d-lat").replaceChildren(); $("d-loc").replaceChildren();
  const W = Math.min(880, $("drawer").clientWidth - 54);
  const tz = (ts) => uPlot.tzDate(new Date(ts * 1000), "Asia/Seoul");
  plots.push(new uPlot({
    width: W, height: 220, axes: axes(), scales: { x: { time: true } }, tzDate: tz, padding: [8, 8, 10, 0],
    series: [{}, { label: "p50", stroke: C.ok, width: 1.5 }, { label: "p95", stroke: C.blue, width: 1.5, fill: "rgba(87,148,242,.08)" },
      { label: "max", stroke: "rgba(255,152,48,.7)", width: 1, dash: [4, 3] }],
    plugins: [statesPlugin(s.states, s.stepSec), markersPlugin(state.schema.filter((e) => e.target === id).map((e) => Date.parse(e.at) / 1000))],
  }, [s.t, s.p50, s.p95, s.max], $("d-lat")));
  // error budget burn-down: what is left of the range's allowance (1 - SLO) x checks,
  // bucket by bucket; below zero the API is over its budget for this range
  const total = s.counted.reduce((a, b) => a + b, 0), allowed = (1 - s.slo) * total;
  let spent = 0;
  const remaining = s.counted.map((c, i) => { spent += s.bad[i]; return c || spent ? Math.round((allowed ? 1 - spent / allowed : 1) * 1000) / 10 : null; });
  $("d-budget").replaceChildren();
  $("d-budget-hint").textContent = `SLO ${pct(s.slo, 1)} · 허용 실패 ${allowed.toFixed(1)}회 중 ${spent}회 사용 (${RANGE_LABEL[state.range]})${s.estimated ? " · 응답 시간은 히스토그램 추정치" : ""}`;
  plots.push(new uPlot({
    width: W, height: 140, scales: { x: { time: true }, y: { range: (u, mn, mx) => [Math.min(-10, mn), 100] } }, tzDate: tz, padding: [8, 8, 0, 0],
    axes: [axes()[0], Object.assign({}, axes()[1], { values: (u, v) => v.map((x) => x + "%") })],
    series: [{}, { label: "버짓 남음", stroke: remaining.filter((x) => x != null).slice(-1)[0] < 0 ? C.out : C.ok, width: 2, fill: "rgba(115,191,105,.07)", spanGaps: true }],
    plugins: [{ hooks: { drawClear: [(u) => { const y = u.valToPos(0, "y", true); u.ctx.fillStyle = "rgba(242,73,92,.08)"; u.ctx.fillRect(u.bbox.left, y, u.bbox.width, u.bbox.top + u.bbox.height - y); }] } }],
  }, [s.t, remaining], $("d-budget")));
  const locNames = Object.keys(s.byLocation).sort();
  if (locNames.length) {
    plots.push(new uPlot({
      width: W, height: 170, axes: axes(), scales: { x: { time: true } }, tzDate: tz, padding: [8, 8, 0, 0],
      series: [{}, ...locNames.map((n, i) => ({ label: n, stroke: LOC_COLORS[i % LOC_COLORS.length], width: 1.5 }))],
    }, [s.t, ...locNames.map((n) => s.byLocation[n])], $("d-loc")));
  } else $("d-loc").append(el("div", "empty", "기록이 없습니다"));
  // per-location state strips (gray = our side: key or quota)
  const cv = $("d-strips"), names = Object.keys(s.locationStates).sort(), rowH = 16, labelW = 110;
  const g = sizeCanvas(cv, W, Math.max(1, names.length) * rowH + 4);
  g.font = "11px Consolas, monospace"; g.textBaseline = "middle";
  names.forEach((n, r) => {
    g.fillStyle = C.axis; g.fillText(n, 0, r * rowH + rowH / 2);
    const arr = s.locationStates[n], bw = (W - labelW) / arr.length;
    arr.forEach((v, i) => { g.fillStyle = v === -2 ? "#4a4e57" : stateColor(v); g.fillRect(labelW + i * bw + 0.5, r * rowH + 2, Math.max(1, bw - 1), rowH - 4); });
  });
  const di = $("d-inc"); di.replaceChildren();
  const inc = state.incidents.filter((i) => i.target === id);
  if (!inc.length) di.append(el("div", "empty", "최근 90일 장애가 없습니다"));
  for (const i of inc.slice(0, 10)) {
    const r = incRow(i, i.reason + (i.lastReason && i.lastReason !== i.reason ? " · 마지막: " + i.lastReason : ""));
    r.style.cursor = "pointer"; r.title = "장애 상세 보기"; r.onclick = () => openIncident(i.id); di.append(r);
  }
  const dsch = $("d-schema"); dsch.replaceChildren();
  const sch = state.schema.filter((e) => e.target === id);
  if (!sch.length) dsch.append(el("div", "empty", "응답 필드 변경 기록이 없습니다"));
  for (const e of sch.slice(0, 10)) dsch.append(schemaRow(e, false));
  const log = $("d-log"); log.replaceChildren();
  const hr = el("tr"); ["시각", "지역", "판정", "응답", "HTTP", "코드", "사유"].forEach((h) => hr.append(el("th", "", h))); log.append(hr);
  for (const c of s.recent) {
    const tr = el("tr");
    const st = el("td"); st.append(el("span", "dot " + c.status), document.createTextNode(" " + (LABEL[c.status] || c.status)));
    const rs = el("td", "r", c.reason || ""); rs.title = c.reason || "";
    tr.append(el("td", "mono", kst(c.at, { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit" })), el("td", "", c.location), st,
      el("td", "num", ms(c.latencyMs)), el("td", "num", c.http || "–"), el("td", "mono", c.code || ""), rs);
    log.append(tr);
  }
}

// ── load ──────────────────────────────────────────────────────────────────────
async function load() {
  const [d, inc, sch] = await Promise.all([
    fetch("/api/v1/overview?range=" + state.range).then((r) => r.json()),
    fetch("/api/v1/incidents").then((r) => r.json()),
    fetch("/api/v1/schema-events").then((r) => r.json()),
  ]);
  state.data = d; state.incidents = inc; state.schema = sch; state.loadedAt = Date.now();
  renderStats(d); renderCorrel(); renderCats(); renderTable(); renderHeat(); renderIncidents(); renderSchema();
}

function renderRanges() {
  const box = $("ranges"); box.replaceChildren();
  for (const r of RANGES) {
    const b = el("button", r === state.range ? "on" : "", r);
    b.onclick = async () => { state.range = r; writeHash(); renderRanges(); await load(); if (state.target) openDrawer(state.target); };
    box.append(b);
  }
}

readHash(); renderRanges();
$("q").oninput = (e) => { state.q = e.target.value; renderTable(); renderHeat(); };
document.querySelectorAll(".svc th[data-sort]").forEach((th) => { th.onclick = () => { state.sort = state.sort === th.dataset.sort ? null : th.dataset.sort; renderTable(); }; });
$("d-close").onclick = closeDrawer; $("scrim").onclick = closeDrawer;
$("refresh").onclick = () => load();
window.addEventListener("resize", () => { if (state.data) renderHeat(); });
setInterval(() => { if (state.loadedAt) $("age").textContent = Math.round((Date.now() - state.loadedAt) / 1000) + "초 전"; }, 1000);
setInterval(() => { if (document.visibilityState === "visible") load(); }, 30000);
load().then(() => { if (state.target) openDrawer(state.target); }).catch((e) => { $("fleet-pill").textContent = "불러오기 실패"; console.error(e); });
