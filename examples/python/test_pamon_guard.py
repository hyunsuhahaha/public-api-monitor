# python3 -m unittest discover -s examples/python
import threading
import time
import unittest
from datetime import datetime, timedelta, timezone

from pamon_guard import PamonGuard, PamonSaysNo


def iso(seconds):
    # as Go writes it: nanoseconds and a zone offset
    t = datetime.now(timezone(timedelta(hours=9))) + timedelta(seconds=seconds)
    return t.strftime("%Y-%m-%dT%H:%M:%S.%f") + "123+09:00"


class Fake:
    def __init__(self, action="retry", fresh=600, down=False):
        self.action, self.fresh, self.down, self.reads = action, fresh, down, 0

    def __call__(self, url):
        self.reads += 1
        time.sleep(0.01)
        if self.down:
            raise OSError("unreachable")
        tid = url.rsplit("/", 1)[1][:-5]
        return {"targets": [{"id": tid, "freshUntil": iso(self.fresh), "retryAdvice": {"action": self.action, "reason": "t"}}]}


def no_sleep(_):
    pass


class GuardTest(unittest.TestCase):
    def test_outage_means_no_calls(self):
        f = Fake("wait")
        g = PamonGuard(read=f, sleep=no_sleep)
        calls, errors = [], []

        def worker():
            try:
                g.call("x", lambda: calls.append(1))
            except PamonSaysNo as e:
                errors.append(e)

        ts = [threading.Thread(target=worker) for _ in range(20)]
        [t.start() for t in ts]
        [t.join() for t in ts]
        self.assertEqual((len(calls), len(errors), f.reads), (0, 20, 1))

    def test_retry_then_give_up(self):
        g = PamonGuard(read=Fake("retry"), sleep=no_sleep)
        n = []

        def fail():
            n.append(1)
            raise RuntimeError("503")

        with self.assertRaisesRegex(RuntimeError, "503"):
            g.call("x", fail)
        self.assertEqual(len(n), 3)

    def test_permanent_not_retried(self):
        g = PamonGuard(read=Fake("retry"), sleep=no_sleep)
        n = []

        def fail():
            n.append(1)
            raise ValueError("404")

        with self.assertRaises(ValueError):
            g.call("x", fail, permanent=lambda e: isinstance(e, ValueError))
        self.assertEqual(len(n), 1)

    def test_fail_open(self):
        for f in (Fake(down=True), Fake("wait", fresh=-5)):
            self.assertEqual(PamonGuard(read=f, sleep=no_sleep).call("x", lambda: "ok"), "ok")

    def test_retries_stop_when_pamon_confirms(self):
        f = Fake("retry")
        g = PamonGuard(read=f, min_refresh=0, sleep=lambda _: setattr(f, "action", "wait"))
        n = []

        def fail():
            n.append(1)
            raise RuntimeError("503")

        with self.assertRaises(PamonSaysNo):
            g.call("x", fail)
        self.assertEqual(len(n), 1)

    def test_skip(self):
        with self.assertRaises(PamonSaysNo) as c:
            PamonGuard(read=Fake("skip"), sleep=no_sleep).call("x", lambda: 1)
        self.assertEqual(c.exception.advice["action"], "skip")


if __name__ == "__main__":
    unittest.main()
