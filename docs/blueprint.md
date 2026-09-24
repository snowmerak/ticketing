# 티케팅 대기열 및 좌석 선점 시스템 Blueprint

> 상태: 로컬 V1 구현됨, 검증 진행 중 — 운영 준비 완료를 의미하지 않음
> 대상 독자: 구현자, 리뷰어, 테스트·운영 담당자
> 기준 입력: `ticketing-architecture-codex.md` v1.0 (2026-09-24)
> 마지막 정리: 2026-09-24
> 변경 시점: 목적, 책임 경계, 주요 불변식, 품질 목표, 마일스톤 또는 핵심 위험이 바뀔 때

> 최신 범위 결정(2026-09-24): 한 사용자는 이벤트별로 좌석을 최대 1석만 구매할 수 있다. 반면 같은 사용자의 대기표 개수는 제한하지 않으며, 이미 좌석을 구매한 뒤에도 새 대기표를 발급받을 수 있다. 이 결정은 기준 입력의 다중 좌석 hold와 `max_seats_per_hold=4`보다 우선한다.

이 문서는 프로젝트의 현재 구현 방향과 전달 순서를 설명하는 상위 지도다. 현재 코드의 실행 구성·데이터 권위·장애 경계는 [architecture.md](./architecture.md)가 설명한다. 정확한 API 스키마, Redis Lua 계약, SQL migration, 설정 스키마는 해당 소스가 권위 있는 원본이며, 이 문서는 그 위치와 관계만 설명한다. 테스트 통과나 처리량 수치를 기록하는 문서가 아니며, 실제 결과는 `docs/reports/TEST-REPORT.md`와 `docs/reports/BENCHMARK-REPORT.md`가 소유한다.

## 1. 현재 기준선

현재 저장소에는 로컬 V1 실행 경로가 구현돼 있다. 실제 통과 결과와 미실행 경계는 `docs/reports/TEST-REPORT.md`, 짧은 성능 관찰은 `docs/reports/BENCHMARK-REPORT.md`가 소유한다.

| 항목 | 현재 상태 |
|---|---|
| Go module | `github.com/snowmerak/ticketing`, Go 1.27.1 |
| 애플리케이션 코드 | Queue/admission, booking, HTTP API, worker, 최소 web client 구현 |
| Redis/MySQL 구성 | digest 고정 Compose, Redis `noeviction`/AOF, MySQL 8.4 READ COMMITTED |
| migration 및 seed | 단일 좌석 제약, purchase guard, event 100/200 synthetic inventory 구현 |
| 테스트 및 실행 보고서 | 단위·실제 dependency 통합·race·짧은 load 관찰 결과 기록 |

아래 설계 중 핵심 로컬 흐름은 구현됐지만 모든 Q/A/S/F ID와 운영 장애 조건이 검증된 것은 아니다. 특히 failover, 실제 인증/PG, 장시간 다중 프로세스와 대규모 queue 부하는 구현 완료로 읽어서는 안 된다.

## 2. 목적과 전달 범위

### 목적

대기자의 순번 권리를 유효기간 동안 유지하면서 현재 응답하는 사용자만 제한된 수용량만큼 구매 영역에 입장시키고, 최종 좌석 소유권은 MySQL/InnoDB 트랜잭션으로 판정하여 중복 선점·중복 판매와 이벤트별 사용자당 1석 초과 구매를 방지한다.

### 주요 사용자와 기대 결과

| 사용자 | 기대 결과 |
|---|---|
| 대기자 | 새로고침이나 짧은 단절 후에도 유효한 대기표로 원래 순번에 복귀한다. |
| 구매자 | 입장 허가 후 지정 좌석 1석 또는 자동배정 1석을 요청하고, hold의 만료·취소·확정을 명확히 확인한다. |
| 운영자 | 입장 상한·입장률·일시 중단을 제어하고, 권한 상태 유실 의심 시 안전하게 신규 입장을 중단한다. |
| 개발·검증 담당자 | 실제 Redis와 MySQL에서 동시성, 실패 복구, 성능 한계를 재현 가능한 명령으로 검증한다. |

### V1에 포함되는 범위

- 여유가 있을 때 queue epoch를 만들지 않는 직접 입장
- 고유 `epoch`와 단조 증가 `seq`를 사용하는 서명 대기표
- 동일 사용자의 복수 대기표 및 구매 완료 후 신규 대기표 발급
- 시간 슬롯·페이지 단위 Redis READY 비트맵과 summary
- 활성 대기자 중 작은 `seq`를 우선하는 admission scheduler
- grant, 멱등 redeem, booking permit, capacity 반환
- MySQL 8.4/InnoDB/`READ COMMITTED` 기반 좌석 재고
- 지정 좌석 `NOWAIT`, 비연석 자동배정 `SKIP LOCKED`
- 이벤트별 사용자당 1석으로 제한된 hold, 만료·취소, 개발용 모의 구매 확정
- HTTP polling 기반 최소 클라이언트와 좌석 snapshot 캐시
- 단위, 실제 DB 통합, 동시성, 실패 주입, 부하 검증 도구

### V1에서 제외되는 범위

- Redis 자동 failover 중 권한 상태 무손실 보장
- 멀티리전 순서 보장과 이벤트 내부 샤딩
- 실제 PG 과금·환불·웹훅·보상 처리
- 사용자별 대기표 개수 제한 및 구매 완료 후 대기표 발급 차단
- 운영 인증 연동, 봇 대응, 특정 CDN 배포
- 연석 자동배정, 판매 취소 후 좌석 재판매
- 한 사용자의 동일 이벤트 내 단체·다좌석 구매
- 자동 AIMD/PID admission 제어
- Kafka, Kubernetes, 별도 분산 트랜잭션 계층 또는 근거 없는 마이크로서비스 분리

Redis failover, 실제 PG, 운영 인증과 abuse 대응처럼 운영 안전성에 필요한 후속 항목을 검증하기 전에는 V1을 운영 배포 완료로 표현하지 않는다. 반면 사용자별 대기표 제한과 다좌석 구매는 의도적인 제품 비목표이므로 후속 운영 준비 과정에서도 자동으로 추가하지 않는다.

## 3. 사용자 흐름

### 대기와 입장

1. 사용자가 이벤트 입장을 요청한다.
2. 서비스가 정상이고 공유 admission budget에 여유가 있으면 queue epoch 없이 booking permit을 발급한다.
3. 여유가 없으면 활성 epoch를 원자적으로 생성하거나 재사용하고, 서명 대기표를 발급한다. 사용자가 유효한 대기표를 제시하면 그 대기표를 갱신하고, 제시하지 않으면 기존 대기표나 구매 기록과 관계없이 새 `ticket_id`와 `seq`를 발급한다.
4. 클라이언트는 대기표를 보존한 채 jitter가 적용된 heartbeat를 보낸다.
5. scheduler는 최근 READY인 미소비 사용자 중 관측된 작은 `seq`부터 grant를 예약한다.
6. 클라이언트는 grant를 같은 booking permit으로 멱등 교환한다.
7. grant를 받지 못했거나 잠시 비활성화된 사용자는 대기표 만료 전 원래 `seq`로 복귀할 수 있다.

