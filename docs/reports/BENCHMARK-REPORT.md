# Benchmark Report — 2026-09-24

> 아래 수치는 Ed25519/별도 티켓 키 Redis 도입 전의 HMAC 버전 1 구현에서 측정한 역사적 결과다. 현재 대기표 발급·heartbeat·redeem 경로에는 추가 Redis 조회가 있으므로 이 수치를 현재 키 검증 경로의 처리량·지연으로 재사용하면 안 된다.

## 결론

이 보고서는 한 대의 개발 노트북에서 수행한 짧은 관찰 결과입니다. 목표 하드웨어, 동시접속 규모와 SLO가 정해지지 않았으므로 pass/fail 임계값이나 운영 처리량 주장을 하지 않습니다.

entry 흐름은 높은 로컬 admission budget에서 오류 없이 약 2,228 operations/s를 관찰했습니다. 좌석 프로필은 MySQL 잠금 경합을 의도적으로 발생시켰고, 안전성 오류나 중복 판매 대신 명시적인 409 응답으로 실패했습니다.

## 환경과 방법

- revision: `f248931` 기반 working tree(본 구현 변경 미커밋)
- Windows 11 Home 10.0.26200, AMD Ryzen 7 8845HS, 16 logical CPU, 27.8 GiB RAM
- Go 1.27.1 windows/amd64
- API와 load generator는 같은 host, Redis/MySQL은 Docker Desktop Linux containers
- Redis 7.4.5 / MySQL 8.4.6(UTC, READ-COMMITTED)
- seed `20260924`, concurrency 8, warmup 1초 제외, 측정 3초
- API request log level `warn`
- entry 측정에는 `admission_rate=100000`, `admission_burst=100000`을 사용해 기본 100/s 정책에 의한 queue 전환을 분리
- 각 operation은 direct permit을 반환하며, hold 프로필은 hold도 취소합니다.
- latency는 한 operation의 entry부터 필요한 cleanup 예약 전까지의 wall time입니다. `http_requests`에는 측정 구간 operation이 수행한 leave/cancel도 포함합니다.

## HTTP workload 결과

| Profile | Ops | HTTP req | 성공 / 충돌·오류 | ops/s | p50 | p95 | p99 | max |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| entry | 6,684 | 13,368 | 6,684 / 0 | 2,228.0 | 3.678 ms | 4.469 ms | 5.366 ms | 12.073 ms |
| hold-auto | 606 | 2,035 | 217 / 389 | 202.0 | 27.234 ms | 75.688 ms | 79.915 ms | 83.760 ms |
| hold-hot | 945 | 2,906 | 71 / 874 | 315.0 | 23.231 ms | 49.756 ms | 56.164 ms | 65.067 ms |

오류 분포:

- `hold-auto`: `NO_ASSIGNABLE_SEATS_NOW` 389. section 10의 4좌석보다 동시 요청이 많을 때 `SKIP LOCKED`가 기다리지 않고 보수적으로 반환한 결과입니다.
- `hold-hot`: `SEAT_BUSY` 833, `SEAT_UNAVAILABLE` 41. 한 좌석 집중 경합에서 `NOWAIT`와 이미 HELD 상태가 구분돼 반환됐습니다.
- 이 409들은 해당 profile이 만들도록 설계한 경합 결과이며 성공으로 합산하지 않았습니다.

기본 admission 설정으로 수행한 첫 entry 시도는 warmup 직후 token bucket을 소진해 거의 모두 QUEUE로 전환됐습니다. 이는 서버 처리 한계 측정이 아니라 제품 정책 작동 결과이므로 위 성능 표에서 제외했습니다.

## 순수 bitmap microbenchmark

명령:

```powershell
go test ./internal/queue -run '^$' -bench . -benchmem -count=3
```

Windows/amd64, 같은 CPU에서 관찰한 원시 결과:

```text
BenchmarkEligibleDensePage-16       129862   9675 ns/op  2540.20 MB/s  8192 B/op  1 allocs/op
BenchmarkEligibleDensePage-16       124741   9471 ns/op  2594.83 MB/s  8192 B/op  1 allocs/op
BenchmarkEligibleDensePage-16       122907  13322 ns/op  1844.82 MB/s  8192 B/op  1 allocs/op
BenchmarkSetOffsetsDensePage-16   32753683     40.16 ns/op                 0 B/op  0 allocs/op
BenchmarkSetOffsetsDensePage-16   27534366     40.73 ns/op                 0 B/op  0 allocs/op
BenchmarkSetOffsetsDensePage-16   30722280     40.91 ns/op                 0 B/op  0 allocs/op
```

