# pamon: 개발자가 의존하는 외부 서비스 상태판

## AI 장애 진단에 사용하기

AI가 사용자 로그와 외부 관측을 비교할 수 있도록 공개 읽기 전용 데이터를 제공합니다. 서버와 Pages에서 경로가 같습니다(운영 사용은 이 변경의 배포 후 가능).

- `llms.txt`: 조회 안내와 해석 규칙
- `ai/index.html`: 필드 설명·AI 요청 예시
- `ai/diagnostics.json`: 서비스명·공급자·호스트로 찾는 전체 카탈로그
- `ai/targets/{id}.json`: 대상 하나의 관측 상태·위치·최근 장애·근거 링크

AI에 `ai/index.html` 링크를 전달하고 오류 시각·호스트·기능을 비교하도록 요청하세요. JSON 링크는 해당 JSON URL 기준으로 해석합니다. `freshUntil`을 현재 시각과 비교해야 하며 `freshAtGeneration`만으로 최신성을 판단하면 안 됩니다. 감시용 인증·쿼터 문제는 공급자 장애와 구분합니다. MCP 서버나 모든 AI의 자동 발견 기능은 포함하지 않습니다.

### 👉 [실시간 상태판 열기](https://hyunsuhahaha.github.io/public-api-monitor/)

