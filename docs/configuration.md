# 설정 참조 (현재 구현)

이 문서는 현재 코드의 설정 동작을 설명한다. 기본값과 검증 조건의 최종 근거는 [`internal/config/config.go`](../internal/config/config.go), 로깅 설정은 [`cmd/ticketing/main.go`](../cmd/ticketing/main.go)다. 아래 기본값은 로컬 개발용이며 운영 권장값을 뜻하지 않는다.

## 적용 방식

- `migrate`, `init-state`, `serve` 모두 시작 시 프로세스 환경 변수에서 설정을 읽고 검증한다. `serve` 실행 중 환경 변수 재적용 기능은 없다.
- 환경 변수가 없거나 빈 문자열이면 코드 기본값을 사용한다. 단, `REDIS_PASSWORD`와 `TICKET_KEY_REDIS_PASSWORD`는 빈 문자열이 그대로 적용된다. `.env.example`은 예시일 뿐이며 앱이 `.env` 파일을 자동으로 읽지 않는다.
- 시간 값은 Go duration 형식이다(예: `250ms`, `3s`, `20m`). `EVENT_IDS`는 0보다 큰 10진수 ID의 쉼표 구분 목록이며 공백을 제거하고 중복을 없앤다.
- 세 명령은 같은 설정 구조를 파싱하지만, `migrate`는 MySQL만 연결하고 `init-state`는 대기열 Redis만 연결한다. `serve`에서만 티켓 키 Redis에 공개키를 등록한다.

## 연결, 식별, 인증, 서명

| 환경 변수 | 기본값 | 용도 |
| --- | --- | --- |
| `HTTP_ADDR` | `:8080` | `serve`의 HTTP 수신 주소 |
| `REDIS_ADDR` | `127.0.0.1:16379` | 대기열·제어 상태 Redis 주소 |
| `REDIS_PASSWORD` | 빈 문자열 | Redis 인증 비밀번호 |
| `TICKET_KEY_REDIS_ADDR` | `127.0.0.1:16380` | 공개 검증키만 저장하는 별도 Redis 주소 |
| `TICKET_KEY_REDIS_PASSWORD` | 빈 문자열 | 티켓 키 Redis 인증 비밀번호 |
| `MYSQL_DSN` | `ticketing:ticketing@tcp(127.0.0.1:13306)/ticketing?parseTime=true&loc=UTC&charset=utf8mb4&multiStatements=true` | MySQL 연결 문자열 |
| `TICKETING_INSTALLATION_ID` | `ticketing-local-v1` | Redis installation marker와 이벤트 제어 상태의 설치 식별자 |
| `EVENT_IDS` | `100,200` | 이 프로세스가 초기화하거나 서비스할 이벤트 ID 목록 |
| `AUTH_MODE` | `development` | 현재 구현된 유일한 인증 모드. 개발용 subject 헤더 사용 |
| `TICKET_KEY_LIFETIME` | `1h` | 한 인스턴스가 생성한 Ed25519 개인키의 서명 가능 기간. 백그라운드 작업이 만료 전에 새 키를 등록·전환 |
| `TICKETING_LOG_LEVEL` | `info` | JSON 로그 수준. `debug`, `warn`, `error`도 인식하며 그 밖의 값은 `info`로 처리 |

기본 MySQL 자격증명은 로컬 fixture다. 운영 비밀번호는 별도로 주입하고 소스·로그에 남기지 않는다. 개인 서명키는 인스턴스 메모리에만 생성·보유하며 Redis에는 공개키만 저장한다. `AUTH_MODE=development`와 개발용 subject 헤더를 공개 서비스에 사용하지 않는다.

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
| `DEPENDENCY_TIMEOUT` | `2s` | 요청·백그라운드 키 등록 시도 등의 의존성 호출 제한 시간. 양수여야 함 |
| `SHUTDOWN_TIMEOUT` | `10s` | HTTP 서버 종료 제한 시간 |

## 시작 시 검증과 다중 인스턴스

현재 시작 시 검증하는 조건은 `AUTH_MODE=development`, 비어 있지 않은 installation ID와 키 Redis 주소, `TICKET_TTL > 0`, `0 < TICKET_KEY_LIFETIME <= 24h`, `DEPENDENCY_TIMEOUT > 0`, `SLOT_WIDTH > 0`, `READY_WINDOW >= SLOT_WIDTH`, `PAGE_BITS > 0` 및 8의 배수, `GRANT_TTL > POLL_INTERVAL`, `BOOKING_IDLE_TTL > 0`, `BOOKING_MAX_LIFETIME >= BOOKING_IDLE_TTL`, 양수인 capacity·admission rate·burst·grant 상한, 비어 있지 않은 `EVENT_IDS`다. 모든 시간·정수 설정의 양수 여부를 일괄 검증하는 것은 아니므로 잘못된 값이 시작 뒤에 실패를 일으킬 수 있다.

