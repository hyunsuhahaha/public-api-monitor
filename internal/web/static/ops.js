"use strict";
// On-call helpers on top of app.js: the incident view (#i=<id>), the postmortem draft,
// the command palette (Ctrl+K) and keyboard shortcuts.

// ── incident view ─────────────────────────────────────────────────────────────
let currentIncident = null;

function closeIncident() {
  $("inc-modal").hidden = true; currentIncident = null;
  const h = new URLSearchParams(location.hash.slice(1)); h.delete("i");
  history.replaceState(null, "", "#" + h.toString());
}

const fmtFull = (d) => kst(d, { year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit" });
const sevLabel = (i) => (i.severity >= 2 ? "장애" : "성능 저하");

// Where an incident's failures happened, from the checks' phases and reasons: the
// provider's server (stalled waiting for the first byte, 5xx, an error result code) or the
// network (stalled in DNS, connect or TLS). The first question of every review.
function whereItFailed(checks) {
  const failing = checks.filter((c) => sev(c.status) >= 1);
  const touts = failing.filter((c) => (c.reason || "").startsWith("timeout: "));
  const stages = {};
  for (const c of touts) { const s = c.reason.slice(9); stages[s] = (stages[s] || 0) + 1; }
  const net = failing.map((c) => c.phases).filter((p) => p && (p.connectMs || p.tlsMs)).map((p) => (p.dnsMs || 0) + (p.connectMs || 0) + (p.tlsMs || 0)).sort((a, b) => a - b);
  const netMed = net.length ? net[Math.floor(net.length / 2)] : null;
  const server = (stages["서버 응답 대기 중 (연결은 됨)"] || 0) + (stages["본문 받는 중"] || 0)
    + failing.filter((c) => c.http >= 500 || (c.http === 200 && c.status !== "slow" && c.status !== "stale")).length;
  const network = (stages["TCP 연결 중"] || 0) + (stages["DNS 조회 중"] || 0) + (stages["TLS 연결 중"] || 0);
  const slow = failing.filter((c) => c.status === "slow" && c.phases && c.latencyMs);
  const waitShare = slow.length ? slow.reduce((a, c) => a + (c.phases.waitMs || 0), 0) / slow.reduce((a, c) => a + c.latencyMs, 0) : 0;
  let who = "알 수 없음", cls = "";
  if (server && !network) { who = "기관 서버"; cls = "c-warn-t"; }
  else if (network && !server) { who = "네트워크"; cls = "c-blue-t"; }
  else if (network && server) { who = "섞임"; cls = "c-deg-t"; }
  else if (slow.length && waitShare >= 0.6) { who = "기관 서버"; cls = "c-warn-t"; }
  else if (slow.length) { who = "네트워크"; cls = "c-blue-t"; }
  const parts = Object.entries(stages).map(([s, n]) => `타임아웃 ${n}회: ${s}`);
  const h5 = failing.filter((c) => c.http >= 500).length;
  if (h5) parts.push(`HTTP 5xx ${h5}회`);
  const coded = failing.filter((c) => c.http === 200 && c.code && c.status !== "slow" && c.status !== "stale");
  if (coded.length) parts.push(`결과 코드 오류 ${coded.length}회 (code ${[...new Set(coded.map((c) => c.code))].join(", ")})`);
  if (slow.length) parts.push(`느린 응답 ${slow.length}회, 서버 대기 ${Math.round(waitShare * 100)}%`);
  if (netMed != null) parts.push(`연결 p50 ${ms(netMed)}`);
  return { who, cls, detail: parts.join(" · ") || "구간 기록 없음" };
}

// What to do next, from where it failed: check the portal's notices and the agency's
// other APIs for a provider problem, the other locations and DNS for a network one; the
// runbook and the escalation path either way. Links, not prose.
const RUNBOOK = "https://github.com/hyunsuhahaha/public-api-monitor/blob/main/docs/runbooks/README.md#공공-api-장애-대응";
function nextSteps(d, w, open) {
  const i = d.incident, t = d.target || {}, steps = [];
  const link = (text, href) => { const a = el("a", "", text); a.href = href; a.target = "_blank"; a.rel = "noopener"; return a; };
  const li = (...parts) => { const x = el("li"); x.append(...parts); steps.push(x); };
  const peers = (state.data.targets || []).filter((x) => x.provider === t.provider && x.id !== t.id);
  const badPeers = peers.filter((x) => sev(x.status) >= 1);
  if (w.who === "네트워크" || w.who === "섞임") {
    li("다른 지역 결과를 본다: 한 지역만 실패면 그 지역 네트워크 문제 (아래 지역 × 시간 표)");
    li("감시 서버에서 DNS·연결을 직접 확인: ", el("code", "", "dig apis.data.go.kr · curl -sv -o /dev/null https://apis.data.go.kr"));
  }
  if (w.who !== "네트워크") {
    li(link("공공데이터포털 공지사항", "https://www.data.go.kr/bbs/ntc/selectNoticeListView.do"), "에서 점검·장애 공지를 찾는다. 공지된 점검이면 ", el("code", "", "targets.yaml maintenance"), "에 등록해 SLO에서 뺀다");
    // same rule as the agency banner: two or more, and half or more, is the agency
    const agencyWide = badPeers.length >= 2 && badPeers.length * 2 >= peers.length;
    li(peers.length ? `같은 기관(${t.provider}) 다른 API ${peers.length}개 중 지금 ${badPeers.length}개가 이상` +
      (agencyWide ? " — 기관 전체 문제일 가능성" : badPeers.length ? " — 일부만 이상, 기관 전체 문제로 보긴 이름" : " — 지금은 이 API만의 문제") : `${t.provider || "기관"}의 다른 API는 감시하지 않음`);
    if (t.docs) li(link("API 안내 페이지", t.docs), "의 문의처·변경 이력을 확인한다");
    if (open && Date.now() - Date.parse(i.started) > 30 * 60e3) li("30분 넘게 계속되면 데이터포털 고객센터(1566-0025)나 기관 문의처에 상태판 링크와 시작 시각을 함께 보내 신고한다");
  }
  if (i.flaps >= 2) li(`불안정 ×${i.flaps}: 알림이 시끄러우면 기관 문제로 기록하고, 반복되는 시간대가 있는지 API별 장애 통계에서 본다`);
  if (!open) li("닫힌 장애: ", el("b", "", "포스트모템 초안 복사"), "로 기록을 남긴다 (원인 항목이 문제 위치로 채워져 있음)");
  li(link("런북: 공공 API 장애 대응", RUNBOOK));
  return steps;
}

async function openIncident(id) {
  const d = await fetch("/api/v1/incidents/" + id).then((r) => (r.ok ? r.json() : null));
  if (!d) return;
  currentIncident = d;
  const h = new URLSearchParams(location.hash.slice(1)); h.set("i", id);
  history.replaceState(null, "", "#" + h.toString());
  const i = d.incident, t = d.target || { name: i.target, provider: "" };
  const open = isOpen(i), end = open ? new Date() : new Date(i.ended);
  $("inc-modal").hidden = false;
  const title = $("im-title"); title.replaceChildren();
  title.append(el("span", "sev " + (open ? "open" : "s" + i.severity), open ? "진행 중" : sevLabel(i)), document.createTextNode(` #${i.id} ${t.name}`));
  $("im-sub").textContent = `${t.provider} · ${fmtFull(i.started)} ~ ${open ? "진행 중" : fmtFull(i.ended)} · ${dur(i.started, end.toISOString())}`;

  const checks = d.checks, locs = [...new Set(checks.map((c) => c.location))].sort();
  const inWin = checks.filter((c) => Date.parse(c.at) >= Date.parse(i.started) && Date.parse(c.at) <= end.getTime());
  const failing = inWin.filter((c) => sev(c.status) === 2);
  const failingLocs = new Set(failing.map((c) => c.location));
  const lat = inWin.filter((c) => sev(c.status) >= 0 && c.http).map((c) => c.latencyMs).sort((a, b) => a - b);
  $("im-stats").replaceChildren(
    statPanel("지속 시간", dur(i.started, end.toISOString()), open ? "아직 진행 중" : "복구됨", open ? "c-out-t" : "c-ok-t"),
    statPanel("실패한 확인", `${failing.length} / ${inWin.length}`, "장애 구간 안의 원시 확인", failing.length ? "c-out-t" : ""),
    statPanel("영향 지역", `${failingLocs.size} / ${locs.length}`, [...failingLocs].join(", ") || "–", failingLocs.size === locs.length ? "c-out-t" : "c-deg-t"),
    i.flaps ? statPanel("불안정", `×${i.flaps}`, "복구된 듯하다 다시 실패", "c-deg-t") : statPanel("처음 원인", i.reason.split(" (")[0], "", ""),
    (() => { const w = whereItFailed(inWin); return statPanel("문제 위치", w.who, w.detail + ` · 응답 p50 ${ms(lat[Math.floor(lat.length / 2)] || 0)}`, w.cls); })());
  $("im-next").replaceChildren(...nextSteps(d, whereItFailed(inWin), open));

  // matrix: one row per location, one cell per check, the incident window shaded
  const cv = $("im-matrix"), W = cv.parentElement.clientWidth - 22, rowH = 18, labelW = 110;
  const g = sizeCanvas(cv, W, locs.length * rowH + 22);
  const from = Date.parse(d.from), to = Date.parse(d.to), xOf = (t) => labelW + (W - labelW) * (t - from) / (to - from);
  g.fillStyle = "rgba(242,73,92,.08)"; g.fillRect(xOf(Date.parse(i.started)), 0, xOf(end.getTime()) - xOf(Date.parse(i.started)), locs.length * rowH);
  g.font = "11px Consolas, monospace"; g.textBaseline = "middle";
  locs.forEach((loc, r) => {
    g.fillStyle = C.axis; g.fillText(loc, 0, r * rowH + rowH / 2);
    for (const c of checks.filter((c) => c.location === loc)) {
      const s = sev(c.status);
      g.fillStyle = s === 2 ? C.out : s === 1 ? C.deg : s === 0 ? C.ok : "#4a4e57";
      g.fillRect(xOf(Date.parse(c.at)) - 2, r * rowH + 3, 4, rowH - 6);
    }
  });
  g.fillStyle = "#5d616b"; g.textBaseline = "top";
  [[from, "left"], [Date.parse(i.started), "center"], [to, "right"]].forEach(([t, a]) => {
    g.textAlign = a; g.fillText(kst(t, { hour: "2-digit", minute: "2-digit" }), Math.min(W - 1, Math.max(labelW, xOf(t))), locs.length * rowH + 6);
  });
  g.textAlign = "left";

  // timeline: the verdicts where the status changed
  const tl = $("im-timeline"); tl.replaceChildren();
  let prev = null;
  for (const v of d.verdicts) {
    if (v.status === prev) continue;
    const row = el("div", "tl");
    row.append(el("span", "t", kst(v.at, { hour: "2-digit", minute: "2-digit", second: "2-digit" })), el("span", "dot " + v.status),
      el("span", "", `${LABEL[v.status] || v.status}${v.reason ? " — " + v.reason : ""} (투표 ${v.voters}곳)`));
    tl.append(row); prev = v.status;
  }
  if (!tl.children.length) tl.append(el("div", "empty", "판정 기록이 없습니다"));

  const log = $("im-log"); log.replaceChildren();
  const hr = el("tr"); ["시각", "지역", "판정", "응답", "HTTP", "코드", "사유"].forEach((x) => hr.append(el("th", "", x))); log.append(hr);
  for (const c of checks.slice().reverse()) {
    const tr = el("tr");
    const st = el("td"); st.append(el("span", "dot " + c.status), document.createTextNode(" " + (LABEL[c.status] || c.status)));
    const rs = el("td", "r", c.reason || ""); rs.title = c.reason || "";
    tr.append(el("td", "mono", kst(c.at, { hour: "2-digit", minute: "2-digit", second: "2-digit" })), el("td", "", c.location), st,
      el("td", "num", ms(c.latencyMs)), el("td", "num", c.http || "–"), el("td", "mono", c.code || ""), rs);
    log.append(tr);
  }
  $("im-count").textContent = `${checks.length}건 (장애 15분 전부터)`;
}

// A postmortem draft from what the monitor saw; the engineer fills in cause and actions.
function postmortemMarkdown(d) {
  const i = d.incident, t = d.target || { name: i.target, provider: "" };
  const open = isOpen(i), end = open ? new Date().toISOString() : i.ended;
  const inWin = d.checks.filter((c) => Date.parse(c.at) >= Date.parse(i.started) && Date.parse(c.at) <= Date.parse(end));
  const failing = inWin.filter((c) => sev(c.status) === 2);
  const locs = [...new Set(d.checks.map((c) => c.location))].sort();
  const flocs = [...new Set(failing.map((c) => c.location))].sort();
  const lines = [
    `# 포스트모템: ${t.name} ${sevLabel(i)} (#${i.id})`, "",
    "| 항목 | 내용 |", "|---|---|",
    `| API | ${t.name} (${t.provider}) |`,
    `| 시작 | ${fmtFull(i.started)} |`,
    `| 종료 | ${open ? "진행 중" : fmtFull(i.ended)} |`,
    `| 지속 | ${dur(i.started, end)} |`,
    `| 심각도 | ${sevLabel(i)} |`,
    `| 처음 증상 | ${i.reason} |`,
    `| 마지막 증상 | ${i.lastReason || i.reason} |`,
    `| 불안정(flapping) | ${i.flaps ? i.flaps + "회 — 복구된 듯하다 다시 실패, 한 장애로 묶음" : "없음"} |`,
    `| 실패한 확인 | ${failing.length} / ${inWin.length} |`,
    `| 영향 지역 | ${flocs.join(", ") || "-"} (전체 ${locs.join(", ")}) |`,
    `| 상태판 | ${location.origin}/#i=${i.id} |`, "",
    "## 타임라인 (KST, 모니터 판정)", "",
  ];
  let prev = null;
  for (const v of d.verdicts) {
    if (v.status === prev) continue;
    lines.push(`- ${kst(v.at, { hour: "2-digit", minute: "2-digit", second: "2-digit" })} ${LABEL[v.status] || v.status}${v.reason ? " — " + v.reason : ""}`);
    prev = v.status;
  }
  lines.push("", "## 영향", "", "- (이 API를 쓰는 서비스와 사용자에게 무엇이 안 됐나)", "",
    "## 원인", "", `- 모니터가 본 위치: **${whereItFailed(inWin).who}** — ${whereItFailed(inWin).detail}`, "- (제공 기관 공지·문의 결과)", "",
    "## 우리가 한 일", "", "- (알림을 받고 한 조치)", "",
    "## 재발 방지 / 개선", "", "| 조치 | 담당 | 상태 |", "|---|---|---|", "| | | |", "");
  return lines.join("\n");
}

async function copy(text, btn) {
  try {
    await navigator.clipboard.writeText(text);
    const old = btn.textContent; btn.textContent = "복사됨 ✓"; setTimeout(() => (btn.textContent = old), 1500);
  } catch (e) { window.prompt("복사하세요", text); }
}

// ── shift handoff ─────────────────────────────────────────────────────────────
// What the next on-call needs: what is wrong now, what happened on this shift, and the
// things that are easy to forget (our-side problems, maintenance, schema changes).
let hoHours = 12;
function handoffMarkdown(hours) {
  const d = state.data, now = Date.now(), from = now - hours * 3600e3;
  const names = Object.fromEntries(d.targets.map((t) => [t.id, t.name]));
  const nm = (id) => names[id] || id;
  const at = (t) => kst(t, { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" });
  const L = [`# 교대 인수인계 · ${at(now)} KST · 지난 ${hours}시간`, ""];
  L.push("## 지금 상태", "");
  const open = state.incidents.filter(isOpen);
  L.push(open.length ? `- **진행 중 장애 ${open.length}건**` : "- 진행 중 장애 없음");
  for (const i of open) L.push(`  - #${i.id} ${nm(i.target)} — ${i.lastReason || i.reason} (${at(i.started)}부터, ${dur(i.started, new Date().toISOString())}${i.flaps ? `, 불안정 ×${i.flaps}` : ""})`);
  const deg = d.targets.filter((t) => sev(t.status) === 1);
  if (deg.length) L.push(`- 성능 저하 ${deg.length}개: ` + deg.map((t) => `${t.name} (${t.reason || LABEL[t.status]})`).join(", "));
  const burning = d.targets.filter((t) => t.burn1h >= 14.4 && t.burn5m >= 14.4 || t.burn6h >= 6 && t.burn30m >= 6);
  if (burning.length) L.push(`- 에러 버짓 소진 경보 ${burning.length}개: ` + burning.map((t) => `${t.name} 1h ${burnText(t.burn1h)} · 6h ${burnText(t.burn6h)}`).join(", "));
  const ours = d.targets.filter((t) => t.status === "client");
  if (ours.length) L.push(`- 조치 필요 (우리 쪽) ${ours.length}개: ` + ours.map((t) => t.name).join(", ") + ` — ${[...new Set(ours.map((t) => t.reason))].join(" / ")}`);
  for (const m of d.maintenance || []) if (m.active || Date.parse(m.start) > now)
    L.push(`- ${m.active ? "점검 중" : "점검 예정"}: ${m.note || "계획 점검"} ${at(m.start)} ~ ${at(m.end)} (${m.targets.map(nm).join(", ")})`);
  const f = d.fleet;
  L.push(`- 전체 가용률 ${pct(f.availability)} (${RANGE_LABEL[state.range]}, 목표 ${pct(f.slo, 1)}) · 에러 버짓 ${budgetText(f.budgetLeft)}`, "");
  const shift = state.incidents.filter((i) => (isOpen(i) ? now : Date.parse(i.ended)) >= from).sort((a, b) => Date.parse(a.started) - Date.parse(b.started));
  L.push(`## 이번 교대의 장애 (${shift.length}건)`, "");
  if (shift.length) {
    L.push("| # | API | 시작 | 지속 | 원인 | 비고 |", "|---|---|---|---|---|---|");
    for (const i of shift) L.push(`| ${i.id} | ${nm(i.target)} | ${at(i.started)} | ${isOpen(i) ? "진행 중" : dur(i.started, i.ended)} | ${i.reason} | ${[i.severity >= 2 ? "장애" : "성능 저하", i.flaps ? `불안정 ×${i.flaps}` : ""].filter(Boolean).join(", ")} |`);
    const byProv = {};
    for (const i of shift) { const p = (d.targets.find((t) => t.id === i.target) || {}).provider || "?"; byProv[p] = (byProv[p] || 0) + 1; }
    const top = Object.entries(byProv).sort((a, b) => b[1] - a[1])[0];
    if (top && top[1] >= 2) L.push("", `- 장애가 몰린 기관: **${top[0]}** (${top[1]}건) — 개별 API보다 기관 쪽 원인을 먼저 보세요`);
  } else L.push("- 없음");
  L.push("");
  const sch = state.schema.filter((e) => Date.parse(e.at) >= from);
  if (sch.length) {
    L.push(`## 응답 형식 변경 (${sch.length}건)`, "");
    for (const e of sch) L.push(`- ${at(e.at)} ${nm(e.target)}: ${e.kind === "removed" ? "필드 사라짐" : "필드 추가"} ${e.paths.join(", ")}`);
    L.push("");
  }
  L.push("## 다음 당번에게", "", "- ", "", `상태판: ${location.origin}/`);
  return L.join("\n");
}
function openHandoff() {
  $("handoff").hidden = false;
  const box = $("ho-hours"); box.replaceChildren();
  for (const h of [8, 12, 24]) {
    const b = el("button", h === hoHours ? "on" : "", h + "시간");
    b.onclick = () => { hoHours = h; openHandoff(); }; box.append(b);
  }
  $("ho-text").textContent = handoffMarkdown(hoHours);
}
$("open-handoff").onclick = openHandoff;
$("ho-close").onclick = () => ($("handoff").hidden = true);
$("handoff").onclick = (e) => { if (e.target.id === "handoff") $("handoff").hidden = true; };
$("ho-copy").onclick = (e) => copy(handoffMarkdown(hoHours), e.target);

$("im-close").onclick = closeIncident;
$("inc-modal").onclick = (e) => { if (e.target.id === "inc-modal") closeIncident(); };
$("im-copy-md").onclick = (e) => currentIncident && copy(postmortemMarkdown(currentIncident), e.target);
$("im-copy-link").onclick = (e) => currentIncident && copy(`${location.origin}/#i=${currentIncident.incident.id}`, e.target);

// ── command palette ───────────────────────────────────────────────────────────
let palItems = [], palSel = 0;

function paletteCommands() {
  const cmds = RANGES.map((r) => ({ label: `기간: ${RANGE_LABEL[r]}`, hint: "명령", run: () => document.querySelectorAll("#ranges button")[RANGES.indexOf(r)].click() }));
  cmds.push({ label: "공개 상태 페이지 열기 (API 사용자용)", hint: "명령", run: () => (location.href = "/status") },
    { label: "월간 가용성 리포트 열기", hint: "명령", run: () => (location.href = "/report") },
    { label: "장애 피드(Atom) 열기", hint: "명령", run: () => (location.href = "/feed.xml") },
    { label: "키보드 단축키 보기", hint: "?", run: () => ($("help").hidden = false) },
    { label: "교대 인수인계 요약 (Markdown)", hint: "h", run: openHandoff });
  const apis = (state.data ? state.data.targets : []).map((t) => ({
    label: t.name, hint: `${t.provider} · ${LABEL[t.status] || t.status}`, status: t.status, run: () => openDrawer(t.id), q: (t.name + t.provider + t.id + t.category).toLowerCase(),
  }));
  const incs = state.incidents.slice(0, 20).map((i) => ({
    label: `장애 #${i.id} ${(state.data.targets.find((t) => t.id === i.target) || {}).name || i.target}`, hint: isOpen(i) ? "진행 중" : short(i.started), run: () => openIncident(i.id), q: ("장애 incident " + i.target + i.reason).toLowerCase(),
  }));
  return [...apis, ...incs, ...cmds];
}

function renderPalette() {
  const q = $("pal-q").value.trim().toLowerCase();
  const all = paletteCommands();
  // every typed word must appear (name, provider, id, category)
  palItems = all.filter((c) => q.split(/\s+/).every((w) => (c.q || c.label.toLowerCase()).includes(w))).slice(0, 30);
  palSel = Math.min(palSel, Math.max(0, palItems.length - 1));
  const list = $("pal-list"); list.replaceChildren();
  palItems.forEach((c, k) => {
    const row = el("div", "pal-item" + (k === palSel ? " on" : ""));
    if (c.status) row.append(el("span", "dot " + c.status));
    row.append(el("span", "", c.label), el("span", "k", c.hint));
    row.onmouseenter = () => { palSel = k; renderPalette(); };
    row.onclick = () => runPalette(k);
    list.append(row);
  });
  if (!palItems.length) list.append(el("div", "empty", "일치하는 항목이 없습니다"));
  const on = list.querySelector(".on"); if (on) on.scrollIntoView({ block: "nearest" });
}

function openPalette() { $("palette").hidden = false; $("pal-q").value = ""; palSel = 0; renderPalette(); $("pal-q").focus(); }
function closePalette() { $("palette").hidden = true; $("pal-q").blur(); } // (focus left in the hidden input swallowed j/k)
function runPalette(k) { const c = palItems[k]; closePalette(); if (c) c.run(); }

$("pal-q").oninput = () => { palSel = 0; renderPalette(); };
$("pal-q").onkeydown = (e) => {
  if (e.key === "ArrowDown") { palSel = Math.min(palItems.length - 1, palSel + 1); renderPalette(); e.preventDefault(); }
  else if (e.key === "ArrowUp") { palSel = Math.max(0, palSel - 1); renderPalette(); e.preventDefault(); }
  else if (e.key === "Enter") { runPalette(palSel); e.preventDefault(); }
};
$("palette").onclick = (e) => { if (e.target.id === "palette") closePalette(); };
$("open-palette").onclick = openPalette;
$("help-close").onclick = () => ($("help").hidden = true);

// ── keyboard ──────────────────────────────────────────────────────────────────
let selRow = -1;
function selectRow(k) {
  const rows = [...document.querySelectorAll("#rows tr[data-id]")];
  if (!rows.length) return;
  selRow = Math.max(0, Math.min(rows.length - 1, k));
  rows.forEach((r, i) => r.classList.toggle("sel", i === selRow));
  rows[selRow].scrollIntoView({ block: "nearest" });
}

document.addEventListener("keydown", (e) => {
  const typing = /INPUT|TEXTAREA|SELECT/.test(document.activeElement.tagName);
  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "k") { e.preventDefault(); $("palette").hidden ? openPalette() : closePalette(); return; }
  if (e.key === "Escape") {
    // close the topmost layer only
    if (!$("palette").hidden) closePalette();
    else if (!$("help").hidden) $("help").hidden = true;
    else if (!$("handoff").hidden) $("handoff").hidden = true;
    else if (!$("inc-modal").hidden) closeIncident();
    else if (!$("drawer").hidden) closeDrawer();
    else if (typing) document.activeElement.blur();
    return;
  }
  if (typing || e.ctrlKey || e.metaKey || e.altKey) return;
  if (e.key === "/") { e.preventDefault(); $("q").focus(); }
  else if (e.key === "?") $("help").hidden = false;
  else if (e.key === "j") selectRow(selRow + 1);
  else if (e.key === "k") selectRow(selRow - 1);
  else if (e.key === "Enter" && selRow >= 0) { const r = document.querySelectorAll("#rows tr[data-id]")[selRow]; if (r) openDrawer(r.dataset.id); }
  else if (e.key === "r") load();
  else if (e.key === "h") openHandoff();
  else if (/^[1-5]$/.test(e.key)) document.querySelectorAll("#ranges button")[+e.key - 1].click();
});

// deep link to an incident: #i=<id>
{
  const i = new URLSearchParams(location.hash.slice(1)).get("i");
  if (i) openIncident(i);
}
