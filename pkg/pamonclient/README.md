# pamonclient: pamon을 보는 서킷 브레이커

외부 API가 멈추면 흔히 이렇게 됩니다.

1. API가 503을 돌려주기 시작합니다.
2. 우리 서버의 모든 작업자가 실패한 요청을 **곧바로, 끝없이** 다시 보냅니다.
3. 제공자의 호출 한도·방화벽이 이것을 공격으로 보고 우리 **IP를 차단**합니다.
4. API는 10분 만에 복구됐는데, 차단은 몇 시간·며칠 갑니다. 공공데이터포털이라면 일일 호출 한도도 재시도로 다 써 버렸습니다.

장애보다 그 뒤의 차단이 더 깁니다. 이 패키지는 호출 전에 [pamon](https://pamon.hyunsuhahaha.workers.dev)에 "지금 이 API가 실패하는 게 보이나?"를 물어, 그렇다면 **호출하지 않고 곧바로 돌아옵니다**.

```go
import "github.com/hyunsuhahaha/public-api-monitor/pkg/pamonclient"

c := pamonclient.New("")                         // https://pamon.hyunsuhahaha.workers.dev
b := c.Breaker("airkorea-sido", pamonclient.BreakerOptions{})

err := b.Do(ctx, func(ctx context.Context) error {
    resp, err := http.Get(url)
    if err != nil {
        return err                                // 네트워크 오류: 재시도 대상
    }
    defer resp.Body.Close()
    if resp.StatusCode >= 500 || resp.StatusCode == 429 {
        return fmt.Errorf("HTTP %d", resp.StatusCode)
    }
    if resp.StatusCode >= 400 {
        return pamonclient.Permanent(fmt.Errorf("HTTP %d", resp.StatusCode)) // 재시도해도 같음
    }
    return nil
})
switch {
case errors.Is(err, pamonclient.ErrProviderDown):  // pamon도 실패를 봄: 캐시된 값으로 대신
case errors.Is(err, pamonclient.ErrMaintenance):   // 공지된 점검: 큐에 넣고 나중에
case errors.Is(err, pamonclient.ErrOpen):          // 우리 쪽 실패가 쌓여 잠시 쉬는 중
}
```

target id는 답변 페이지 주소의 그것입니다(`/s/airkorea-sido.html` → `airkorea-sido`). 전체 목록: [`/ai/diagnostics.json`](https://pamon.hyunsuhahaha.workers.dev/ai/diagnostics.json).

## 하는 일

`Do`는 호출마다 순서대로 확인합니다.

1. **점검 공지**: [`/maintenance.json`](../../docs/maintenance.md)에 지금 이 API를 덮는 점검 창이 있으면 `ErrMaintenance`. 5분마다 한 번만 읽습니다.
2. **pamon 관측**: `/ai/targets/{id}.json`의 `assessment`가 `failure_observed`이고 아직 `freshUntil` 전이면 `ErrProviderDown`. 호출하지 않습니다.
   - 캐시: pamon은 5분마다 확인하므로 다음 확인 시각(`checkedAt + intervalSeconds`)이 지나기 전에는 다시 읽지 않고, 지나도 1분에 한 번까지만 읽습니다. 동시에 들어온 호출들은 읽기 하나를 기다려 같이 씁니다.
   - `freshUntil`이 지난 관측은 믿지 않습니다. 멈춘 내보내기가 "장애"라고 계속 말해도 호출을 막지 않습니다.
3. **자체 브레이커**: 연속 5번 실패하면 30초 동안 열립니다(`ErrOpen`). 그 뒤 한 번만 시험 호출해, 성공하면 닫고 실패하면 다시 30초.
4. **재시도**: 최대 3번(첫 호출 포함), 200ms에서 두 배씩 늘어나는 대기에 무작위 지연(jitter)을 섞습니다. **대기가 끝날 때마다 pamon에 다시 묻고**, pamon이 실패를 확인했거나 다른 작업자의 실패로 브레이커가 열렸으면 재시도를 멈춥니다. `Permanent`로 감싼 오류(4xx)는 재시도하지 않고 브레이커 실패로도 세지 않습니다.

pamon은 신호 하나일 뿐 의존성이 아닙니다. **pamon을 읽지 못하면 pamon이 없는 것처럼** 3·4번만으로 동작합니다. 모니터가 멈췄다고 우리 서비스가 멈추면 안 됩니다.

## 게임 데이

[`gameday_test.go`](gameday_test.go)는 장애 중 30번째 실패 요청에서 IP를 차단하는(그리고 복구 뒤에도 풀지 않는) 가짜 API에 작업자 10개가 계속 요청하는 상황을 재현합니다.

| 상황 | API에 간 요청 | 결과 |
|---|---|---|
| 그냥 재시도 (성공할 때까지) | 약 1,000건 | 차단, 복구 뒤에도 403 |
| 브레이커 + pamon이 장애를 봄 | 0건 (pamon 읽기 2번) | 복구되면 바로 정상 |
| 브레이커만 (pamon에 연결 안 됨) | 약 10~15건 | 차단 피함, 30초 뒤 시험 호출로 복구 |
| pamon 관측이 1시간 묵음 | 평소대로 호출 | 오래된 "장애"를 믿지 않음 |
| 점검 공지 중 | 0건 | `ErrMaintenance` |

```sh
go test -race -v ./pkg/pamonclient/
```

직접 돌려 보려면 [가짜 API](../../cmd/fakeapi)의 `-ban-after`를 씁니다.

```sh
go run ./cmd/fakeapi -addr :9000 -ban-after 30 &
curl -X POST 'localhost:9000/admin/mode?m=down'                                   # API 장애
go run ./pkg/pamonclient/example -naive -url http://localhost:9000/api -n 3        # 요청 150건, 403 차단
curl -X DELETE localhost:9000/admin/ban                                            # 차단 풀기
go run ./pkg/pamonclient/example -url http://localhost:9000/api -n 8               # 요청 5건 뒤 브레이커가 멈춤
```

실제 서비스로는 `-target`에 pamon target id를 줍니다.

```sh
go run ./pkg/pamonclient/example -target github-api -url https://api.github.com/zen -n 5
```

## 한계

- pamon은 미국 서부 한 곳에서 서비스의 기능 하나를 5분마다 확인합니다. pamon이 정상이라고 해도 우리가 쓰는 다른 기능, 우리 계정·네트워크만의 문제일 수 있습니다. 그래서 pamon이 정상일 때도 자체 브레이커는 그대로 동작합니다.
- pamon이 장애를 확정하기까지 최대 10분(5분 간격으로 연속 2회 실패)이 걸립니다. 그 사이는 자체 브레이커가 막습니다.
- 다른 언어는 JSON을 직접 읽으면 됩니다: [docs/maintenance.md](../../docs/maintenance.md)의 예시.