### 좌석 선점과 구매

1. 유효한 booking permit을 가진 사용자가 좌석 snapshot을 조회한다.
2. 사용자는 좌석 ID 하나를 지정하거나 구역에서 한 좌석의 자동배정을 요청한다.
3. MySQL 트랜잭션이 구매 이력과 실제 재고를 잠그고 1석을 선점하거나 전체 실패한다.
4. hold는 취소되거나 DB 시각 기준으로 만료되거나, 개발용 모의 결제로 한 주문에 한 번만 확정된다.
5. 같은 사용자가 복수 ticket·booking·hold를 갖더라도 같은 이벤트에서 첫 주문 하나만 확정될 수 있다.
6. 화면의 availability가 오래됐더라도 최종 판정은 항상 MySQL committed state가 내린다.

## 4. 시스템 경계와 권한

V1은 하나의 Go 코드베이스 안에서 기능을 모듈로 분리하는 모듈형 서비스로 시작한다. API 프로세스는 복수 실행할 수 있지만, 이벤트별 admission writer는 lease와 fencing으로 하나만 유효하게 만든다. 별도 load generator는 제품 런타임과 분리한다.

| 경계 | 책임 | 권위 있는 상태 | 실패 시 기본 동작 |
|---|---|---|---|
| Client | 대기표 보존, polling/backoff, grant 교환, 좌석 표시 | 권위 없음 | 네트워크 오류와 실제 만료를 구분하고 토큰을 보존한다. |
| Queue API | ticket 발급·갱신, heartbeat, 상태 조회 | Redis 상태를 검증해 응답 | Redis 불가 시 신규 권한을 fail-closed한다. |
| Admission scheduler | 후보 탐색, grant claim, 만료 회수 | Redis의 fenced 원자 전이 | 중단 중에는 처리량을 잃되 기존 grant/booking은 유지한다. |
| Booking API | permit 검증, hold·cancel·confirm 진입점 | MySQL 트랜잭션 결과 | MySQL 불가 시 신규 입장과 좌석 변경을 중단한다. |
| Hold reaper | 만료 hold의 소유권 재확인과 반환 | MySQL 현재 행 | 지연 시 좌석을 보수적으로 HELD로 남긴다. |
| Seat snapshot refresher | committed inventory의 조회용 projection | 권위 없음 | 마지막 snapshot과 stale 시각을 노출한다. |
| Redis primary | epoch, READY, spent/reserved, grant, booking permit, control | 대기 순번과 입장 권한의 서버 측 권위 | 핵심 상태 유실 의심 시 RECOVERING/PAUSED로 전환한다. |
| 티켓 키 Redis | Ed25519 공개 검증키와 만료 TTL | `kid` 기반 검증을 위한 공개키 레지스트리 | 장애 시 대기표 발급·검증이 닫히며 공개키 유실 시 과거 토큰 복구는 보장하지 않는다. |
| MySQL primary | seat definition/inventory, hold, order | 좌석·주문 소유권의 최종 권위 | 트랜잭션 결과만 신뢰하고 불확실한 commit은 멱등 조회한다. |

### 의존 방향

```text
Client
  ├─ Queue/Admission API ── 대기열 Redis
  │                       └─ 티켓 서명·검증 ── 티켓 키 Redis
  └─ Booking API ────────── MySQL
             └───────────── Seat snapshot cache (derived)

Background loops
  ├─ admission / grant / booking reaper ── Redis
  ├─ hold reaper ───────────────────────── MySQL
  └─ seat refresher ────────────────────── MySQL → cache
```

Redis와 MySQL 사이에 분산 트랜잭션을 만들지 않는다. queue 순번, 입장 권한, 좌석 hold, 주문은 서로 다른 상태이며 각각의 권위와 멱등 경계에서 완료된다.

## 5. 핵심 불변식

| ID | 불변식 |
|---|---|
| Q1 | 같은 epoch 안에서 `seq`를 재사용하지 않는다. 발급 실패로 생긴 번호 공백은 허용한다. |
| Q2 | READY에서 사라져도 대기표가 유효하면 같은 `seq`로 복귀할 수 있다. |
| Q3 | 소비된 대기표는 다시 grant 또는 booking을 만들 수 없다. |
| Q4 | summary 누락 때문에 실제 후보 페이지를 영구적으로 건너뛰지 않는다. |
| Q5 | `estimated_ahead`는 표시용 추정치이며 입장 순서나 좌석 판정에 사용하지 않는다. |
| Q6 | 같은 `(event_id, subject_id)`에 여러 대기표를 발급할 수 있고, 좌석 구매 이력은 대기표 발급·갱신을 차단하지 않는다. 각 대기표는 독립된 `ticket_id`와 `seq`를 가진다. |
| A1 | capacity가 고정된 동안 `reserved + active_booking <= capacity`다. |
| A2 | grant·redeem·expire·leave의 재시도가 capacity를 중복 차감하거나 반환하지 않는다. |
| S1 | 같은 `(event_id, seat_id)`에 동시에 두 개의 유효한 hold가 존재하지 않는다. |
| S2 | 같은 `(event_id, seat_id)`를 두 주문에 판매하지 않는다. |
| S3 | 하나의 hold와 하나의 order에는 정확히 한 좌석만 연결된다. 2석 이상 요청은 좌석 상태를 바꾸기 전에 거절한다. |
| S4 | alias 변경이나 숫자 ID의 공백은 좌석 식별·잠금·소유권을 바꾸지 않는다. |
| S5 | 같은 `(event_id, subject_id)`에는 최대 한 개의 확정 order와 한 개의 SOLD 좌석만 존재한다. 여러 ticket·booking·hold와 동시 confirm도 이 제한을 우회할 수 없다. |

capacity를 현재 점유량 아래로 낮춘 경우 기존 사용자를 강제 종료하지 않는다. 신규 grant를 중단하고 점유량이 새 상한 아래로 내려올 때까지 기다린다.

## 6. 시간, 식별자와 기본 설정

서버 시간은 UTC다. Redis 전이는 Redis 시간을, 좌석 트랜잭션은 MySQL 시간을 사용한다. 클라이언트 시각은 만료 승인에 사용하지 않는다. 시간 계산은 테스트 가능한 Clock 경계로 감싸되, 가짜 시각 제어를 운영 API에 노출하지 않는다.