`EligibleDensePage`는 8 KiB page 한 장을 생성하므로 8,192 B 할당이 예상됩니다. 이 수치는 Redis RTT, Lua 실행, 네트워크 또는 scheduler 전체 비용이 아닙니다.

## 해석 한계와 다음 측정

- 3초 측정은 장시간 GC, connection pool, AOF fsync, thermal throttling을 대표하지 않습니다.
- 서버와 generator가 같은 host라 CPU/네트워크 자원 경쟁이 있습니다. generator 포화 여부를 별도 측정하지 않았습니다.
- CPU/RSS/GC, Redis command latency·실메모리, MySQL lock wait와 examined rows의 동시 수집은 하지 않았습니다.
- 10만/100만/1천만 logical seq bitmap 크기, polling RPS, no-show 0/10/50%, summary 분포 비교는 `NOT RUN`입니다.
- dependency 중단·복구 중 부하는 `NOT RUN`입니다.

위 Go load tester 기준 다음 유효한 성능 단계는 서버와 generator 분리, 5분 이상 steady state, OS/Go/Redis/MySQL resource telemetry 동시 수집, 고정 fixture reset, no-show profile 추가입니다.

## Docker k6 분리 측정 — 2026-09-24

위 Go load tester 결과와 **서로 다른 workload·런타임**이므로 수치를 직접 비교하지 않습니다. 이 절은 기준 revision `c0e25d6`와 이 보고서에 포함된 [k6 스크립트](../../bench/k6.js), [실행기](../../bench/run.ps1)로 얻은 로컬 관찰입니다.

### 조건과 재현

- Docker Desktop Linux/amd64, 16 vCPU·13.5 GiB Docker 메모리 한도. API, k6 v2.3.0, Redis 7.4.5, MySQL 8.4.6이 같은 노트북/Compose network를 공유했습니다.
- synthetic event 1개, 구역 10의 좌석 256개, capacity 64, admission rate/burst 각 100,000. 이 설정은 정책상의 100/s 입장 한도를 제거하고 정상 경로 비용을 보기 위한 것입니다.
- 각 프로필은 4 VU, 별도 warm-up 5회 후 측정 100회, `shared-iterations` 30초 상한. 생성·확정은 겹치지 않는 좌석/subject를 사용했습니다. `hold-cancel`은 VU별 분리 좌석을 잡고 즉시 취소했습니다.
- `entry`, `seat-map`, `hold-cancel`, `confirm`, `full`은 direct permit으로 시작합니다. `full`은 entry → seat-map → 지정 좌석 hold → 모의 결제 confirm → leave입니다. `queue`는 setup에서 64개 permit으로 정원을 채우고, 측정 반복마다 새 ticket 발급 → WAITING heartbeat을 호출했습니다.
- 명령: `docker compose up -d` 후 `go run ./cmd/ticketing migrate`를 1회 실행하고, `powershell -File .\bench\run.ps1 -VUs 4 -Iterations 100 -WarmupIterations 5`. Docker k6 image는 digest로 고정되어 있습니다. JSON 요약은 `benchmark-results/<profile>.json`에 보존됩니다(버전 관리 제외). 실행기는 고유 event만 만들고 종료 시 해당 event의 MySQL/Redis fixture를 정리합니다.

| 프로필 | 성공/측정 | 반복 전체 p50 / p95 | 핵심 HTTP p95 | 관찰 반복/s* |
|---|---:|---:|---:|---:|
| Direct entry + leave | 100/100 | 2 / 3.05 ms | entry 2.02 ms | 1,501 |
| Seat map (+ entry/leave) | 100/100 | 5 / 6.05 ms | map 2.19 ms | 783 |
| Hold + cancel (+ entry/leave) | 100/100 | 48 / 61 ms | hold 32.47 ms; cancel 28.41 ms | 80 |
| Hold + confirm (+ entry/leave) | 100/100 | 53 / 63 ms | hold 31.67 ms; confirm 33.73 ms | 74 |
| Full direct purchase (+ leave) | 100/100 | 54 / 65.05 ms | map 1.82 ms; hold 30.19 ms; confirm 34.39 ms | 72 |
| Queue ticket + WAITING heartbeat | 100/100 | 2 / 3 ms | ticket 1.60 ms; heartbeat 1.54 ms | 504† |

