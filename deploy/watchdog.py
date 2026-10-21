"""Check the published snapshot independently of the monitor's self-dispatch chain."""
import json
import sys
from datetime import datetime, timezone
from urllib.request import Request, urlopen


def snapshot_age(snapshot, now):
    generated = datetime.fromisoformat(snapshot["generatedAt"].replace("Z", "+00:00"))
    if generated.tzinfo is None:
        raise ValueError("generatedAt must include a timezone")
    age = (now - generated).total_seconds()
    if age < -60:
        raise ValueError("snapshot time is in the future")
    if age > 600:
        raise ValueError(f"monitor publication stopped: snapshot is {int(age)}s old (limit 600s)")
    if not isinstance(snapshot.get("targets"), list) or not snapshot["targets"]:
        raise ValueError("snapshot contains no targets")
    return age


def main():
    request = Request(sys.argv[1], headers={"Cache-Control": "no-cache", "User-Agent": "pamon-watchdog"})
    with urlopen(request, timeout=20) as response:
        snapshot = json.load(response)
    age = snapshot_age(snapshot, datetime.now(timezone.utc))
    print(f"monitor snapshot is fresh ({max(0, int(age))}s old)")


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print(f"monitor watchdog failed: {error}", file=sys.stderr)
        sys.exit(1)
