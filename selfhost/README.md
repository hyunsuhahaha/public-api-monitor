# 내 API를 내 키로 감시하기

공개 pamon은 정해진 160개 API를 pamon의 요청으로 확인합니다. 하지만 내 서비스가 깨지는 건 **내가 쓰는 결제·AI·공공 API를 내 키로 부를 때**입니다. 키가 막혔는지, 호출 한도가 바닥났는지, 200인데 빈 데이터가 오는지는 공개 pamon으로는 알 수 없습니다. 이 폴더는 같은 pamon을 내 서버에서 내 목록으로 돌리는 설정입니다.

필요한 것: Docker가 있는 서버 한 대(작은 VM이나 집 PC도 됩니다).

## 5분 설치

```sh
git clone https://github.com/hyunsuhahaha/public-api-monitor && cd public-api-monitor/selfhost
cp .env.example .env            # 키와 Slack/Discord 웹훅 주소를 채웁니다
$EDITOR targets.yaml            # 쓰지 않는 예시는 지우고 내 API를 넣습니다
docker compose run --rm pamon -check          # 설정 확인
docker compose run --rm pamon -try github-rate-limit   # 하나 시험 (키는 출력되지 않음)
docker compose up -d
```

`http://서버:8080`이 운영 대시보드, `http://서버:8080/s/`가 서비스별 상태 페이지입니다.

## 내 API 추가하기

```sh
docker compose run --rm pamon -try https://api.example.com/v1/items -samples 5
```

몇 번 불러 판정하고, 응답을 보고 **"200인데 데이터가 비었거나 틀린 경우"를 잡는 규칙**(`json_expect`)을 제안하고, `targets.yaml`에 붙여 넣을 항목을 출력합니다. OpenAPI 문서가 있으면 문서와 실제 응답이 다른 곳(필수 필드 누락, 숫자인데 문자열로 옴, 문서에 없는 필드)도 확인할 수 있습니다. 문서 파일을 컨테이너에 넣어 넘깁니다.

```sh
docker compose run --rm -v "$PWD/openapi.yaml:/spec.yaml:ro" pamon -try https://api.example.com/v1/items -openapi /spec.yaml
```

인증 방식별 예:

```yaml
# 키가 쿼리에 들어가는 API (공공데이터포털 등)
key_env: DATA_GO_KR_KEY
url: https://apis.data.go.kr/...?serviceKey={{.Key}}&...

# 헤더 인증 (Bearer, Basic, 자체 헤더)
auth: headers
headers:
  Authorization: {env: STRIPE_SECRET_KEY, prefix: "Bearer "}
  X-Api-Key: {env: MY_API_KEY}
```

결제처럼 부작용이 있는 API는 **읽기 전용 호출**(잔액·목록·없는 주문 조회)만 쓰세요. pamon은 GET만 보냅니다.

## 알림

`.env`의 `PAMON_WEBHOOK_URL`에 Slack 또는 Discord의 Incoming Webhook 주소를 넣으면 이런 일이 생길 때 메시지가 옵니다.

- 장애 시작·복구 (연속 2번 실패에 시작, 연속 2번 정상에 끝)
- 응답 형식 변경: 필드 추가·사라짐, 값 형식 바뀜(숫자 → 문자열, 목록이 빈 배열로)
- 같은 기관의 API가 30초 안에 함께 실패하면 한 번만

웹훅 없이도 `http://서버:8080/feed.xml`(전체)이나 `/feed/{id}.xml`(API 하나)을 RSS 리더나 Slack `/feed subscribe`로 구독할 수 있습니다.

## 점검 공지

제공사의 점검 공지를 `targets.yaml`의 `maintenance`에 넣으면 그 시간의 실패는 장애·알림·가용률에서 빠지고, `/maintenance.json`·`/maintenance.ics`로 나옵니다. 내 서비스 코드가 이 JSON을 읽어 점검 중에는 호출을 미루게 할 수 있습니다([docs/maintenance.md](../docs/maintenance.md)).

## 코드에서 쓰기

내 pamon의 `/ai/targets/{id}.json`은 공개 pamon과 같은 형식입니다. `retryAdvice.action`이 `wait`(pamon도 실패를 봄)이나 `skip`(점검 중)이면 호출하지 않는 예제가 Go·Node·Python·Java(Resilience4j)로 있습니다: [examples](../examples). 주소만 내 서버로 바꾸면 됩니다.

## 키는 어디에도 나가지 않습니다

- 키는 `.env`(환경 변수)에만 있고, `targets.yaml`에는 변수 이름만 씁니다.
- 화면, 로그, 피드, JSON, 웹훅 메시지에서 키는 `REDACTED`로 바뀌고, 키가 든 URL은 출력하지 않습니다.
- 키가 없거나 거절되면(401·403·한도 초과) '판정 불가'로 남고 장애로 세지 않습니다. 내 키 문제를 따로 알고 싶다면 `json_expect`로 남은 한도를 확인하세요(예: `github-rate-limit`).

## 업데이트·백업

```sh
docker compose pull && docker compose up -d       # 새 버전
docker compose exec pamon /pamon -snapshot /data/backup.db   # 깨지지 않은 사본 만들기
```

데이터는 `pamon-data` 볼륨의 SQLite 파일 하나입니다. 확인 기록은 100일, 시간별 집계는 그 4배 동안 남습니다.
