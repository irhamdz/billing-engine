# Billing Engine

Loan billing service implemented per [billing-engine-prd.md](billing-engine-prd.md).
Go 1.26 + SQLite (WAL) + REST. All money is `int64` minor units (sen, IDR);
all timestamps UTC; `due_date` is a calendar date in `Asia/Jakarta`.

## Layout

```
cmd/api/                          # binary entrypoint
internal/
  domain/                         # Loan aggregate, schedule, payment, invariants (no DB)
  service/                        # BillingService — orchestration + idempotency
  repository/                     # contracts
    sqlite/                       # modernc.org/sqlite implementation, BEGIN IMMEDIATE
      migrations/001_init.sql     # schema, embedded into the binary
  http/                           # chi router, handlers, middleware, DTOs, error envelope
    swagger/                      # OpenAPI 3.0 spec + vendored Swagger UI assets, embedded
tests/
  integration/                    # full lifecycle, catch-up, replay attack
  concurrency/                    # 50-goroutine race, same-key replay, snapshot reads
```

## Run the test suite

```bash
go test ./... -race -count=1
```

- `./...` — runs tests in all packages recursively (domain, service, repository, http, integration, concurrency, cmd)
- `-race` — enables Go's race detector; catches concurrent memory access bugs at runtime (relevant here since the project has a 50-goroutine concurrency test)
- `-count=1` — disables test result caching, forces every test to actually re-run instead of returning a cached pass

### Coverage

```bash
go test ./... -coverpkg=./internal/...,./cmd/... -coverprofile=cover.out
go tool cover -func=cover.out | tail
```

- `-coverpkg=./internal/...,./cmd/...` — measures coverage across these packages even when the test file lives in a different package (e.g. `tests/integration` calling into `internal/service`). Without this, only the package containing the test file gets measured.
- `-coverprofile=cover.out` — writes the raw coverage data to `cover.out`
- `go tool cover -func=cover.out | tail` — prints per-function coverage percentages; `| tail` shows just the last few lines, which includes the total coverage summary at the bottom

## Build and run the API

### Option 1: Build then run

```bash
go build -o billing-engine ./cmd/api
./billing-engine --db /tmp/billing.db --addr :8080
```

- `go build -o billing-engine ./cmd/api` — compiles the binary and outputs it as `billing-engine` in the current directory
- `--db` — path to the SQLite database file (created automatically if it does not exist)
- `--addr` — address and port the HTTP server listens on

### Option 2: Build and run in one step

```bash
go run ./cmd/api --db /tmp/billing.db --addr :8080
```

## Open the Swagger UI

After the server is running, open **<http://localhost:8080/docs>** in a browser
for an interactive API explorer. The raw OpenAPI 3.0 spec is at
**<http://localhost:8080/docs/openapi.yaml>**.

The UI and spec are embedded into the binary (Swagger UI dist is vendored under
`internal/http/swagger/ui/`), so the docs page works fully offline once the
server is up.

## Endpoints

Versioned API (under `/v1/`):

| Method | Path                                    |
| ------ | --------------------------------------- |
| POST   | `/v1/loans`                             |
| GET    | `/v1/loans/{loan_id}`                   |
| GET    | `/v1/loans/{loan_id}/schedule`          |
| GET    | `/v1/loans/{loan_id}/outstanding`       |
| GET    | `/v1/loans/{loan_id}/delinquency?as_of=YYYY-MM-DD` |
| POST   | `/v1/loans/{loan_id}/payments`          |
| GET    | `/v1/loans/{loan_id}/payments`          |

POST requests should carry an `Idempotency-Key` header (PRD section 5.2).

Meta routes (intentionally outside `/v1/` — not part of the versioned contract):

| Method | Path                  | Purpose                       |
| ------ | --------------------- | ----------------------------- |
| GET    | `/docs`               | Interactive Swagger UI        |
| GET    | `/docs/openapi.yaml`  | Raw OpenAPI 3.0 spec          |
| GET    | `/healthz`            | Liveness probe (200 when up)  |

## Sample flow (Swagger UI)

The Swagger UI ships with example payloads pre-filled, so each "Try it out"
form is one-click executable.

1. Open **<http://localhost:8080/docs>**.
2. Expand **`POST /v1/loans`** → click **Try it out** → the request body is
   already populated (`principal: 5000000`, `term_weeks: 50`,
   `start_date: 2026-05-06`). Set the `Idempotency-Key` header to `init` →
   click **Execute**.
   → 201 Created. Copy `loan_id` from the response body.
