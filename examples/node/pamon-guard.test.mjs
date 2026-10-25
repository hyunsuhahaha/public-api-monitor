// node --test examples/node/pamon-guard.test.mjs
import { test } from "node:test";
import assert from "node:assert/strict";
import { PamonGuard, PamonSaysNo } from "./pamon-guard.mjs";

function fakePamon(state) {
  let reads = 0;
  const f = async (url) => {
    reads++;
    if (state.down) throw new Error("pamon unreachable");
    const id = decodeURIComponent(url.split("/").pop().replace(/\.json$/, ""));
    return { ok: true, json: async () => ({ targets: [{ id, freshUntil: state.freshUntil, retryAdvice: { action: state.action, reason: "test" } }] }) };
  };
  return { f, reads: () => reads };
}

const soon = () => new Date(Date.now() + 600_000).toISOString();
const noSleep = async () => {};

test("pamon sees the outage: no call at all", async () => {
  const p = fakePamon({ action: "wait", freshUntil: soon() });
  const g = new PamonGuard({ fetch: p.f, sleep: noSleep });
  let calls = 0;
  const all = await Promise.allSettled(Array.from({ length: 50 }, () => g.call("x", async () => { calls++; })));
  assert.equal(calls, 0);
  assert.ok(all.every((r) => r.status === "rejected" && r.reason instanceof PamonSaysNo));
  assert.equal(p.reads(), 1, "50 concurrent calls share one read");
});

test("maintenance: skip", async () => {
  const g = new PamonGuard({ fetch: fakePamon({ action: "skip", freshUntil: soon() }).f, sleep: noSleep });
  await assert.rejects(g.call("x", async () => 1), (e) => e.advice.action === "skip");
});

test("pamon says retry: retries a few times, then gives up with the API's error", async () => {
  const g = new PamonGuard({ fetch: fakePamon({ action: "retry", freshUntil: soon() }).f, sleep: noSleep });
  let calls = 0;
  await assert.rejects(g.call("x", async () => { calls++; throw new Error("503"); }), /503/);
  assert.equal(calls, 3);
  calls = 0;
  assert.equal(await g.call("x", async () => (++calls < 2 ? Promise.reject(new Error("blip")) : "ok")), "ok");
});

test("a 4xx is not retried", async () => {
  const g = new PamonGuard({ fetch: fakePamon({ action: "retry", freshUntil: soon() }).f, sleep: noSleep });
  let calls = 0;
  await assert.rejects(g.call("x", async () => { calls++; throw Object.assign(new Error("404"), { status: 404 }); }, { permanent: (e) => e.status < 500 }));
  assert.equal(calls, 1);
});

test("pamon unreachable or stale: calls go ahead", async () => {
  for (const state of [{ down: true }, { action: "wait", freshUntil: new Date(Date.now() - 1000).toISOString() }]) {
    const g = new PamonGuard({ fetch: fakePamon(state).f, sleep: noSleep });
    assert.equal(await g.call("x", async () => "ok"), "ok");
  }
});

test("pamon learns of the outage between retries: retries stop", async () => {
  const state = { action: "retry", freshUntil: soon() };
  const g = new PamonGuard({ fetch: fakePamon(state).f, minRefreshMs: 0, sleep: async () => { state.action = "wait"; } });
  let calls = 0;
  await assert.rejects(g.call("x", async () => { calls++; throw new Error("503"); }), PamonSaysNo);
  assert.equal(calls, 1);
});
