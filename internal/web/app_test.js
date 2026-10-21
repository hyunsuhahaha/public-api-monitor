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
