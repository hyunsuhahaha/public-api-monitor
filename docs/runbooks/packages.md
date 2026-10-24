# 패키지 조회·다운로드 장애 대응

대상: `npm-metadata`, `npm-download`, `pypi-metadata`, `pypi-download`. 키가 필요 없는 고정 버전 검사다. 검사 파일을 실행·설치·압축 해제하지 않는다.

## 영향과 확인

- 메타데이터만 실패: 패키지 정보 조회 경로의 문제 후보. 고정 URL의 다운로드는 계속 검사한다.
- 다운로드만 실패: 파일 전달 경로·프록시·CDN 문제 후보. 정보 조회 성공이 설치 가능을 보장하지 않는다.
- 둘 다 실패: 공급자 상태와 다른 측정 지점의 DNS·TCP·TLS 기록을 함께 확인한다.
- HTTP 429/403: 공유 출발 IP의 호출 제한·접근 차단 가능성을 확인한다. 공급자 전체 장애라고 단정하지 않는다. 호출 빈도와 재시도를 낮춘다.

공식 상태: [npm](https://status.npmjs.org/), [Python/PyPI](https://status.python.org/). 공식 공지는 직접 검사 결과와 구분한다.

## HTTP 200인데 파일 오류

`artifact: incomplete or undersized download`: 파일이 잘리거나 예상보다 작다. `download exceeds byte limit`: 파일이 고정 버전보다 크다. `SHA256 mismatch`: 크기는 맞아도 내용이 다르다. 모두 사용할 수 없는 파일로 보고 가용률 실패로 센다.

먼저 같은 URL을 별도로 받아 상태·파일 크기·SHA-256을 비교한다. 오류 HTML·인증 페이지가 200으로 내려오는지, 프록시가 내용을 바꾸는지 확인한다. 근거 없이 기준 해시를 새 응답에 맞춰 바꾸지 않는다. 검사 파일이 삭제·철회됐다면 공급자 전체 장애와 구분해 대상을 교체한다.

최대 읽기는 `max_bytes+1`, 설정 가능한 최대치는 1MiB다. 큰 패키지로 바꾸어 네트워크와 메모리를 낭비하지 않는다. 설치 전체·모든 버전·신규 배포·인증·미러 동기화는 이 검사 범위에 포함되지 않는다.

## 검사 파일 변경

공식 버전 메타데이터에서 배포 URL을 확인한다. 파일을 별도로 내려받아 SHA-256·크기를 측정하고, PyPI는 제공된 `digests.sha256`과 비교한다. `targets.yaml`의 메타데이터 이름·버전·다운로드 URL·해시·크기를 함께 바꾼다. 검사에 사용할 소형 불변 버전으로 고정한다. 변경 후 설정 검증, 실제 조회·다운로드, 다음 Pages 스냅샷과 정상 판정 2회를 확인한다.

문서: [npm Registry API](https://github.com/npm/registry/blob/main/docs/REGISTRY-API.md), [PyPI JSON API](https://docs.pypi.org/api/json/).
