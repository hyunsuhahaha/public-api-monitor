"use strict";
// The trace card: hover a row of 최근 확인 and the request is replayed. A packet runs from
// the probe through DNS, TCP, TLS and the server at the speed the request really went
// (scaled to a few seconds), lights each step as it passes, stops and pulses where a
// timeout stopped it, then the conclusion and its evidence come in: the other servers
// checked at that moment as dots (red: stuck in the same step), the same provider's other
// APIs, the other locations, the re-sent request. Click pins the card; Esc closes it.
// Uses PHASES, PHASE_REST, el, ms, kst, LABEL and state from app.js.

const TRACE_KIND = {
  monitor_network: ["감시 지점 네트워크", "blue"], path: ["지역 경로", "blue"], server_edge: ["서버 앞단", "warn"],
  server: ["서버", "warn"], provider: ["제공자 전체", "out"], dns: ["DNS", "purple"], content: ["응답 내용", "deg"],
  monitor: ["감시 쪽 설정", "dim"], unknown: ["판단 보류", "dim"],
};
const TRACE_SHORT = { dnsMs: "DNS", connectMs: "TCP", tlsMs: "TLS", waitMs: "서버" };
const STEP_KEY = { dns: "dnsMs", connect: "connectMs", tls: "tlsMs", wait: "waitMs" };
const reducedMotion = () => window.matchMedia && matchMedia("(prefers-reduced-motion: reduce)").matches;

let traceCard = null, traceTimer = 0, tracePinned = false, traceRow = null, traceAnim = 0;

function locName(id) {
  for (const t of (state.data && state.data.targets) || []) for (const l of t.locations || []) if (l.location === id && l.name) return l.name;
  return id;
}

// the segments of one request: measured phases, then what is left (or, for a timeout,
// the step it was stuck in until the time ran out)
function traceSegments(c) {
  const p = c.phases || {}, total = c.latencyMs || 0, segs = [];
  let used = 0;
  for (const [k, name, color] of PHASES) {
    const v = p[k] || 0; if (!v) continue;
    used += v; segs.push({ key: k, name, short: TRACE_SHORT[k], color, ms: v });
  }
  // where it ended: a timeout stopped it in that step, an error broke it there
  const why = c.reason || "", ended = c.trace && c.trace.phase && (why.startsWith("timeout: ") || why.startsWith("request failed"));
  const stalled = ended ? c.trace.phase : "";
  const rest = Math.max(0, total - used);
  if (rest) {
    const k = STEP_KEY[stalled];
    const hit = k && segs.find((s) => s.key === k);
    if (hit) hit.ms += rest;                       // the clock ran out inside that step
    else if (stalled) {
      const ph = PHASES.find(([key]) => key === k);
      const name = ph ? ph[1] : stalled === "pool" ? "연결 대기" : stalled === "send" ? "요청 보내기" : PHASE_REST[1];
      segs.push({ key: k || stalled, name, short: TRACE_SHORT[k], color: ph ? ph[2] : stalled === "pool" ? "#5794f2" : PHASE_REST[2], ms: rest });
    }
    else segs.push({ key: "body", name: PHASE_REST[1], color: PHASE_REST[2], ms: rest });
  }
  if (stalled) {
    let s = segs.find((x) => x.key === (STEP_KEY[stalled] || stalled));
    if (!s) { // broke at once: nothing measured, still show the step it broke in
      const k = STEP_KEY[stalled], ph = PHASES.find(([key]) => key === k);
      s = { key: k || stalled, name: ph ? ph[1] : stalled, short: TRACE_SHORT[k], color: ph ? ph[2] : "#5794f2", ms: Math.max(1, total - used) };
      segs.push(s);
    }
    s.stalled = true; s.failed = why.startsWith("request failed");
  } else if (c.status === "slow" && segs.length) {
    segs.reduce((a, b) => (b.ms > a.ms ? b : a)).slowest = true;
  }
  return segs;
}