여러 `serve` 인스턴스가 같은 이벤트를 다룬다면 같은 installation ID, 티켓 키 Redis, 호환되는 대기표·대기열 설정을 사용해야 한다. 개인키는 공유하지 않아도 되지만, 다른 인스턴스가 발급한 토큰을 검증하려면 같은 공개키 레지스트리에 접근해야 한다. `TICKET_TTL`, `PAGE_BITS`, `SLOT_WIDTH`, `READY_WINDOW`처럼 토큰 수명이나 Redis 비트맵 해석에 영향을 주는 값도 인스턴스 간 일치시킨다.

## 서명키와 로테이션

대기표 버전 2는 Ed25519로 서명한다. `serve`는 인스턴스별 키쌍을 메모리에서 생성하고 공개키를 별도 Redis에 **먼저** 등록한 뒤 서명한다. `kid`는 공개키 바이트의 SHA-256 해시를 Base64 URL-safe(no padding)로 인코딩한 값이다. 검증은 토큰의 `kid`로 공개키를 조회하고 해시·서명·subject·event·epoch·만료·순번 상한을 확인한다. Redis 접근과 저장 형식은 [키 Redis 어댑터](../internal/keyredis/registry.go)에 캡슐화되어 있다.

장기 보관하는 개인키는 `memguard`의 잠긴·읽기 전용 `LockedBuffer`에 둔다. 키 생성 시 seed를 잠긴 메모리로 받고, 생성 과정의 일반 Go 메모리 사본은 보호 버퍼로 옮긴 직후 지운다. Go 1.27의 표준 `crypto/ed25519`는 보호 버퍼 주소로 직접 서명할 수 없어 서명 호출 중에만 일반 메모리 복사본을 만들고 직후 지운다. 교체된 키는 즉시, 만료된 키는 백그라운드 작업이 감지할 때, 활성 키는 서비스 종료 시 버퍼를 파기한다. 운영 환경은 `mlock`/`VirtualLock` 가능한 메모리 한도를 제공해야 한다. 이 조치는 임의 노출 위험을 낮추지만 프로세스 권한을 가진 공격자나 Go 암호 라이브러리 내부 임시 사본까지 차단하는 엔클레이브 보장은 아니다.

서명 가능 기간은 키 생성 시각부터 `TICKET_KEY_LIFETIME`이다. 백그라운드 작업이 기간의 10%(최대 1분) 전에 새 키를 생성·등록하고 전환한다. 등록에 실패하면 같은 `kid`로 제한 시간 있는 시도를 지수 백오프(최대 5초, jitter)하며 계속 반복한다. 기존 키가 유효한 동안은 이를 유지하고, 만료까지 대체 키가 등록되지 못하면 서명 필요 요청은 `TICKET_KEY_UNAVAILABLE`(503, 재시도 가능)로 닫힌다. `serve`는 키 Redis가 불가해도 HTTP를 시작하지만 키 등록 전 `/readyz`는 503이다. `/readyz`와 발급·갱신 요청은 키를 생성하거나 등록하지 않는다.

공개키의 Redis TTL은 등록 시점부터 남은 서명 가능 기간에 `TICKET_TTL + 1m`을 더한 값이며, 이전 개인키가 사라져도 이전 공개키로 미만료 대기표를 검증할 수 있다. 발급 전에는 현재 공개키가 등록되어 있는지 재확인한다. 평상시 현재 공개키가 유실되면 백그라운드 작업이 최대 5초 간격으로 조회해 같은 키를 재등록한다. 교체 구간에는 새 키 등록을 우선하므로 이미 유실된 구 키로 서명한 대기표의 복구까지 보장하지 않는다. 키 Redis가 불가하거나 현재 키가 사라지면 발급·검증·readiness가 `TICKET_KEY_UNAVAILABLE`로 닫힌다. 다만 서명 없이 가능한 `DIRECT` 입장은 키가 없어도 계속 가능하다. 모르는 `kid`는 `TICKET_INVALID`다.

키 Redis에는 AOF와 `noeviction`을 사용하지만 Redis 장애·상태 손실의 무손실 복구는 보장하지 않는다. 현재 인스턴스가 보유한 공개키는 재등록할 수 있지만, 이전 인스턴스나 이미 교체한 키의 공개키 유실은 자동 복구할 수 없다. 공개키 등록 권한은 별도 Redis의 네트워크·인증/ACL로 제한해야 하며, 공개키 해시만으로 등록 주체를 인증하지는 못한다. 키 Redis 접근 가능자가 임의 키를 등록할 수 있으면 토큰을 위조할 수 있다. 현재는 긴급 폐기, 다중 버전 무중단 롤백을 제공하지 않는다. 기존 HMAC 버전 1 대기표는 새 버전 2에서 거부되므로, 배포 전 미만료 대기표의 처리 방침을 정해야 한다.