- `epoch`, `booking_id`, `hold_id`, `order_id`는 재사용하지 않는 128비트 무작위 식별자를 기본으로 한다. `ticket_id`와 `grant_id`도 재시도 상관관계에 사용할 안정적인 불투명 ID로 두되 정확한 표현은 M0에서 고정한다.
- `seq`는 epoch 안에서 0부터 단조 증가한다.
- `seat_id`는 이벤트 내부 숫자 키이며 `seq`와 관계없다.
- JSON의 64비트 식별자는 문자열로 직렬화한다.

| 설정 | 시작값 | 비고 |
|---|---:|---|
| `ticket_ttl` | 20분 | 갱신된 대기표 유효기간 |
| `poll_interval` | 3초 | 기본 polling 간격 |
| `poll_jitter` | ±20% | 요청 분산 |
| `ready_window` | 10초 | heartbeat 최소 유지 목표 |
| `slot_width` | 2초 | READY 슬롯 폭 |
| `page_bits` | 65,536 | 페이지 bitmap 최대 8 KiB |
| `scheduler_interval` | 250ms | admission 후보 선택 주기 |
| `grant_ttl` | 15초 | grant 생성부터 교환까지 |
| `stats_interval` | 1초 | 대기 추정치 갱신 |
| `booking_idle_ttl` | 60초 | 구매 세션 idle 만료 |
| `booking_max_lifetime` | 10분 | 구매 세션 절대 수명 |
| `hold_ttl` | 120초 | 좌석 hold 수명 |
| `hold_reaper_interval` | 1초 | hold 반환 주기 |
| `capacity` | 1,000 | 개발 시작값 |
| `admission_rate` | 100명/초 | 개발 시작값 |
| `admission_burst` | 25명 | token bucket 상한 |
| `max_grants_per_tick` | 25명 | tick당 claim 상한 |
| `max_seq_per_epoch` | 10,000,000 | 비정상 bitmap 확장 방지 |

사용자당 좌석 구매 한도 1석은 조정 가능한 운영 설정이 아니라 V1의 고정 도메인 불변식이다. 이 값은 config로 완화할 수 없다. V1에는 주문 취소나 재판매가 없으므로 한 번 확정된 구매 한도는 해당 이벤트 동안 되돌리지 않는다. 나머지 값은 운영 권장치나 처리량 보장이 아니며, 설정 검증은 `ready_window`, `slot_width`, TTL과 polling 지연의 관계까지 확인해야 한다.

## 7. Queue와 admission 설계

### 대기표

대기표에는 고정 버전의 직렬화 형식으로 다음을 서명한다.

```text
version, key_id, event_id, epoch, seq,
subject_id, ticket_id, issued_at_ms, expires_at_ms
```

대기표 버전 2는 인스턴스별 메모리의 Ed25519 개인키로 서명한다. `key_id`는 공개키 SHA-256 해시의 Base64 URL-safe 표현이고, 공개키는 별도 Redis에 서명 전에 등록한다. 검증기는 해당 Redis에서 `key_id`로 공개키를 찾는다. 알고리즘은 클라이언트가 선택하지 않는다. 사용자·이벤트·epoch·만료·`max_seq_per_epoch`를 검증한 뒤에만 Redis bitmap offset을 계산한다. ticket과 grant 원문은 URL query나 로그에 남기지 않는다. 이전 HMAC 버전 1 대기표는 호환되지 않는다.

발급·갱신은 `max_ticket_expiry = max(existing, issued_expiry)`를 Redis에 먼저 원자 기록한 뒤 토큰을 반환한다. 갱신은 `seq`, `ticket_id`, `subject_id`를 유지한다. 늦게 도착한 갱신 응답 때문에 클라이언트가 더 짧은 만료시각으로 되돌아가서는 안 된다.

Redis에는 `(event_id, subject_id)`별 대기표 uniqueness index를 두지 않는다. QUEUE 모드에서 요청이 유효한 기존 대기표를 제시한 경우에만 그 대기표를 재사용하고, 대기표 없이 새 발급을 요청하면 같은 사용자의 다른 대기표나 MySQL 구매 기록을 조회하지 않고 새 대기표를 만든다. 따라서 구매 전후를 가리지 않고 복수 대기표가 허용되며, 각 대기표의 `seq`, spent, grant, redeem 수명주기는 독립적이다. DIRECT 모드에서는 기존 설계대로 대기표 대신 booking permit을 반환한다.

### READY bitmap과 summary

```text
page   = seq / page_bits
offset = seq % page_bits
slot   = floor(redis_now_ms / slot_width_ms)
K      = ceil(ready_window / slot_width) + 1

raw_ready(page) = OR(최근 K개 slot bitmap)
eligible(page)  = raw_ready AND NOT spent AND NOT reserved
summary_union   = OR(최근 K개 summary bitmap)
```

기본값에서는 6개 슬롯을 읽어 heartbeat를 약 10~12초 동안 후보로 유지한다. summary는 후보 페이지의 인덱스일 뿐 인원수의 권위가 아니다. false positive는 허용하지만 실제 READY 페이지의 영구 누락은 허용하지 않는다. 큰 bitmap 스캔은 Lua 안에서 수행하지 않는다.

페이지별 `eligible` count와 prefix sum은 공유 통계로 주기 생성한다. 페이지 내부 위치는 다음 근사로 표시한다.

```text
estimated_ahead ≈ Prefix[p] + C[p] × offset / L[p]
```

마지막 미완성 페이지를 포함해 분모와 결과를 유효 범위로 제한한다. 통계에는 `stats_as_of`와 `estimate_is_stale`을 함께 제공하며, 갱신 실패를 0명으로 표현하지 않는다.

### 세션 수명주기

- 정상 상태와 공유 budget에 여유가 있으면 직접 booking permit을 발급한다.
- 여유가 없으면 active epoch를 `get-or-create`하고 신규 사용자를 같은 epoch에 등록한다.
- READY가 0이라는 이유로 epoch를 닫거나 늦게 온 사용자를 직접 입장으로 우회시키지 않는다.
- `now >= max_ticket_expiry`, 미처리 grant 없음, 갱신·등록과 종료가 원자 직렬화됐을 때만 `CLOSED`로 전이한다.
- 먼저 `CLOSED`를 확정하고 active pointer를 조건부 해제한 뒤 bitmap key를 제한된 batch로 정리한다.
- booking, hold, order의 수명은 epoch와 독립적이다.

### 후보 선택과 grant

```text
free   = max(0, capacity - active_booking - reserved)
budget = min(free, floor(rate_tokens), max_grants_per_tick)
```

summary의 작은 페이지와 페이지 안의 작은 `seq`부터 후보를 조사한다. 영구적인 monotonic head는 두지 않는다. 최종 claim은 epoch, READY, spent, reserved, capacity, rate budget, writer fencing을 Redis에서 다시 확인하는 짧은 원자 전이로 확정한다.