function buildTraceCard(c, target) {
  const tr = c.trace, card = el("div", "trace-card");
  card.setAttribute("role", "dialog"); card.setAttribute("aria-label", "요청 추적");
  const head = el("div", "tc-head");
  const st = el("span", "pill " + (sev(c.status) >= 2 ? "out" : sev(c.status) === 1 ? "deg" : c.status === "ok" ? "ok" : ""), LABEL[c.status] || c.status);
  const clock = el("span", "tc-clock num", "0ms");
  head.append(st, el("span", "tc-when", `${kst(c.at, { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit" })} · ${c.locationName || locName(c.location)}`), clock);
  const replay = el("button", "tc-btn", "↻"); replay.title = "다시 재생"; replay.type = "button";
  const close = el("button", "tc-btn tc-close", "×"); close.title = "닫기 (Esc)"; close.type = "button";
  head.append(replay, close);
  card.append(head);

  // the route: probe ── DNS ── TCP ── TLS ── server ── back
  const route = el("div", "tc-route");
  route.append(el("span", "tc-end", "감시기"), el("span", "tc-arrow", ""), el("span", "tc-end tc-srv", (target && target.name) || "서버"));
  card.append(route);
  const segs = traceSegments(c), total = segs.reduce((a, s) => a + s.ms, 0) || 1;
  const track = el("div", "tc-track"), labels = el("div", "tc-labels");
  for (const s of segs) {
    const w = Math.max(4, (s.ms / total) * 100);
    const seg = el("div", "tc-seg" + (s.stalled ? " stalled" : "") + (s.slowest ? " slowest" : ""));
    seg.style.flexGrow = w; seg.style.setProperty("--c", s.color);
    seg.append(el("i"));                     // the fill that runs as the packet passes
    seg.title = `${s.name} ${ms(s.ms)}`;
    s.el = seg; track.append(seg);
    const lab = el("div", "tc-lab" + (s.stalled ? " stalled" : ""));
    lab.style.flexGrow = w;
    const n = el("b", "num", "–"); s.num = n;
    lab.append(el("span", "", s.short || s.name), n);
    lab.title = s.name;
    labels.append(lab);
  }
  const packet = el("span", "tc-packet");
  track.append(packet);
  card.append(track, labels);
  const stallNote = el("div", "tc-stall");
  const stalledSeg = segs.find((s) => s.stalled);
  if (stalledSeg) stallNote.textContent = stalledSeg.failed ? `✕ 여기서 실패: ${stalledSeg.name} 단계 (${ms(c.latencyMs)} 만에)` : `⏱ 여기서 멈춤: ${stalledSeg.name} 중 시간 초과 (${ms(c.latencyMs)})`;
  else if (segs.find((s) => s.slowest)) stallNote.textContent = `가장 오래 걸린 단계: ${segs.find((s) => s.slowest).name}`;
  card.append(stallNote);

  // the conclusion and what it rests on
  const verdict = el("div", "tc-verdict");
  if (tr) {
    const [kname, kcolor] = TRACE_KIND[tr.kind] || TRACE_KIND.unknown;
    const tag = el("span", "tc-kind k-" + kcolor, kname);
    verdict.append(tag, el("p", "tc-headline", tr.headline));
    const peers = tr.peers || {};
    if (peers.hosts > 0 && ["dns", "connect", "tls", "pool"].includes(tr.phase)) {
      const box = el("div", "tc-peers");
      const dots = el("div", "tc-dots");
      const n = Math.min(peers.hosts, 60);
      const red = Math.round((peers.sameStep / peers.hosts) * n);
      for (let i = 0; i < n; i++) { const d = el("i", i < red ? "bad" : ""); d.style.animationDelay = (i * 18) + "ms"; dots.append(d); }
      box.append(dots, el("span", "tc-cap", `같은 시각 다른 서버 ${peers.hosts}곳 · ${peers.sameStep}곳이 같은 단계에서 막힘`));
      verdict.append(box);
    }
    if (tr.elsewhere && tr.elsewhere.length) {
      const locs = el("div", "tc-locs");
      for (const e of tr.elsewhere) locs.append(el("span", "tc-loc s-" + e.status, `${e.name}: ${LABEL[e.status] || e.status}`));
      verdict.append(locs);
    }
    const ul = el("ul", "tc-evidence");
    (tr.evidence || []).forEach((t, i) => { const li = el("li", "", t); li.style.animationDelay = (i * 90) + "ms"; ul.append(li); });
    verdict.append(ul);
  } else {
    verdict.append(el("span", "tc-kind k-ok", "정상"), el("p", "tc-headline", c.status === "ok" ? "모든 단계를 정상으로 마쳤습니다." : "이 확인에는 원인 추정이 없습니다."));
  }
  card.append(verdict);
  return { card, segs, packet, clock, stallNote, verdict, replay, close };
}

