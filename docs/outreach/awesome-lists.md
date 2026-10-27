# awesome 목록 PR 초안

각 저장소의 CONTRIBUTING.md를 먼저 읽고, 알파벳 순서·설명 형식·마침표 규칙을 맞추세요. 한 PR에 한 항목만 넣습니다. 저장소 이름과 섹션은 작성 시점 기준이니 올리기 전에 실제 목록을 확인하세요.

---

## 1. awesome-sre (dastergon/awesome-sre)

**섹션**: Monitoring and Observability (또는 그 목록의 가장 가까운 섹션)

**추가할 줄**

```markdown
- [pamon](https://github.com/hyunsuhahaha/public-api-monitor) - External dependency monitor for 160 public APIs and SaaS with per-service SLOs, error budgets, burn-rate alerts, request-phase traces, postmortems and runbooks; open data via JSON, Atom and MCP.
```

**PR 제목**: `Add pamon (external dependency monitoring with SLOs)`

**PR 본문**

```
This adds pamon, an open-source monitor for the external services an application depends on (public data APIs, OAuth logins, payment gateways, AI APIs, clouds, package registries).

Why it fits this list: it is built around SRE practice rather than uptime pings. Each target has an SLO and error budget with multi-window burn-rate alerts (Prometheus rules included), a phase breakdown of the failing request (DNS/TCP/TLS/server wait), lead time versus the provider's own status page, and the repository contains its postmortems, runbooks and game-day scripts.

Live instance: https://pamon.hyunsuhahaha.workers.dev/
I am the author.
```

---

## 2. awesome-status-pages (ivbeg/awesome-status-pages)

**섹션**: Opensource (자체 호스팅 가능한 상태 페이지) — 목록에 "Monitoring / status of third-party services" 같은 섹션이 있으면 그쪽이 더 맞습니다.

**추가할 줄**

```markdown
- [pamon](https://github.com/hyunsuhahaha/public-api-monitor) - Go status page that checks third-party APIs from outside every 5 minutes (content-aware, multi-location), with plain-language per-service pages, badges, Atom feeds and a comparison against providers' official status pages.
```

**PR 제목**: `Add pamon`

**PR 본문**

```
pamon is a self-hostable status page (single Go binary + SQLite, Docker image) for the external services you depend on, not your own. It checks response content, not only status codes (maintenance and block pages that return 200 count as failures), from several locations, and shows each service's verdict next to the provider's official status page when one exists.

Live instance: https://pamon.hyunsuhahaha.workers.dev/
I am the author.
```

---

## 3. awesome-mcp-servers (punkpeye/awesome-mcp-servers)

이 목록은 형식이 엄격합니다. 범례 아이콘(🏠 로컬 / ☁️ 클라우드, 언어 아이콘 🏎️ Go, 📇 TypeScript/JS 등)과 섹션별 알파벳 순서를 README 상단 범례로 확인하세요. 서버 코드는 JavaScript(Cloudflare Worker)이고 원격(클라우드) 서비스입니다.

**섹션**: Monitoring (없으면 "Developer Tools")

**추가할 줄**

```markdown
- [hyunsuhahaha/public-api-monitor](https://github.com/hyunsuhahaha/public-api-monitor) 📇 ☁️ - Is this API down? Remote, no-auth MCP server with live external checks of 160 services (Korean public data APIs, Kakao/Naver login, banks and payment gateways, AI APIs, clouds, SaaS): verdict, failing step, official status page, recent incidents.
```

**PR 제목**: `Add pamon (remote MCP: is this API down?)`

**PR 본문**

```
Adds pamon, a remote MCP server (Streamable HTTP, no auth, read-only) at https://pamon.hyunsuhahaha.workers.dev/mcp.

Tools:
- check_service(query): look up a service by name, provider, id or the host/URL of a failing request; returns the current verdict (recomputed against the observation's validity window), which step failed, the provider's official status if any, incidents in the last 24 h and evidence links.
- list_problems(): what is failing or degraded right now.
- list_services(category?): everything monitored.

Useful when a developer asks their coding assistant whether a dependency is down: the assistant gets outside observations instead of guessing.

Try it: claude mcp add --transport http pamon https://pamon.hyunsuhahaha.workers.dev/mcp
Source: deploy/access/mcp.mjs. I am the author.
```

---

참고: 다른 MCP 디렉터리(mcp.so, glama.ai, PulseMCP)는 공식 MCP Registry(`server.json`)나 GitHub 저장소를 자동으로 수집하는 경우가 많습니다. Registry에 먼저 올리고 며칠 뒤 나타나는지 보세요.