```text
READY + unspent
  └─ claim ──> RESERVED/grant
                  ├─ redeem ──> spent + ACTIVE_BOOKING
                  └─ expire ──> reservation 반환 + 기존 READY 제거
```

grant 만료 때 현재 유효 슬롯의 해당 READY bit를 제거해 오래된 heartbeat만으로 무한 재허가하지 않는다. 이후 새 heartbeat는 유효한 원래 ticket으로 READY를 복구할 수 있다. 같은 redeem은 항상 같은 booking ID를 반환한다.

booking permit에는 idle TTL과 절대 수명을 모두 둔다. 만료 worker가 아직 물리 기록을 지우지 않아도 논리적으로 만료된 permit은 되살리지 않는다. 좌석 SQL 동시 실행 수는 booking capacity와 별도의 고정 상한으로 제한한다.

### writer와 핵심 Redis key family

이벤트별 scheduler는 lease와 증가하는 fencing token을 가진다. 모든 claim이 현재 lease와 fencing을 검사하며, 이전 writer의 지연 요청은 실패한다. 새 writer는 이미 존재하는 grant와 booking을 이어받는다.

| key family | 역할 |
|---|---|
| `q:{event}:active_epoch` | 현재 epoch 포인터 |
| `q:{event}:e:<epoch>:meta` | 상태, next seq, max ticket expiry, config version |
| `...:ready:<slot>:<page>` / `...:summary:<slot>` | 최근 응답 의사와 후보 페이지 |
| `...:spent:<page>` / `...:reserved:<page>` | 소비·예약 상태 |
| `...:stats` | count, prefix, 생성 시각 |
| `q:{event}:grants` / `grant_expiry` | grant와 만료 인덱스 |
| `q:{event}:bookings` / `booking_expiry` | booking과 만료 인덱스 |
| `...:redeemed` | `seq → booking_id` 재시도 결과 |
| `q:{event}:control` | 모드, expected epoch, rate, capacity, pause, bucket, fencing |

Redis는 권한 상태가 eviction되지 않도록 `noeviction`을 사용한다. QUEUE 모드의 meta나 installation marker가 사라진 경우 빈 정상 상태를 자동 생성하지 않는다. 명시적 `init-state` 절차와 배포 설정의 expected installation ID로 최초 초기화와 상태 유실을 구분한다.

## 8. 좌석, hold와 주문 설계

### 식별과 저장 모델

좌석의 권위 있는 키는 `(event_id, seat_id)`다. `section_id`, 행·번호, `display_alias`, `allocation_order`는 명시적 데이터다. 숫자 ID의 거리나 allocation 순서는 물리적 연석을 의미하지 않는다.

| 테이블 | 주요 계약 |
|---|---|
| `seat_definitions` | PK `(event_id, seat_id)`, 이벤트·구역 내 alias UNIQUE, 정적 표시 정보 |
| `seat_inventory` | 동일 PK, state/hold/order/version, 자동배정용 `(event_id, section_id, state, allocation_order, seat_id)` index |
| `holds` | hold 소유자·booking·상태·만료·request hash, `(event_id, subject_id, idempotency_key)` UNIQUE |
| `hold_items` | PK `(hold_id, seat_id)`, `hold_id` UNIQUE로 hold당 1석 강제, hold와 좌석 definition 참조 |
| `event_purchase_guards` | PK `(event_id, subject_id)`, `order_id NULL UNIQUE`, 동일 사용자의 confirm을 직렬화하는 영속 guard |
| `orders` | PK `order_id`, `hold_id` UNIQUE, `(event_id, subject_id)` UNIQUE, 소유자와 모의 결제 결과 |
| `order_items` | PK `(event_id, seat_id)`, `order_id` UNIQUE로 order당 1석 강제, 구매 시점 alias snapshot |

시간 컬럼은 UTC `DATETIME(6)`를 사용한다. `seat_inventory`에 복제한 section과 allocation order는 판매 개시 후 고정하고 definition과의 일치를 migration/seed 테스트로 검증한다.

```text
seat: AVAILABLE ──> HELD ──> SOLD
                    └──────> AVAILABLE (cancel/expire)

hold: HELD ──> CONFIRMED | CANCELLED | EXPIRED
```

표현 가능한 상태 조건은 CHECK, UNIQUE, FK로 강제하고 나머지는 트랜잭션과 실제 MySQL 통합 테스트로 검증한다. `event_purchase_guards`와 `orders`의 이중 제약으로 사용자당 구매 제한을 application의 사전 조회에만 의존하지 않는다. hold 수명 동안 DB transaction이나 row lock을 열어두지 않는다.

### 공통 트랜잭션 규칙

- 인증, 이벤트, booking permit, 요청 크기를 DB 접근 전에 검증한다.
- 지정 좌석 목록의 중복 제거 결과와 자동배정 `quantity`는 정확히 1이어야 한다. 그 외 요청은 DB 상태를 바꾸기 전에 `SEAT_PURCHASE_LIMIT_EXCEEDED`로 거절한다.
- primary에서 `READ COMMITTED`를 사용하고 binary log 사용 시 ROW 형식을 고정한다.
- 네트워크 호출, 사용자 입력 대기, 실제 PG 호출을 transaction 안에 넣지 않는다.
- connection pool, request deadline, mutation 동시성 상한을 명시한다.
- 사용자 구매 제한이 관련된 hold/confirm에서는 `(event_id, subject_id)`의 purchase guard를 먼저 확보하고, 그다음 hold와 좌석을 잠근다. 모든 경로가 같은 잠금 순서를 사용한다.
- deadlock과 lock timeout은 전체 transaction 단위의 제한된 재시도 또는 안정적인 실패로 처리한다.
- hold 생성에는 Idempotency-Key가 필수다. 같은 key·같은 payload는 같은 결과, 다른 payload는 `IDEMPOTENCY_CONFLICT`다.
- COMMIT 응답 유실은 새 요청으로 덮지 않고 같은 key와 권위 상태 조회로 판정한다.

### 지정 좌석

정확히 하나인 seat ID를 같은 transaction에서 PK `FOR UPDATE NOWAIT`로 잠근다. purchase guard를 잠근 뒤 이미 order가 연결돼 있으면 `SEAT_PURCHASE_LIMIT_EXCEEDED`를 반환한다. 좌석이 존재하고 AVAILABLE일 때만 단일 hold와 inventory를 함께 기록한다. 잠금 충돌은 `SEAT_BUSY`, 이미 HELD/SOLD인 좌석은 `SEAT_UNAVAILABLE`다. 임의의 대체 좌석은 허용하지 않는다.

