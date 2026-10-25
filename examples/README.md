# pamon을 보고 재시도를 멈추는 예제

외부 API가 멈췄을 때 재시도를 계속 돌리면 호출 한도를 다 쓰거나 IP가 차단됩니다. 이 예제들은 호출 전과 **재시도 대기가 끝날 때마다** pamon의 `ai/targets/{id}.json`을 읽어 `retryAdvice.action`을 확인합니다.

| action | 뜻 | 예제의 동작 |
|---|---|---|
| `retry` | 외부에서는 실패가 안 보임 (내 쪽의 일시적 문제일 가능성) | 백오프로 몇 번 재시도 |
| `backoff` | 느림 | 재시도하되 간격을 늘림 |
| `wait` | pamon도 실패를 봄 | **호출하지 않음**, 재시도 중이면 멈춤 |
| `skip` | 공지된 점검 중 | **호출하지 않음** (큐에 넣고 나중에) |
| `unknown` | 새 관측 없음 | 평소대로 (자체 서킷 브레이커가 판단) |

모두 같은 원칙을 따릅니다.
- pamon을 읽지 못하거나 관측이 `freshUntil`을 지났으면 **평소대로 호출**합니다. 모니터가 멈췄다고 내 서비스가 멈추면 안 됩니다.
- 대상마다 1분에 한 번만 읽고, 동시에 들어온 호출은 읽기 하나를 같이 씁니다.
- 4xx처럼 재시도해도 같은 오류는 재시도하지 않습니다.

| 언어 | 파일 | 의존성 | 테스트 |
|---|---|---|---|
| Go | [`pkg/pamonclient`](../pkg/pamonclient) | 없음 | `go test ./pkg/pamonclient` |
| Node.js 18+ | [`node/pamon-guard.mjs`](node/pamon-guard.mjs) | 없음 (내장 fetch) | `node --test examples/node/pamon-guard.test.mjs` |
| Python 3.9+ | [`python/pamon_guard.py`](python/pamon_guard.py) | 없음 (표준 라이브러리) | `python3 -m unittest discover -s examples/python` |
| Java 17+ (Spring 등) | [`java-resilience4j`](java-resilience4j) | Resilience4j Retry·CircuitBreaker, Jackson | `mvn -f examples/java-resilience4j test` |

셋 다 CI에서 테스트합니다. 테스트는 가짜 pamon 서버로 "pamon이 장애를 보면 50번 호출해도 API에는 0번", "pamon에 연결 못 해도 평소대로", "재시도 중에 pamon이 장애를 확인하면 멈춤"을 확인합니다.

```js
// Node
import { PamonGuard, PamonSaysNo } from "./pamon-guard.mjs";
const guard = new PamonGuard();
try {
  const zen = await guard.call("github-api", async () => {
    const r = await fetch("https://api.github.com/zen");
    if (!r.ok) throw Object.assign(new Error(`HTTP ${r.status}`), { status: r.status });
    return r.text();
  }, { permanent: (e) => e.status >= 400 && e.status < 500 && e.status !== 429 });
} catch (e) {
  if (e instanceof PamonSaysNo) { /* 장애·점검 중: 캐시나 큐로 */ }
}
```

```python
# Python
from pamon_guard import PamonGuard, PamonSaysNo
guard = PamonGuard()
try:
    data = guard.call("airkorea-sido", fetch_airkorea)
except PamonSaysNo as e:
    data = cache.get("airkorea")  # e.advice["action"] == "wait" or "skip"
```

```java
// Java + Resilience4j
PamonAware api = PamonAware.defaults(new PamonGuard(), "github-api");
try {
    String zen = api.call(() -> httpGet("https://api.github.com/zen"));
} catch (PamonAware.PamonSaysNo e) {
    // 장애·점검 중: 대체 경로
} catch (CallNotPermittedException e) {
    // 우리 쪽 실패가 쌓여 브레이커가 열림
}
```

Java 예제에서 pamon의 "호출하지 마"는 서킷 브레이커 실패로 세지 않고(`ignoreExceptions`), 재시도도 하지 않습니다. Spring Boot에서는 `PamonAware`를 빈으로 만들어 쓰거나, `@Retry`·`@CircuitBreaker` 메서드 맨 앞에서 `guard.advice(id).blocks()`를 확인하면 됩니다.

target id는 답변 페이지 주소의 그것입니다(`/s/github-api.html` → `github-api`). .NET(Polly) 예제는 아직 없습니다. 같은 JSON을 읽어 `retryAdvice.action`이 `wait`·`skip`이면 정책 실행 전에 막으면 됩니다.
