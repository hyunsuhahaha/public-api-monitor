const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");
const path = require("node:path");

// Exercise the dashboard's real advice function without starting a browser/server.
const source = fs.readFileSync(path.join(__dirname, "static/app.js"), "utf8");
const advice = vm.runInNewContext(source.slice(source.indexOf("function clientAdvice("), source.indexOf("function renderCorrel(")) + "\nclientAdvice");

test("body quota code takes precedence over a generic HTTP 403", () => {
  const [cause, action] = advice("http 403: code 22: LIMIT_EXCEEDED");
  assert.equal(cause, "일일 호출 한도");
  assert.match(action, /운영계정 신청/);
});
test("per-second quota and unknown 429 are distinct from daily quota", () => {
  assert.equal(advice("code 23: TOO_MANY_REQUESTS")[0], "초당 호출 제한");
  assert.equal(advice("http 429: 호출 제한")[0], "호출 제한 확인");
});
test("a bare 403 does not claim approval is missing", () => {
  assert.match(advice("http 403")[1], /확정할 수 없습니다/);
  assert.equal(advice("code 31")[0], "키 사용기간 만료");
  assert.equal(advice("code 30")[0], "인증키 확인");
});

test("map quotas do not inherit public-portal traffic advice", () => {
  for (const code of ["KAKAO:-10", "KAKAO:-11", "NAVER:400"]) {
    const [cause, action] = advice(`code ${code}: quota`);
    assert.equal(cause, "쿼터·이용 한도 확인");
    assert.doesNotMatch(action, /운영계정 신청/);
  }
  assert.equal(advice("code NAVER:410: throttle")[0], "호출 속도 제한");
  assert.equal(advice("code KAKAO:-401: invalid key")[0], "인증·요청 설정 확인");
  assert.equal(advice("credential not set (NAVER_MAPS_CLIENT_SECRET)")[0], "인증 설정 필요");
  assert.equal(advice("prerequisite: manifest not checked; registry auth http 503")[0], "선행 인증 검사 실패");
});

test("vendor HTTP 400 processing errors are attributed to the provider", () => {
  const ops = fs.readFileSync(path.join(__dirname, "static/ops.js"), "utf8");
  const locate = vm.runInNewContext(ops.slice(ops.indexOf("function whereItFailed("), ops.indexOf("const RUNBOOK")) + "\nwhereItFailed", {
    sev: (status) => ({ ok: 0, slow: 1, stale: 1, error: 2, down: 2, client: -1 }[status]),
    ms: (n) => `${n}ms`,
  });
  const result = locate([{ status: "error", http: 400, code: "KAKAO:-7" }]);
  assert.equal(result.who, "공급자 서버");
  assert.match(result.detail, /KAKAO:-7/);
  assert.equal(locate([{ status: "client", http: 400, code: "KAKAO:-10" }]).who, "알 수 없음");
});

// 최근 확인 must render whatever trace.js does: renderLog once referenced a variable of
// the function it was split from, and the table came up empty in production.
test("renderLog draws every row and hands each to the trace card with its target", () => {
  const node = (tag) => ({ tag, children: [], className: "", textContent: "", title: "", style: {}, dataset: {},
    append(...xs) { this.children.push(...xs); }, prepend(x) { this.children.unshift(x); }, replaceChildren() { this.children = []; },
    querySelector() { return null; }, classList: { add() {}, remove() {}, toggle() {} } });
  const log = node("table"), attached = [];
  const ctx = {
    $: () => log, document: { createTextNode: (t) => ({ text: t }) },
    el: (tag, cls, text) => Object.assign(node(tag), { className: cls || "", textContent: text ?? "" }),
    kst: () => "10. 26. 09:00:00", ms: (v) => v + "ms", LABEL: { ok: "정상", down: "장애" }, PHASE_HELP: "",
    phaseCell: () => node("td"), state: { target: "a", data: { targets: [{ id: "a", name: "A" }] } },
    attachTrace: (tr, c, t) => attached.push([c.status, t && t.id]), console,
  };
  const renderLog = vm.runInNewContext(source.slice(source.indexOf("function renderLog("), source.indexOf("async function loadMoreChecks(")) + "\nrenderLog", ctx);
  renderLog([{ at: "2026-10-26T00:00:00Z", status: "ok", latencyMs: 80 }, { at: "2026-10-26T00:05:00Z", status: "down", latencyMs: 5000, reason: "timeout: TLS 연결 중" }]);
  assert.equal(log.children.length, 3, "header and two rows");
  assert.deepEqual(attached, [["ok", "a"], ["down", "a"]]);
  // a trace card that throws costs the card, never the table
  ctx.attachTrace = () => { throw new Error("boom"); };
  ctx.console = { error() {} };
  const again = vm.runInNewContext(source.slice(source.indexOf("function renderLog("), source.indexOf("async function loadMoreChecks(")) + "\nrenderLog", ctx);
  again([{ at: "2026-10-26T00:00:00Z", status: "ok", latencyMs: 80 }]);
  assert.equal(log.children.length, 2);
});