### 자동배정

이벤트와 section에서 AVAILABLE 좌석을 `(allocation_order, seat_id)` 순서로 `LIMIT 1 FOR UPDATE SKIP LOCKED`한다. purchase guard에 order가 없어야 같은 transaction에서 한 좌석의 hold를 만든다. 좌석을 얻지 못하면 `NO_ASSIGNABLE_SEATS_NOW`를 반환하며 영구 매진으로 확정하지 않는다.

### 만료, 취소와 확정

hold reaper는 만료된 HELD hold를 제한된 batch로 `FOR UPDATE SKIP LOCKED`하고, 잠금 후 DB 시각과 상태를 재검증한다. 해당 hold가 아직 소유하는 좌석만 AVAILABLE로 되돌린 뒤 hold를 EXPIRED로 전이한다. 취소도 같은 잠금 순서와 소유권 조건을 사용한다.

모의 구매 확정은 `(event_id, subject_id)` purchase guard를 생성 또는 잠근 뒤 진행한다. guard에 다른 order가 있으면 `SEAT_PURCHASE_LIMIT_EXCEEDED`로 종료한다. 비어 있으면 hold와 단일 소유 좌석의 유효기간·상태를 재검증한 뒤 order/order_item 생성, 좌석 SOLD, hold CONFIRMED, guard의 `order_id` 기록을 하나의 transaction으로 완료한다. `(event_id, subject_id)`와 `order_id` UNIQUE는 구현 오류나 동시 race에 대한 마지막 DB 방어선이다.

같은 hold의 중복 confirm은 기존 order를 반환한다. 서로 다른 ticket·booking·hold에서 같은 사용자의 confirm이 동시에 들어오면 purchase guard가 직렬화하며, 정확히 하나만 성공한다. 패배한 hold의 좌석은 SOLD로 바뀌지 않고 취소하거나 만료 worker가 안전하게 반환한다. 늦은 모의 결제 성공은 만료되거나 재할당된 좌석을 되찾지 않는다.

## 9. 외부 API 의미

경로는 구현 시 router 관례에 맞게 조정할 수 있지만 의미와 안정적인 오류 코드는 유지한다.

| API | 의미 |
|---|---|
| `POST /events/{event}/entry` | 제시한 유효 대기표는 갱신하고, 미제시 시 구매 여부·기존 개수와 무관하게 새 대기표 또는 direct booking permit 반환 |
| `POST /queue/heartbeat` | ticket 갱신, READY 기록, WAITING/GRANTED와 추정치 반환 |
| `POST /queue/redeem` | grant를 같은 booking permit으로 멱등 교환 |
| `POST /booking/heartbeat` | 절대 수명을 넘지 않는 idle TTL 갱신 |
| `POST /booking/leave` | permit을 멱등 반환; hold 취소와는 별도 행위 |
| `GET /events/{event}/seat-map` | 정적 좌석 표시와 시각·버전이 있는 availability snapshot 반환 |
| `POST /events/{event}/holds` | 지정 좌석 1개 또는 section의 자동배정 1개, Idempotency-Key 필수 |
| `GET /holds/{hold}` | 소유자에게 hold, 좌석, 만료, 연결 order 반환 |
| `POST /holds/{hold}/cancel` | 소유자의 hold를 멱등 취소 |
| `POST /holds/{hold}/confirm` | 개발 모드 전용 모의 확정, 기존 order 재사용 |

heartbeat 응답은 서버 시각, 다음 권장 polling 간격, ticket 만료, 상태, `estimated_ahead`, `stats_as_of`를 포함한다. WAITING은 정상 응답이다. 오류는 HTTP status와 함께 안정적인 `code`, `retryable`, 선택적 `retry_after_ms`를 제공한다.

최소 오류 코드는 다음과 같다.

```text
TICKET_EXPIRED, EPOCH_CLOSED, EVENT_CLOSED,
QUEUE_CAPACITY_REACHED, ADMISSION_PAUSED,
GRANT_EXPIRED, BOOKING_EXPIRED,
SEAT_BUSY, SEAT_UNAVAILABLE, NO_ASSIGNABLE_SEATS_NOW,
HOLD_EXPIRED, IDEMPOTENCY_CONFLICT,
SEAT_PURCHASE_LIMIT_EXCEEDED
```

`SEAT_PURCHASE_LIMIT_EXCEEDED`는 같은 이벤트와 사용자에 대해서는 재시도로 해결되지 않는 비재시도 오류다. 다른 이벤트의 구매에는 영향을 주지 않는다.

정확한 request/response schema와 status mapping은 구현 마일스톤에서 OpenAPI 또는 코드 기반 계약 한 곳을 권위로 정하고 중복 수기 정의하지 않는다.

## 10. 보안과 신뢰 경계

- `subject_id`는 사전 인증된 불투명 사용자 ID다. 개발 fixture를 제외하고 임의 HTTP header 자체를 신뢰하지 않는다.
- ticket, grant, booking, hold의 소유자를 현재 인증 주체와 대조한다.
- 사용자당 1석 제한은 `subject_id`의 신뢰성에 의존한다. 운영 인증이 동일 사용자를 여러 subject로 발급하면 이 제한을 우회할 수 있으므로, 운영 연동 전 subject 안정성과 계정 병합 정책을 별도로 검증한다.
- 각 인스턴스는 서명 가능 기간이 있는 Ed25519 키쌍을 생성한다. 개인키는 메모리에만 두고 공개키를 별도 Redis에 TTL과 함께 등록한다. 기간이 지나면 다음 발급에서 새 키를 등록하고 전환한다. 공개키는 구 대기표가 만료될 때까지 유지하며 키 Redis 장애에는 발급·검증을 닫는다. 공개키 등록 권한은 인프라 수준에서 제한한다.
- 외부 ID의 불투명성은 authorization을 대신하지 않는다.
- bitmap offset을 계산하기 전에 서명, 사용자, 이벤트, epoch, 만료와 seq 상한을 검증해 비정상 메모리 확장을 막는다.
- 개발용 confirm endpoint는 운영 결제 API로 공개하지 않는다.
- 운영 인증, abuse/bot 제어, 실제 PG 보상은 V1 외부 위험으로 유지한다.

## 11. 장애와 복구 정책

