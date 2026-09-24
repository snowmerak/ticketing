# 설정 참조 (현재 구현)

이 문서는 현재 코드의 설정 동작을 설명한다. 기본값과 검증 조건의 최종 근거는 [`internal/config/config.go`](../internal/config/config.go), 로깅 설정은 [`cmd/ticketing/main.go`](../cmd/ticketing/main.go)다. 아래 기본값은 로컬 개발용이며 운영 권장값을 뜻하지 않는다.

## 적용 방식

- `migrate`, `init-state`, `serve` 모두 시작 시 프로세스 환경 변수에서 설정을 읽고 검증한다. `serve` 실행 중 환경 변수 재적용 기능은 없다.
- 환경 변수가 없거나 빈 문자열이면 코드 기본값을 사용한다. 단, `REDIS_PASSWORD`는 빈 문자열이 그대로 적용된다. `.env.example`은 예시일 뿐이며 앱이 `.env` 파일을 자동으로 읽지 않는다.
- 시간 값은 Go duration 형식이다(예: `250ms`, `3s`, `20m`). `EVENT_IDS`는 0보다 큰 10진수 ID의 쉼표 구분 목록이며 공백을 제거하고 중복을 없앤다.
- 각 명령이 실제로 사용하지 않는 연결 정보도 설정 로드 대상이다. 예를 들어 `migrate` 역시 현재는 유효한 티켓 서명키 설정을 요구한다.

## 연결, 식별, 인증, 서명

| 환경 변수 | 기본값 | 용도 |
| --- | --- | --- |
| `HTTP_ADDR` | `:8080` | `serve`의 HTTP 수신 주소 |
| `REDIS_ADDR` | `127.0.0.1:16379` | 대기열·제어 상태 Redis 주소 |
| `REDIS_PASSWORD` | 빈 문자열 | Redis 인증 비밀번호 |
| `MYSQL_DSN` | `ticketing:ticketing@tcp(127.0.0.1:13306)/ticketing?parseTime=true&loc=UTC&charset=utf8mb4&multiStatements=true` | MySQL 연결 문자열 |
| `TICKETING_INSTALLATION_ID` | `ticketing-local-v1` | Redis installation marker와 이벤트 제어 상태의 설치 식별자 |
| `EVENT_IDS` | `100,200` | 이 프로세스가 초기화하거나 서비스할 이벤트 ID 목록 |
| `AUTH_MODE` | `development` | 현재 구현된 유일한 인증 모드. 개발용 subject 헤더 사용 |
| `TICKET_HMAC_KEY_ID` | `local-v1` | 대기표의 `kid`에 기록하는 현재 대칭키 식별자 |
| `TICKET_HMAC_KEY` | 개발용 Base64 fixture ([코드](../internal/config/config.go)) | Base64로 인코딩한 HMAC-SHA-256 비밀키. 디코딩 결과 최소 32바이트 필요 |
| `TICKETING_LOG_LEVEL` | `info` | JSON 로그 수준. `debug`, `warn`, `error`도 인식하며 그 밖의 값은 `info`로 처리 |

기본 MySQL 자격증명과 HMAC 키는 로컬 fixture다. 운영 비밀은 별도로 주입하고 소스·로그에 남기지 않는다. `AUTH_MODE=development`와 개발용 subject 헤더를 공개 서비스에 사용하지 않는다.

## 대기열과 입장 제어

| 환경 변수 | 기본값 | 용도 |
| --- | --- | --- |
| `TICKET_TTL` | `20m` | 새로 발급하거나 갱신한 대기표의 만료 시간 |
| `POLL_INTERVAL` | `3s` | heartbeat 응답의 다음 폴링 간격 |
| `READY_WINDOW` | `10s` | 입장 준비 판정에 쓰는 시간 창 |
| `SLOT_WIDTH` | `2s` | 준비 상태 비트맵의 시간 슬롯 폭 |
| `PAGE_BITS` | `65536` | 대기 순번 비트맵 한 페이지의 비트 수 |
| `SCHEDULER_INTERVAL` | `250ms` | 입장 스케줄러 실행 주기 |
| `GRANT_TTL` | `15s` | 발급된 입장 grant의 수명 |
| `STATS_INTERVAL` | `1s` | 대기열 통계 갱신 주기 |
| `SCHEDULER_LEASE_TTL` | `2s` | 이벤트별 스케줄러 writer lease 수명 |
| `MAX_GRANTS_PER_TICK` | `25` | 스케줄러 한 번에 처리하는 grant 후보 상한 |
| `MAX_SEQ_PER_EPOCH` | `10000000` | epoch당 허용하는 대기 순번 상한 |
| `CAPACITY` | `1000` | 이벤트의 동시 입장·예약 용량. 최초 `init-state`에서 Redis 제어 상태에 저장 |
| `ADMISSION_RATE` | `100` | Redis token bucket의 초당 입장 토큰 충전량. 최초 `init-state`에서 저장 |
| `ADMISSION_BURST` | `25` | Redis token bucket 최대 토큰 수. 최초 `init-state`에서 저장 |