[![ci](https://github.com/hyunsuhahaha/public-api-monitor/actions/workflows/ci.yml/badge.svg)](https://github.com/hyunsuhahaha/public-api-monitor/actions/workflows/ci.yml) [![monitor](https://github.com/hyunsuhahaha/public-api-monitor/actions/workflows/monitor.yml/badge.svg)](https://github.com/hyunsuhahaha/public-api-monitor/actions/workflows/monitor.yml)

공공데이터포털(data.go.kr)과 서울 열린데이터광장의 **공공 API 77개**, **카카오맵·네이버 Maps 4개 기능**, **npm·PyPI·Docker Hub·GitHub 8개 기능**이 지금 동작하는지, 느린지, 잘못된 결과를 주는지 5분마다 확인합니다(여러 지역 판정 지원). 결과는 SLO와 에러 버짓, 장애 기록으로 보여줍니다. 지도 API는 별도 인증키가 필요하며, 키가 없으면 '확인 불가'로 표시하고 가용률에서 제외합니다. 개발·배포 의존성 8개 검사는 키 없이 동작합니다.

![dashboard](docs/img/dashboard.png)

*실제 운영 화면(2026-10-19 오후).*
- **조치 필요 배너:** 승인 대기로 403이 나는 API 4개를 우리 쪽 문제로 묶어 보여준다.
- **소진 속도 열:** 에어코리아 API들이 에러 버짓을 태우는 속도(burn rate)를 보여준다. 1시간과 5분 창을 함께 넘은 API에만 '호출' 태그가, 6시간과 30분 창을 함께 넘은 API에만 '티켓' 태그가 붙는다.

![drawer](docs/img/drawer.png)

*API 상세 화면 (에어코리아 미세먼지 경보).*
- **타임아웃 재확인:** 맨 위 줄이 결과를 보여준다. 이 API에서는 타임아웃 직후 다시 보낸 요청이 141ms 만에 응답했다(재시도로 바로 됨 3, 늦게 응답 1).
- **응답 시간 차트:** 학습한 '느림' 기준선(평소 37ms → 437ms)과 고정 기준(slow_after 3s)을 함께 그리고, 장애 구간은 붉게 칠한다.
- **에러 버짓:** 바닥난 시각을 표시한다.
- **시간이 쓰인 곳:** 응답 시간의 100%가 기관 서버 대기다.

![incident](docs/img/incident.png)

*장애 상세 화면.*
- **문제 위치:** 기관 서버로 판정하고, 근거(타임아웃 3회 모두 서버 응답 대기에서 멈춤)를 함께 보여준다.
- **다음 할 일:** 공지 확인, 같은 기관의 다른 API 상태, 문의처, 런북 순서로 안내한다.
- **원시 기록:** 지역 × 시간 표, 판정이 바뀐 순간, 원시 확인 기록이 이어진다.
- **포스트모템 초안 복사:** 이 내용으로 원인 항목이 채워진 초안을 만든다.

## 한눈에

| | |
|---|---|
| **감시 대상** | 공공 API 77개 + 지도 4개 기능 + 개발·배포 의존성 8개 기능 (총 89개) |
| **핵심 문제** | 공공 API는 실패해도 HTTP 200을 준다. 본문의 결과 코드까지 읽어 판정한다 |
| **판정** | 6단계(정상·느림·데이터 이상·오류·장애·확인 불가), API별로 학습한 평소 속도 기준의 "느림", 여러 지역 과반수 규칙 |
| **SRE** | API별 SLO와 에러 버짓, 모니터 자체 SLO와 burn-rate 알림, 게임데이(장애 주입 훈련), 포스트모템, 런북 |
| **관측성** | Prometheus 지표, OpenTelemetry 트레이스(확인 1건 = 트레이스 1개: 대기 → HTTP 호출 → 판정, 로그에 trace_id), 구조화 로그 |
| **인프라** | Go 단일 바이너리, SQLite, Prometheus·Grafana·Jaeger, Terraform(OCI), Ansible, 멀티 아키텍처 이미지, GitHub Actions |
| **운영 중 잡은 것** | 에어코리아 API 타임아웃은 우리 네트워크가 아니라 기관 서버 쪽: 대부분은 일부 요청만 걸려 재시도하면 바로 되고, 가끔 기관 전체가 30~57초씩 느려짐. 출근 시간대 버스 API 지연, 내 모니터의 오탐 3종 (아래) |

## 실데이터가 알려준 것, 그리고 고친 것

집 PC에서 하룻밤 돌린 데이터를 보고 모니터 자신의 결함을 고쳤다. 고친 것마다 실제 사례가 있다.

| 본 것 | 원인 | 고친 것 |
|---|---|---|
| 들쭉날쭉한 API(40ms~1s)가 평범한 1.2초에 장애로 잡힘 | 느림 기준이 중앙값만 봄 | 기준에 p95 퍼짐을 반영 |
| 평소 45ms인 API의 느림 기준이 밤새 1.48초로 올라감 | 느리다고 판정한 답까지 퍼짐 계산에 들어가, 느림이 기준을 끌어올림 | 퍼짐은 정상 판정된 답으로만 계산 (회귀 테스트) |
| 하룻밤 "응답 형식 변경" 6건이 모두 오탐 | 게이트웨이 에러 응답의 필드를 학습함, 시간대마다 바뀌는 필드(4일차 예보, 운행 중 버스) | 에러 응답은 학습하지 않음, 하루 동안 학습한 뒤부터 보고 |
| 한 장애가 복구됐다 다시 실패하며 여러 건으로 쪼개짐 | 연속 실패 규칙만 있었음 | 10분 안에 재발하면 같은 장애를 다시 열고 "불안정 ×N"으로 셈 |
| 한 기관의 API 4개가 같은 분에 각각 알림 | 알림이 API 단위 | 기관 단위로 30초 묶어 한 번 (Alertmanager의 group_wait) |
| "timeout"만으로는 누구 문제인지 모름 | 요청 시간을 통으로만 잼 | DNS·연결·TLS·서버 대기로 나눠 기록, 타임아웃은 멈춘 구간을 사유에 남김 |
| 에어코리아 타임아웃이 늘 "서버 응답 대기 중"에서 멈춤: 느린 건가, 멈춘 건가? | 타임아웃 뒤에 무슨 일이 있는지 몰랐음 | 타임아웃이 나면 같은 요청을 한 번 더 길게 기다려 본다. 첫날 16건 중 대부분은 0.1초 안에 바로 응답(**일부 요청만 걸리고 재시도하면 되는** 유형)이었고, 12:21에는 에어코리아 API 4개가 동시에 30~57초 걸린 **기관 전체 지연**도 있었다. 둘은 대응이 다르다(재시도 vs 타임아웃·기관 문의). 월간 리포트에 API별로 집계된다 |

## 장애 대응을 돕는 화면

- **문제 위치:** 장애마다 "기관 서버인가, 네트워크인가"를 근거와 함께 보여준다(멈춘 구간, 5xx, 결과 코드, 서버 대기 비율). 포스트모템 초안의 원인 항목도 이 줄로 시작한다.
- **API별 burn rate:** 서비스 표에 에러 버짓 소진 속도(1시간·6시간)를 보여준다. Prometheus 규칙과 같은 multi-window 조건(1시간+5분 14.4×는 호출, 6시간+30분 6×는 티켓)이라 화면과 호출기가 같은 판단을 한다.
- **기관 단위 배너, 조치 필요 배너:** 한 기관의 여러 API가 함께 실패하면 하나의 문제로 묶는다. 키·승인 같은 우리 쪽 문제는 할 일 목록으로 따로 모은다.
- **API별 장애 통계:** 장애 수, 영향 시간, MTTR, MTBF, 불안정 횟수. 무엇부터 손볼지 정하는 표.
- **알림 끄기 (silence):** 원인을 이미 아는 API는 1·4·24시간 동안 알림을 끌 수 있다(관리 토큰 필요). 장애 기록과 SLO는 그대로이고, 표에 🔕가 붙는다. 공지된 점검(`maintenance`)과 달리 판정은 바꾸지 않는다.
- **장애 메모:** 확인한 것과 한 일을 장애에 남기면(관리 토큰 필요) 포스트모템 초안의 '운영자 메모'로 들어간다.
- **교대 인수인계:** 지금 상태, 이번 교대의 장애, 장애가 몰린 기관, 형식 변경을 다음 당번에게 넘길 Markdown으로 만든다(`h`).
- **계획 점검:** 공지된 점검 시간(`targets.yaml`의 `maintenance`)은 '점검 중'으로 남기고 가용률·SLO·장애·알림에서 뺀다. 설정은 CI에서 `pamon -check`로 검증한다.
- **공개 상태 페이지 (`/status`):** 상태판은 운영자용이고, 이 페이지는 API를 쓰는 개발자용이다. 기관별 현재 상태, 90일 일별 막대, 최근 장애를 Statuspage 형식으로 보여준다.
- **휴대폰:** 당직 중 휴대폰으로 볼 때는 상태·이름·가용률·소진 속도만 남긴다.

## 신뢰성 작업

- **[게임데이 결과](docs/gameday/RESULTS.md):** 가짜 공공 API와 중앙·원격 감시기를 Docker로 띄우고 장애 8종과 알림 흐름(장애 → 담당 → 복구)을 일부러 일으켜, 감지·알림 시간과 유실 여부를 측정한다(`python deploy/gameday/gameday.py`, 9/9 통과).
- **포스트모템 2건:** [진행 중 장애가 사라진 버그](docs/postmortems/2026-10-19-open-incidents-invisible.md)(게임데이가 잡음), [배포 직후 시작 단계 교착으로 감시 10분 중단](docs/postmortems/2026-10-19-startup-deadlock.md)(내 배포가 낸 장애).
- **[런북](docs/runbooks/README.md):** 알림마다 확인·조치·종료 조건을 정리했다.
- **모니터 자체의 SLO:** "예정된 확인의 99.9%가 한 주기 안에 실행"을 기준으로 multi-window burn-rate 알림을 건다(`deploy/alerts.yml`).
- **[성능: 대시보드 2초 → 7ms](docs/performance.md):** 운영 규모의 한 달치 데이터로 미리 측정했다. 프로파일을 따라 시간별 롤업, 최신 상태 테이블, 응답 캐시를 넣었다.
- **변경 감지:** 응답 필드가 추가되거나 사라지면 기록하고 알린다(살아 있어도 클라이언트를 깨뜨리는 변경). 매주 CI가 모든 주소를 두드려 폐기된 API를 찾는다(코드 12).
- **무엇이 돌고 있나:** 바이너리에 커밋 해시를 넣는다. `/version`, `pamon_build_info` 지표, 상태판 하단에 보인다.

## 구조

```mermaid
flowchart LR
  subgraph probes[원격 감시기]
    P1[pamon -role probe<br/>home-wsl]
  end
  subgraph central[중앙 (서버로 돌릴 때: OCI 춘천)]
    C[pamon -role central] --> DB[(SQLite)]
    C --> W[상태판 · API · 피드 · 리포트]
    C --> M[/metrics/]
  end
  APIs[(공공 API 77개<br/>data.go.kr · 서울시)]
  P1 -- 5분마다 --> APIs
  C -- 5분마다 --> APIs
  P1 -- 결과 push<br/>큐·재시도 --> C
  M --> Prom[Prometheus<br/>자체 SLO 알림] --> G[Grafana]
  C -- 장애 열림·복구 --> Hook[디스코드·슬랙]
```

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
| 데이터 이상 `stale` | 정상이지만 데이터 시각이 너무 오래됐거나, 값이 말이 안 됨(예: 기온 300도, `expect` 규칙) | 정상(저하) |
| 오류 `error` | 제공 기관 쪽 오류 코드 (DB 오류, 타임아웃 `05`, 서버 오류 등), 200 안의 HTML 오류 페이지 | 장애 |
| 장애 `down` | 응답 없음, 타임아웃, 5xx | 장애 |
| 확인 불가 `client` | **우리 쪽 문제**: 키 미등록 `30`, 호출 한도 `22`, 잘못된 파라미터 `10` 등 | 제외 |

**느림**은 API마다 다릅니다. 평소 50ms인 API가 800ms가 되면 3초 기준으로는 못 잡으므로, 대상마다 최근 응답 288개(24시간)를 학습해 `min(slow_after, max(4×중앙값, 중앙값+400ms, 1.5×p95))`를 넘으면 느림으로 봅니다("slow: 812ms (usually 48ms, limit 448ms)"). p95는 느림으로 판정되지 않은 응답만으로 계산해, 느린 상태가 기준을 끌어올려 스스로를 숨기지 못하게 합니다. 장애 중 응답은 학습하지 않고, 오래 지속된 변화는 반나절쯤 지나 새 기준이 됩니다. API 상세 화면의 응답 시간 차트에 이 기준선이 그려집니다.

한 번 실패는 노이즈로 봅니다. 장애는 **연속 2회 실패**에 열리고 **연속 2회 정상**에 닫힙니다.

## 여러 지역에서 확인하기

한 곳에서만 실패하면 대개 그 지역의 네트워크 문제입니다. 그래서 다른 곳에 원격 감시기를 두고 결과를 중앙으로 보냅니다.

```
[원격 감시기: 집 PC]  --결과 push-->  [중앙: OCI 춘천]  --> 상태 페이지, DB, 장애 판정
   pamon -role probe                     pamon -role central
```

- 모든 지역이 같은 시각 슬롯에 같은 API를 확인합니다(대상 ID 해시로 정한 오프셋).
- 중앙은 자기 확인이 끝날 때마다, 최근 2.5주기 안에 결과를 보낸 지역들로 **판정(verdict)** 을 냅니다. 투표한 지역의 **과반수가 실패해야 장애**이고, 소수만 실패하면 "일부 지역에서만 실패"로 기록만 합니다.
- 가용률과 일별 막대는 판정 기준, 응답 시간은 지역별 원시 기록 기준입니다.
- 원격 감시기는 결과를 메모리 큐에 쌓아 10초마다 보내고, 중앙이 죽어 있으면 백오프로 재시도합니다(최대 5,000건, 넘치면 오래된 것부터 버림).

## 구성

```
cmd/pamon            실행 파일 (모니터 + 웹 서버)
internal/probe       호출, 응답 판정(결과 코드 표), 데이터 신선도 검사
internal/incident    장애 열기·닫기 (연속 실패 기준, 재시작 시 복원)
internal/quorum      여러 지역의 최신 결과를 하나의 판정으로 (과반수 규칙)
internal/monitor     대상별 스케줄 (지역 간 같은 슬롯), 중앙(Central): 저장·판정·장애
internal/push        원격 감시기 → 중앙 전송 (큐, 재시도)
internal/store       SQLite (cgo 없는 순수 Go 드라이버)
internal/web         상태 페이지, JSON API, 배지, /healthz /readyz /metrics
internal/notify      디스코드·슬랙 웹훅 알림
internal/report      월간 가용성 리포트 (웹·마크다운)
cmd/fakeapi          게임데이용 가짜 공공 API (장애 모드 전환)
deploy/gameday       게임데이: 중앙 + 원격 감시기 + 가짜 API, 시나리오 실행기
docs/                게임데이 결과, 포스트모템, 런북
deploy/              Prometheus 설정, 자체 SLO 알림 규칙, Grafana 대시보드, Terraform(OCI), Ansible
targets.yaml         감시 대상 API 목록
```

## 실행 (로컬)

```bash
cp .env.example .env     # 키 입력
go run ./cmd/pamon -once # 대상마다 한 번 호출하고 결과 표 출력 (targets.yaml 수정 후 확인용)
go run ./cmd/pamon       # http://localhost:8080
docker compose up        # pamon + Prometheus(:9090) + Grafana(:3000, 대시보드 자동 등록) + Jaeger(:16686, 트레이스)
go run ./cmd/pamon -report 2026-10   # 월간 리포트를 마크다운으로 (블로그·GeekNews 게시용)
```

Go 프로그램은 `.env`를 자동으로 읽지 않습니다. 직접 실행할 때는 셸 환경변수로 키를 전달하세요. Docker Compose는 `env_file`로, WSL 실행 스크립트는 `.env`를 읽어 전달합니다.

## 카카오맵·네이버 Maps 감시

카카오의 주소·장소 검색과 네이버의 주소 검색·자동차 길찾기를 별도 대상으로 감시합니다. HTTP 성공뿐 아니라 서울시청 주소의 좌표 범위, 서울역 장소 검색 결과, 시청→서울역 자동차 경로의 거리·소요시간 범위까지 검사합니다. 결과 순위·좌표 소수점·교통 상황에 따른 정확한 경로 일치는 요구하지 않습니다. 이 결과는 해당 기능의 검사이며 공급자 전체 서비스의 정상 여부를 보장하지 않습니다.

1. 카카오 Developers 앱에서 카카오맵을 활성화하고 REST API 키를 발급합니다.
2. NCP Maps 앱에서 **Geocoding과 Directions 5**를 선택하고 Client ID/Secret을 발급합니다. API를 선택하지 않아도 429가 날 수 있습니다.
3. `.env` 또는 실행 환경에 `KAKAO_REST_API_KEY`, `NAVER_MAPS_CLIENT_ID`, `NAVER_MAPS_CLIENT_SECRET`을 등록합니다. GitHub Pages 운영은 같은 이름의 Actions Secrets를 등록합니다. Ansible 환경은 `vault.yml.example`의 지도 키 항목을 사용합니다.
4. `pamon -check`로 설정을 확인하고 `pamon -dump kakao-address`, `pamon -dump naver-geocode`, `pamon -dump naver-directions`로 실제 결과를 검증합니다. `-dump`도 인증 헤더를 적용하고 키를 가립니다.

키 누락·앱 권한·쿼터·금액 한도·잘못된 요청은 `client`(가용률 제외), 공급자 처리 오류는 `error`, 타임아웃·5xx는 `down`입니다. HTTP 200이어도 빈 결과나 잘못된 좌표·경로는 `stale`(데이터 이상)로 표시합니다. 카카오 `-1/-7/-603`은 HTTP 400이어도 공급자 오류이고 `-10/-11`은 우리 쿼터 문제입니다. 코드에는 `KAKAO:`/`NAVER:` 접두사를 붙여 공공 API 코드와 충돌하지 않게 합니다.

대상마다 5분 주기는 30일 기준 8,640회이며 지역 수·재확인에 따라 증가합니다. 키 발급 시 무료 쿼터 적용 앱·호출 요금·금액 한도를 확인하세요. 기존 `-once -paths`의 더미 키 검사는 공공 API 주소 검사이며 인증이 필요한 지도 API의 기능 검증을 대신하지 않습니다.

확장 설정 예:

```yaml
auth: headers                 # 기본 key: 기존 URL 키 / none: 키 없는 HTTP API
classifier: kakao             # 기본 public / naver_maps
headers:
  Authorization: {env: KAKAO_REST_API_KEY, prefix: "KakaoAK "}
json_expect:
  - {path: documents, min_items: 1}
  - {path: documents.0.x, min: 126.96, max: 126.99}
```

`json_expect`는 점으로 구분한 객체 키·배열 인덱스(`documents.0.x`)를 사용합니다. 문자열 `equals`, 숫자(또는 숫자 문자열) `min/max`, 배열 `min_items`를 지원하고 누락·null·잘못된 타입도 검사합니다. 헤더 값은 환경변수에서만 읽고 다른 호스트나 프로토콜로 리다이렉트될 때 전달하지 않습니다. [지도 API 런북](docs/runbooks/maps.md)을 참고하세요.

## npm·PyPI 패키지 의존성 감시

패키지 정보 조회와 다운로드를 독립된 대상으로 검사합니다. 고정 버전을 사용하므로 새 버전 출시나 릴리스 중단을 서버 장애로 오판하지 않으며, 메타데이터 조회가 실패해도 파일 전달 검사는 계속합니다.

| 대상 | 검사 |
|---|---|
| `npm-metadata` | `is-number@7.0.0`의 이름·버전·배포 URL·shasum |
| `npm-download` | 해당 tarball 전체 3,730바이트와 고정 SHA-256 |
| `pypi-metadata` | `sampleproject==4.0.0`의 이름·버전·배포 파일 목록 |
| `pypi-download` | 해당 wheel 전체 4,661바이트와 공식 메타데이터의 SHA-256 |

추가 키가 필요하지 않습니다. 내려받은 코드는 설치·실행·압축 해제하지 않습니다. 한 번에 각 파일 약 4KB를 읽고, 예상 최대 크기+1바이트에서 읽기를 중단해 잘못된 응답이 무한정 메모리를 쓰지 않게 합니다. 체크섬 불일치·잘린 파일·초과 크기는 `error`(가용률 실패), 5xx·연결 실패는 `down`, 429는 `client`(공유 IP의 호출 제한 등)로 처리합니다. 메타데이터가 JSON이 아니거나 필수 필드가 다르면 기존 JSON 검증 규칙으로 표시합니다.

다운로드 설정은 `classifier: http`와 다음을 사용합니다. `sha256`은 64자리 16진수, `min_bytes/max_bytes`는 1..1MiB 범위이며 다운로드와 JSON·신선도 검증은 혼용하지 않습니다. 공식 배포 파일을 별도로 내려받아 크기·해시를 확인한 뒤 설정하세요. 정상 결과는 그 고정 파일의 조회·전달 경로를 의미하며 신규 배포·로그인·전체 패키지 설치 성공을 보장하지 않습니다. [패키지 런북](docs/runbooks/packages.md)을 참고하세요.

```yaml
auth: none
classifier: http
artifact:
  sha256: 7b75c1057198cf97696909a9bee176c9c5e9bcb5b03bf3ecef2f484defadd51e
  min_bytes: 3730
  max_bytes: 3730
```

## Docker Hub·GitHub 감시

| 대상 | 실제 검사 범위 |
|---|---|
| `docker-auth` | 공개 hello-world 이미지의 pull 범위로 익명 토큰 발급·유효 응답 검사 |
| `docker-manifest` | 별도 토큰을 받아 고정 digest의 이미지 index 조회, 구조·전체 크기·SHA-256 검증 |
| `github-api` | 고정 공개 저장소 `octocat/Hello-World`의 API 응답·저장소 이름·ID |
| `github-git` | 동일 저장소의 smart HTTP Git 참조 조회: 응답 타입·pkt-line·HEAD·마지막 flush 검증 |

Docker는 이미지 레이어를 내려받거나 컨테이너를 실행하지 않습니다. Git은 객체를 내려받거나 코드를 실행하지 않으며 clone·push 전체를 검사하지 않습니다. 토큰은 요청 메모리에서만 사용하고 로그·결과·트레이스·덤프에 출력하지 않습니다. 토큰 URL은 설정에 명시한 HTTPS 주소만 쓰고 서버 응답의 임의 realm을 따라가지 않습니다.

인증 선행 요청이 실패하면 manifest 자체는 `client`(선행 검사 실패, 가용률 제외)로 남깁니다. 별도 `docker-auth` 결과와 함께 확인하세요. manifest의 지연은 인증 후 본 요청 기준이고, 전체 검사 시간 제한 안에서 두 요청을 수행합니다. GitHub API·Docker Hub는 공유 출발 IP의 익명 호출 제한에 걸릴 수 있습니다. 이때의 403/429는 공급자 전체 장애라고 단정하지 않습니다.

공식 상태 정보는 직접 관측과 별도로 확인합니다. [Docker](https://www.dockerstatus.com/), [GitHub](https://www.githubstatus.com/), [운영 런북](docs/runbooks/dependencies.md).

## 지금의 운영: GitHub Actions + GitHub Pages (서버 없음)

[`.github/workflows/monitor.yml`](.github/workflows/monitor.yml)이 5분마다 `pamon -cycle -export site`를 실행합니다.

- **한 바퀴 실행:** 모든 대상을 한 번씩 검사해 SQLite에 쌓고 끝납니다. 장애 판정은 서버와 같습니다. 연속 실패 횟수는 메모리에 있던 값이라, 시작할 때 최근 1시간 판정을 다시 넣어 실행이 바뀌어도 "연속 2번 실패"가 이어집니다.
- **DB 보관:** SQLite 파일은 Actions 캐시에 두고 실행마다 이어 씁니다. 동시 실행은 `concurrency`로 줄 세워 쓰는 쪽이 늘 하나뿐입니다. 캐시가 비면 집 서버 시절 이력을 암호화해 둔 `db-seed` 릴리스에서 한 번 복원합니다.
- **정적 내보내기:** 상태판과 공개 페이지, 그 페이지들이 읽는 JSON(범위별 개요, API별 시계열, 장애 상세)을 파일로 떠서 Pages에 올립니다. 페이지 안의 작은 fetch 심이 `/api/v1/...` 요청을 그 파일로 돌립니다. 화면의 "N분 전"은 데이터가 만들어진 시각(`generatedAt`) 기준이고, 10분(검사 주기 두 번)을 넘기면 경고색으로 바뀝니다.
- **알려진 한계:** GitHub이 붐비면 예약 실행이 몇 분씩 밀립니다. 검사는 미국 러너에서 나가므로 응답 시간에 태평양 왕복이 더해집니다(느림 판정 기준은 위치별로 따로 학습). 공지 노트·알림 끄기처럼 서버에 쓰는 기능은 정적 페이지에서는 동작하지 않습니다.

## 내 PC에서 계속 돌리기 (WSL)

```powershell
deploy\local\start-wsl.ps1   # 백그라운드 시작 (숨은 wsl.exe가 WSL을 살려 둠), http://localhost:8080
deploy\local\stop-wsl.ps1    # 중지
```

- DB와 로그는 WSL 리눅스 디스크의 `~/.pamon`에 둡니다(SQLite를 /mnt/c 위에 두면 느리고 잠금이 불안정).
- 시작할 때마다 저장소에서 다시 빌드하고, 죽으면 10초 뒤 다시 띄우며, 로그가 50MB를 넘으면 한 번 돌립니다.
- PC가 꺼지거나 절전에 들어가면 멈춥니다. 다시 켠 뒤 `start-wsl.ps1`을 실행하면 같은 DB에 이어서 쌓입니다.

## 배포 (OCI 무료 서버 + Ansible)

```bash
cd deploy/terraform/oci
cp terraform.tfvars.example terraform.tfvars   # OCI API 키 정보, 내 IP
terraform init && terraform apply              # ARM VM 생성, ../../ansible/inventory.ini 작성

cd ../../ansible
cp group_vars/all/vault.yml.example group_vars/all/vault.yml
ansible-vault encrypt group_vars/all/vault.yml # 서비스 키, 수집 토큰
ansible-playbook site.yml --ask-vault-pass     # 중앙 + [probes]에 적은 원격 감시기
```

- **Terraform:** VCN, 인터넷 게이트웨이, 보안 목록(SSH는 내 IP만, 80/443 공개), A1 Flex VM(Ubuntu 24.04 ARM).
- **Ansible:** 보안 업데이트 자동화, 시간 동기화(원격 결과의 시각 검증), 키 전용 SSH, OCI 이미지의 iptables 차단 해제, Docker, 그리고 중앙에는 pamon + Caddy(도메인이 있으면 HTTPS 자동 발급) + Prometheus + Grafana(`/grafana`), 원격 감시기에는 pamon만.
- **이미지:** CI가 amd64·arm64 이미지를 `ghcr.io/hyunsuhahaha/public-api-monitor`에 올립니다.
- **CI:** 테스트(race 포함), `terraform validate`, Ansible 문법 검사, 그리고 **매주 월요일 감시 중인 API 주소가 아직 살아 있는지 확인**(폐기되면 실패).

키는 [공공데이터포털](https://www.data.go.kr)의 **일반 인증키(Decoding)** 를 씁니다. API마다 따로 활용신청해야 합니다. 서울 열린데이터광장 API는 `sample` 키로도 동작합니다(행 수 제한).

## API

| 경로 | 내용 |
|---|---|
| `GET /api/v1/overview?range=1h…30d` | 전체 가용률·에러 버짓·p50/p95/p99, API별 가용률·버짓·스파크라인·구간별 상태 |
| `GET /api/v1/series?target=id&range=…` | API 하나의 p50/p95/max, 지역별 p95와 상태, 최근 확인 기록 |
| `GET /api/v1/status` | 대상별 현재 상태, 30·90일 가용률, 일별 기록 (30초 캐시) |
| `GET /api/v1/incidents?target=id` | 최근 90일 장애 |
| `GET /report/YYYY-MM` · `/api/v1/report/YYYY-MM` | 월간 가용성 리포트 (가용률 낮은 API, 최장 장애, 기관별) |
| `GET /feed.xml` | 장애 Atom 피드 (RSS 리더, 슬랙 RSS 앱으로 구독) |
| `GET /badge/{id}.svg` | 30일 가용률 배지 |
| `GET /metrics` | Prometheus 지표 (`pamon_checks_total`, `pamon_check_latency_seconds`, `pamon_target_up`, `pamon_check_phase_seconds`, `pamon_check_timeouts_total`, `pamon_build_info`, …) |
| `POST /api/v1/ingest` | 원격 감시기 결과 수신 (`Authorization: Bearer $PAMON_INGEST_TOKEN`) |
| `GET /api/v1/incidents/{id}` | 장애 하나: 그 구간의 원시 확인(요청 구간 포함), 판정, 운영자 메모 |
| `GET/POST /api/v1/silences`, `DELETE /api/v1/silences/{id}` | API 하나의 알림 끄기·다시 켜기 (쓰기는 관리 토큰) |
| `POST /api/v1/incidents/{id}/notes` | 장애에 메모 남기기 (`Authorization: Bearer $PAMON_ADMIN_TOKEN`, 토큰이 없으면 꺼짐) |
| `GET /api/v1/schema-events` | 응답 필드 추가·사라짐 기록 (90일) |
| `GET /status` | 공개 상태 페이지 (API 사용자용) |
| `GET /version` | 실행 중인 빌드(커밋), Go 버전, 가동 시간 |
| `GET /healthz`, `/readyz` | 살아 있음 / DB 응답 + 최근 2주기 안에 확인함 |

## 모니터 자체의 SLO

모니터가 죽으면 상태판이 거짓말을 합니다. 그래서 모니터 자신에게도 SLO를 겁니다(`deploy/alerts.yml`).

- **적시성:** 예정된 확인의 99.9%가 한 주기 안에 실행된다. 에러 버짓 소진 속도(1시간 14.4배, 6시간 6배)로 알림.
- **커버리지:** 모든 대상이 10분 안에 한 번은 확인된다.
- **원격 감시기:** 중앙으로 보내기가 계속 실패하거나 큐가 쌓이면 티켓.
- **저장:** DB 쓰기 실패는 즉시 티켓.

## 대상 추가

`targets.yaml`에 한 항목을 추가하고 `pamon -once`로 확인합니다. 키가 없어도 주소는 확인할 수 있습니다. 가짜 키로 호출하면 살아 있는 data.go.kr 주소는 403(코드 30, 키 미등록), 없거나 폐기된 주소는 400(코드 12)을 줍니다: `DATA_GO_KR_KEY=dummy pamon -once -paths`.

현재 77개: 공공데이터포털 55개(기상청 13, 환경 7, 부동산 실거래 4, 교통 8, 의료 6, 금융·경제 8, 나라장터 1, 생활·관광 8)와 서울 열린데이터광장 22개(대기질·하천 수위·강우량·수질, 지하철·버스·따릉이·주차장, 약국·병원, 생활인구·생필품 가격, 공공서비스 예약 등). 서울 API는 공개 `sample` 키로도 앞 5건이 조회되어 신선도·값 검사에 충분합니다. URL은 Go 템플릿이라 날짜 파라미터를 자동으로 채울 수 있습니다.

```yaml
- id: airkorea-sido
  name: 에어코리아 시도별 실시간 대기오염
  url: https://apis.data.go.kr/B552584/ArpltnInforInqireSvc/getCtprvnRltmMesureDnsty?serviceKey={{.Key}}&returnType=json&numOfRows=10&pageNo=1&sidoName={{esc "서울"}}&ver=1.0
  freshness:            # 매시 갱신 데이터: 3시간 넘게 멈추면 '데이터 이상'
    pattern: '"dataTime"\s*:\s*"(\d{4}-\d{2}-\d{2} \d{2}:\d{2})"'
    layout: "2006-01-02 15:04"
    max_age: 3h
  expect:               # 값 검사: 측정값이 말이 되는 범위인지 ("-"처럼 숫자가 아니면 건너뜀)
    - name: pm10Value
      pattern: '"pm10Value"\s*:\s*"(-?[\d.]+)"'
      min: 0
      max: 2000
```

응답 본문이 어떻게 생겼는지는 `pamon -dump <id>`로 봅니다(키가 든 URL은 출력하지 않음).