| 상황 | 정책 |
|---|---|
| MySQL 중단 | 신규 입장과 좌석 변경을 중단한다. Redis가 정상이면 ticket 갱신과 대기 화면은 유지한다. |
| Redis 일시 단절 | 신규 입장과 permit 기반 좌석 변경을 fail-closed한다. 클라이언트 ticket은 보존한다. |
| READY만 유실 | epoch/seq/spent가 온전하면 다음 heartbeat로 재구성하고 통계를 stale로 표시한다. |
| epoch/meta/spent/permit 유실 의심 | 자동 DIRECT나 같은 epoch 재생성을 금지하고 RECOVERING/PAUSED를 유지한다. |
| scheduler 중단 | 기존 grant/booking을 유지하고 새 writer가 fencing 후 승계한다. |
| Redis 만료 worker 중단 | capacity를 보수적으로 덜 사용하고 재시작 후 제한된 batch로 회수한다. |
| hold reaper 중단 | 중복 판매 없이 좌석이 일시 HELD로 남을 수 있으며 복구 후 소유권을 확인해 반환한다. |
| MySQL COMMIT 응답 유실 | idempotency key와 hold/order 조회로 결과를 판정한다. |
| 통계·snapshot 갱신 실패 | 마지막 값의 시각과 stale 상태를 반환하며 0 또는 AVAILABLE로 초기화하지 않는다. |

Redis 비동기 replica와 `WAIT`만으로 failover 무손실을 주장하지 않는다. 일반 서버 시작은 상태 초기화를 수행하지 않는다. graceful shutdown은 신규 admission을 멈추고 소유 loop를 bounded drain하지만, crash recovery는 정상 종료에 의존하지 않는다.

## 12. 관측성과 운영 신호

구현은 다음 질문에 답할 수 있는 최소 신호를 제공해야 한다.

- HTTP, Redis, SQL별 요청률, 성공·충돌·오류·timeout, p50/p95/p99는 얼마인가?
- grant 발급·교환·만료와 `free/reserved/active`가 일관되는가?
- Redis Lua 시간, command latency, bitmap scan page/byte, key 수, 실제 메모리는 얼마인가?
- MySQL transaction, pool wait, row lock wait, deadlock, examined rows는 얼마인가?
- seat snapshot과 queue stats는 언제 생성됐고 stale한가?
- worker가 마지막으로 성공한 시점, backlog, lease/fencing 상태는 무엇인가?
- 프로세스 CPU, RSS, GC, network와 load generator 포화 여부는 어떤가?

ticket, grant, subject, idempotency key 원문은 metric label이나 일반 로그에 넣지 않는다. 고카디널리티 상관관계는 필요할 때 redacted/hashed 식별자를 로그 또는 trace에 사용하고, metric은 이벤트·결과·오류 종류처럼 bounded label만 사용한다.

## 13. 검증 전략과 합격 기준

### 검증 층

| 층 | 담당하는 증거 |
|---|---|
| 단위 | Clock, token serialization/signature, bitmap 계산, 상태 전이, 추정치 clamp, idempotency 정책 |
| 실제 Redis 통합 | Lua 원자성, bit order, slot 전환, TTL, claim/redeem/expire, lease/fencing |
| 실제 MySQL 통합 | migration, purchase guard/UNIQUE 제약, NOWAIT/SKIP LOCKED, 다중 connection 잠금 경합, commit 불확실성 |
| API/E2E | 복수 ticket과 구매 후 ticket 발급, refresh 복구, polling/backoff, grant 교환, 단일 좌석 hold·confirm 흐름 |
| 실패 주입 | dependency 단절, worker kill/restart, 응답 유실, recovery/paused 동작 |
| 부하 | 분포·경합·no-show별 처리량, tail latency, 자원, 병목과 안전성 |

mock 단위 테스트, 컴파일, 단일 DB connection에서 직렬화된 goroutine만으로 실제 경계나 동시성 통과를 주장하지 않는다. 시간 경합은 arbitrary sleep 대신 Clock, barrier, channel과 bounded eventual assertion으로 재현한다. property test는 seed를 출력하고 실패를 재현 가능하게 한다.

### 테스트 ID와 마일스톤 연결

| 그룹 | ID | 반드시 증명할 범위 |
|---|---|---|
| Queue | Q-01~Q-15 | direct entry, 단일 epoch, bit/slot/OR, 복귀, token 거절, summary 경합, 추정치, 종료·갱신 경합, 복수 ticket |
| Admission | A-01~A-12 | 관측 순서, inactive skip, 재고려, claim/redeem 멱등, expire 경합, capacity, pause, fencing |
| Seat | S-01~S-19 | 좌석 경합, 멱등, 단일 좌석 자동배정, 잠금 충돌, 만료·확정, 사용자당 1석, alias/event 격리, deadlock |
| Failure | F-01~F-07 | Redis/MySQL 단절, 핵심 상태 유실, 응답·commit 유실, worker 재시작, 브라우저 복구 |

이번 범위 결정으로 다음 acceptance case를 추가하거나 변경한다.

| ID | 시나리오 | 합격 조건 |
|---|---|---|
| Q-14 | QUEUE 모드에서 같은 사용자가 같은 이벤트의 대기표를 연속·동시 발급 | 각 신규 발급은 서로 다른 `ticket_id`와 `seq`를 가지며 모두 독립적으로 유효하다. |
| Q-15 | QUEUE 모드에서 좌석 구매를 마친 사용자가 새 대기표 발급·갱신 | MySQL 주문 조회 없이 정상 처리되며 기존 구매 상태를 바꾸지 않는다. |
| S-07 (변경) | 지정 좌석 여러 개 또는 자동배정 `quantity > 1` 요청 | 기준 설계의 다중 좌석 부분 실패 시나리오를 대체한다. `SEAT_PURCHASE_LIMIT_EXCEEDED`이며 hold·inventory·guard·order에 변경이 없다. |
| S-17 | 이미 한 좌석을 구매한 사용자의 추가 hold/confirm | 같은 이벤트에서는 안정적인 limit 오류를 반환하고 추가 SOLD 좌석과 order가 없다. |
| S-18 | 같은 사용자의 서로 다른 ticket·booking·hold에서 동시 confirm | 정확히 하나의 order와 한 개의 SOLD 좌석만 생긴다. 나머지는 limit 오류를 반환하고 order/SOLD 변경 없이 기존 hold 상태를 유지해 취소 또는 만료로 반환된다. |
| S-19 | 같은 사용자가 서로 다른 이벤트에서 각각 구매 | 이벤트별 한도이므로 각 이벤트에서 한 좌석씩 성공하고 서로의 guard를 막지 않는다. |

세부 테스트 이름은 해당 ID를 포함한다. 테스트 보고서는 각 ID를 `PASS`, `FAIL`, `NOT RUN` 중 하나로 기록하고, 실행 명령·환경·미실행 사유를 남긴다. skip은 pass로 합산하지 않는다.

안전성 불변식 위반은 처리량과 관계없이 실패다. 기능 합격과 성능 측정은 별도 결과로 보고한다.

## 14. 성능 검증 계획

### 계산 모델

실제 polling 사용자 수를 `N`, 평균 polling 간격을 `T`, 발급 seq 범위를 `M`, 유효 READY 슬롯 수를 `K`라 하면:

