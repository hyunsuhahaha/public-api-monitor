"""Game day for pamon: break a fake API (and the monitor itself) on purpose and measure
how fast the monitor notices, whether it stays quiet when it should, and whether it loses
anything. Writes docs/gameday/RESULTS.md and results.json.

    python deploy/gameday/gameday.py            # needs Docker; ~10 minutes
    python deploy/gameday/gameday.py --keep     # leave the stack running afterwards

Scenarios (the fake API is checked every 10 s from two locations, "central" and "probe"):
  1 hard outage        HTTP 503 everywhere      -> incident opens, then closes on recovery
  2 silent failure     HTTP 200 + resultCode 05 -> still an outage (a plain HTTP check says up)
  3 regional failure   only "probe" loses the API -> no incident, noted as regional
  4 central outage     central down 45 s        -> probe queues, then delivers everything
  5 restart mid-incident central restarts      -> the same incident stays open, then closes
  6 stale data         200 OK but 6 h old data  -> "stale" (degraded)
  7 adaptive slow      600 ms on a ~10 ms API   -> "slow (usually …)" under the 2 s limit
"""
import argparse
import json
import os
import subprocess
import sys
import time
import urllib.request
from datetime import datetime, timezone, timedelta

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.abspath(os.path.join(HERE, "..", ".."))
COMPOSE = ["docker", "compose", "-f", os.path.join(HERE, "compose.yml")]
CENTRAL = "http://localhost:28080"
PROBE = "http://localhost:28081"
FAKE = "http://localhost:29000"
INTERVAL = 10
KST = timezone(timedelta(hours=9))


def sh(*args, check=True):
    r = subprocess.run(list(args), capture_output=True, text=True, encoding="utf-8", errors="replace")
    if check and r.returncode != 0:
        raise RuntimeError(f"{' '.join(args)}: {r.stderr.strip()[-400:]}")
    return r.stdout


def get(url, timeout=5):
    with urllib.request.urlopen(url, timeout=timeout) as r:
        return r.read().decode("utf-8")


def get_json(url):
    return json.loads(get(url))


def ready(base, path="/readyz"):
    try:
        return get(base + path, timeout=2).strip() in ("ready", "ok")
    except Exception:
        return False


def mode(m, ms=0):
    req = urllib.request.Request(f"{FAKE}/admin/mode?m={m}&ms={ms}", method="POST")
    urllib.request.urlopen(req, timeout=5).read()


def target():
    for t in get_json(CENTRAL + "/api/v1/status")["targets"]:
        if t["id"] == "fake":
            return t
    raise RuntimeError("fake target missing")


def incidents():
    return get_json(CENTRAL + "/api/v1/incidents?target=fake")


def open_incident():
    for i in incidents():
        if i["ended"].startswith("0001"):
            return i
    return None


def metric(base, name):
    total = 0.0
    found = False
    for line in get(base + "/metrics").splitlines():
        if line.startswith(name + " ") or line.startswith(name + "{"):
            total += float(line.rsplit(" ", 1)[1])
            found = True
    return total if found else None


def wait(cond, timeout, poll=1.0):
    """Seconds until cond() is truthy, or None on timeout."""
    t0 = time.time()
    while time.time() - t0 < timeout:
        try:
            if cond():
                return round(time.time() - t0, 1)
        except Exception:
            pass
        time.sleep(poll)
    return None


results = []


def record(no, name, expect, ok, measured, note=""):
    results.append({"no": no, "name": name, "expect": expect, "pass": bool(ok), "measured": measured, "note": note})
    print(f"[{'PASS' if ok else 'FAIL'}] {no} {name}: {measured} {note}", flush=True)


def settle():
    mode("ok")
    wait(lambda: open_incident() is None and target()["status"] == "ok", 120)