3. Expand **`GET /v1/loans/{loan_id}/outstanding`** → click **Try it out** →
   paste the `loan_id` → **Execute**.
   → `{"loan_id":"…","outstanding":5500000}`.
4. Expand **`POST /v1/loans/{loan_id}/payments`** → **Try it out** → paste
   `loan_id`, set `Idempotency-Key: pay-1`, leave the body as `{"amount": 110000}` →
   **Execute**.
   → 201. Re-clicking **Execute** with the same key returns the same payment
   (idempotent replay). Changing the amount and re-executing with the same key
   returns 409 `IDEMPOTENCY_CONFLICT`.
5. Expand **`GET /v1/loans/{loan_id}/delinquency`** → set `as_of=2026-06-01` →
   **Execute**.
   → `{"is_delinquent": true, …}` (two installments are overdue at that date).

## Sample flow (curl)

```bash
curl -sX POST localhost:8080/v1/loans \
  -H 'Idempotency-Key: init' -H 'Content-Type: application/json' \
  -d '{"borrower_id":"11111111-1111-1111-1111-111111111111",
       "principal":5000000,"annual_interest_rate":0.10,
       "term_weeks":50,"start_date":"2026-05-06"}'
# → 201 with loan_id, weekly_amount=110000, total_amount=5500000

LOAN=<loan_id from above>
curl -s localhost:8080/v1/loans/$LOAN/outstanding
# → {"loan_id":"...","outstanding":5500000}

curl -sX POST localhost:8080/v1/loans/$LOAN/payments \
  -H 'Idempotency-Key: pay-1' -H 'Content-Type: application/json' \
  -d '{"amount":110000}'
# → 201; replaying the same Idempotency-Key returns the same body
# → 409 IDEMPOTENCY_CONFLICT if the body differs

curl -s "localhost:8080/v1/loans/$LOAN/delinquency?as_of=2026-06-01"
# → {"is_delinquent":true|false,...}
```

## Architecture notes

- **Concurrency safety (PRD section 6.1).** Write path opens `BEGIN IMMEDIATE`
  on a pinned connection so the SQLite reserved write lock is held from the
  start of the transaction, eliminating the "two readers both decide to write"
  race. WAL mode lets readers proceed without blocking. `busy_timeout=5000ms`
  is applied per-connection via DSN params (modernc.org/sqlite re-applies these
  on every pool checkout, which a one-shot `PRAGMA` does not). Optimistic
  version check on `loans.version` is the defense-in-depth catch.
- **Idempotency.** `UNIQUE(loan_id, idempotency_key)` on `payments`. The
  service does a pre-tx lookup; if a peer commits the same key while we are in
  flight, the in-tx domain re-check returns the existing payment and the
  service skips persistence (no version bump on replay).
- **Domain invariants** are re-checked after every aggregate mutation (see
  `internal/domain/loan.go`). Invariant violations bubble up as 500s and are
  logged loudly per PRD section 5.3.

## Edge cases (PRD section 7) — explicit test coverage

| # | Where | Test |
|---|-------|------|
| 1, 13, 15 | domain | `TestMakePayment_ExactAmount`, `TestMakePayment_RoundingFinalInstallment` |
| 2, 3 | domain + e2e | `TestIsDelinquent_CatchUpScenario`, `TestE2E_CatchUp` |
| 5 | concurrency | `TestRace_FiftyGoroutines_DistinctKeys` |
| 6, 7 | domain + http | `TestMakePayment_IdempotentReplay/Conflict`, `TestHTTP_MakePayment_IdempotencyConflict` |
| 8 | domain + http + e2e | `TestMakePayment_ClosedLoanRejected`, `TestHTTP_MakePayment_LoanClosed`, `TestE2E_FullLifecycle` |
| 9 | domain | `TestGetOutstanding` (closed-loan branch) |
| 10, 11 | domain | `TestIsDelinquent` (`before any due`), `TestGenerateSchedule_FutureStart` |
| 14 | concurrency | `TestRace_OutstandingNeverIncreases` |
| 16 | service | `TestService_MakePayment_LoanClosed` (sequential proxy of the post-close race; SAVE inside a fresh tx re-reads status) |