모든 측정의 HTTP error rate는 0%였습니다. `반복 전체`는 k6 클라이언트의 wall time이며 direct 프로필에서는 후처리 `leave`도 포함합니다. 같은 반복 내 단계별 p95를 더해 전체 p95를 계산하면 안 됩니다.

같은 100회 측정을 1 VU와 8 VU에서도 반복했습니다(`-ResultSuffix vu1`/`vu8`; 8 VU의 warm-up은 executor 제약에 따라 8회). 모든 행의 HTTP 오류는 0%였고, 반복 전체 p95는 다음과 같았습니다. 같은 노트북의 짧은 burst 간 비교입니다.

| 프로필 | 1 VU p95 | 4 VU p95 | 8 VU p95 |
|---|---:|---:|---:|
| Direct entry + leave | 3 ms | 3.05 ms | 8 ms |
| Seat map | 4 ms | 6.05 ms | 11.05 ms |
| Hold + cancel | 34.2 ms | 61 ms | 64 ms |
| Hold + confirm | 41 ms | 63 ms | 73.05 ms |
| Full direct purchase | 42.05 ms | 65.05 ms | 74.05 ms |
| Queue ticket + WAITING heartbeat | 4 ms | 3 ms | 6 ms |

\* 반복/s는 **100회짜리 짧은 닫힌 루프에서 관찰한 속도일 뿐 서버 최대 처리량이나 지속 가능 RPS가 아닙니다.** 특히 4 VU가 완료 즉시 다음 반복을 시작하므로 구매 프로필은 VU 수와 지연이 자체적으로 속도를 제한합니다. † queue 수치는 setup의 64개 입장과 teardown의 64개 leave가 실행 시간에 들어가 있어 다른 행의 반복/s와 비교할 수 없습니다. queue의 개별 단계 지연과 반복 wall time만 해석합니다.

### Happy path 추정 범위

이 노트북의 **direct 입장·경합 없음·캐시 예열·모의 결제** 조건에서는 4 VU에서 한 좌석 구매 API 연쇄가 약 54 ms 중앙값, 65 ms p95였습니다. 같은 짧은 실험의 1–8 VU 범위에서는 p95가 약 42–74 ms였습니다. 이는 동일 조건의 단기 관찰을 요약한 값이지 운영 환경 SLO나 74 ms 보장이 아닙니다. 실결제, 실제 인증, 브라우저 렌더링, 공용망 RTT, 대규모 좌석 맵, 치열한 동시 좌석 선점은 포함되지 않았습니다.

대기열 경로에서는 ticket 발급과 WAITING heartbeat 자체가 각각 약 1.5–1.6 ms p95였지만, **대기 시간·grant·redeem은 이 k6 측정에 포함되지 않았습니다.** 전체 대기 구매 시간은 `대기열 체류 + grant 확인(기본 poll interval 3초 영향) + redeem + 구매`로 생각할 수 있으나, 앞선 인원·이탈률·입장 budget·동시 점유 시간이 없으면 수치 예측은 성립하지 않습니다. grant/redeem의 동작 자체는 별도 [E2E 테스트](../../e2e/journey_test.go)로 검증했습니다.

이 규모의 Docker k6 실행은 단일 노트북에서 완료됐습니다. 다만 CPU/RSS를 동시 수집하지 않았으므로 generator의 자원 부담을 수치화할 수 없고, generator와 DB가 자원을 공유하므로 더 높은 VU/지속 시간에서 얻은 포화점을 서비스 단독 한계로 해석할 수 없습니다. 운영 capacity를 추정하려면 별도 generator, 실제 이벤트 규모, 1/4/8/16+ VU 및 고정 arrival rate, 수분 단위 안정 구간, k6 dropped iterations·서버/Redis/MySQL CPU·RSS·지연·lock wait를 함께 측정해야 합니다.
