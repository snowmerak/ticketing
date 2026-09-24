# HTTP API 계약

이 문서는 개발용 V1의 사용 계약을 설명합니다. 최종 route, status와 JSON 필드는 [server.go](../internal/httpapi/server.go)의 코드가 권위 있는 원본입니다.

## 공통 규칙

- 개발 인증: 모든 사용자 API에 `X-Subject-ID: <opaque-id>`가 필요합니다. 허용 형식은 `[A-Za-z0-9._:@-]{1,128}`입니다.
- JSON request에는 `Content-Type: application/json`을 사용합니다.
- 64비트 이벤트·좌석·queue sequence 식별자는 JSON 문자열로 전달합니다.
- hold 생성에는 길이 1~128의 `Idempotency-Key` header가 필수입니다.
- 오류는 `{"code":"...","message":"...","retryable":false,"retry_after_ms":0}` 형태입니다. `retry_after_ms`는 값이 있을 때만 나타납니다.
- ticket, grant와 booking은 발급받은 subject와 event에 묶입니다.

## 상태 확인

| Method/path | 결과 |
|---|---|
| `GET /livez` | 프로세스 생존 여부 |
| `GET /readyz` | 대기열 Redis와 installation marker, 티켓 키 Redis의 현재 공개키, MySQL 준비 여부 |
| `GET /metrics` | route/status class별 현재 프로세스 요청 누계 |
| `GET /` | 개발용 브라우저 클라이언트 |

## 입장과 대기열

### `POST /events/{event}/entry`

새 요청은 `{}`입니다. 여유가 있으면 `DIRECT`와 booking permit을, 아니면 독립된 새 대기표를 반환합니다.

```json
{"kind":"DIRECT","server_time_ms":1790190676776,"booking":{"booking_id":"...","idle_expires_at_ms":1790190736776,"absolute_expires_at_ms":1790191276776}}
```

```json
{"kind":"QUEUE","server_time_ms":1790190676776,"ticket":"...","ticket_expires_at_ms":1790191876776,"seq":"42","epoch":"..."}
```

기존 ticket을 갱신하려면 `{"ticket":"..."}`을 보냅니다. 새 ticket 없이 호출할 때마다 같은 사용자에게도 새 대기표를 만들 수 있습니다.

대기표 버전 2는 서버가 생성한 Ed25519 키로 서명하며 `kid`로 별도 Redis의 공개키를 찾습니다. 키 Redis가 불가하거나 현재 서명키를 조회할 수 없으면 `TICKET_KEY_UNAVAILABLE`(503, 재시도 가능)을 반환합니다. 알 수 없는 `kid`와 이전 HMAC 버전 1 대기표는 `TICKET_INVALID`입니다.

### `POST /queue/heartbeat`

요청:

```json
{"event_id":"100","ticket":"..."}
```

응답 status는 `WAITING` 또는 `GRANTED`입니다. 항상 갱신된 `ticket`, `ticket_expires_at_ms`, `server_time_ms`, `next_poll_ms`, `seq`, `epoch`를 포함합니다. 통계가 있으면 `estimated_ahead`, `stats_as_of_ms`, `estimate_is_stale`가 나오고, grant가 있으면 `grant_id`, `grant_expires_at_ms`가 나옵니다.

### `POST /queue/redeem`

```json
{"event_id":"100","ticket":"...","grant_id":"..."}
```

성공하면 booking permit을 반환합니다. 같은 grant의 재시도는 permit이 살아 있는 동안 같은 `booking_id`를 반환합니다.

### `POST /booking/heartbeat`, `POST /booking/leave`

두 endpoint 모두 다음 body를 사용합니다.

```json
{"event_id":"100","booking_id":"..."}
```

heartbeat는 idle 만료를 절대 수명 이내에서 연장합니다. leave는 멱등으로 capacity를 반환합니다.

## 좌석과 주문

### `GET /events/{event}/seat-map`

`X-Booking-ID` header가 필요합니다. `event_id`, snapshot `version`, `as_of`, `is_stale`, `seats`를 반환합니다. 화면의 AVAILABLE은 참고용이며 최종 소유권은 hold 트랜잭션이 판정합니다.

### `POST /events/{event}/holds`

지정 좌석 한 석:

```json
{"booking_id":"...","mode":"specified","seat_ids":["1"],"quantity":1}
```

구역 자동배정 한 석:

```json
{"booking_id":"...","mode":"auto","section_id":"10","quantity":1}
```

두 석 이상 요청은 상태를 변경하기 전에 `SEAT_PURCHASE_LIMIT_EXCEEDED`로 거절합니다. 같은 idempotency key와 같은 booking/payload는 같은 hold를 반환하고, booking을 포함해 다른 요청에 key를 재사용하면 `IDEMPOTENCY_CONFLICT`입니다.

### `GET /holds/{hold}`

현재 인증 subject가 소유한 hold를 반환합니다.

### `POST /holds/{hold}/cancel`

body는 `{}`이며 취소된 hold를 반환합니다. 확정·취소·만료된 hold의 반복 취소는 현재 상태를 반환합니다.

### `POST /holds/{hold}/confirm`

개발용 모의 결제 endpoint입니다.

```json
{"booking_id":"...","payment_result_id":"fixture-payment-id"}
```

성공하면 단일 seat의 order를 반환합니다. 같은 hold의 재시도는 같은 order를 반환합니다. 같은 subject가 해당 event에서 이미 구매했다면 `SEAT_PURCHASE_LIMIT_EXCEEDED`이며 추가 order/SOLD 좌석을 만들지 않습니다.

## 안정적인 오류 코드

| Code | 의미 |
|---|---|
| `UNAUTHENTICATED`, `FORBIDDEN` | 인증 누락 또는 소유자 불일치 |
| `INVALID_REQUEST` | 형식·필드 오류 |
| `TICKET_INVALID`, `TICKET_EXPIRED`, `TICKET_SPENT`, `EPOCH_CLOSED` | 대기표 수명주기 오류 |
| `QUEUE_RECOVERING`, `QUEUE_CAPACITY_REACHED` | queue 권위 복구 또는 seq 상한 |
| `GRANT_EXPIRED`, `BOOKING_EXPIRED` | 입장 권한 만료 |
| `SEAT_BUSY`, `SEAT_UNAVAILABLE`, `NO_ASSIGNABLE_SEATS_NOW` | 좌석 잠금·재고 경합 |
| `HOLD_NOT_FOUND`, `HOLD_EXPIRED` | hold 조회·만료 오류 |
| `IDEMPOTENCY_CONFLICT` | key를 다른 요청에 재사용 |
| `SEAT_PURCHASE_LIMIT_EXCEEDED` | 다좌석 요청 또는 이벤트별 추가 구매 |
| `REDIS_UNAVAILABLE`, `MYSQL_UNAVAILABLE`, `INTERNAL_ERROR` | 의존성 또는 내부 오류 |

`retryable` 값을 우선해 재시도 여부를 결정하고, non-retryable 오류에서 새 권한을 임의로 만들지 않습니다.