// the replay: real proportions, a few seconds at most (a 5-second timeout takes ~3 s, a
// 150 ms answer under one)
function playTrace(t, c) {
  cancelAnimationFrame(traceAnim);
  const total = t.segs.reduce((a, s) => a + s.ms, 0) || 1;
  const dur = reducedMotion() ? 0 : Math.min(3000, 700 + Math.sqrt(c.latencyMs || 0) * 30);
  t.card.classList.remove("done");
  for (const s of t.segs) { s.el.classList.remove("lit", "now"); s.el.firstChild.style.width = "0%"; s.num.textContent = "–"; }
  const bounds = []; let acc = 0;
  for (const s of t.segs) { bounds.push([acc / total, (acc + s.ms) / total]); acc += s.ms; }
  const t0 = performance.now();
  const frame = (now) => {
    const f = dur ? Math.min(1, (now - t0) / dur) : 1;
    // ease out: the packet slows into the end, which is where a stall is
    const x = 1 - Math.pow(1 - f, 1.6);
    t.packet.style.left = (x * 100) + "%";
    t.clock.textContent = ms(Math.round(x * (c.latencyMs || 0))) || "0ms";
    t.segs.forEach((s, i) => {
      const [a, b] = bounds[i];
      const inside = x > a && x < b;
      const part = x >= b ? 1 : inside ? (x - a) / (b - a) : 0;
      s.el.firstChild.style.width = (part * 100) + "%";
      s.el.classList.toggle("now", inside);
      if (part > 0) s.num.textContent = ms(Math.round(part * s.ms));
      if (x >= b) s.el.classList.add("lit");
    });
    if (f < 1) traceAnim = requestAnimationFrame(frame);
    else { t.clock.textContent = ms(c.latencyMs); t.card.classList.add("done"); }
  };
  traceAnim = requestAnimationFrame(frame);
}

function placeTrace(card, row) {
  const r = row.getBoundingClientRect(), w = Math.min(560, innerWidth - 24);
  card.style.width = w + "px";
  if (innerWidth <= 700) return; // a bottom sheet on phones (app.css)
  const h = card.offsetHeight || 360;
  let top = r.bottom + 6;
  if (top + h > innerHeight - 8) top = Math.max(8, r.top - h - 6);
  const left = Math.min(Math.max(12, r.left + 40), innerWidth - w - 12);
  card.style.top = top + "px"; card.style.left = left + "px";
}

function hideTrace(force) {
  if (tracePinned && !force) return;
  cancelAnimationFrame(traceAnim);
  if (traceCard) traceCard.remove();
  if (traceRow) traceRow.classList.remove("traced");
  traceCard = null; traceRow = null; tracePinned = false;
}

function showTrace(row, c, target, pin) {
  if (traceRow === row && traceCard) { if (pin) { tracePinned = true; traceCard.classList.add("pinned"); } return; }
  hideTrace(true);
  const t = buildTraceCard(c, target);
  traceCard = t.card; traceRow = row; tracePinned = !!pin;
  row.classList.add("traced");
  if (pin) t.card.classList.add("pinned");
  document.body.append(t.card);
  placeTrace(t.card, row);
  t.replay.onclick = (e) => { e.stopPropagation(); playTrace(t, c); };
  t.close.onclick = (e) => { e.stopPropagation(); hideTrace(true); };
  t.card.onmouseenter = () => clearTimeout(traceTimer);
  t.card.onmouseleave = () => { traceTimer = setTimeout(() => hideTrace(false), 200); };
  t.card.onclick = () => { tracePinned = true; t.card.classList.add("pinned"); };
  playTrace(t, c);
}

// app.js calls this for every row of 최근 확인
function attachTrace(row, c, target) {
  row.classList.add("traceable");
  if (c.trace) {
    row.classList.add("has-trace");
    const why = row.querySelector("td.r");
    if (why) { const chip = el("span", "tc-chip", "원인 추정"); why.prepend(chip); why.title = c.trace.headline + "\n" + (c.reason || ""); }
  }
  row.onmouseenter = () => { if (tracePinned) return; clearTimeout(traceTimer); traceTimer = setTimeout(() => showTrace(row, c, target, false), 140); };
  row.onmouseleave = () => { clearTimeout(traceTimer); traceTimer = setTimeout(() => hideTrace(false), 200); };
  row.onclick = () => showTrace(row, c, target, true);
}

document.addEventListener("keydown", (e) => { if (e.key === "Escape" && traceCard) hideTrace(true); });
document.addEventListener("click", (e) => {
  if (traceCard && tracePinned && !traceCard.contains(e.target) && !(traceRow && traceRow.contains(e.target))) hideTrace(true);
});
addEventListener("scroll", () => { if (!traceCard) return; if (tracePinned) placeTrace(traceCard, traceRow); else hideTrace(true); }, true);
