"use strict";

const LABEL = { ok: "정상", slow: "느림", stale: "갱신 멈춤", error: "오류", down: "장애", client: "확인 불가", unknown: "측정 전" };
const DAYS = 90;
let state = null;
let filter = "전체";
let scrolled = false;

const el = (tag, cls, text) => {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
};
const pct = (v) => (v < 0 ? "–" : (v * 100).toFixed(v === 1 ? 0 : 2) + "%");
const ago = (iso) => {
  const s = Math.max(0, (Date.now() - new Date(iso)) / 1000);
  if (s < 60) return Math.round(s) + "초 전";
  if (s < 3600) return Math.round(s / 60) + "분 전";
  return Math.round(s / 3600) + "시간 전";
};
const kstDate = (d) => new Date(d.getTime() + 9 * 3600e3).toISOString().slice(0, 10);
const fmt = (iso) => new Date(iso).toLocaleString("ko-KR", { timeZone: "Asia/Seoul", month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" });
const dur = (a, b) => {
  const m = Math.round((new Date(b) - new Date(a)) / 60000);
  return m < 60 ? m + "분" : Math.floor(m / 60) + "시간 " + (m % 60) + "분";
};

function dayClass(d) {
  if (!d || d.counted === 0) return "";
  const up = d.healthy / d.counted;
  if (d.outage > 0 && up < 0.99) return "s-down";
  if (d.outage > 0 || d.degraded > d.counted * 0.1) return "s-slow";
  return "s-ok";
}

function renderTargets() {
  const box = document.getElementById("targets");
  box.replaceChildren();
  const today = new Date();
  for (const t of state.targets) {
    if (filter !== "전체" && t.category !== filter) continue;
    const card = el("div", "card");
    card.id = t.id;
    const row = el("div", "row");
    row.append(el("span", "dot s-" + t.status));
    const title = el("div");
    title.append(el("div", "name", t.name), el("div", "prov", (t.provider || "") + " · " + (LABEL[t.status] || t.status)));
    row.append(title);
    const right = el("div", "right");
    right.append("30일 ", el("b", "", pct(t.uptime30d)), " · p95 ", el("b", "", t.p95Ms ? t.p95Ms + "ms" : "–"));
    right.append(el("br"), t.checkedAt ? ago(t.checkedAt) + " 확인" : "");
    row.append(right);
    card.append(row);
    if (t.status === "client") card.append(el("div", "reason", "모니터 쪽 서비스 키·호출 한도 문제로 확인하지 못했습니다 (" + t.reason + ")"));
    else if (t.status !== "ok" && t.reason) card.append(el("div", "reason", t.reason));

    const byDate = Object.fromEntries((t.days || []).map((d) => [d.date, d]));
    const bars = el("div", "bars");
    for (let i = DAYS - 1; i >= 0; i--) {
      const date = kstDate(new Date(today.getTime() - i * 864e5));
      const d = byDate[date];
      const b = el("span", dayClass(d));
      b.title = date + (d && d.counted ? ` · 가용률 ${pct(d.healthy / d.counted)} · 장애 ${d.outage}회 · 저하 ${d.degraded}회` : " · 기록 없음");
      bars.append(b);
    }
    card.append(bars);
    const legend = el("div", "legend");
    legend.append(el("span", "", DAYS + "일 전"), el("span", "", "90일 " + pct(t.uptime90d)), el("span", "", "오늘"));
    card.append(legend);
    box.append(card);
  }
}

function renderFilters() {
  const cats = ["전체", ...new Set(state.targets.map((t) => t.category).filter(Boolean))];
  const box = document.getElementById("filters");
  box.replaceChildren();
  for (const c of cats) {
    const b = el("button", c === filter ? "on" : "", c);
    b.onclick = () => { filter = c; renderFilters(); renderTargets(); };
    box.append(b);
  }
}

// "5m0s" -> "5분"
function interval(go) {
  const m = /(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?/.exec(go) || [];
  const h = +(m[1] || 0), min = +(m[2] || 0), sec = +(m[3] || 0);
  return [h && h + "시간", min && min + "분", sec && sec + "초"].filter(Boolean).join(" ") || go;
}

function renderBanner() {
  const s = state.summary;
  const bad = (s.down || 0) + (s.error || 0);
  const warn = (s.slow || 0) + (s.stale || 0);
  const unknown = (s.client || 0) + (s.unknown || 0);
  const ok = s.ok || 0;
  const b = document.getElementById("banner");
  b.className = "banner " + (bad ? "bad" : warn ? "warn" : ok ? "ok" : "");
  let text = bad ? `${bad}개 API 장애 중` : warn ? `${warn}개 API 성능 저하` : ok ? `확인된 ${ok}개 API 모두 정상` : "아직 확인된 API가 없습니다";
  if (unknown) text += ` · ${unknown}개 확인 불가`;
  b.textContent = text;
  document.getElementById("meta").textContent =
    `${state.targets.length}개 API · ${interval(state.interval)}마다 확인 · ${ago(state.generatedAt)} 갱신`;
}

async function loadIncidents() {
  const r = await fetch("/api/v1/incidents");
  const list = await r.json();
  const names = Object.fromEntries(state.targets.map((t) => [t.id, t.name]));
  const box = document.getElementById("incidents");
  box.replaceChildren();
  if (!list.length) { box.append(el("div", "card inc", "최근 90일 장애 기록이 없습니다.")); return; }
  for (const i of list.slice(0, 30)) {
    const c = el("div", "card inc");
    const open = !i.ended || i.ended.startsWith("0001");
    const head = el("div", "row");
    head.append(el("span", "dot " + (open ? "s-down" : "s-ok")), el("b", "", names[i.target] || i.target));
    head.append(el("span", "right", open ? "진행 중" : dur(i.started, i.ended)));
    c.append(head, el("div", "reason", i.reason), el("div", "reason when", fmt(i.started) + (open ? " ~" : " ~ " + fmt(i.ended))));
    box.append(c);
  }
}

async function load() {
  try {
    const r = await fetch("/api/v1/status");
    state = await r.json();
    renderBanner();
    renderFilters();
    renderTargets();
    await loadIncidents();
    if (!scrolled && location.hash) {
      scrolled = true;
      document.getElementById(location.hash.slice(1))?.scrollIntoView();
    }
  } catch (e) {
    document.getElementById("banner").textContent = "상태를 불러오지 못했습니다.";
  }
}

load();
setInterval(load, 60000);