```text
heartbeat HTTP RPS         ≈ N / T
READY + summary bit writes ≈ 2N / T
dense bitmap raw payload   ≈ (K + 2) × M / 8 bytes
```

기본 `K=6`, `M=10,000,000`이면 조밀한 bitmap 원시 payload만 약 10 MB다. key/HASH/ZSET, 페이지 정렬, cleanup margin, allocator, persistence buffer와 redeemed 기록은 별도 측정한다.

### 재현할 workload

1. 논리적 대기자 10만/100만/1천만의 데이터 크기와 실제 heartbeat RPS를 분리한다.
2. 조밀, 무작위 이탈, 앞부분 이탈, 낮은 seq 복귀, summary false-positive 분포를 비교한다.
3. grant no-show 0%/10%/50%와 polling 지연별 admission 활용률을 측정한다.
4. 지정 좌석 요청을 분산 경합과 단일 좌석 집중 경합으로 나눈다.
5. 자동배정 앞 후보가 0%/50%/90% 잠긴 경우와 재고 소진 직전을 측정한다.
6. 대기부터 모의 확정·취소·만료까지의 통합 흐름과 MySQL 중단·복구를 측정한다.

워밍업을 측정 구간에서 제외하고, workload seed·code revision·dependency version/image digest·hardware/topology·동시성·지속시간·오류를 보고한다. load generator가 목표 RPS를 보내지 못한 경우 서버 한계로 해석하지 않는다. 목표 hardware와 동시접속 규모가 정해지기 전에는 임의의 처리량 pass/fail 선을 두지 않는다.

## 15. 전달 마일스톤

각 마일스톤은 다음 마일스톤에 필요한 권위와 테스트 경계를 완성한다. 체크박스 수가 아니라 외부에서 관찰 가능한 결과와 실행 증거로 종료한다.

### M0 — 실행 기반과 계약 골격

산출물:

- 버전이 고정된 Go dependency와 Redis/MySQL container image digest
- 검증되는 설정 모델, Clock, ID와 token 직렬화 계약
- Redis key builder와 Lua 호출 경계
- MySQL migration, alias/재고 seed, 실제 migration test
- 명시적 Redis `init-state`와 installation marker 절차
- PowerShell에서 실행 가능한 로컬 환경 명령

종료 조건:

- clean environment에서 dependency가 readiness를 통과한다.
- migration/seed가 적용되고 definition과 inventory 복제 필드가 일치한다.
- token golden test, bit order 경계, slot 계산, 설정 검증이 통과한다.
- 실제 dependency가 없을 때 integration suite는 성공으로 위장하지 않고 준비 실패 또는 명시적 미실행으로 기록된다.

### M1 — 대기표와 READY queue

산출물:

- direct/queue 진입과 epoch 수명주기
- ticket 발급·갱신·검증
- READY/summary bitmap, 통계와 추정치
- HTTP heartbeat와 최소 대기 client state

종료 조건:

- Q-01~Q-15가 실제 Redis를 포함해 통과한다.
- refresh와 짧은 단절 후 원래 ticket/seq 복구가 관찰된다.
- 아직 좌석 기능이 없어도 queue 기능만 독립적으로 시연할 수 있다.

### M2 — admission과 booking permit

산출물:

- token bucket과 공유 capacity
- fenced scheduler, atomic claim/redeem/expire
- booking heartbeat/leave와 회수 worker
- capacity 상태 모델 property test

종료 조건:

- A-01~A-12가 실제 Redis와 복수 API process에서 통과한다.
- `reserved + active <= capacity`가 전이 기록 또는 model oracle로 모든 순간에 검증된다.
- 이 지점을 좌석 구현과 분리된 체크포인트로 보고한다.

### M3 — 좌석 hold와 모의 주문

산출물:

- seat map snapshot/cache와 stale 표시
- 지정 좌석 NOWAIT와 비연석 SKIP LOCKED 자동배정
- 단일 좌석 hold, purchase guard, 멱등 cancel/reaper/confirm/order
- 실제 MySQL 다중 connection 경합과 commit failure test

종료 조건:

- S-01~S-19(변경된 S-07 포함)가 실제 MySQL에서 통과한다.
- 같은 좌석의 동시 성공 hold와 order가 각각 최대 하나임을 DB 상태로 검증한다.
- 동일 사용자의 복수 ticket·booking·hold와 동시 confirm에서도 이벤트당 order와 SOLD 좌석이 각각 최대 하나다.
- 만료와 확정, 재시도와 응답 유실에서 좌석 상태 누수나 중복 주문이 없다.

### M4 — 전체 흐름과 복구

산출물:

- 최소 client 또는 headless flow
- 대기 → 입장 → 조회 → hold → confirm/cancel/expire E2E
- dependency/worker failure injection harness
- README의 실제 실행·복구·제한 명령

종료 조건:

- F-01~F-07이 통과하거나 미실행 prerequisite와 경계를 정확히 기록한다.
- Redis/MySQL 장애 시 신규 권한은 fail-closed되고 이미 권위에 commit된 상태는 멱등 조회·복구된다.
- 핵심 Redis 상태 유실 의심이 정상 DIRECT 상태로 위장되지 않는다.

### M5 — 부하 측정과 결과 정리

산출물:

- fixture generator와 correctness/stress profile이 분리된 load generator
- `docs/reports/TEST-REPORT.md`
- `docs/reports/BENCHMARK-REPORT.md`와 원시 결과 위치
- 발견한 병목, 검증된 한계, 미구현·미검증 목록

종료 조건:

- 대표 workload별 명령과 환경을 재현할 수 있다.
- 기능 결과와 성능 결과가 분리되고 p50/p95/p99, 오류·충돌, 자원 사용량이 함께 기록된다.
- 처리량 주장을 추정치나 mock 결과로 대신하지 않는다.

## 16. 목표 저장소 구성

세부 이름은 M0에서 확정하되 책임은 다음처럼 분리한다.

```text
cmd/
  ticketing/              # API와 선택한 background role의 실행 진입점
  loadtest/               # 제품과 분리된 workload driver
internal/
  config/                 # 설정 파싱과 관계 검증
  clock/                  # Redis/MySQL 경계 밖의 테스트 가능한 시각
  queue/                  # ticket, epoch, READY, estimate
  admission/              # scheduler, grant, booking capacity
  booking/                # API orchestration, hold/order use cases
  seatmap/                # definition과 availability projection
  store/redis/            # key, Lua adapter, installation state
  store/mysql/            # transaction과 query adapter
migrations/               # MySQL schema migration
redis/lua/                # review 가능한 원자 상태 전이
testdata/                 # synthetic seat와 workload fixture
docs/
  blueprint.md
  reports/
    TEST-REPORT.md
    BENCHMARK-REPORT.md
```

