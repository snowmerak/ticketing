# Test Report — 2026-09-24

> 아래의 기존 revision·상세 ID별 결과는 Ed25519/별도 티켓 키 Redis 도입 전의 기록이다. 새 서명 경로의 현재 검증은 다음 절에 별도로 기록하며, 과거 결과를 새 코드의 증거로 일반화하지 않는다.

## Ed25519 전환 후 추가 검증

같은 Windows 개발 노트북에서 Compose 대기열 Redis·티켓 키 Redis·MySQL을 띄우고 `go run ./cmd/ticketing migrate` 후 다음을 실행했다.

| 명령 | 결과와 범위 |
|---|---|
| `go test ./...` / `go vet ./...` | PASS, 기본 단위 테스트·정적 검사 |
| `go test -count=1 -tags=integration ./...` | PASS, 실제 두 Redis와 MySQL. 공개키 TTL·손상 거부, 키 Redis 중단 시 readiness 503 포함 |
| `go test -count=1 -tags=e2e ./e2e` | PASS, 실제 바이너리 재시작 후 이전 인스턴스가 발급한 버전 2 대기표를 새 인스턴스가 검증해 구매 완료 |
| Linux 컨테이너 `go test -race -count=1 ./internal/ticket` | PASS, 동시 키 교체 포함한 서명기 단위 범위 |
| Linux 컨테이너 `go test -race -count=1 -tags=integration ./...` | PASS, 실제 두 Redis·MySQL 연동과 동시성 범위 |

기존 HMAC 버전 1의 동시 수용, 키 Redis failover·공개키 손실 복구, 운영 ACL/인증, 변경 후 k6 성능은 검증하지 않았다. 버전 1 대기표는 의도적으로 거부한다.

## 결론

구현된 단위 테스트, 실제 Redis/MySQL 통합 테스트, HTTP E2E와 race detector는 통과했습니다. 특히 이벤트별 사용자당 1석 제한, 동일 사용자의 복수 대기표와 구매 후 대기표 발급은 실제 의존성에서 확인됐습니다.

이 결과는 blueprint의 Q-01~Q-15, A-01~A-12, S-01~S-19, F-01~F-07 전체 완료를 뜻하지 않습니다. 아래에 명시되지 않은 세부 ID와 failure-injection/장시간 다중 프로세스 항목은 `NOT RUN`입니다.

## 환경

| 항목 | 값 |
|---|---|
| 기준 revision | `11a9a05` 기반 working tree(이번 E2E 테스트 추가분 포함) |
| Host | Windows 11 Home 10.0.26200 |
| CPU / RAM | AMD Ryzen 7 8845HS, 16 logical CPU / 27.8 GiB |
| Go | 1.27.1 windows/amd64 |
| Docker / Compose | 29.8.0 / v5.5.1 |
| Redis | 7.4.5, `redis@sha256:bb186d083732f669da90be8b0f975a37812b15e913465bb14d845db72a4e3e08` |
| MySQL | 8.4.6, `mysql@sha256:869218921e61d6c3c89820955d63cca42971f0e3e6c1e2792247bbd944ebc6e9` |
| MySQL runtime | UTC, READ-COMMITTED |

## 실행 결과

| 명령 | 결과 |
|---|---|
| `go test ./...` | PASS |
| `go vet ./...` | PASS |
| `go test -count=1 -tags=integration ./...` | PASS |
| `docker run ... golang:1.27.1-bookworm go test -race ./...` | PASS |
| `docker run ... golang:1.27.1-bookworm go test -race -count=1 -tags=integration ./...` | PASS |
| `go run ./cmd/ticketing migrate` | PASS, clean MySQL 8.4.6에 schema/seed 적용 |
| `go run ./cmd/ticketing init-state` | PASS, installation marker와 event control 생성 |
| HTTP direct → seat map → hold → confirm smoke | PASS |
| `go test -tags=e2e -count=1 -v ./e2e` | PASS, 실제 빌드 바이너리·HTTP 포트·Redis/MySQL·admission worker를 통과하는 전체 구매 경로 |

