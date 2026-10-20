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

// the checker's reason in words an API user reads at a glance
function why(r) {
  if (!r) return "";
  r = r.replace(/ \(usually .*\)$/, "");
  const dur = (x) => x.replace(/(\d+)h/, "$1시간 ").replace(/(\d+)m(?!s)/, "$1분 ").replace(/(\d+(?:\.\d+)?)s/, "$1초").replace(/(\d+)ms/, "$1ms").replace(/ 0초$/, "").trim();
  let m;
  if ((m = r.match(/^slow: (.+)$/))) return `응답이 ${dur(m[1])} 걸림`;
  if ((m = r.match(/^stale: newest data (.+) old$/))) return `최신 데이터가 ${dur(m[1])} 전 것 (갱신 멈춤)`;
  if ((m = r.match(/^bad value: (.+)$/))) return `값 이상: ${m[1].replace(/ missing \(.*\)$/, " 항목이 응답에 없음")}`;
  if ((m = r.match(/^timeout: (.+)$/))) return `응답 없음 (시간 초과) — ${m[1]}`;
  if ((m = r.match(/^http (\d+)/))) return `HTTP ${m[1]} 응답` + (r.includes(":") ? " — " + r.split(":").slice(1).join(":").trim() : "");
  if ((m = r.match(/^code (\S+): (.+)$/))) return `오류 코드 ${m[1]} — ${m[2]}`;
  return r;
}

function lasting(t, open) {
  const i = open[t.id];
  if (!i) return "";
  const m = Math.max(1, Math.round((Date.now() - Date.parse(i.started)) / 60000));
  return (m >= 60 ? `${Math.floor(m / 60)}시간 ${m % 60}분째` : `${m}분째`);
}

const rank = (t) => ({ out: 0, deg: 1, maint: 2 })[(NOW[t.status] || [])[1]] ?? 3;

function render(st, incs) {
  const ts = st.targets.filter((t) => t.status !== "client"); // our side is not the API's state
  const bad = ts.filter((t) => rank(t) === 0), deg = ts.filter((t) => rank(t) === 1);
  const open = {};
  for (const i of incs) if (i.ended.startsWith("0001")) open[i.target] = i;
  const banner = $("banner");
  banner.className = "banner " + (bad.length ? "out" : deg.length ? "deg" : "ok");
  const title = bad.length ? `API ${bad.length}개 장애` + (deg.length ? ` · ${deg.length}개 지연·데이터 이상` : "")
    : deg.length ? `API ${deg.length}개가 느리거나 데이터가 이상합니다` : "모든 API가 정상입니다";
  banner.replaceChildren(el("div", "title", title),
    el("small", "", `${ts.length}개 API · ${kst(st.generatedAt, { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" })} 확인`));
  if (bad.length + deg.length) {
    const ul = el("ul", "probs");
    for (const t of [...bad, ...deg]) {
      const [label, cls] = NOW[t.status];
      const li = el("li"), nm = el("div", "nm", t.name);
      nm.append(el("small", "", t.provider), el("span", "why", why(t.reason)));
      li.append(el("span", "lab st " + cls, label), nm, el("span", "dur", lasting(t, open)));
      li.onclick = () => { const r = $("api-" + t.id); if (!r) return; r.closest("details").open = true; r.scrollIntoView({ behavior: "smooth", block: "center" }); r.classList.remove("flash"); void r.offsetWidth; r.classList.add("flash"); };
      ul.append(li);
    }
    banner.append(ul);
  }

  const groups = {};
  for (const t of st.targets) (groups[t.provider] = groups[t.provider] || []).push(t);
  const worstOf = (list) => Math.min(...list.map(rank));
  const box = $("groups"); box.replaceChildren();
  for (const [prov, list] of Object.entries(groups).sort((a, b) => worstOf(a[1]) - worstOf(b[1]) || a[0].localeCompare(b[0], "ko"))) {
    const w = worstOf(list), worst = w === 0 ? "out" : w === 1 ? "deg" : "ok";
    const g = el("details", "group"); g.open = worst !== "ok" || Object.keys(groups).length <= 3;
    const s = el("summary"); s.append(el("span", "", `${prov} (${list.length})`), el("span", "st " + worst, worst === "ok" ? "정상" : worst === "deg" ? "일부 지연" : "일부 장애"));
    g.append(s);
    for (const t of [...list].sort((a, b) => rank(a) - rank(b))) {
      const [label, cls] = NOW[t.status] || [t.status, "none"];
      const a = el("div", "api"), h = el("div", "api-h"), name = el("b", "", t.name);
      a.id = "api-" + t.id;
      if (t.docs) { const l = el("a", "", ""); l.href = t.docs; l.append(name); h.append(l); } else h.append(name);
      h.append(el("span", "st " + cls, label + (rank(t) < 2 && lasting(t, open) ? ` · ${lasting(t, open)}` : "")));
      a.append(h);
      if (rank(t) < 2 && t.reason) a.append(el("div", "why", why(t.reason)));
      const sc = el("div", "scale muted");
      sc.append(el("span", "d90", "90일 전"), el("span", "d30", "30일 전"), el("span", "", `90일 가용률 ${pct(t.uptime90d)}`), el("span", "", "오늘"));
      a.append(bars(t.days), sc);
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

let lastOk = Date.now();
async function load() {
  try {
    const get = (u) => fetch(u).then((r) => { if (!r.ok) throw new Error(u + " " + r.status); return r.json(); });
    const [st, incs] = await Promise.all([get("/api/v1/status"), get("/api/v1/incidents")]);
    render(st, incs);
    lastOk = Date.now();
  } catch (e) {
    // keep what is on screen but never pass it off as current; try again soon
    if (!document.querySelector(".api")) $("banner").textContent = "상태를 불러오지 못했습니다. 잠시 후 다시 시도합니다.";
    else {
      const b = $("banner");
      b.className = "banner deg";
      let note = b.querySelector(".stale");
      if (!note) { note = el("small", "stale"); b.append(note); }
      note.textContent = `⚠ 서버에 연결하지 못해 ${Math.max(1, Math.round((Date.now() - lastOk) / 60000))}분 전 정보를 보여주는 중입니다`;
    }
    setTimeout(load, 5000);
  }
}
load();
setInterval(load, 60000);
