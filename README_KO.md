# Ticketing V1

이 문서는 [영어 README](./README.md)의 한국어 번역입니다. 영어 문서가 기준 문서입니다.

Redis 기반 대기열·입장 제어와 MySQL 기반 단일 좌석 선점/모의 구매를 구현한 Go 서비스입니다. 각 인스턴스가 메모리의 Ed25519 키로 대기표를 서명하고 별도 Redis에 공개 검증키를 저장합니다.

핵심 제품 규칙은 두 가지입니다.

- 한 사용자는 같은 이벤트에서 좌석을 최대 1석만 구매할 수 있습니다. MySQL purchase guard와 UNIQUE 제약이 동시 요청에서도 이 제한을 강제합니다.
- 같은 사용자는 대기표를 여러 개 받을 수 있고, 좌석 구매 후에도 새 대기표를 받을 수 있습니다. 각 대기표의 `ticket_id`와 `seq`는 독립적입니다.

현재 구현은 개발·검증용 V1입니다. 운영 인증, 실제 결제, Redis 자동 failover 무손실 보장은 포함하지 않습니다.

## 로컬 실행

필요한 도구는 Go 1.27.1, Docker Engine과 Docker Compose입니다. 아래 명령은 PowerShell과 저장소 루트를 기준으로 실제 검증했습니다.

```powershell
docker compose up -d --wait
go run ./cmd/ticketing migrate
go run ./cmd/ticketing init-state
go run ./cmd/ticketing serve
```

