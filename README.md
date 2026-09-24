# Ticketing V1

[Korean translation](./README_KO.md)

This Go service implements a Redis-backed waiting queue and admission control, with MySQL-backed single-seat holds and simulated purchases. Each service instance signs queue tickets with an ephemeral Ed25519 key; a separate Redis stores public verification keys for cross-instance use.

Two product rules are central:

- A user may purchase at most one seat per event. A MySQL purchase guard and UNIQUE constraints enforce this even under concurrent requests.
- A user may obtain multiple queue tickets, including after purchasing a seat. Each ticket has an independent `ticket_id` and `seq`.

This V1 is for local development and validation, not production deployment. It does not include production authentication, real payments, or a guarantee that Redis failover preserves every acknowledged state change.

## Run locally

You need Go 1.27.1, Docker Engine, and Docker Compose. The following commands were verified from the repository root in PowerShell:

```powershell
docker compose up -d --wait
go run ./cmd/ticketing migrate
go run ./cmd/ticketing init-state
go run ./cmd/ticketing serve
```

Then open [http://127.0.0.1:8080](http://127.0.0.1:8080) for the minimal development client. Check readiness with:

```powershell
Invoke-RestMethod http://127.0.0.1:8080/readyz
```

`migrate` and `init-state` are explicit setup steps. A normal `serve` does not create or recover Redis authority state automatically; it refuses to start if the installation marker is missing.

Defaults are in [config.go](./internal/config/config.go), and development examples are in [.env.example](./.env.example). See the [configuration reference (Korean)](./docs/configuration.md) for all settings and their lifecycle. The application does not load `.env` automatically, so pass any required values through the process environment. `serve` needs both the queue Redis and the separate ticket-key Redis; `migrate` and `init-state` do not register signing keys.

To reduce request logging:

```powershell
$env:TICKETING_LOG_LEVEL = 'warn'
go run ./cmd/ticketing serve
```

## Test

Run dependency-free unit tests and static checks with:

```powershell
go test ./...
go vet ./...
```

Start both Redis instances and MySQL before running integration tests. If they are unavailable, these tests fail rather than silently skip:

```powershell
docker compose up -d --wait
go run ./cmd/ticketing migrate
go test -count=1 -tags=integration ./...
```

The full API end-to-end test runs a built binary, HTTP listener, and admission worker. It creates a dedicated random event and cleans up that event's MySQL/queue-Redis data; generated public keys remain in the separate Redis until their TTL expires. It does not touch the existing seat and order data for events 100/200:

```powershell
go test -tags=e2e -count=1 -v ./e2e
```

The journey covers DIRECT admission occupying capacity; multiple QUEUE tickets for one subject; a server restart that verifies an earlier instance's ticket via the public-key Redis; heartbeat, grant, and redeem; seat-map lookup, hold, cancellation, automatic assignment, and confirmation; then new tickets and a new booking permit after purchase, with another purchase still rejected. It does not execute browser JavaScript or charge a real payment method.

If the Windows host has no C compiler, you can run the race detector in a Linux container:

```powershell
docker run --rm -v "${PWD}:/src" -w /src `
  -e REDIS_ADDR=host.docker.internal:16379 `
  -e TICKET_KEY_REDIS_ADDR=host.docker.internal:16380 `
  -e "MYSQL_DSN=ticketing:ticketing@tcp(host.docker.internal:13306)/ticketing?parseTime=true&loc=UTC&charset=utf8mb4&multiStatements=true" `
  golang:1.27.1-bookworm go test -race -count=1 -tags=integration ./...
```

See [TEST-REPORT.md](./docs/reports/TEST-REPORT.md) for the checks that ran and the boundaries still unverified.

## Load observations

The [Go load tester](./cmd/loadtest/main.go) runs separately from the product process. See [load-small.json](./testdata/load-small.json) for example input.

```powershell
go run ./cmd/loadtest -profile entry -warmup 2s -duration 10s -concurrency 16
go run ./cmd/loadtest -profile hold-auto -warmup 2s -duration 10s -concurrency 8
go run ./cmd/loadtest -profile hold-hot -warmup 2s -duration 10s -concurrency 8 -seat 1
```

The `entry` profile releases its booking permit on normal completion; hold profiles release both the hold and permit. `NO_ASSIGNABLE_SEATS_NOW` in `hold-auto` and `SEAT_BUSY`/`SEAT_UNAVAILABLE` in `hold-hot` are intended contention outcomes. Exceeding the default admission rate normally switches the server into QUEUE mode. To observe the direct path itself, start from clean local event state and set the rate/burst for that purpose.

Results and interpretation limits are in [BENCHMARK-REPORT.md](./docs/reports/BENCHMARK-REPORT.md).

For a short, per-area happy-path measurement with Docker k6 on one laptop, prepare Compose and migrations, then run:

```powershell
docker compose up -d --wait
go run ./cmd/ticketing migrate
powershell -File .\bench\run.ps1 -VUs 4 -Iterations 100 -WarmupIterations 5
```

The runner uses a separate synthetic event and Docker API/k6 containers, then cleans up only that event. Raw summaries remain in the untracked `benchmark-results/` directory. The [k6 workload](./bench/k6.js) separates admission, queue ticket/heartbeat, seat-map reads, hold/cancel, confirmation, and a full purchase. Its short-run iteration rate is not a production capacity estimate; see the “Docker k6” section of the benchmark report for observations and limitations.

## State and recovery

- Both Redis instances use `noeviction` and AOF. If loss of queue epoch/meta/spent/permit state is suspected, the system fails closed with `QUEUE_RECOVERING` instead of silently switching to DIRECT admission. Ticket-key Redis outages stop ticket issuance/verification and readiness; loss of old public keys can invalidate outstanding tickets.
- When MySQL is unavailable, the queue API issues tickets instead of direct booking permits. Seat mutations fail.
- `init-state` is for initial installation, not a way to overwrite partial state loss with an apparently healthy empty state.
- Use `docker compose down -v` only when you intend to discard all local data and start over. It permanently deletes both local Redis volumes and the MySQL volume; run `up`, `migrate`, and `init-state` afterward.

## Contracts and authority

- HTTP usage contract: [API.md](./docs/API.md)
- Authoritative HTTP routes and status mapping: [server.go](./internal/httpapi/server.go)
- Seat and order authority: [migrations](./migrations) and [booking store](./internal/booking/store.go)
- Atomic queue transitions: [Redis Lua](./redis/lua)
- Current topology, data authority, and failure boundaries: [architecture.md](./docs/architecture.md)
- Project-level design and limits: [blueprint.md](./docs/blueprint.md)

## Before production

Production authentication and stable subject identity, a real payment provider and compensation flow, Redis failover/data-loss experiments, long-running multi-API-process soak tests, and target-hardware SLO/capacity assessment remain unverified. Do not expose the development `X-Subject-ID` scheme or confirmation endpoint to the public internet.
