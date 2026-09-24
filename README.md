# Ticketing V1

Redis 기반 대기열·입장 제어와 MySQL 기반 단일 좌석 선점/모의 구매를 구현한 Go 서비스입니다.

핵심 제품 규칙은 두 가지입니다.

- 한 사용자는 같은 이벤트에서 좌석을 정확히 최대 1석만 구매할 수 있습니다. MySQL purchase guard와 UNIQUE 제약이 동시 요청에서도 이 제한을 강제합니다.
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

기본 설정은 [config.go](./internal/config/config.go)에 있고, 개발용 예시는 [.env.example](./.env.example)에 있습니다. 이 애플리케이션은 `.env`를 자동으로 읽지 않으므로 필요한 값은 프로세스 환경 변수로 주입해야 합니다. `TICKET_HMAC_KEY` 기본값은 오직 로컬 개발 fixture입니다.

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

실제 Redis/MySQL 통합 테스트는 먼저 Compose 의존성을 띄운 뒤 실행합니다. 의존성이 없으면 테스트는 skip으로 성공하지 않고 실패합니다.

```powershell
docker compose up -d --wait
go run ./cmd/ticketing migrate
go test -count=1 -tags=integration ./...
```

Windows 호스트에 C 컴파일러가 없을 때도 Linux 컨테이너에서 race detector를 실행할 수 있습니다.

```powershell
docker run --rm -v "${PWD}:/src" -w /src `
  -e REDIS_ADDR=host.docker.internal:16379 `
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

## 상태와 복구

- Redis는 `noeviction`과 AOF를 사용합니다. epoch/meta/spent/permit 손실이 의심되면 자동 DIRECT로 우회하지 않고 `QUEUE_RECOVERING`으로 닫힙니다.
- MySQL이 불가하면 Queue API는 직접 booking permit을 만들지 않고 대기표 경로로 전환합니다. 좌석 변경은 실패합니다.
- `init-state`는 최초 설치 절차이며 부분 손실을 정상 상태로 덮어쓰는 복구 명령이 아닙니다.
- 로컬 데이터를 완전히 버리고 다시 시작할 때만 `docker compose down -v`를 사용할 수 있습니다. 이 명령은 로컬 Redis/MySQL 볼륨을 영구 삭제하므로 그 다음 `up`, `migrate`, `init-state`를 다시 실행해야 합니다.

## 계약과 권위

- HTTP 사용 계약: [API.md](./docs/API.md)
- HTTP route와 최종 status mapping: [server.go](./internal/httpapi/server.go)
- 좌석·주문 권위: [migrations](./migrations)와 [booking store](./internal/booking/store.go)
- Queue 원자 전이: [Redis Lua](./redis/lua)
- 상위 설계와 한계: [blueprint.md](./docs/blueprint.md)

## 운영 전 남은 범위

운영 인증/subject 안정성, 실제 PG와 보상, Redis failover와 데이터 유실 실험, 다중 API 프로세스 장시간 soak, 목표 하드웨어 기반 SLO 및 용량 판정은 별도 검증이 필요합니다. 개발용 `X-Subject-ID`와 confirm endpoint를 인터넷에 공개하면 안 됩니다.
