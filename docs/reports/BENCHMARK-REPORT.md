# Benchmark Report — 2026-09-24

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

다음 유효한 성능 단계는 서버와 generator 분리, 5분 이상 steady state, OS/Go/Redis/MySQL resource telemetry 동시 수집, 고정 fixture reset, queue heartbeat/no-show profile 추가입니다.