도메인 패키지는 vendor client나 HTTP 타입보다 의미 있는 capability와 안정적인 오류에 의존한다. 실행 역할을 한 binary의 mode로 둘지 별도 command로 나눌지는 M0에서 lifecycle·배포 단순성을 기준으로 결정하되, 이를 별도 마이크로서비스 경계로 확대하지 않는다.

## 17. 필수 산출물과 문서 소유권

| 산출물 | 소유하는 사실 |
|---|---|
| `README.md` | 현재 실제로 실행 가능한 최소 경로, prerequisite, 명령, 장애·복구 한계 |
| `docs/blueprint.md` | 현재 프로젝트 목적, 경계, 품질 목표, 마일스톤과 핵심 위험 |
| API contract | 정확한 request/response/error schema와 compatibility |
| migrations/seed | 권위 있는 MySQL schema와 synthetic inventory |
| Redis Lua scripts | 원자 전이와 key 입력 계약 |
| `TEST-REPORT.md` | 테스트 ID별 실제 실행 결과와 미실행 경계 |
| `BENCHMARK-REPORT.md` | workload, 환경, 원시 결과, 해석과 한계 |

README와 보고서는 구현이 생긴 뒤 실제 명령을 실행해 검증한다. 계획 중인 명령을 현재 기능처럼 문서화하지 않는다.

## 18. 구현 전 확정할 결정

| 결정 | 필요 시점 | 완료 조건 |
|---|---|---|
| Redis 실제 버전과 image digest | M0 | bitmap/Lua/TTL 통합 테스트 환경과 보고서에 고정 |
| MySQL 8.4 patch와 image digest | M0 | NOWAIT/SKIP LOCKED 및 migration 환경에 고정 |
| Go Redis/MySQL driver와 version | M0 | timeout, cancellation, error mapping 기준 확정 |
| HTTP router와 contract authority | M0~M1 | schema 원본 한 곳과 생성/검증 방법 결정 |
| 프로세스 role packaging과 shutdown | M0 | API, scheduler, reaper, refresher의 ownership과 readiness 정의 |
| 개발 인증 fixture 형식 | M1 | 운영 인증과 혼동되지 않는 synthetic subject 검증 경계 정의 |
| signing key 주입·회전 방식 | M1 | 인스턴스별 Ed25519 생성·별도 공개키 Redis·key ID 검증 경로 구현. 운영 키 Redis 인증·손실 복구는 별도 검증 |
| Redis persistence/failover profile | M4 이전 | 보존 가능한 상태와 미보장 범위를 README에 명시 |
| 목표 hardware·동시접속·성능 판정선 | M5 이전 | 측정 환경과 제품 목표에 근거한 threshold 또는 관찰 보고 방식 결정 |

## 19. 주요 위험과 중단 조건

- Redis 핵심 상태가 일부만 남으면 순번과 capacity 권한이 모순될 수 있다. 일관성 확인 전 신규 입장을 중단한다.
- grant/booking record와 보조 counter가 어긋나면 capacity가 중복 반환될 수 있다. 전이를 원자화하고 model oracle로 대조한다.
- slot 경계, bit order, 높은 offset 처리 오류는 활성자 누락 또는 메모리 확장을 만든다. 경계값과 실제 Redis raw byte를 검증한다.
- `SKIP LOCKED`의 부족 결과는 매진이 아니다. 이를 사용자 오류나 inventory 0으로 고정하지 않는다.
- 오래된 hold reaper가 새 소유 좌석을 반환할 수 있다. 모든 반환에 현재 state와 `hold_id` 소유권 조건을 둔다.
- stale snapshot은 선택 UX를 악화시킬 수 있지만 authority가 아니다. MySQL 거절을 명확하고 재시도 가능하게 표현한다.
- 대기표 수를 구매 제한의 대용으로 사용하면 복수 ticket 정책과 충돌한다. 구매 제한은 오직 신뢰 가능한 `subject_id`와 MySQL purchase guard/UNIQUE 제약에서 강제한다.
- 같은 사용자의 복수 hold는 첫 구매 확정 전까지 존재할 수 있다. 한 hold가 확정된 뒤 나머지는 추가 구매에 실패하고 취소 또는 만료로 반환되므로, hold 남용 제어가 필요해지면 구매 불변식과 별개의 정책으로 설계한다.
- load generator 포화, mock, 단일 connection 직렬화는 서버 성능·동시성 증거가 아니다.
- 실제 PG, 운영 인증, Redis failover 무손실이 필요해지면 V1 완료 범위를 확장하지 말고 별도 설계와 검증 결정을 먼저 만든다.

## 20. 완료 판정

V1 완료는 다음이 모두 충족될 때만 선언한다.

1. M0~M5의 사용자·운영자 관찰 결과가 구현돼 있다.
2. Q/A/S/F 테스트 ID별 실제 결과와 명령이 기록돼 있다.
3. Redis/MySQL 실제 integration, 다중 connection/process concurrency, 실패 주입을 실행했다.
4. 안전성 불변식 위반이 없고, 실패·미실행·skip을 숨기지 않았다.
5. benchmark 환경, workload, tail latency, 오류, 자원, 병목과 generator 한계를 기록했다.
6. README의 시작·테스트·복구 명령이 현재 저장소에서 실행된다.
7. 운영 전 별도 검증 범위와 현재의 보장 한계가 명시돼 있다.

설계 변경이 필요하면 변경된 계약, 이유, 영향받는 불변식·테스트 ID, 검증 결과를 함께 기록한다. 장기적인 구조·호환성·보안·운영 비용을 바꾸는 결정은 별도 ADR로 남기고 이 blueprint에서 현재 결론을 링크한다.

## 참고 자료

- [Redis SETBIT](https://redis.io/docs/latest/commands/setbit/)
- [Redis Lua scripting](https://redis.io/docs/latest/develop/programmability/eval-intro/)
- [Redis transactions](https://redis.io/docs/latest/develop/using-commands/transactions/)
- [Redis bitmaps](https://redis.io/docs/latest/develop/data-types/strings/bitmaps/)
- [MySQL 8.4 clustered and secondary indexes](https://dev.mysql.com/doc/refman/8.4/en/innodb-index-types.html)
- [MySQL 8.4 transaction isolation levels](https://dev.mysql.com/doc/refman/8.4/en/innodb-transaction-isolation-levels.html)
- [MySQL 8.4 locking reads](https://dev.mysql.com/doc/refman/8.4/en/innodb-locking-reads.html)
- [MySQL 8.4 locks set by statements](https://dev.mysql.com/doc/refman/8.4/en/innodb-locks-set.html)
- [Redis replication](https://redis.io/docs/latest/operate/oss_and_stack/management/replication/)
