import unittest
from datetime import datetime, timedelta, timezone

from watchdog import snapshot_age


class SnapshotAgeTest(unittest.TestCase):
    def test_fresh_and_failure_cases(self):
        now = datetime(2026, 10, 21, tzinfo=timezone.utc)
        for seconds, good in [(0, True), (600, True), (601, False), (-61, False)]:
            with self.subTest(seconds=seconds):
                snapshot = {"generatedAt": (now - timedelta(seconds=seconds)).isoformat(), "targets": [{"status": "down"}]}
                if good:
                    self.assertEqual(snapshot_age(snapshot, now), seconds)
                else:
                    with self.assertRaises(ValueError):
                        snapshot_age(snapshot, now)
        for snapshot in [{}, {"generatedAt": "invalid"}, {"generatedAt": "2026-10-21T00:00:00"}, {"generatedAt": now.isoformat(), "targets": []}]:
            with self.subTest(snapshot=snapshot), self.assertRaises((KeyError, ValueError)):
                snapshot_age(snapshot, now)


if __name__ == "__main__":
    unittest.main()
