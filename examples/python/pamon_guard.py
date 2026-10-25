"""pamon_guard: ask pamon before calling (and before retrying) an API it monitors.

Standard library only. Copy this file into your project.

    from pamon_guard import PamonGuard, PamonSaysNo
    guard = PamonGuard()
    data = guard.call("airkorea-sido", fetch_airkorea)

It reads https://pamon.hyunsuhahaha.workers.dev/ai/targets/{id}.json, whose
retryAdvice.action says: retry (nothing seen from outside), backoff (slow), wait (pamon
sees it failing), skip (announced maintenance) or unknown. On wait and skip it does not
call at all and raises PamonSaysNo. pamon is a signal, not a dependency: when it cannot
be read, or its observation is past freshUntil, calls go ahead.
"""
import json
import random
import re
import threading
import time
import urllib.parse
import urllib.request
from datetime import datetime

DEFAULT_BASE = "https://pamon.hyunsuhahaha.workers.dev"


class PamonSaysNo(Exception):
    def __init__(self, advice, last_error=None):
        super().__init__(f"pamon: {advice.get('action')} ({advice.get('reason', '')})")
        self.advice = advice
        self.last_error = last_error


def _read_json(url, timeout=5):
    with urllib.request.urlopen(url, timeout=timeout) as r:
        return json.load(r)


class PamonGuard:
    def __init__(self, base=DEFAULT_BASE, min_refresh=60.0, read=_read_json, now=time.time, sleep=time.sleep):
        self.base = base.rstrip("/")
        self.min_refresh = min_refresh
        self.read, self.now, self.sleep = read, now, sleep
        self._lock = threading.Lock()
        self._cache = {}  # id -> (read_at, target doc or None)
        self._reading = {}  # id -> Lock: concurrent callers wait for one read

    def advice(self, target_id):
        """pamon's retryAdvice, or {"action": "unknown"} without a fresh observation."""
        with self._lock:
            lock = self._reading.setdefault(target_id, threading.Lock())
        with lock:
            at, doc = self._cache.get(target_id, (None, None))
            if at is None or self.now() - at >= self.min_refresh:
                fresh = self._fetch(target_id)
                doc = fresh if fresh is not None else doc
                self._cache[target_id] = (self.now(), doc)
        if not doc or not doc.get("freshUntil"):
            return {"action": "unknown", "reason": "no observation"}
        # Go writes nanoseconds and "Z"; fromisoformat before 3.11 takes neither
        stamp = re.sub(r"(\.\d{6})\d+", r"\1", doc["freshUntil"].replace("Z", "+00:00"))
        until = datetime.fromisoformat(stamp).timestamp()
        if self.now() >= until:
            return {"action": "unknown", "reason": "observation expired"}
        return doc.get("retryAdvice") or {"action": "unknown"}

    def _fetch(self, target_id):
        try:
            d = self.read(f"{self.base}/ai/targets/{urllib.parse.quote(target_id)}.json")
            return next((t for t in d.get("targets", []) if t.get("id") == target_id), None)
        except Exception:
            return None  # fail open

    def call(self, target_id, fn, attempts=3, base_delay=0.2, max_delay=5.0, permanent=lambda e: False):
        """Run fn with a few retries and growing, jittered waits, asking pamon first and
        again after every wait. permanent(err) -> True stops retrying (a 4xx)."""
        last = None
        for i in range(attempts):
            if i:
                d = min(base_delay * 2 ** (i - 1), max_delay)
                self.sleep(d / 2 + random.random() * d / 2)
            a = self.advice(target_id)
            if a.get("action") in ("wait", "skip"):
                raise PamonSaysNo(a, last)
            try:
                return fn()
            except Exception as e:  # noqa: BLE001 - the caller's error, re-raised below
                last = e
                if permanent(e):
                    raise
        raise last
