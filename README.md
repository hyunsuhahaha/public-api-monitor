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
deploy/              Prometheus 설정, 자체 SLO 알림 규칙, Grafana 대시보드, Terraform(OCI), Ansible
targets.yaml         감시 대상 API 목록
```

## 실행 (로컬)

```bash
cp .env.example .env     # 키 입력
go run ./cmd/pamon -once # 대상마다 한 번 호출하고 결과 표 출력 (targets.yaml 수정 후 확인용)
go run ./cmd/pamon       # http://localhost:8080
docker compose up        # pamon + Prometheus(:9090) + Grafana(:3000, 대시보드 자동 등록)
```

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
| `GET /api/v1/status` | 대상별 현재 상태, 30·90일 가용률, p50/p95 응답 시간, 일별 기록, 진행 중 장애 |
| `GET /api/v1/incidents?target=id` | 최근 90일 장애 |
| `GET /badge/{id}.svg` | 30일 가용률 배지 |
| `GET /metrics` | Prometheus 지표 (`pamon_checks_total`, `pamon_check_latency_seconds`, `pamon_target_up`, …) |
| `POST /api/v1/ingest` | 원격 감시기 결과 수신 (`Authorization: Bearer $PAMON_INGEST_TOKEN`) |
| `GET /healthz`, `/readyz` | 살아 있음 / DB 응답 + 최근 2주기 안에 확인함 |

## 모니터 자체의 SLO

모니터가 죽으면 상태판이 거짓말을 합니다. 그래서 모니터 자신에게도 SLO를 겁니다(`deploy/alerts.yml`).

- **적시성:** 예정된 확인의 99.9%가 한 주기 안에 실행된다. 에러 버짓 소진 속도(1시간 14.4배, 6시간 6배)로 알림.
- **커버리지:** 모든 대상이 10분 안에 한 번은 확인된다.
- **원격 감시기:** 중앙으로 보내기가 계속 실패하거나 큐가 쌓이면 티켓.
- **저장:** DB 쓰기 실패는 즉시 티켓.

## 대상 추가

`targets.yaml`에 한 항목을 추가하고 `pamon -once`로 확인합니다. 키가 없어도 주소는 확인할 수 있습니다. 가짜 키로 호출하면 살아 있는 data.go.kr 주소는 403(코드 30, 키 미등록), 없거나 폐기된 주소는 400(코드 12)을 줍니다: `DATA_GO_KR_KEY=dummy pamon -once -paths`.

현재 46개: 기상청 8, 에어코리아·환경 6, 부동산 실거래 4, 교통 9(서울 버스·지하철·따릉이·주차장, TAGO, 인천공항), 의료 4(응급실 실시간 병상 포함), 금융·경제 6, 생활·관광 9. URL은 Go 템플릿이라 날짜 파라미터를 자동으로 채울 수 있습니다.

```yaml
- id: airkorea-sido
  name: 에어코리아 시도별 실시간 대기오염
  url: https://apis.data.go.kr/B552584/ArpltnInforInqireSvc/getCtprvnRltmMesureDnsty?serviceKey={{.Key}}&returnType=json&numOfRows=10&pageNo=1&sidoName={{esc "서울"}}&ver=1.0
  freshness:            # 매시 갱신 데이터: 3시간 넘게 멈추면 '갱신 멈춤'
    pattern: '"dataTime"\s*:\s*"(\d{4}-\d{2}-\d{2} \d{2}:\d{2})"'
    layout: "2006-01-02 15:04"
    max_age: 3h
```
