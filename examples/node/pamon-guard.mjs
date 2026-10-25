// pamon-guard: ask pamon before calling (and before retrying) an API it monitors.
// No dependencies (Node 18+ fetch). Copy this file into your project.
//
//   import { PamonGuard } from "./pamon-guard.mjs";
//   const guard = new PamonGuard();
//   const data = await guard.call("airkorea-sido", () => fetchAirKorea());
//
// It reads https://pamon.hyunsuhahaha.workers.dev/ai/targets/{id}.json, whose
// retryAdvice.action says: retry (nothing seen from outside), backoff (slow), wait
// (pamon sees it failing), skip (announced maintenance) or unknown. On wait and skip it
// does not call at all and throws PamonSaysNo. pamon is a signal, not a dependency:
// when it cannot be read, or its observation is past freshUntil, calls go ahead.

export class PamonSaysNo extends Error {
  constructor(advice, lastError) {
    super(`pamon: ${advice.action}${advice.reason ? ` (${advice.reason})` : ""}`);
    this.name = "PamonSaysNo";
    this.advice = advice;
    this.lastError = lastError;
  }
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

export class PamonGuard {
  constructor({ base = "https://pamon.hyunsuhahaha.workers.dev", minRefreshMs = 60_000, fetch: f = globalThis.fetch, now = Date.now, sleep: s = sleep } = {}) {
    this.base = base.replace(/\/$/, "");
    this.minRefreshMs = minRefreshMs;
    this.fetch = f;
    this.now = now;
    this.sleep = s;
    this.cache = new Map(); // id -> { at, doc, pending }
  }

  // advice returns pamon's retryAdvice for a target, or {action: "unknown"} when pamon
  // cannot be read or its observation is stale. Cached for minRefreshMs; concurrent
  // callers share one read.
  async advice(id) {
    const now = this.now();
    let c = this.cache.get(id);
    if (!c || (now - c.at >= this.minRefreshMs && !c.pending)) {
      c = { at: now, doc: c?.doc, pending: null };
      c.pending = this.#read(id).then((doc) => { c.doc = doc ?? c.doc; c.pending = null; });
      this.cache.set(id, c);
    }
    if (c.pending) await c.pending;
    const t = c.doc;
    if (!t || !t.freshUntil || this.now() >= Date.parse(t.freshUntil)) return { action: "unknown", reason: "no fresh observation" };
    return t.retryAdvice ?? { action: "unknown" };
  }

  async #read(id) {
    try {
      const r = await this.fetch(`${this.base}/ai/targets/${encodeURIComponent(id)}.json`, { signal: AbortSignal.timeout(5000) });
      if (!r.ok) return null;
      const doc = await r.json();
      return doc.targets?.find((t) => t.id === id) ?? null;
    } catch {
      return null; // fail open
    }
  }

  // call runs fn with up to `attempts` tries and growing, jittered waits, asking pamon
  // first and again after every wait. permanent(err) => true stops retrying (4xx).
  async call(id, fn, { attempts = 3, baseMs = 200, maxMs = 5000, permanent = () => false } = {}) {
    let last;
    for (let i = 0; i < attempts; i++) {
      if (i > 0) {
        const d = Math.min(baseMs * 2 ** (i - 1), maxMs);
        await this.sleep(d / 2 + Math.random() * d / 2);
      }
      const a = await this.advice(id);
      if (a.action === "wait" || a.action === "skip") throw new PamonSaysNo(a, last);
      try {
        return await fn();
      } catch (err) {
        last = err;
        if (permanent(err)) throw err;
      }
    }
    throw last;
  }
}
