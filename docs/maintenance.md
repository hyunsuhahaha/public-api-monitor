# 공지된 점검 피드

공공데이터포털과 각 기관은 점검을 공지사항 게시판으로만 알립니다. 기계가 읽을 수 있는 형식이 없어서, 점검 시간에 재시도를 계속 돌리다 호출 한도를 다 쓰거나 IP가 차단되는 일이 생깁니다. pamon은 `targets.yaml`의 `maintenance`에 등록한 점검을 세 가지 형식으로 내보냅니다.

| 주소 | 용도 |
|---|---|
| `/maintenance.json` | 코드에서 읽기 (서킷 브레이커, 배치 작업, 배포 스크립트) |
| `/maintenance.ics` | 캘린더 구독 (Google 캘린더: 다른 캘린더 → URL로 추가, Outlook: 인터넷 캘린더 구독) |
| `/maintenance.html` | 사람용 일정표 |

각 서비스의 답변 페이지(`/s/{id}.html`)에도 그 API에 걸린 점검이 보입니다. 지난 30일 안에 끝난 점검까지 나오고, 그보다 오래된 것은 빠집니다.

## 점검 등록

```yaml
maintenance:
  - match: [한국환경공단]          # 대상 API id 또는 provider
    start: 2026-11-02T02:00:00+09:00
    end: 2026-11-02T06:00:00+09:00
    note: 에어코리아 서버 교체
    url: https://www.airkorea.or.kr/...   # 공지 원문 (선택, https만)
```

`pamon -check`(CI에서도 실행)가 형식과 대상 이름을 검사합니다. 점검 시간의 판정은 '점검 중'으로 남고 가용률·SLO·장애·알림에서 빠집니다.

자동 수집은 하지 않습니다. 공공데이터포털과 카카오 개발자 공지는 HTML 게시판뿐이고 안정적인 기계용 형식(RSS, API)이 없어, 긁어 오면 화면이 바뀔 때마다 조용히 틀립니다. 로그인이 필요한 곳은 읽지 않습니다. 공지를 보고 사람이 등록합니다.

## JSON 형식

```json
{
  "schemaVersion": "1.0",
  "generatedAt": "2026-11-01T12:00:00+09:00",
  "windows": [
    {
      "id": "591cd05e86fb",
      "start": "2026-11-02T02:00:00+09:00",
      "end": "2026-11-02T06:00:00+09:00",
      "state": "upcoming",
      "note": "에어코리아 서버 교체",
      "url": "https://www.airkorea.or.kr/...",
      "match": ["한국환경공단"],
      "targets": [{"id": "airkorea-sido", "name": "에어코리아 실시간 대기정보", "provider": "한국환경공단"}]
    }
  ]
}
```

- `state`: `upcoming`(예정), `active`(지금 점검 중), `ended`(끝남). 만들어진 시각 기준이므로, 읽는 쪽은 `start`·`end`를 자기 시계와 직접 비교하세요.
- `id`: 시작·끝·대상이 같으면 바뀌지 않습니다(캘린더의 UID도 같은 값).
- 5분마다 다시 만들어지므로 그보다 자주 읽을 필요가 없습니다.

## 서킷 브레이커에 연결하기

규칙은 하나입니다. **호출하려는 API가 점검 창 안이면 호출하지 말고, 재시도도 하지 말고, 미루거나 큐에 넣습니다.**

Go라면 [`pkg/pamonclient`](../pkg/pamonclient)가 이 JSON과 `ai/targets/{id}.json`(pamon이 관측한 상태)을 함께 읽습니다.

```go
c := pamonclient.New("https://pamon.hyunsuhahaha.workers.dev")
b := c.Breaker("airkorea-sido", pamonclient.BreakerOptions{})
err := b.Do(ctx, func(ctx context.Context) error { return callAirKorea(ctx) })
switch {
case errors.Is(err, pamonclient.ErrMaintenance):    // 공지된 점검: 큐에 넣고 끝난 뒤에
case errors.Is(err, pamonclient.ErrProviderDown):   // pamon도 실패를 봄: 재시도 없이 캐시로
}
```

다른 언어에서는 점검 창 확인만 넣어도 됩니다.

```js
// Node.js: 5분 캐시
let cache = { at: 0, windows: [] };
async function inMaintenance(targetId, now = Date.now()) {
  if (now - cache.at > 5 * 60_000) {
    try {
      const r = await fetch("https://pamon.hyunsuhahaha.workers.dev/maintenance.json");
      cache = { at: now, windows: (await r.json()).windows };
    } catch { /* pamon을 못 읽으면 마지막 값 그대로: pamon 장애가 내 서비스 장애가 되면 안 됩니다 */ }
  }
  return cache.windows.some(w => w.targets.some(t => t.id === targetId)
    && Date.parse(w.start) <= now && now < Date.parse(w.end));
}
```

```python
# Python: 점검 중이면 호출 대신 큐로
import time, requests
_cache = {"at": 0, "windows": []}
def in_maintenance(target_id):
    now = time.time()
    if now - _cache["at"] > 300:
        try:
            _cache.update(at=now, windows=requests.get("https://pamon.hyunsuhahaha.workers.dev/maintenance.json", timeout=3).json()["windows"])
        except Exception:
            pass  # 마지막 값 그대로
    from datetime import datetime
    return any(t["id"] == target_id and datetime.fromisoformat(w["start"]).timestamp() <= now < datetime.fromisoformat(w["end"]).timestamp()
               for w in _cache["windows"] for t in w["targets"])
```

pamon을 읽지 못하면 "점검 아님"으로 보고 평소대로 호출하세요. 모니터가 멈췄다고 내 서비스가 멈추면 안 됩니다.

점검 공지는 관측이 아닙니다. 점검이 일찍 끝났는지, 공지 없이 멈췄는지는 `/ai/targets/{id}.json`의 `assessment`로 확인합니다.
