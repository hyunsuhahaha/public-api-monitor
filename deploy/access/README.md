# Pages를 유지하는 접속 기록 입구

GitHub Pages의 기존 파일과 5분 감시 워크플로는 그대로 둡니다. Cloudflare Worker가 고정 Pages 원본을 읽어 전달하고 D1에 요청 기록을 저장합니다. 별도 도메인 없이 workers.dev 주소로 시작할 수 있습니다. Worker 주소로 온 HTML·JSON·llms.txt·HEAD·오류 응답을 기록하므로 JS를 실행하지 않는 AI 요청도 포함합니다.

**기존 github.io 주소로 직접 오는 요청은 기록할 수 없습니다.** AI와 사용자에게 배포한 Worker 주소를 안내해야 합니다. Worker 코드 추가만으로 운영 수집이 활성화되지는 않습니다. Cloudflare 계정, D1 생성 및 배포가 필요합니다. 요금과 한도는 계정의 현재 Workers/D1 플랜을 확인하세요.

## 배포

현재 Wrangler CLI를 사용할 수 있는 환경에서 이 디렉터리로 이동합니다.

```sh
npx wrangler login
npx wrangler d1 create pamon-access
# 출력된 database_id로 wrangler.jsonc의 REPLACE_WITH_D1_DATABASE_ID를 교체
npx wrangler d1 execute pamon-access --remote --file schema.sql
npx wrangler secret put ACCESS_ADMIN_TOKEN
npx wrangler deploy
```

관리자 토큰은 충분히 긴 임의 값으로 정하고 Secret으로만 저장합니다. 코드·쿼리 문자열·공개 Pages 산출물에 넣지 않습니다. 관리자 화면은 `https://<배포주소>/__access`, 공개 상태판은 `https://<배포주소>/`입니다(예전 `/public-api-monitor/` 주소는 루트로 301 이동). 관리자 화면은 다크 대시보드입니다: AI 요청·사람 페이지뷰·스크립트·크롤러 수(이전 기간 대비), 시간대/일별 막대 그래프(KST), AI가 가장 많이 읽은 API, 찾아온 AI, 많이 열린 페이지, 국가·유입 출처, 최근 접속(종류 필터·이전 기록). 기간은 24시간·7일·30일. 토큰을 한 번 넣으면 Worker가 토큰의 HMAC을 HttpOnly·SameSite=Strict 쿠키(30일)로 주고, 그 쿠키를 가진 브라우저의 방문은 기록하지 않습니다(관리자 본인 제외). 토큰 자체는 브라우저에 저장하지 않습니다. `/favicon.ico`는 Worker가 바로 204로 답해 집계에서 빠집니다. API: `GET /__access/stats?range=24h|7d|30d`, `GET /__access/logs`, `POST /__access/session`, `POST /__access/logout`.

```sh
curl 'https://<배포주소>/ai/diagnostics.json' -A 'ChatGPT-User/1.0'
curl 'https://<배포주소>/__access/logs?kind=ai_claimed&limit=50' \
  -H "Authorization: Bearer $ACCESS_ADMIN_TOKEN"
```

첫 호출 뒤 관리자 화면에 ai_json / ai_claimed가 보이는지 확인합니다. 조회 토큰이 없거나 틀리면 401입니다. 새 AI JSON은 해당 기능 PR의 Pages 배포 후에 존재합니다. 원본에 없는 파일은 실제 404로 기록합니다.

## 기록 의미

- 시각, 메서드, 경로, 응답 코드, 응답 헤더까지의 처리 시간, User-Agent(최대 512자), 신고된 클라이언트 종류, 국가 코드, 유입 origin, 자료 종류를 저장합니다.
- IP, Cookie, Authorization, 요청 쿼리, 요청/응답 본문은 저장하지 않습니다. 유입 경로·쿼리는 버리고 origin만 보존합니다. User-Agent와 URL 경로는 방문자가 정하는 값이므로 신뢰할 수 없는 데이터입니다.
- ai_claimed / bot_claimed / browser_claimed / automation_claimed는 User-Agent 추정입니다. 숨기거나 위조한 AI를 모두 판별하지 못합니다. browser_claimed도 사람임을 보증하지 않습니다. 집계는 요청 수이며 순방문자나 실제 정보 사용 횟수가 아닙니다. 요약 결과는 필터와 무관한 전체 최근 24시간입니다.
- 응답 완료 여부·사용자가 내용을 읽었는지·스크래핑한 내용의 활용 여부는 알 수 없습니다. 스트림 본문 전달 전에 로그를 예약하므로 처리 시간은 전체 다운로드 시간이 아닙니다.
- 매일 UTC 03시에 30일 초과 기록을 지웁니다. 로그는 공개 정적 내보내기에 넣지 않습니다. 저장 실패 시 공개 조회는 유지하고 Worker 콘솔에 access_log_write_failed를 남기므로 저장소 장애 동안 기록이 누락될 수 있습니다.
- Worker 응답은 no-store로 반환합니다. 브라우저 캐시만으로 끝나 Worker까지 오지 않은 조회는 기록할 수 없습니다. 고정 원본만 조회하며 방문자의 인증 헤더와 쿠키는 원본에 전달하지 않습니다.

## 검색엔진 알림 (IndexNow)

`INDEXNOW_KEY`와 `PUBLIC_URL`이 있으면 Worker가 `/<key>.txt`로 키를 보여 주고, 매일 UTC 03시 cron에서 원본 sitemap의 URL(PUBLIC_URL 호스트만)을 Bing(IndexNow 공용 엔드포인트, Yandex 등과 공유)과 네이버 서치어드바이저에 제출합니다. 키는 소유 확인용 공개 값이라 Secret이 아닙니다. 실패하면 콘솔에 indexnow_failed / indexnow_rejected를 남깁니다. Google은 IndexNow를 받지 않으므로 Search Console에서 sitemap.xml을 직접 제출합니다.

관리자 로그에서 응답 코드 404가 많으면 Pages 반영 여부를 먼저 확인하세요. 모니터 자체의 API 검사 로그와 이 웹 접속 로그는 별개입니다.

검증: `node --test deploy/access/worker.test.mjs`. 테스트는 프록시·인증·보존 정책의 로컬 검증입니다. 실제 Cloudflare 배포와 D1 연동은 배포 후 위 두 요청으로 확인해야 합니다.

공식 문서: [D1 prepared statements](https://developers.cloudflare.com/d1/worker-api/prepared-statements/), [Workers secrets](https://developers.cloudflare.com/workers/configuration/secrets/), [Fetch](https://developers.cloudflare.com/workers/runtime-apis/fetch/).
