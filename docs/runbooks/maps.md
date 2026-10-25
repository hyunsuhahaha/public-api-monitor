# 지도 API 장애 대응

대상: `kakao-address`, `kakao-place`, `naver-geocode`, `naver-directions`.

## 인증·권한·쿼터 (client)

- `credential not set`: 실행 환경의 `KAKAO_REST_API_KEY` 또는 `NAVER_MAPS_CLIENT_ID`/`NAVER_MAPS_CLIENT_SECRET`을 등록한다. `.env` 파일만 만들어도 직접 실행하는 Go에는 전달되지 않는다. Actions는 Secrets, Compose는 env_file, Ansible은 vault 설정과 배포를 확인한다.
- `KAKAO:-401`: REST API 키인지 확인한다. `-3/-5` 등은 카카오맵 활성화·앱 사용 권한을 확인한다.
- `KAKAO:-10/-11`: 일간·월간 쿼터, 유료 금액 한도와 무료 쿼터 적용 앱을 확인한다. 무조건 재시도하지 않는다.
- `NAVER:200/210`: NCP Maps 앱의 Client ID/Secret·권한 확인. `NAVER:400`은 할당량 초과뿐 아니라 Geocoding/Directions 5 미선택도 확인한다. `410/420`은 호출 속도를 줄인다.
- `NAVER:1/2/4/5`, `NAVER:INVALID_REQUEST`: 경로의 좌표·도로 접근성·요청값을 확인한다. 대상 자체를 수정해야 하는 문제다.

이 상태는 공급자 가용률에 포함하지 않는다. 공개 페이지에서 확인 불가인 대상을 정상으로 읽지 않는다.

## 공급자 오류·지연 (error/down/slow)

- 카카오 `-1/-7/-603`은 HTTP 400에서도 공급자 처리·점검·내부 타임아웃이다. Naver `SYSTEM_ERROR`, 경로 코드 `3`은 처리 실패다.
- 같은 공급자의 다른 감시 기능과 다른 지역의 결과를 비교한다. 한 감시 지점만 실패하면 DNS·TCP·TLS·출발 IP 제한부터 확인한다.
- 공급자 공식 문서의 지원·공지 경로에서 점검 여부를 확인한다. 공공데이터포털 고객센터는 지도 API 문의처가 아니다.
- HTTP 5xx·타임아웃은 기존 네트워크 구간 기록과 재확인 기록을 본다. 재시도도 쿼터와 요금에 포함될 수 있다.

## 데이터 이상 (stale)

`bad data: documents...`, `addresses...`, `route...`는 고정 검사 주소/경로의 필드 누락·빈 결과·좌표 범위 이탈·거리/시간 범위 이탈이다. 응답 지연과 별도로 조사한다.

`pamon -dump <id>`로 본문을 확인한다. 주소 데이터·도로망의 정상 변경이면 검사 주소·허용 범위를 수정하되, 응답 구조가 깨진 것을 정상으로 숨기지 않는다. 실제 키를 URL·공유 로그·이슈·스크린샷에 넣지 않는다.

## 종료 조건

인증 설정 문제는 올바른 키로 실제 정상 본문을 확인한 뒤 종료한다. 열린 장애는 정상 판정 2회로 닫힌 것을 확인한다. 배포 후 `/readyz`와 해당 대상의 최신 검사 시각을 확인한다. Pages는 생성 시각과 다음 게시 결과를 확인한다.

공식 문서: [카카오맵 REST](https://developers.kakao.com/docs/ko/kakaomap/rest-api), [카카오 오류 코드](https://developers.kakao.com/docs/ko/rest-api/error-code), [NCP Maps 공통 설정](https://api.ncloud-docs.com/docs/application-maps-overview), [Geocoding](https://api.ncloud-docs.com/docs/application-maps-geocoding), [Directions 5](https://api.ncloud-docs.com/docs/application-maps-directions5).
