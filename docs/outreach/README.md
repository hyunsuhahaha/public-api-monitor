# 외부 게시 초안 (검토 후 직접 제출)

여기 있는 글은 모두 **초안**입니다. 아무것도 제출·게시되지 않았습니다. 직접 읽고 고친 뒤 본인 계정으로 올리세요.

| 파일 | 어디에 | 할 일 |
|---|---|---|
| [geeknews.md](geeknews.md) | GeekNews (news.hada.io) "Show GN" | 제목·본문 붙여넣기 |
| [awesome-lists.md](awesome-lists.md) | awesome-sre, awesome-status-pages, awesome-mcp-servers | 각 저장소 포크 → 한 줄 추가 → PR (본문 초안 포함) |
| [server.json](server.json) | 공식 MCP Registry (registry.modelcontextprotocol.io) | `mcp-publisher login github` → `mcp-publisher publish` |
| [smithery.md](smithery.md) | Smithery (smithery.ai) | 원격 서버 URL로 등록, 설명 붙여넣기 |
| [custom-gpt.md](custom-gpt.md) | ChatGPT GPTs | 새 GPT → 설명·지침 붙여넣기 → Actions에 openapi.json URL |

## 올리기 전에 확인할 것

1. **Worker를 먼저 배포하세요.** `cd deploy/access && npx wrangler deploy`. 배포 전에는 `/mcp`가 404입니다. 확인:
   ```sh
   curl -s https://pamon.hyunsuhahaha.workers.dev/mcp -H 'content-type: application/json' \
     -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' | head -c 300
   npx @modelcontextprotocol/inspector   # Streamable HTTP, URL에 위 주소
   ```
2. `/openapi.json`, `/s/kakao-login.md`, `/robots.txt`가 열리는지 확인합니다(Go 쪽은 CI 통과 후 자동 배포).
3. **라이선스**: 저장소에 LICENSE 파일이 없습니다. awesome 목록 다수가 오픈소스 라이선스를 요구하거나 선호합니다. 넣을지는 직접 정하세요(초안은 라이선스를 언급하지 않습니다).
4. server.json의 `$schema` 날짜는 작성 시점 기준입니다. 게시 직전 [registry 문서](https://github.com/modelcontextprotocol/registry)에서 최신 스키마를 확인하세요. `io.github.hyunsuhahaha/` 이름은 GitHub 로그인으로 소유를 증명합니다.
5. 같은 날 여러 곳에 한꺼번에 올리기보다 하나씩, 피드백을 반영하며 올리는 편이 낫습니다. 각 목록의 CONTRIBUTING 규칙(정렬 순서, 설명 길이, 한 PR에 한 항목)을 먼저 읽으세요.

## 성과 확인

`/__access` 관리자 화면의 "MCP로 물어본 AI"(MCP 클라이언트 이름별 연결·도구 호출 수), "찾아온 AI", "유입 출처"(news.hada.io, github.com, smithery.ai 등)를 보면 어디서 왔는지 알 수 있습니다.
