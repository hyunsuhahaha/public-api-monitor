# MCP Registry · Smithery 등록 초안

## 공식 MCP Registry (server.json)

[server.json](server.json)이 그대로 게시할 파일입니다(원격 서버만 있고 패키지는 없음).

```sh
# mcp-publisher 설치: https://github.com/modelcontextprotocol/registry 의 안내를 따르세요
cd docs/outreach
mcp-publisher login github      # io.github.hyunsuhahaha/* 이름의 소유 증명
mcp-publisher publish           # server.json 게시
```

- `description`은 100자 제한이라 짧습니다(현재 97자).
- 버전을 올릴 때마다 `version`을 바꿔 다시 게시합니다. 같은 버전은 다시 올릴 수 없습니다.
- 게시 직전 `$schema` 최신 날짜를 확인하세요.

## Smithery (smithery.ai)

Smithery는 외부에서 운영하는 원격 MCP 서버를 URL로 등록할 수 있습니다. 로그인 → 새 서버 → 외부(원격) URL에 `https://pamon.hyunsuhahaha.workers.dev/mcp`. 등록 화면이 바뀌었을 수 있으니 안내를 따르세요. 설정(config) 항목은 없습니다(인증 없음).

**이름**: pamon: is this API down?

**짧은 설명** (한 줄)

> Live external checks of 160 APIs and services: is it down, where it fails, and what the provider says.

**긴 설명**

```
pamon calls one fixed, read-only function of about 160 services every 5 minutes from Seoul and US West and tells your assistant what it saw. Coverage: Korean public data APIs (data.go.kr, Seoul open data), Kakao and Naver login, Hometax and Gov24, banks and payment gateways, OpenAI/Anthropic and other AI APIs, AWS, GCP and Naver Cloud, Slack, Discord, GitHub, npm, PyPI, Docker Hub and more.

Tools
- check_service(query): look up by service name (Korean or English), provider, id, or the host/URL of the failing request. Returns the verdict as of now (an expired observation is "unknown", never "ok"), last check time and validity, which step failed (DNS, TCP, TLS, server wait, HTTP code, wrong data), the provider's official status page when it has one, incidents in the last 24 hours, retry advice, and links to cite.
- list_problems(): everything failing or degraded right now.
- list_services(category?): what is monitored, optionally by category (login, payments, government, AI, cloud, SaaS, packages, weather, traffic...).

Read-only, no authentication, no setup. These are independent outside observations of one function per service, not the provider's official statement.
```

**태그**: monitoring, status, outage, sre, korea, api, uptime

**예시 질문**
- "카카오 로그인이 지금 안 되는데 카카오 쪽 장애야?"
- "Our calls to api.tosspayments.com time out since 14:05 KST. Is it them?"
- "What is down right now?"