그 다음 [http://127.0.0.1:8080](http://127.0.0.1:8080)을 열면 최소 개발 클라이언트를 사용할 수 있습니다. readiness는 다음처럼 확인합니다.

```powershell
Invoke-RestMethod http://127.0.0.1:8080/readyz
```

`migrate`와 `init-state`는 명시적 절차입니다. 일반 `serve`는 Redis 권한 상태를 자동 생성하거나 복구하지 않으며 installation marker가 없으면 시작을 거부합니다.

기본 설정은 [config.go](./internal/config/config.go)에 있고, 개발용 예시는 [.env.example](./.env.example)에 있습니다. 전체 설정과 적용 시점은 [설정 참조 문서](./docs/configuration.md)를 보세요. 이 애플리케이션은 `.env`를 자동으로 읽지 않으므로 필요한 값은 프로세스 환경 변수로 주입해야 합니다. `serve`에는 대기열 Redis와 별도 티켓 키 Redis가 모두 필요하며, `migrate`·`init-state`는 서명키를 등록하지 않습니다.

요청 로그를 줄이려면 다음처럼 실행할 수 있습니다.

```powershell
$env:TICKETING_LOG_LEVEL = 'warn'
go run ./cmd/ticketing serve
```

## 테스트

의존성 없는 단위 테스트와 정적 검사는 다음 명령으로 실행합니다.

```powershell
go test ./...
go vet ./...
```

실제 Redis 두 개와 MySQL의 통합 테스트는 먼저 Compose 의존성을 띄운 뒤 실행합니다. 의존성이 없으면 테스트는 skip으로 성공하지 않고 실패합니다.

```powershell
docker compose up -d --wait
go run ./cmd/ticketing migrate
go test -count=1 -tags=integration ./...
```

실제 바이너리와 HTTP 포트, admission worker를 포함한 전체 API E2E는 별도 명령으로 실행합니다. 테스트가 임의의 전용 이벤트를 만들고 종료 시 그 이벤트의 MySQL/대기열 Redis 데이터만 정리합니다. 별도 키 Redis의 공개키는 TTL이 끝날 때까지 남습니다. 기존 이벤트 100/200의 좌석·주문은 사용하지 않습니다.

```powershell
go test -tags=e2e -count=1 -v ./e2e
```

이 테스트는 DIRECT 입장으로 capacity 점유 → 동일 사용자의 복수 QUEUE 대기표 → 서버 프로세스 재시작과 이전 인스턴스 공개키 조회 → heartbeat/grant/redeem → 좌석 조회·선점·취소·자동배정·확정 → 구매 후 새 대기표와 새 booking permit 발급 → 다른 permit으로도 추가 구매 거절을 검증합니다. 브라우저 JavaScript 실행과 실제 결제는 포함하지 않습니다.

Windows 호스트에 C 컴파일러가 없을 때도 Linux 컨테이너에서 race detector를 실행할 수 있습니다.

```powershell
docker run --rm -v "${PWD}:/src" -w /src `
  -e REDIS_ADDR=host.docker.internal:16379 `
  -e TICKET_KEY_REDIS_ADDR=host.docker.internal:16380 `
  -e "MYSQL_DSN=ticketing:ticketing@tcp(host.docker.internal:13306)/ticketing?parseTime=true&loc=UTC&charset=utf8mb4&multiStatements=true" `
  golang:1.27.1-bookworm go test -race -count=1 -tags=integration ./...
```

검증 결과와 미실행 범위는 [TEST-REPORT.md](./docs/reports/TEST-REPORT.md)에 기록돼 있습니다.

## 부하 관찰

부하 도구는 제품 프로세스와 분리된 [cmd/loadtest](./cmd/loadtest/main.go)입니다. 예시는 [load-small.json](./testdata/load-small.json)에 있습니다.

```powershell
go run ./cmd/loadtest -profile entry -warmup 2s -duration 10s -concurrency 16
go run ./cmd/loadtest -profile hold-auto -warmup 2s -duration 10s -concurrency 8
go run ./cmd/loadtest -profile hold-hot -warmup 2s -duration 10s -concurrency 8 -seat 1
```

`entry`는 정상 종료 때 booking permit을 반환하고, hold 프로필은 hold와 permit을 반환합니다. `hold-auto`의 `NO_ASSIGNABLE_SEATS_NOW`와 `hold-hot`의 `SEAT_BUSY`/`SEAT_UNAVAILABLE`는 의도적인 경합 관측값입니다. 기본 admission rate를 넘으면 서버는 정상적으로 QUEUE 모드로 전환하므로, 직행 경로 자체를 관찰할 때는 깨끗한 로컬 상태에서 rate/burst를 목적에 맞게 설정해야 합니다.

측정 결과와 해석 한계는 [BENCHMARK-REPORT.md](./docs/reports/BENCHMARK-REPORT.md)에 있습니다.

Docker k6로 단일 노트북에서 정상 경로를 영역별로 짧게 측정하려면 Compose와 migration 준비 후 다음을 실행합니다. 실행기는 별도 synthetic event와 Docker API/k6 컨테이너를 사용하고 종료 시 해당 event만 정리합니다. 원시 요약은 버전 관리하지 않는 `benchmark-results/`에 남습니다.

```powershell
docker compose up -d --wait
go run ./cmd/ticketing migrate
powershell -File .\bench\run.ps1 -VUs 4 -Iterations 100 -WarmupIterations 5
```

이 측정은 [k6 workload](./bench/k6.js)의 입장·대기 ticket/heartbeat·좌석 조회·hold/cancel·확정·전체 구매를 분리해 보지만, 단기 반복 속도를 운영 capacity로 해석하지 않습니다. 최신 관찰치는 위 보고서의 “Docker k6 분리 측정” 절에 있습니다.

## 상태와 복구

- 두 Redis 모두 `noeviction`과 AOF를 사용합니다. 대기열 epoch/meta/spent/permit 손실이 의심되면 자동 DIRECT로 우회하지 않고 `QUEUE_RECOVERING`으로 닫힙니다. 키 Redis 장애는 대기표 발급·검증과 readiness를 중단시키고, 이전 공개키 손실은 유효 대기표를 무효화할 수 있습니다.
- MySQL이 불가하면 Queue API는 직접 booking permit을 만들지 않고 대기표 경로로 전환합니다. 좌석 변경은 실패합니다.
- `init-state`는 최초 설치 절차이며 부분 손실을 정상 상태로 덮어쓰는 복구 명령이 아닙니다.
- 로컬 데이터를 완전히 버리고 다시 시작할 때만 `docker compose down -v`를 사용할 수 있습니다. 이 명령은 두 Redis와 MySQL의 로컬 볼륨을 영구 삭제하므로 그 다음 `up`, `migrate`, `init-state`를 다시 실행해야 합니다.

## 계약과 권위

- HTTP 사용 계약: [API.md](./docs/API.md)
- HTTP route와 최종 status mapping: [server.go](./internal/httpapi/server.go)
- 좌석·주문 권위: [migrations](./migrations)와 [booking store](./internal/booking/store.go)
- Queue 원자 전이: [Redis Lua](./redis/lua)
- 현재 구현의 구성·데이터 권위·장애 경계: [architecture.md](./docs/architecture.md)
- 상위 설계와 한계: [blueprint.md](./docs/blueprint.md)

## 운영 전 남은 범위

운영 인증/subject 안정성, 실제 PG와 보상, Redis failover와 데이터 유실 실험, 다중 API 프로세스 장시간 soak, 목표 하드웨어 기반 SLO 및 용량 판정은 별도 검증이 필요합니다. 개발용 `X-Subject-ID`와 confirm endpoint를 인터넷에 공개하면 안 됩니다.
