# 런북

알림 하나에 문서 하나. 각 문서는 **무엇이 깨졌나 → 영향 → 확인 → 조치 → 확인 후 종료** 순서로 쓴다. 알림 규칙은 `deploy/alerts.yml`에 있다.

| 알림 | 등급 | 문서 |
|---|---|---|
| `PamonDown` | page | [모니터가 응답하지 않음](#pamondown-모니터가-응답하지-않음) |
| `PamonTargetNotChecked` | page | [특정 API를 10분 넘게 확인하지 못함](#pamontargetnotchecked-특정-api를-10분-넘게-확인하지-못함) |
| `PamonLateChecksFastBurn` / `SlowBurn` | page / ticket | [확인이 늦어짐 (자체 SLO 버짓 소진)](#pamonlatechecks-확인이-늦어짐) |
| `PamonProbeNotPushing` | ticket | [원격 감시기가 중앙으로 못 보냄](#pamonprobenotpushing-원격-감시기가-중앙으로-못-보냄) |
| `PamonStoreErrors` | ticket | [DB 쓰기 실패](#pamonstoreerrors-db-쓰기-실패) |
| (상태판) 대상이 `확인 불가` | – | [서비스 키·호출 한도 문제](#확인-불가-서비스-키호출-한도-문제) |
| (상태판) 대상이 `장애` | – | [공공 API 장애 대응](#공공-api-장애-대응) |

공통 명령 (OCI 중앙, `/opt/pamon`):

```bash
docker compose ps
docker compose logs --since 30m pamon | grep -v '"msg":"check"'   # 확인 로그를 뺀 나머지
curl -s localhost:8080/readyz
curl -s localhost:8080/metrics | grep -E '^pamon_(open_incidents|late|store|push)'
```

---

## PamonDown: 모니터가 응답하지 않음

- **영향:** 상태판, 장애 감지, 알림이 모두 멈춘다. 공공 API 장애가 나도 아무도 모른다.
- **확인:** `docker compose ps`로 컨테이너 상태와 재시작 횟수, `docker compose logs --tail 100 pamon`로 마지막 오류(`config`, `db`, `exit`), `df -h`와 `free -m`로 디스크·메모리.
- **조치:**
  1. 설정 오류로 시작 실패 → 직전 `targets.yaml` 변경을 되돌리고 `pamon -once`로 확인한 뒤 재배포한다.
  2. 디스크 가득 → `docker system prune`로 정리하고, 보존 기간(`-keep`)을 줄인다.
  3. 원인 불명 → `docker compose up -d --force-recreate pamon`. 진행 중 장애는 DB에서 복원된다(게임데이 시나리오 5로 검증).
- **종료 조건:** `/readyz` 200, `pamon_scheduled_checks_total`이 다시 증가.

## PamonTargetNotChecked: 특정 API를 10분 넘게 확인하지 못함

- **영향:** 그 API의 상태가 굳은 채로 보인다.
- **확인:** `pamon_last_check_timestamp_seconds{target="…"}`가 어느 지역에서 멈췄는지 본다. 한 지역만이면 그 감시기 문제, 전부면 중앙 스케줄러나 설정 문제다.
- **조치:** 원격 감시기면 그 호스트에서 `docker compose restart pamon`. 전부면 로그에서 해당 대상의 `url template` 오류를 확인한다(날짜 템플릿, `missingkey`).

## PamonLateChecks: 확인이 늦어짐

- **의미:** 자체 SLO("예정된 확인의 99.9%가 한 주기 안에 실행")의 에러 버짓이 빠르게 탄다. FastBurn은 1시간 동안 14.4배 속도(30일 버짓의 2%를 1시간에 소진)다.
- **흔한 원인:** 응답이 매우 느린 대상이 동시 요청 슬롯(`MaxParallel=4`)을 오래 붙잡음, 호스트 CPU 부족, 절전에서 깨어남(로컬 실행).
- **확인:** 대상별 `pamon_check_latency_seconds`에서 timeout(10초) 근처에 몰린 대상을 찾는다.
- **조치:** 그 대상의 `timeout`을 줄이거나 `interval`을 늘린다. 계속되면 `MaxParallel`을 올린다.

## PamonProbeNotPushing: 원격 감시기가 중앙으로 못 보냄

- **영향:** 그 지역의 결과가 판정에서 빠진다. 결과는 큐(최대 5,000건)에 쌓이고, 넘치면 오래된 것부터 버린다(`pamon_push_dropped_total`).
- **확인:** 감시기 로그의 `push failed` 사유. 401이면 토큰 불일치, 연결 거부면 중앙 다운이나 네트워크 문제.
- **조치:** 토큰 문제면 vault의 `vault_ingest_token`을 맞추고 재배포한다. 중앙 문제면 PamonDown 런북을 따른다.
- **종료 조건:** `pamon_push_queue`가 0으로 내려옴. 게임데이 시나리오 4에서는 45초 중단 후 큐가 유실 없이 비워졌다.

## PamonStoreErrors: DB 쓰기 실패

- **영향:** 확인 결과나 장애 기록이 빠진다. 판정과 알림은 메모리로 계속 돈다.
- **확인:** 로그의 `save check` / `save incident` 오류. `database is locked`면 동시 쓰기, `disk I/O error`나 `no space`면 디스크 문제.
- **조치:** 디스크를 확보한다. 계속되면 `sqlite3 /data/pamon.db 'PRAGMA integrity_check'`를 실행하고, 손상됐으면 최근 백업으로 복구한다.

## 확인 불가: 서비스 키·호출 한도 문제

- **의미:** 판정은 `client`이고 가용률에서 제외된다. 공공 API 장애가 아니라 **우리 쪽** 문제다.
- **코드별 조치:**

| 코드 | 의미 | 조치 |
|---|---|---|
| 30 | 등록되지 않은 키 | 활용신청 직후라면 1~2시간 반영 대기. 아니면 키 재확인 |
| 22 | 일일 호출 한도 초과 | 해당 API의 `interval`을 늘리거나 운영계정 트래픽 증가 신청 |
| 23 | 초당 호출 한도 초과 | 동시 호출과 재시도 간격 조정. 일일 한도 증설과 구분 |
| 31 | 키 기간 만료 | 데이터포털에서 활용 연장 신청 |
| 20 | 접근 거부 | 활용신청 상태(승인·중지) 확인 |

- 트래픽 증설: 공공데이터포털 로그인 → 마이페이지 → 데이터 활용 → Open API → 활용신청 현황 → 해당 API → 운영계정 신청. 활용사례와 서비스 URL을 등록한다. 이미 운영계정인 경우 제공기관에 추가 증설을 문의한다. 한도는 신청 화면의 API별 값을 확인한다.
- 본문 코드가 없는 401·403만으로 승인 누락을 단정하지 않는다. 승인받은 버전·주소와 `targets.yaml`을 비교하고 키·접근 제한을 확인한다. 금융위 주식·지수·일반상품시세는 승인받은 V2 경로와 메서드를 사용한다.
- 상태판의 `판정 가능`은 **가용률 계산에 포함된 판정 / (포함된 판정 + client 판정)**이다. 계획 점검과 미수집 시간은 분모에 들어가지 않는다. 확인 불가 횟수는 선택한 기간의 합계이며 여러 지역의 원시 호출 수가 아닌 최종 판정 수다. 전부 확인 불가이면 가용률은 산정하지 않는다.

## GitHub Pages 수집·공개 갱신 중단

- `watchdog` 워크플로는 `monitor`의 자체 예약 체인과 독립적으로 공개 JSON을 읽는다. 생성 시각이 10분을 초과하거나 요청·JSON·시간 정보가 잘못되면 실패한다. API 자체가 장애여도 새 검사 결과가 공개되고 있으면 통과한다.
- 저장소를 Watch하고 GitHub 알림 설정의 Actions 실패 알림을 활성화해야 워크플로 실패 알림을 받을 수 있다. 새 웹훅이나 유료 감시 서비스는 필요하지 않다.
- 실패 시 `monitor` 실행 이력에서 검사 실패·Pages 배포 실패·예약 중단을 구분한다. 문제를 수정한 뒤 `monitor`를 수동 실행하고 공개 JSON의 `generatedAt` 갱신 및 다음 `watchdog` 성공을 확인한다.
- GitHub 전체의 장애·예약 지연은 두 워크플로 모두에 영향을 준다. 이 범위까지 감지하려면 GitHub 밖에서 `python deploy/watchdog.py <공개 overview.json URL>`을 실행하도록 별도 감시기를 연결해야 한다.

## 공공 API 장애 대응

우리 시스템이 아니라 제공 기관의 장애라서, 할 일은 **정확히 기록하고 알리는 것**이다.

1. 상태판 상세 패널에서 모든 지역이 실패하는지(진짜 장애), 한 지역만 실패하는지(그 지역 네트워크) 확인한다.
2. 사유가 `code 05`, `code 12`처럼 본문 코드면 제공 기관 쪽, `timeout`이면 게이트웨이나 기관 서버 쪽이다. `code 12`(서비스 없음)가 계속되면 API가 폐기된 것일 수 있으니 데이터포털 공지를 확인하고 `targets.yaml`에서 정리한다(CI의 주간 엔드포인트 점검도 잡는다).
3. 장애가 길어지면 데이터포털 고객센터(1566-0025)나 기관 문의처에 신고한다. 상태판 링크와 시작 시각을 함께 보낸다.
4. 월간 리포트(`/report`)에 자동으로 집계된다.
