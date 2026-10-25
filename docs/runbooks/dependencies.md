# Docker Hub·GitHub 장애 대응

공식 상태: [Docker](https://www.dockerstatus.com/), [GitHub](https://www.githubstatus.com/). 해당 상태와 직접 측정은 구분한다. [npm·PyPI 런북](packages.md)도 참고한다.

## Docker 인증과 manifest

`docker-auth`는 hello-world 공개 이미지 pull 범위의 익명 토큰 발급을 검사한다. `docker-manifest`는 매 검사마다 토큰을 따로 받아 고정 digest의 index를 검사한다. 어느 쪽도 사용자 계정 키가 필요하지 않다.

토큰 발급 실패 시 manifest 요청은 보내지 않는다. manifest는 `prerequisite:` 사유의 client(확인 불가)로 가용률에서 제외한다. 인증은 정상인데 manifest만 실패하면 registry 경로·익명 pull 제한·image digest 보존 여부를 확인한다. 429·403이면 출발 IP를 공유하는 CI/다른 프로세스의 호출량도 확인하고 재시도 빈도를 낮춘다.

manifest의 schemaVersion·mediaType·참조 digest·크기를 검사하고 고정 파일 크기·SHA-256까지 비교한다. index 11KB 정도만 조회하며 이미지 레이어 다운로드·이미지 실행·push는 검사하지 않는다. 이미지의 삭제·철회는 검사 대상 변경 사유일 수 있으므로 전체 Hub 장애와 구분한다. 기준 해시는 정상이라고 확인한 공식 digest와 일치해야 한다.

토큰은 로그·이슈·트레이스에 남기지 않는다. `pamon -dump docker-auth`는 본문을 숨긴다. `-dump docker-manifest`도 임시 인증을 적용하고 키를 가린다. 외부 응답이 제안하는 임의 인증 URL을 따라가지 않는다.

## GitHub API와 Git 전송 경로

`github-api`는 고정 공개 저장소 이름·ID가 맞는 JSON 응답인지 확인한다. `github-git`는 Git smart HTTP의 참조 광고를 검사한다. HTTP 200이어도 HTML 로그인·오류 페이지, 잘린 pkt-line, HEAD 누락, 마지막 flush 누락이면 실패다.

API만 실패하면 호출 제한·API 경로를, Git만 실패하면 Git 전송 경로·콘텐츠 필터·프록시·저장소 주소 변경을 확인한다. GitHub API의 403/429는 출발 IP 공유에 의한 제한일 수 있다. 두 결과의 차이를 유지한다.

참조 조회는 ls-remote 단계다. 실제 객체 다운로드·인증된 clone·push·Actions·패키지·웹훅의 정상 여부를 의미하지 않는다. 지원하는 참조 광고는 Git protocol v0이며 Git-Protocol 헤더로 v2를 요청하지 않는다.

## 종료 조건

원인이 해소된 뒤 해당 검사가 정상 2회로 장애를 닫았는지 확인한다. Pages의 생성 시각과 새 스냅샷도 확인한다. 테스트 대상 변경 시 URL·불변 버전/digest·구조·체크섬을 함께 검증한다.
