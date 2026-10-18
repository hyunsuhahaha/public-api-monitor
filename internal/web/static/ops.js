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
    statPanel("응답 시간 p50", ms(lat[Math.floor(lat.length / 2)] || 0), "장애 구간, 응답 온 것만", "c-blue-t"));

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
    "## 원인", "", "- (제공 기관 공지·문의 결과)", "",
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

$("im-close").onclick = closeIncident;
$("inc-modal").onclick = (e) => { if (e.target.id === "inc-modal") closeIncident(); };
$("im-copy-md").onclick = (e) => currentIncident && copy(postmortemMarkdown(currentIncident), e.target);
$("im-copy-link").onclick = (e) => currentIncident && copy(`${location.origin}/#i=${currentIncident.incident.id}`, e.target);

// ── command palette ───────────────────────────────────────────────────────────
let palItems = [], palSel = 0;

function paletteCommands() {
  const cmds = RANGES.map((r) => ({ label: `기간: ${RANGE_LABEL[r]}`, hint: "명령", run: () => document.querySelectorAll("#ranges button")[RANGES.indexOf(r)].click() }));
  cmds.push({ label: "월간 가용성 리포트 열기", hint: "명령", run: () => (location.href = "/report") },
    { label: "장애 피드(Atom) 열기", hint: "명령", run: () => (location.href = "/feed.xml") },
    { label: "키보드 단축키 보기", hint: "?", run: () => ($("help").hidden = false) });
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
  else if (/^[1-5]$/.test(e.key)) document.querySelectorAll("#ranges button")[+e.key - 1].click();
});

// deep link to an incident: #i=<id>
{
  const i = new URLSearchParams(location.hash.slice(1)).get("i");
  if (i) openIncident(i);
}
