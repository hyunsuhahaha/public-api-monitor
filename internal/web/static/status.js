// Public status page: for people who use these APIs, not for the operator. Per agency,
// each API's state now and a bar per day for 90 days, then the last two weeks' incidents.
const $ = (id) => document.getElementById(id);
const el = (tag, cls, text) => { const e = document.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; };
const pct = (x) => (x < 0 ? "–" : (x * 100).toFixed(x >= 0.9995 ? 1 : 2) + "%");
const NOW = { ok: ["정상", "ok"], slow: ["느림", "deg"], stale: ["데이터 이상", "deg"], error: ["오류", "out"], down: ["장애", "out"],
  maint: ["점검 중", "maint"], client: ["확인 중", "none"], unknown: ["측정 전", "none"] };
const kst = (d, o) => new Date(d).toLocaleString("ko-KR", Object.assign({ timeZone: "Asia/Seoul", hour12: false }, o));

function dayClass(d) {
  if (!d || !d.counted) return "";
  if (d.outage) return "out";
  if (d.degraded) return "deg";
  return "ok";
}

function bars(days) {
  const by = Object.fromEntries((days || []).map((d) => [d.date, d]));
  const box = el("div", "bars");
  const today = new Date(Date.now() + 9 * 3600e3); // KST date
  for (let i = 89; i >= 0; i--) {
    const date = new Date(today - i * 86400e3).toISOString().slice(0, 10);
    const d = by[date], b = el("i", dayClass(d));
    b.dataset.tip = !d || !d.counted ? `${date} · 기록 없음`
      : `${date} · 가용률 ${pct(d.healthy / d.counted)}` + (d.outage ? ` · 실패 ${d.outage}회` : "") + (d.degraded ? ` · 저하 ${d.degraded}회` : "");
    box.append(b);
  }
  return box;
}

function render(st, incs) {
  const ts = st.targets.filter((t) => t.status !== "client"); // our side is not the API's state
  const bad = ts.filter((t) => NOW[t.status] && NOW[t.status][1] === "out"), deg = ts.filter((t) => NOW[t.status] && NOW[t.status][1] === "deg");
  const banner = $("banner");
  banner.className = "banner " + (bad.length ? "out" : deg.length ? "deg" : "ok");
  banner.replaceChildren(document.createTextNode(bad.length ? `API ${bad.length}개에 장애가 있습니다` : deg.length ? `API ${deg.length}개가 느리거나 데이터가 늦습니다` : "모든 API가 정상입니다"),
    el("small", "", `${ts.length}개 API · ${kst(st.generatedAt, { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" })} 기준`));

  const groups = {};
  for (const t of st.targets) (groups[t.provider] = groups[t.provider] || []).push(t);
  const box = $("groups"); box.replaceChildren();
  for (const [prov, list] of Object.entries(groups).sort((a, b) => a[0].localeCompare(b[0], "ko"))) {
    const worst = list.some((t) => NOW[t.status]?.[1] === "out") ? "out" : list.some((t) => NOW[t.status]?.[1] === "deg") ? "deg" : "ok";
    const g = el("details", "group"); g.open = worst !== "ok" || Object.keys(groups).length <= 3;
    const s = el("summary"); s.append(el("span", "", `${prov} (${list.length})`), el("span", "st " + worst, worst === "ok" ? "정상" : worst === "deg" ? "일부 지연" : "일부 장애"));
    g.append(s);
    for (const t of list) {
      const [label, cls] = NOW[t.status] || [t.status, "none"];
      const a = el("div", "api"), h = el("div", "api-h"), name = el("b", "", t.name);
      if (t.docs) { const l = el("a", "", ""); l.href = t.docs; l.append(name); h.append(l); } else h.append(name);
      h.append(el("span", "st " + cls, label));
      const sc = el("div", "scale muted");
      sc.append(el("span", "d90", "90일 전"), el("span", "d30", "30일 전"), el("span", "", `90일 가용률 ${pct(t.uptime90d)}`), el("span", "", "오늘"));
      a.append(h, bars(t.days), sc);
      g.append(a);
    }
    box.append(g);
  }

  const names = Object.fromEntries(st.targets.map((t) => [t.id, t.name]));
  const since = Date.now() - 14 * 86400e3;
  const recent = incs.filter((i) => Date.parse(i.started) >= since);
  const ib = $("incs"); ib.replaceChildren();
  if (!recent.length) ib.append(el("p", "muted", "지난 14일 동안 장애가 없었습니다."));
  for (const i of recent.slice(0, 40)) {
    const open = i.ended.startsWith("0001");
    const mins = Math.round(((open ? Date.now() : Date.parse(i.ended)) - Date.parse(i.started)) / 60000);
    const c = el("div", "inc");
    c.append(el("div", "t", `${names[i.target] || i.target} — ${i.severity >= 2 ? "장애" : "성능 저하"}${open ? " (진행 중)" : ""}`),
      el("div", "muted", `${kst(i.started, { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" })}부터 ${mins >= 60 ? Math.floor(mins / 60) + "시간 " : ""}${mins % 60}분` +
        ` · ${(i.lastReason || i.reason).replace(/ \(.*\)$/, "")}`));
    ib.append(c);
  }
}

const tip = $("tip");
document.addEventListener("mouseover", (e) => {
  const t = e.target.dataset && e.target.dataset.tip;
  if (!t) { tip.style.display = "none"; return; }
  tip.textContent = t; tip.style.display = "block";
  const r = e.target.getBoundingClientRect();
  tip.style.left = Math.min(innerWidth - 270, Math.max(4, r.left - 60)) + "px"; tip.style.top = (r.bottom + 6) + "px";
});

async function load() {
  try {
    const get = (u) => fetch(u).then((r) => { if (!r.ok) throw new Error(u + " " + r.status); return r.json(); });
    const [st, incs] = await Promise.all([get("/api/v1/status"), get("/api/v1/incidents")]);
    render(st, incs);
  } catch (e) {
    // keep what is on screen; say the page is stale and try again soon
    if (!document.querySelector(".api")) $("banner").textContent = "상태를 불러오지 못했습니다. 잠시 후 다시 시도합니다.";
    setTimeout(load, 5000);
  }
}
load();
setInterval(load, 60000);