Windows host의 직접 `go test -race`는 GCC 부재로 실행할 수 없었습니다. 동일 working tree를 공식 `golang:1.27.1-bookworm` 컨테이너에서 실행해 단위·통합 race 결과를 모두 PASS로 대체했습니다.

## 명시적 acceptance 결과

| ID | 결과 | 증거 |
|---|---|---|
| Q-14 | PASS | 실제 Redis에서 같은 subject의 연속 2건 + 동시 64건이 모두 고유 `ticket_id`/`seq`를 획득 |
| Q-15 | PASS | HTTP E2E로 구매 확정 후 같은 subject가 독립 대기표 2개를 발급받고 MySQL order 수가 1로 유지 |
| Q-13 | PASS(종료 정리 범위) | 만료 조건을 충족한 epoch가 원자적으로 CLOSED/DIRECT 전환되고 epoch key에 bounded batch로 cleanup TTL 적용 |
| A capacity/redeem | PASS(부분 범위) | 실제 Redis에서 32 READY를 동시 claim해 capacity 8만 예약; redeem/replay/leave 후 `reserved + active <= 8` 확인 |
| A-12 | PASS | lease fence를 강제로 승계한 뒤 이전 writer claim은 실패하고 현재 writer만 성공 |
| S-01 | PASS | 실제 MySQL 다중 connection의 동일 좌석 동시 hold에서 정확히 1건만 성공 |
| S-07 | PASS | 지정 2석과 자동배정 quantity 2가 limit 오류; hold/guard/inventory 무변경 |
| S-17 | PASS | 구매 후 추가 hold가 안정적인 `SEAT_PURCHASE_LIMIT_EXCEEDED` 반환 |
| S-18 | PASS | 같은 subject의 서로 다른 두 hold를 동시 confirm해 정확히 order 1건만 생성 |
| S-19 | PASS | 같은 subject가 서로 다른 두 event에서 각각 한 좌석 구매 성공 |
| Hold idempotency | PASS | 같은 booking/payload/key는 같은 hold, booking이 달라지면 `IDEMPOTENCY_CONFLICT` |
| F-01 | PASS(신규 entry 범위) | Redis 접속 불가 시 503 `REDIS_UNAVAILABLE`, booking permit 미발급 |
| F-02 | PASS(신규 entry 범위) | MySQL 접속 불가 시 DIRECT permit 대신 Redis 대기표 발급 |
| F-03 | PASS(핵심 상태 유실 범위) | QUEUE control만 남고 active epoch가 없으면 `QUEUE_RECOVERING`으로 fail-closed 및 latch |
| API E2E 재시작 | PASS | DIRECT 점유 → 복수 QUEUE ticket → 서버 프로세스 재시작 → 기존 ticket으로 grant/redeem → 좌석 hold/cancel/auto/confirm → 구매 후 새 ticket/permit에서도 추가 구매 거절; 주문 1건·SOLD 1석을 DB에서 확인하고 전용 event 데이터 정리 |

추가로 token tamper/subject/event/expiry/seq 상한, Redis MSB bit order·page 경계, READY OR와 spent/reserved 제거, 설정 관계를 단위 테스트로 확인했습니다. grant redeem replay가 같은 booking ID를 반환하는 것도 실제 Redis에서 확인했습니다.

## NOT RUN과 경계

- Q/A/S 그룹 중 위 표와 단위 테스트 설명에 직접 대응하지 않는 개별 ID는 아직 ID별 독립 증거가 없으므로 `NOT RUN`입니다.
- 전체 API E2E에서 서버 프로세스 재시작 후 queue 진행은 검증했습니다. Redis/MySQL process kill, COMMIT 응답 유실, 독립 worker 장애 주입과 F-04~F-07의 개별 수용 조건은 `NOT RUN`입니다.
- 복수 API 프로세스의 장시간 lease handoff/soak는 `NOT RUN`입니다.
- 실제 PG, 운영 인증, Redis failover는 V1 범위 밖이며 테스트하지 않았습니다.
- 통합 테스트는 실제 dependency가 없으면 skip하지 않고 실패하도록 작성했습니다.

따라서 현재 결과는 로컬 V1 구현의 핵심 안전성 증거이며 운영 준비 완료 선언은 아닙니다.
