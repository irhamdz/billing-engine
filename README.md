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
tests/
  integration/                    # full lifecycle, catch-up, replay attack
  concurrency/                    # 50-goroutine race, same-key replay, snapshot reads
```

## Run the test suite

```bash
go test ./... -race -count=1
```

Coverage:

```bash
go test ./... -coverpkg=./internal/...,./cmd/... -coverprofile=cover.out
go tool cover -func=cover.out | tail
```

## Run the API

```bash
go run ./cmd/api --db /tmp/billing.db --addr :8080
```

Endpoints (all under `/v1/`):

| Method | Path                                    |
| ------ | --------------------------------------- |
| POST   | `/v1/loans`                             |
| GET    | `/v1/loans/{loan_id}`                   |
| GET    | `/v1/loans/{loan_id}/schedule`          |
| GET    | `/v1/loans/{loan_id}/outstanding`       |
| GET    | `/v1/loans/{loan_id}/delinquency?as_of=YYYY-MM-DD` |
| POST   | `/v1/loans/{loan_id}/payments`          |
| GET    | `/v1/loans/{loan_id}/payments`          |

POST requests should carry an `Idempotency-Key` header (PRD §5.2).

### Sample flow

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

- **Concurrency safety (PRD §6.1).** Write path opens `BEGIN IMMEDIATE`
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
  logged loudly per PRD §5.3.

## Edge cases (PRD §7) — explicit test coverage

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

## TDD record

The build was driven test-first, layer by layer:

1. Domain (pure, no DB) — schedule, loan aggregate, payment.
2. Repository (sqlite tx, loan repo, payment repo).
3. Service (orchestration + idempotency).
4. HTTP (chi router + handlers + middleware).
5. Integration + concurrency suites.
6. `cmd/api` wiring + smoke test.

Each layer added failing tests first, then minimal implementation to turn
them green; only then was the next layer started.