def s1_hard_outage():
    settle()
    mode("down")
    t_open = wait(lambda: open_incident() is not None, 90)
    status = target()["status"]
    mode("ok")
    t_close = wait(lambda: open_incident() is None, 90)
    ok = t_open is not None and t_close is not None and status == "down" and t_open <= 3 * INTERVAL + 5
    record(1, "하드 장애 (HTTP 503)", "장애 감지 ≤ 3주기(35초), 복구 시 자동 종료", ok,
           f"감지 {t_open}초 · 복구 감지 {t_close}초", f"판정 {status}")


def s2_silent_failure():
    settle()
    mode("error05")
    t_open = wait(lambda: open_incident() is not None, 90)
    reason = (open_incident() or {}).get("reason", "")
    mode("ok")
    wait(lambda: open_incident() is None, 90)
    record(2, "조용한 장애 (HTTP 200 + resultCode 05)", "일반 HTTP 체크가 '정상'이라 하는 응답을 장애로 감지", t_open is not None and "05" in reason,
           f"감지 {t_open}초", f"원인: {reason}")


def s3_regional():
    settle()
    before = len(incidents())
    sh("docker", "network", "disconnect", "gameday_api", "gameday-probe-1")
    try:
        seen = wait(lambda: any(l["location"] == "probe" and l["status"] == "down" for l in target()["locations"]), 60)
        time.sleep(4 * INTERVAL)  # long enough that two verdicts in a row would have opened one
        t = target()
        quiet = len(incidents()) == before and open_incident() is None
        noted = "failing only from probe" in (t.get("reason") or "")
    finally:
        sh("docker", "network", "connect", "gameday_api", "gameday-probe-1")
    record(3, "한 지역만 장애 (probe의 API 경로 차단)", "장애를 열지 않고 '일부 지역에서만 실패'로 기록", seen is not None and quiet and noted,
           f"probe 실패 관측 {seen}초 · 40초 동안 새 장애 {len(incidents()) - before}건", f"판정 {t['status']}, 사유 '{t.get('reason', '')}'")


def s4_central_outage():
    settle()
    sent0 = metric(PROBE, "pamon_push_sent_total") or 0
    sh(*COMPOSE, "stop", "central")
    time.sleep(45)
    queued = metric(PROBE, "pamon_push_queue") or 0
    errors = metric(PROBE, "pamon_push_errors_total") or 0
    sh(*COMPOSE, "start", "central")
    t_ready = wait(lambda: ready(CENTRAL), 60)
    t_flush = wait(lambda: (metric(PROBE, "pamon_push_queue") or 0) == 0, 300)
    sent = (metric(PROBE, "pamon_push_sent_total") or 0) - sent0
    dropped = metric(PROBE, "pamon_push_dropped_total") or 0
    ok = queued > 0 and t_flush is not None and sent >= queued and dropped == 0
    record(4, "중앙 서버 45초 중단", "원격 감시기가 결과를 쌓아 두었다가 유실 없이 전달", ok,
           f"중단 중 쌓인 결과 {int(queued)}건 · 전송 실패 {int(errors)}회 · 재기동 {t_ready}초 · 큐 비움 {t_flush}초", f"전달 {int(sent)}건, 버림 {int(dropped)}건")


def s5_restart_mid_incident():
    settle()
    mode("down")
    wait(lambda: open_incident() is not None, 90)
    first = open_incident()
    sh(*COMPOSE, "restart", "central")
    wait(lambda: ready(CENTRAL), 60)
    time.sleep(2 * INTERVAL)
    during = open_incident()
    mode("ok")
    t_close = wait(lambda: open_incident() is None, 90)
    same = [i for i in incidents() if i["id"] == first["id"]]
    dup = [i for i in incidents() if i["started"] > first["started"] and i["id"] != first["id"]]
    ok = during is not None and during["id"] == first["id"] and same and not same[0]["ended"].startswith("0001") and not dup
    record(5, "장애 중 중앙 서버 재시작", "같은 장애가 이어지고 복구 때 그 장애가 닫힘 (중복 없음)", ok,
           f"재시작 후 장애 #{(during or {}).get('id')} 유지 · 복구 감지 {t_close}초", f"처음 #{first['id']}, 중복 {len(dup)}건")