`CAPACITY`, `ADMISSION_RATE`, `ADMISSION_BURST`는 기존 이벤트의 경우 프로세스 환경 변수보다 Redis의 이벤트 제어 상태가 실제 동작의 기준이다. `init-state`는 신규 제어 상태를 만들 때 설정값을 쓰지만, 기존 필드는 `HSETNX` 방식으로 보존한다. 따라서 환경 변수를 바꾼 뒤 재시작하거나 `init-state`를 다시 실행해도 기존 이벤트의 값은 수정되지 않는다. `init-state`는 부분 손실 복구 명령도 아니다. 이 값의 운영 중 변경 절차는 아직 제공하지 않는다.

## 예약, DB, 시간 제한

| 환경 변수 | 기본값 | 용도 |
| --- | --- | --- |
| `BOOKING_IDLE_TTL` | `60s` | booking 세션의 유휴 만료 시간 |
| `BOOKING_MAX_LIFETIME` | `10m` | booking 세션의 최대 수명 |
| `HOLD_TTL` | `120s` | MySQL 좌석 hold의 만료 시간 |
| `HOLD_REAPER_INTERVAL` | `1s` | 만료된 hold 정리 주기 |
| `DB_MUTATION_LIMIT` | `64` | 프로세스별 동시 DB 변경 작업 상한 |
| `DEPENDENCY_TIMEOUT` | `2s` | 요청·백그라운드 작업의 의존성 호출 제한 시간 |
| `SHUTDOWN_TIMEOUT` | `10s` | HTTP 서버 종료 제한 시간 |

## 시작 시 검증과 다중 인스턴스

현재 시작 시 검증하는 조건은 `AUTH_MODE=development`, HMAC 키 최소 길이, 비어 있지 않은 installation/key ID, `SLOT_WIDTH > 0`, `READY_WINDOW >= SLOT_WIDTH`, `PAGE_BITS > 0` 및 8의 배수, `GRANT_TTL > POLL_INTERVAL`, `BOOKING_IDLE_TTL > 0`, `BOOKING_MAX_LIFETIME >= BOOKING_IDLE_TTL`, 양수인 capacity·admission rate·burst·grant 상한, 비어 있지 않은 `EVENT_IDS`다. 모든 시간·정수 설정의 양수 여부를 일괄 검증하는 것은 아니므로 잘못된 값이 시작 뒤에 실패를 일으킬 수 있다.

여러 `serve` 인스턴스가 같은 이벤트를 다룬다면 같은 installation ID와 호환되는 대기표·대기열 설정을 사용해야 한다. 특히 현재 서명 검증기는 프로세스에 주입된 단일 HMAC 키만 알고 있으므로 모든 인스턴스가 동일한 `TICKET_HMAC_KEY_ID`와 `TICKET_HMAC_KEY`를 가져야 서로의 대기표를 검증할 수 있다. `PAGE_BITS`, `SLOT_WIDTH`, `READY_WINDOW`처럼 Redis 비트맵 해석에 영향을 주는 값도 인스턴스 간 일치시킨다.

## 서명키와 로테이션의 현재 상태

현재 대기표는 HMAC-SHA-256으로 서명하며 `kid`를 포함한다. 그러나 `kid`는 공개키 해시가 아니라 `TICKET_HMAC_KEY_ID` 환경 변수의 문자열이다. 프로세스 시작 시 단일 대칭 비밀키만 서명기·검증기에 넣는다. 과거 키를 설정으로 추가하거나 Redis에서 키를 조회하는 기능, 자동 키 생성·등록·회전 기능은 없다. 단순히 키나 ID를 바꾸면 기존 미만료 대기표가 검증되지 않을 수 있다.

제안된 **인스턴스별 메모리 내 비대칭 키쌍 생성 → 공개키 해시를 `kid`로 사용 → 별도 Redis에 공개키 등록 → 토큰의 `kid`로 조회·검증** 방식은 현재 설정·구현에 포함되어 있지 않다. 이는 HMAC의 단순 로테이션이 아니라 서명 프로토콜과 검증 신뢰 경계의 변경이다. 채택한다면 공개키 등록 주체의 신뢰, 서명 전 등록 보장, 만료된 토큰이 모두 사라질 때까지 이전 공개키 유지, 키 Redis 장애 시 처리 정책을 별도로 정해야 한다.
