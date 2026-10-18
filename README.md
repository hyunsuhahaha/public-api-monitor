# 공공 API 상태판 (public-api-monitor)

공공데이터포털(data.go.kr)과 서울 열린데이터광장 같은 한국 공공 API가 **지금 살아 있는지, 느린지, 엉뚱한 값을 주는지**를 5분마다 확인하고 공개 상태 페이지로 보여줍니다.

## 왜 HTTP 체크로는 안 되나

한국 공공 API는 실패해도 대부분 **HTTP 200**을 돌려주고, 진짜 결과는 본문 안의 결과 코드에 넣습니다.

```xml
<response><header><resultCode>05</resultCode><resultMsg>SERVICETIMEOUT_ERROR</resultMsg></header></response>
```

일반 업타임 모니터는 이걸 "정상"으로 셉니다. pamon은 본문을 읽어서 판정합니다.

| 상태 | 뜻 | 가용률 |
|---|---|---|
| 정상 `ok` | 정상 응답 (결과 없음 `03`/`INFO-200` 포함) | 정상 |
| 느림 `slow` | 정상이지만 기준보다 느림 | 정상(저하) |
| 갱신 멈춤 `stale` | 정상이지만 데이터 시각이 너무 오래됨 | 정상(저하) |
| 오류 `error` | 제공 기관 쪽 오류 코드 (DB 오류, 타임아웃 `05`, 서버 오류 등), 200 안의 HTML 오류 페이지 | 장애 |
| 장애 `down` | 응답 없음, 타임아웃, 5xx | 장애 |
| 확인 불가 `client` | **우리 쪽 문제**: 키 미등록 `30`, 호출 한도 `22`, 잘못된 파라미터 `10` 등 | 제외 |

한 번 실패는 노이즈로 봅니다. 장애는 **연속 2회 실패**에 열리고 **연속 2회 정상**에 닫힙니다.

## 구성

```
cmd/pamon            실행 파일 (모니터 + 웹 서버)
internal/probe       호출, 응답 판정(결과 코드 표), 데이터 신선도 검사
internal/incident    장애 열기·닫기 (연속 실패 기준, 재시작 시 복원)
internal/monitor     대상별 스케줄 (ID 해시로 간격 안에 분산), 지표, 알림 연결
internal/store       SQLite (cgo 없는 순수 Go 드라이버)
internal/web         상태 페이지, JSON API, 배지, /healthz /readyz /metrics
internal/notify      디스코드·슬랙 웹훅 알림
deploy/              Prometheus 설정, 모니터 자체 SLO 알림 규칙
targets.yaml         감시 대상 API 목록
```

## 실행

```bash
cp .env.example .env     # 키 입력
go run ./cmd/pamon -once # 대상마다 한 번 호출하고 결과 표 출력 (targets.yaml 수정 후 확인용)
go run ./cmd/pamon       # http://localhost:8080
docker compose up        # pamon + Prometheus(:9090) + Grafana(:3000)
```

키는 [공공데이터포털](https://www.data.go.kr)의 **일반 인증키(Decoding)** 를 씁니다. API마다 따로 활용신청해야 합니다. 서울 열린데이터광장 API는 `sample` 키로도 동작합니다(행 수 제한).

## API

| 경로 | 내용 |
|---|---|
| `GET /api/v1/status` | 대상별 현재 상태, 30·90일 가용률, p50/p95 응답 시간, 일별 기록, 진행 중 장애 |
| `GET /api/v1/incidents?target=id` | 최근 90일 장애 |
| `GET /badge/{id}.svg` | 30일 가용률 배지 |
| `GET /metrics` | Prometheus 지표 (`pamon_checks_total`, `pamon_check_latency_seconds`, `pamon_target_up`, …) |
| `GET /healthz`, `/readyz` | 살아 있음 / DB 응답 + 최근 2주기 안에 확인함 |

## 모니터 자체의 SLO

모니터가 죽으면 상태판이 거짓말을 합니다. 그래서 모니터 자신에게도 SLO를 겁니다(`deploy/alerts.yml`).

- **적시성:** 예정된 확인의 99.9%가 한 주기 안에 실행된다. 에러 버짓 소진 속도(1시간 14.4배, 6시간 6배)로 알림.
- **커버리지:** 모든 대상이 10분 안에 한 번은 확인된다.
- **저장:** DB 쓰기 실패는 즉시 티켓.

## 대상 추가

`targets.yaml`에 한 항목을 추가하고 `pamon -once`로 확인합니다. URL은 Go 템플릿이라 날짜 파라미터를 자동으로 채울 수 있습니다.

```yaml
- id: airkorea-sido
  name: 에어코리아 시도별 실시간 대기오염
  url: https://apis.data.go.kr/B552584/ArpltnInforInqireSvc/getCtprvnRltmMesureDnsty?serviceKey={{.Key}}&returnType=json&numOfRows=10&pageNo=1&sidoName={{esc "서울"}}&ver=1.0
  freshness:            # 매시 갱신 데이터: 3시간 넘게 멈추면 '갱신 멈춤'
    pattern: '"dataTime"\s*:\s*"(\d{4}-\d{2}-\d{2} \d{2}:\d{2})"'
    layout: "2006-01-02 15:04"
    max_age: 3h
```