def s6_stale():
    settle()
    mode("stale")
    t = wait(lambda: target()["status"] == "stale", 90)
    reason = target().get("reason", "")
    mode("ok")
    record(6, "데이터 갱신 멈춤 (200 OK, 6시간 전 데이터)", "응답은 정상이어도 '갱신 멈춤'으로 판정", t is not None,
           f"감지 {t}초", reason)


def s7_adaptive_slow(started):
    settle()
    # the central's baseline needs 24 healthy answers (4 minutes at 10 s)
    warm = 24 * INTERVAL + 20 - (time.time() - started)
    if warm > 0:
        time.sleep(warm)
    usual = target()["p50Ms"]
    mode("slow", 600)
    t = wait(lambda: target()["status"] == "slow", 60)
    reason = target().get("reason", "")
    mode("ok")
    record(7, "평소보다 느려짐 (600ms, 고정 기준 2초 미만)", "API별로 학습한 평소 속도 대비 느림으로 판정", t is not None and "usually" in reason,
           f"감지 {t}초", f"평소 p50 {usual}ms → {reason}")


def write_report(started_at, duration):
    os.makedirs(os.path.join(ROOT, "docs", "gameday"), exist_ok=True)
    with open(os.path.join(ROOT, "docs", "gameday", "results.json"), "w", encoding="utf-8") as f:
        json.dump({"startedAt": started_at, "seconds": duration, "results": results}, f, ensure_ascii=False, indent=2)
    passed = sum(r["pass"] for r in results)
    lines = [
        "# 게임데이 결과",
        "",
        f"- 실행: {started_at} (KST), {duration // 60}분 {duration % 60}초",
        "- 구성: 중앙 1 + 원격 감시기 1 (Docker), 가짜 공공 API 1개를 10초마다 확인. 장애는 연속 2회 판정에 열리고 2회 정상에 닫힘",
        f"- 결과: **{passed}/{len(results)} 통과**",
        "- 재현: `python deploy/gameday/gameday.py`",
        "",
        "| # | 시나리오 | 기대 | 결과 | 측정 | 비고 |",
        "|---|---|---|---|---|---|",
    ]
    for r in results:
        lines.append(f"| {r['no']} | {r['name']} | {r['expect']} | {'✅' if r['pass'] else '❌'} | {r['measured']} | {r['note'].replace('|', '/')} |")
    with open(os.path.join(ROOT, "docs", "gameday", "RESULTS.md"), "w", encoding="utf-8", newline="\n") as f:
        f.write("\n".join(lines) + "\n")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--keep", action="store_true")
    ap.add_argument("--only", type=int, nargs="*")
    args = ap.parse_args()
    started_at = datetime.now(KST).strftime("%Y-%m-%d %H:%M")
    t0 = time.time()
    print("building and starting the stack…", flush=True)
    sh(*COMPOSE, "down", "-v", check=False)
    sh(*COMPOSE, "up", "-d", "--build")
    if wait(lambda: ready(CENTRAL) and ready(PROBE, "/healthz"), 180) is None:
        sys.exit("stack did not become ready")
    started = time.time()
    scenarios = [s1_hard_outage, s2_silent_failure, s3_regional, s4_central_outage, s5_restart_mid_incident, s6_stale,
                 lambda: s7_adaptive_slow(started)]
    try:
        for i, s in enumerate(scenarios, 1):
            if args.only and i not in args.only:
                continue
            try:
                s()
            except Exception as e:
                record(i, f"시나리오 {i}", "-", False, "오류", str(e)[:200])
    finally:
        try:
            mode("ok")
        except Exception:
            pass
        if not args.keep:
            sh(*COMPOSE, "down", "-v", check=False)
    write_report(started_at, int(time.time() - t0))
    print(f"{sum(r['pass'] for r in results)}/{len(results)} passed; docs/gameday/RESULTS.md")
    sys.exit(0 if all(r["pass"] for r in results) else 1)


if __name__ == "__main__":
    main()
