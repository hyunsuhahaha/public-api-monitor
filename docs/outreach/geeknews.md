# GeekNews 초안 (Show GN)

**제목** (GeekNews 제목은 짧게)

> Show GN: pamon – "카카오 로그인 지금 안 되나요?"를 5분마다 밖에서 확인하는 상태판 (MCP 지원)

**URL**: https://pamon.hyunsuhahaha.workers.dev/s/

**본문**

---

장애 대응하다 보면 "우리 문제인가, 저쪽 문제인가"를 가리는 데 시간을 제일 많이 씁니다. 공식 상태 페이지는 늦게 바뀌거나 아예 없고(국내 서비스는 특히), 검색해 봐도 커뮤니티 글이 올라오기 전까지는 알 수가 없어서 직접 만들었습니다.

**pamon**은 개발자와 사람들이 의존하는 서비스 160개를 5분마다 서울·미국 서부에서 직접 호출해 봅니다.

- 공공데이터포털·서울 열린데이터 API 77개, 카카오·네이버 로그인, 홈택스·정부24, 은행·PG, OpenAI·Anthropic 등 AI API, AWS·네이버클라우드, Slack·Discord, npm·PyPI·Docker Hub·GitHub
- 응답 코드만 보지 않고 내용까지 봅니다. 점검·차단 페이지가 200으로 와도 정상으로 치지 않고, 공공 API가 HTTP 200 안에 오류 코드를 넣어 보내는 것도 잡습니다. 인증이 필요한 API는 키 없이 불러 그 서비스 고유의 401 응답이 오는지로 확인합니다.
- 실패하면 어느 단계에서 실패했는지(DNS·TCP·TLS·서버 대기·HTTP 코드) 쉬운 말로 보여 줍니다.
- 공식 상태 페이지가 있는 곳(24곳)은 나란히 보여 주고, 공식 발표보다 몇 분 먼저/늦게 잡았는지도 따로 기록합니다.

서비스마다 "OO 지금 되나요?" 답 페이지(`/s/{id}`)가 있고, 같은 내용의 JSON·Markdown·Atom 피드가 있습니다.

**AI 도구에서 바로 쓰기**: 개발자가 Claude Code나 Cursor에서 "토스페이먼츠 결제가 타임아웃 나는데 저쪽 장애야?"라고 물었을 때 AI가 추측 대신 관측 데이터를 보도록 원격 MCP 서버를 열었습니다. 인증 없음, 읽기 전용입니다.

```sh
claude mcp add --transport http pamon https://pamon.hyunsuhahaha.workers.dev/mcp
```

도구는 `check_service`(서비스 이름·호스트·실패한 URL로 조회), `list_problems`(지금 실패 중), `list_services` 세 개입니다. ChatGPT GPT Actions용 OpenAPI(`/openapi.json`)도 있습니다. 판정은 묻는 시점의 유효 기한과 비교해서, 오래된 관측은 "정상"이 아니라 "판단 불가"로 답합니다.

구조는 Go 단일 바이너리(SQLite) + GCP VM의 5분 cron, 앞단은 Cloudflare Worker(접속 기록 D1, MCP)입니다. SLO·에러 버짓·번 레이트 알림, 포스트모템, 런북, 게임데이도 저장소에 있습니다. SRE 포트폴리오로 시작한 개인 프로젝트라 감시 대상 추천이나 오탐 제보 환영합니다.

- 상태판: https://pamon.hyunsuhahaha.workers.dev/
- 서비스별 답 페이지: https://pamon.hyunsuhahaha.workers.dev/s/
- AI 연결 안내: https://pamon.hyunsuhahaha.workers.dev/llms.txt
- 소스: https://github.com/hyunsuhahaha/public-api-monitor

---

메모 (게시 전 삭제):
- 숫자(160개, 24곳, 77개)는 README 기준입니다. 올리기 직전에 맞는지 확인하세요.
- 데모용으로 실제 장애가 기록된 답 페이지 링크 하나를 덧붙이면 설득력이 큽니다(최근 장애가 있었던 서비스의 `/s/{id}.html`).
