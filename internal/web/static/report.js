// "나도 안 돼요" on the answer pages: the gateway (deploy/access) keeps the reports.
// Without it (a local server) the box stays hidden.
(() => {
  const box = document.getElementById("report");
  const list = document.querySelectorAll("[data-report]");
  const say = (c) => (c && c.lastHour ? `최근 1시간 ${c.lastHour}명이 안 된다고 알렸습니다` : c && c.last24h ? `최근 24시간 ${c.last24h}명이 안 된다고 알렸습니다` : "아직 제보가 없습니다");
  if (box) {
    const id = box.dataset.target, count = box.querySelector(".count"), btn = box.querySelector("button");
    fetch("/__report/summary?target=" + encodeURIComponent(id)).then((r) => (r.ok ? r.json() : Promise.reject()))
      .then((d) => { box.hidden = false; count.textContent = say(d.counts[id]); })
      .catch(() => {});
    btn.addEventListener("click", () => {
      btn.disabled = true;
      fetch("/__report", { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ target: id }) })
        .then((r) => (r.ok ? r.json() : Promise.reject(r.status)))
        .then((d) => { count.textContent = say(d.counts[id]); btn.textContent = d.counted ? "알려 주셔서 고맙습니다" : "이미 알려 주셨습니다"; })
        .catch((s) => { btn.disabled = false; count.textContent = s === 429 ? "잠시 뒤에 다시 해 주세요" : "지금은 보낼 수 없습니다"; });
    });
  }
  if (list.length) {
    fetch("/__report/summary").then((r) => (r.ok ? r.json() : Promise.reject())).then((d) => {
      list.forEach((el) => {
        const c = d.counts[el.dataset.report];
        if (c && c.lastHour) { el.textContent = ` · 제보 ${c.lastHour}`; el.hidden = false; }
      });
    }).catch(() => {});
  }
})();
