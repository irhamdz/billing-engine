# Product Requirements Document: Billing Engine API

| | |
|---|---|
| **Project** | Loan Billing Engine |
| **Version** | 1.0 |
| **Status** | Draft |
| **Author** | Irham Dzuhri |
| **Date** | May 2026 |
| **Stack** | Go, SQLite (WAL mode), REST |

---

## 1. Overview

### 1.1 Purpose

The Billing Engine is the system of record for the financial lifecycle of fixed-term, flat-interest loans. It owns every loan's repayment schedule, processes incoming repayments, computes outstanding balances on demand, and identifies delinquent borrowers who have missed consecutive payments.

This service is the single source of truth for two questions consumed across the platform: *"how much does borrower X owe right now?"* and *"is borrower X behind on payments?"* — answered by the Loan Engine, Collections, Risk, and Customer Service systems.

### 1.2 Scope

**In scope (v1):**

- Loan creation with deterministic schedule generation
- Repayment processing with idempotency and concurrency safety
- Outstanding balance calculation
- Delinquency detection
- Schedule and payment history retrieval
- Automatic loan closure on full repayment

### 1.3 Goals

- **Correctness.** Outstanding balance and delinquency status must be accurate to the rupiah at all times.
- **Idempotency.** Payment processing must be safe under network retries and client replays.
- **Concurrency safety.** Concurrent operations on the same loan must not corrupt state.
- **Auditability.** Every payment is traceable; loan state is reconstructible from event history.
- **Performance.** Read endpoints respond in <50 ms p99; payment processing in <200 ms p99.

---

## 2. Glossary / Ontology

| Term | Definition |
|------|------------|
| **Principal** | The original loan amount disbursed to the borrower. |
| **Interest Rate** | Flat rate applied to principal to compute total interest, expressed annually. |
| **Total Loan Amount** | `Principal + Total Interest`. The full sum the borrower must repay. |
| **Term** | Loan duration in weeks. |
| **Schedule** | The deterministic, time-ordered sequence of installments due over the loan term. |
| **Installment** | A single scheduled payment slot within a schedule (e.g., Week 7 of 50). |
| **Weekly Amount** | The fixed amount due per week, derived from `Total Loan Amount / Term`. |
| **Payment** | A successful, immutable repayment event applied to exactly one installment. |
| **Outstanding** | Total amount the borrower still owes at any point in time. |
| **Delinquent** | A loan state where the borrower has ≥ 2 consecutive missed installments past their due date. |
| **Closed Loan** | A loan whose installments are all fully paid. Outstanding = 0. |
| **Idempotency Key** | A client-supplied unique key that ensures a payment is applied at most once. |

---

## 3. Business Rules

### 3.1 Loan Creation

A loan is created with `borrower_id`, `principal`, `annual_interest_rate`, `term_weeks`, and `start_date`. The schedule is generated immediately and atomically as part of loan creation.

The default product, per the spec, is principal = 5,000,000 IDR, rate = 10%, term = 50 weeks.

### 3.2 Schedule Generation

```
total_interest = principal × annual_interest_rate
total_amount   = principal + total_interest
weekly_amount  = total_amount / term_weeks
```

For the default loan: `weekly_amount = 5,500,000 / 50 = 110,000 IDR`.

Each installment has a `due_date` computed as `start_date + (week_number × 7 days)`. Rounding strategy: integer rupiah; any remainder from division is added to the **final** installment to guarantee `sum(installments) == total_amount`.

### 3.3 Payment Processing

- A payment must be **exactly equal** to `weekly_amount`. Over- and under-payment are rejected.
- Payments apply to the **oldest unpaid installment** first (FIFO).
- A borrower with N missed installments must make N separate payments to fully catch up.
- Payments on a closed loan are rejected.
- Payments are idempotent via a client-supplied `Idempotency-Key` header.

### 3.4 Delinquency

- A borrower is **delinquent** on a loan if there are **≥ 2 consecutive installments** that are past their `due_date` and unpaid.
- Delinquency is computed at query time from current state — it is not a stored flag, which prevents staleness and removes the need for a background job.
- Once the borrower catches up such that no two consecutive missed installments remain, they are no longer delinquent.

### 3.5 Loan Closure

- A loan transitions to `CLOSED` automatically when its final installment is paid.
- A closed loan reports `outstanding = 0` and rejects further payments.
- Closure is terminal — no transition back to `ACTIVE`.

---

## 4. Domain Model

The domain is modeled around four core constructs: **Borrower**, **Loan**, **Installment**, and **Payment**. The **Loan aggregate** is the consistency boundary for all billing operations — every state-changing operation passes through it.

### 4.1 Entity: Borrower

The party who has taken the loan and is responsible for repayment.

**Attributes**

| Field | Type | Notes |
|-------|------|-------|
| `id` | UUID | Primary key, immutable |
| `external_id` | string | Optional link to identity service |
| `name` | string | |
| `created_at` | timestamp | |

**Behaviors**

- Holds zero or more loans.
- Borrower-level delinquency is derived as `OR` over active loans (any delinquent loan ⇒ borrower is delinquent).

**Invariants**

- `id` is immutable once created.

---

### 4.2 Aggregate Root: Loan

The aggregate root for all billing concerns. Owns its schedule, governs all state transitions, and enforces invariants.

**Attributes**

| Field | Type | Notes |
|-------|------|-------|
| `id` | UUID | Primary key |
| `borrower_id` | UUID | FK → Borrower |
| `principal` | int64 | Stored in minor units (sen) |
| `annual_interest_rate` | decimal | e.g., 0.10 |
| `term_weeks` | int | |
| `total_amount` | int64 | Derived, persisted |
| `weekly_amount` | int64 | Derived, persisted |
| `start_date` | date | Asia/Jakarta calendar date |
| `status` | enum | `ACTIVE`, `CLOSED` |
| `created_at` | timestamp | |
| `closed_at` | timestamp | Nullable |
| `version` | int | Optimistic concurrency control |

**Behaviors**

| Operation | Description |
|-----------|-------------|
| `Create(...)` | Generates schedule atomically. |
| `GetOutstanding()` | Returns `total_amount − sum(payments.amount)`. Returns `0` if closed. |
| `IsDelinquent(asOf)` | Counts consecutive `PENDING` installments where `due_date < asOf`; returns `true` if count ≥ 2. |
| `MakePayment(amount, idempotencyKey)` | Validates and applies payment to oldest pending installment; closes loan on final payment. |
| `GetSchedule()` | Returns full installment list. |
| `GetPaymentHistory()` | Returns all payments in chronological order. |
| `close()` | Internal; called when last installment is paid. |

**Invariants**

- `total_amount == principal + (principal × annual_interest_rate)`
- `sum(installments.amount) == total_amount`
- `sum(payments.amount) ≤ total_amount`
- `status == CLOSED` ⇔ all installments are `PAID`
- Once `CLOSED`, status cannot transition back.

**State Diagram**

```
        ┌──────────┐   final installment paid   ┌──────────┐
   ───▶ │  ACTIVE  │ ────────────────────────▶  │  CLOSED  │
        └──────────┘                            └──────────┘
                                                  (terminal)
```

---

### 4.3 Entity: Installment *(within Loan aggregate)*

A single weekly payment slot. Not independently addressable — only mutated through the parent `Loan`.

**Attributes**

| Field | Type | Notes |
|-------|------|-------|
| `id` | UUID | Primary key |
| `loan_id` | UUID | FK → Loan |
| `week_number` | int | 1-indexed; unique per loan |
| `amount` | int64 | Equal to `weekly_amount` (last installment may absorb rounding remainder) |
| `due_date` | date | |
| `status` | enum | `PENDING`, `PAID` |
| `paid_at` | timestamp | Nullable |
| `payment_id` | UUID | FK → Payment, nullable |

**Behaviors**

- `markPaid(paymentID, paidAt)` — internal; called only by parent `Loan`.
- `IsOverdue(asOf) → bool` — `true` if `status == PENDING && asOf > due_date`.

**Invariants**

- `week_number ∈ [1, term_weeks]`, unique within loan.
- `status == PAID` ⇔ `paid_at != null && payment_id != null`.
- Once `PAID`, never reverts to `PENDING`.

---

### 4.4 Entity: Payment

An immutable record of a successful repayment event.

**Attributes**

| Field | Type | Notes |
|-------|------|-------|
| `id` | UUID | Primary key |
| `loan_id` | UUID | FK → Loan |
| `installment_id` | UUID | FK → Installment |
| `amount` | int64 | Equal to the satisfied installment's amount |
| `idempotency_key` | string | Unique per loan |
| `created_at` | timestamp | |

**Behaviors**

- Append-only. No update or delete operations exist.

**Invariants**

- `amount == installment.amount` (exact-amount rule).
- `(loan_id, idempotency_key)` is unique — a duplicate request returns the existing payment.

---

### 4.5 Relationships & ERD

```
┌──────────────┐       ┌──────────────────────────┐       ┌──────────────┐
│   Borrower   │ 1───* │           Loan           │ 1───* │  Installment │
│              │       │     (Aggregate Root)     │       │              │
│  id          │       │                          │       │  id          │
│  name        │       │  borrower_id             │       │  loan_id     │
│              │       │  principal               │       │  week_number │
└──────────────┘       │  weekly_amount           │       │  amount      │
                       │  status (ACTIVE|CLOSED)  │       │  due_date    │
                       │  version                 │       │  status      │
                       └────────────┬─────────────┘       └──────┬───────┘
                                    │ 1                          │ 1
                                    │                            │
                                    │ *                          │ 1
                              ┌─────┴────────────────────────────┴───┐
                              │              Payment                 │
                              │                                      │
                              │  id                                  │
                              │  loan_id, installment_id             │
                              │  amount, idempotency_key             │
                              │  created_at                          │
                              └──────────────────────────────────────┘
```

**Aggregate boundaries**

- The **Loan** aggregate encloses the `Loan` root, its `Installment`s, and references to its `Payment`s.
- All mutations to `Installment` go through `Loan` — direct mutation is forbidden — to keep invariants enforced in one place.
- `Borrower` is a separate aggregate, referenced only by ID. The Billing Engine does not own borrower identity.
- `Payment` is conceptually owned by the Loan aggregate but persisted as a sibling row for auditability and append-only semantics.

---

## 5. API Design

All endpoints live under `/v1/`. All money amounts are transmitted as **integers in minor units (sen)** to eliminate floating-point ambiguity.

### 5.1 Endpoints

| Method | Path | Purpose |
|--------|------|---------|
| `POST` | `/v1/loans` | Create a new loan and generate its schedule |
| `GET` | `/v1/loans/{loan_id}` | Get loan summary |
| `GET` | `/v1/loans/{loan_id}/schedule` | Get the full installment schedule |
| `GET` | `/v1/loans/{loan_id}/outstanding` | Get current outstanding amount |
| `GET` | `/v1/loans/{loan_id}/delinquency` | Get delinquency status |
| `POST` | `/v1/loans/{loan_id}/payments` | Make a payment (idempotent) |
| `GET` | `/v1/loans/{loan_id}/payments` | List payment history |

### 5.2 Idempotency

- All `POST` requests honor an `Idempotency-Key` header.
- For `POST /payments`, the key is scoped to the loan.
- A repeat request with the same key returns the original `201` response — not `409`.
- A repeat with the same key but a different request body returns `409 IDEMPOTENCY_CONFLICT`.
- Keys are retained for at least 24 hours.

### 5.3 Error Model

| HTTP | Code | Meaning |
|------|------|---------|
| `400` | `INVALID_AMOUNT` | Payment amount does not equal the weekly amount |
| `400` | `INVALID_REQUEST` | Malformed request body |
| `404` | `LOAN_NOT_FOUND` | Loan does not exist |
| `409` | `LOAN_CLOSED` | Payment attempted on a closed loan (no installments remain) |
| `409` | `IDEMPOTENCY_CONFLICT` | Same key reused with different payload |
| `500` | `INTERNAL_ERROR` | Unexpected server error (includes invariant violations such as an `ACTIVE` loan with no pending installments — should never occur, logged loudly if it does) |

---

## 6. Non-Functional Requirements

### 6.1 Concurrency Safety

The Loan aggregate is the consistency boundary. Concurrent payment attempts on the same loan must be serialized to prevent double-payment of an installment, skipped installments, or torn outstanding calculations.

**Strategy.** SQLite is run in **WAL mode** so concurrent readers proceed alongside a single writer. Write-path serialization is achieved by opening transactions with `BEGIN IMMEDIATE`, which acquires a reserved write lock at transaction start rather than on first write. This eliminates the classic "two readers both decide to write" race that a plain `BEGIN` would allow. An optimistic version check on the `loans` row provides defense-in-depth and catches lost-update bugs in tests.

```sql
-- PRAGMAs applied to every connection at startup:
--   PRAGMA journal_mode = WAL;
--   PRAGMA foreign_keys = ON;
--   PRAGMA busy_timeout = 5000;
--   PRAGMA synchronous = NORMAL;

BEGIN IMMEDIATE;
SELECT version FROM loans WHERE id = ?;
-- 1. find oldest PENDING installment for this loan
-- 2. validate amount == installment.amount
-- 3. INSERT payment (UNIQUE on loan_id, idempotency_key)
-- 4. UPDATE installment SET status='PAID', payment_id=?, paid_at=?
-- 5. if last installment: UPDATE loans SET status='CLOSED', closed_at=?
UPDATE loans SET version = version + 1 WHERE id = ? AND version = ?;
COMMIT;
```

Read endpoints (`GetOutstanding`, `IsDelinquent`) use plain (deferred) transactions. Under WAL mode, readers see a consistent snapshot and never block writers.

`busy_timeout = 5000` ensures that if a second writer arrives while the first holds the reserved lock, it waits up to 5 seconds rather than failing immediately with `SQLITE_BUSY`. Combined with `BEGIN IMMEDIATE`, this delivers clean serialization without any in-process mutexes.

No application-level mutexes are used — the database remains the source of truth for serialization, which keeps the code correct if v2 swaps SQLite for a server database.

### 6.2 Idempotency

Implemented via a `UNIQUE (loan_id, idempotency_key)` constraint on the `payments` table. On insert conflict (`SQLITE_CONSTRAINT_UNIQUE`), the existing payment row is fetched and its body compared against the incoming request; mismatches return `409 IDEMPOTENCY_CONFLICT`.

### 6.3 Auditability

Payments are append-only and immutable. Loan state can be reconstructed entirely by replaying payments against the schedule, which makes recovery from data corruption tractable. All state transitions emit structured logs with `request_id`, `loan_id`, and `idempotency_key`.

### 6.4 Performance

| Operation | Target p99 |
|-----------|-----------|
| `GetOutstanding` | < 50 ms |
| `IsDelinquent` | < 50 ms |
| `MakePayment` | < 200 ms |
| `GetSchedule` | < 100 ms |

`GetOutstanding` is a single aggregate query: `SELECT total_amount - COALESCE(SUM(amount), 0) FROM payments WHERE loan_id = ?`.

### 6.5 Observability

- **Logging.** Structured logs (zerolog) with `request_id`, `loan_id`, `borrower_id`, `idempotency_key`, `latency_ms`.
- **Metrics.** Prometheus: `payments_total`, `payments_rejected_total{reason}`, `delinquent_loans_gauge`, `payment_processing_duration_seconds`.
- **Tracing.** OpenTelemetry spans across HTTP → service → repository → database.

---

## 7. Edge Cases

| # | Scenario | Expected Behavior |
|---|----------|-------------------|
| 1 | Payment amount ≠ weekly amount (over or under) | Reject `400 INVALID_AMOUNT` |
| 2 | Borrower misses 5 weeks then pays once | Apply to week 1; weeks 2–5 still pending; borrower remains delinquent |
| 3 | Borrower misses 5 weeks then pays 5 times | Each payment applies to oldest pending; borrower is no longer delinquent only after the 4th catch-up payment |
| 4 | Payment received after `due_date` of all installments | Apply normally to oldest pending; no late fee in v1 |
| 5 | Two concurrent `MakePayment` calls on the same loan | Serialized via row lock; both succeed and apply to the next two pending installments |
| 6 | Same idempotency key sent twice (same body) | Second request returns the original payment; no new payment created |
| 7 | Same idempotency key sent twice (different body) | Reject `409 IDEMPOTENCY_CONFLICT` |
| 8 | Payment on a closed loan | Reject `409 LOAN_CLOSED` |
| 9 | `GetOutstanding` on a closed loan | Returns `0` |
| 10 | `IsDelinquent` checked before week 1 due date | Returns `false`; nothing is yet overdue |
| 11 | Loan created with `start_date` in the future | Schedule generated normally; not delinquent until first due_date passes |
| 12 | `start_date` timezone | All timestamps stored in UTC; `due_date` is a calendar date in Asia/Jakarta |
| 13 | Final installment with rounding remainder | Last installment absorbs the remainder; final payment must equal that exact remainder |
| 14 | `GetOutstanding` during in-flight payment | Returns pre-payment amount under snapshot isolation; consistent post-commit |
| 15 | Negative or zero amount | Reject `400 INVALID_AMOUNT` |
| 16 | Race: payment B arrives just after final payment A closed the loan | B sees `status == CLOSED`, rejected with `409 LOAN_CLOSED` |

---

## 8. Functional Requirements — Method Specs

### 8.1 Required Methods (per spec)

#### `MakePayment(loan_id, amount, idempotency_key) → Payment`

1. Look up loan; return `404` if missing.
2. Reject `409 LOAN_CLOSED` if `status == CLOSED`. By invariant, this also covers the "no pending installments" case.
3. Check for existing payment with same `(loan_id, idempotency_key)`; return it if found and amount matches; return `409 IDEMPOTENCY_CONFLICT` if amounts differ.
4. Open `BEGIN IMMEDIATE` transaction.
5. Find oldest `PENDING` installment for this loan.
   - If none is found despite `status == ACTIVE`, this is an invariant violation: log loudly, return `500 INTERNAL_ERROR`, and emit an alert metric. This branch should be unreachable in correct system state.
6. Validate `amount == installment.amount`; reject `400 INVALID_AMOUNT` otherwise.
7. Insert `Payment`, mark installment `PAID`.
8. If this was the final installment, set loan `status = CLOSED` and `closed_at = NOW()`.
9. Bump loan `version`.
10. Commit.

#### `GetOutstanding(loan_id) → int64`

Returns `total_amount - sum(payments.amount)`. Returns `0` for closed loans (which by invariant equals the formula anyway, but is short-circuited).

#### `IsDelinquent(loan_id, asOf time.Time) → bool`

Counts consecutive `PENDING` installments with `due_date < asOf`, scanning by `week_number` ascending from the oldest pending. Returns `true` if the count is ≥ 2.

### 8.2 Extended Methods

| Method | Purpose |
|--------|---------|
| `CreateLoan(borrower_id, principal, rate, term, start_date) → Loan` | Generates schedule atomically and returns the full loan |
| `GetSchedule(loan_id) → []Installment` | Returns all installments with status, due_date, paid_at |
| `GetPaymentHistory(loan_id) → []Payment` | Returns all payments in chronological order |
| `GetLoanSummary(loan_id) → LoanSummary` | Single-call view: status, outstanding, next_due_date, next_due_amount, is_delinquent, paid_count, remaining_count — useful for borrower-facing dashboards |

---

## 9. Testing Strategy

### 9.1 Unit Tests

- Schedule generation across various `(principal, rate, term)` combinations.
- Rounding edge case: remainder absorption into final installment.
- `GetOutstanding` correctness at every stage of loan life.
- `IsDelinquent` across boundary conditions: 0 misses, 1 miss, 2 misses, 3 misses, miss-then-pay, miss-then-partial-catchup.
- Idempotency key handling: same body, different body, missing key.
- Payment validation: exact-amount rule, closed-loan rejection, no-pending rejection.

### 9.2 Integration Tests

- End-to-end loan lifecycle: create → 50 successful payments → automatic closure → reject post-closure payment.
- Catch-up scenario: skip 3 weeks, make 3 sequential payments, verify delinquency clears at the right moment.
- Replay attack: same idempotency key sent 100 times → exactly one payment recorded.

### 9.3 Concurrency Tests

- 50 goroutines invoking `MakePayment` on the same loan concurrently → exactly one succeeds per pending installment; the rest either return existing payments (same key) or fail validation (no pending left).
- Concurrent `MakePayment` and `GetOutstanding` → no torn reads; outstanding is monotonically non-increasing across snapshots.
- Property test: under any interleaving of N valid payments, final state satisfies all Loan invariants.

### 9.4 Coverage Targets

- ≥ 85% line coverage overall.
- 100% on the `Loan` aggregate methods.
- All listed edge cases from §7 have an explicit test.

---

## 10. Technical Architecture (Go)

### 10.1 Layered Architecture

```
┌────────────────────────────────────────┐
│       HTTP Handlers (chi router)       │  ← request decoding, response encoding
├────────────────────────────────────────┤
│       Application Service Layer        │  ← orchestration, transactions, idempotency
├────────────────────────────────────────┤
│       Domain Layer (Aggregates)        │  ← Loan, Installment, Payment, invariants
├────────────────────────────────────────┤
│       Repository Interfaces            │  ← persistence abstraction
├────────────────────────────────────────┤
│  SQLite Implementation (modernc/sqlite)│  ← pure-Go driver, no CGo
└────────────────────────────────────────┘
```

### 10.2 Package Layout

```
billing-engine/
├── cmd/api/                  # main.go, server bootstrap
├── internal/
│   ├── domain/
│   │   ├── loan.go           # Loan aggregate, invariants
│   │   ├── schedule.go       # Installment, schedule generation
│   │   ├── payment.go        # Payment entity
│   │   └── errors.go         # typed domain errors
│   ├── service/
│   │   └── billing.go        # use-case orchestration
│   ├── repository/
│   │   ├── loan_repo.go      # interfaces
│   │   └── sqlite/           # modernc.org/sqlite implementation
│   │       └── migrations/   # 001_init.sql, embedded via go:embed
│   ├── http/
│   │   ├── handlers.go
│   │   ├── middleware.go     # idempotency, request_id, recovery, logging
│   │   └── dto.go            # request/response types
│   └── platform/
│       ├── db.go
│       └── observability.go
└── tests/
    ├── integration/
    └── concurrency/
```

The migration SQL lives next to the sqlite repository because Go's `//go:embed`
directive is constrained to the embedding package's subtree (no `..` paths). Keeping
it there gives the binary a single source of truth and a self-contained executable —
no working-directory or external schema file required at startup.

### 10.3 Key Interfaces

```go
type LoanRepository interface {
    Create(ctx context.Context, loan *Loan) error
    GetByID(ctx context.Context, id uuid.UUID) (*Loan, error)
    GetByIDForUpdate(ctx context.Context, tx Tx, id uuid.UUID) (*Loan, error)
    Save(ctx context.Context, tx Tx, loan *Loan) error
}

type PaymentRepository interface {
    Insert(ctx context.Context, tx Tx, p *Payment) error
    GetByIdempotencyKey(ctx context.Context, loanID uuid.UUID, key string) (*Payment, error)
    ListByLoan(ctx context.Context, loanID uuid.UUID) ([]Payment, error)
}

type BillingService interface {
    CreateLoan(ctx context.Context, req CreateLoanRequest) (*Loan, error)
    MakePayment(ctx context.Context, loanID uuid.UUID, amount int64, key string) (*Payment, error)
    GetOutstanding(ctx context.Context, loanID uuid.UUID) (int64, error)
    IsDelinquent(ctx context.Context, loanID uuid.UUID) (bool, error)
    GetSchedule(ctx context.Context, loanID uuid.UUID) ([]Installment, error)
    GetPaymentHistory(ctx context.Context, loanID uuid.UUID) ([]Payment, error)
}
```

### 10.4 Concurrency Primitives

- **`BEGIN IMMEDIATE` transactions** for the write path — acquires SQLite's reserved write lock at transaction start, serializing concurrent payments deterministically.
- **WAL journal mode** so reads never block writes and vice versa.
- **`busy_timeout = 5000`** for graceful waiting when another writer holds the reserved lock.
- **Optimistic version check** as defense-in-depth (catches lost-update bugs in code review and tests, not just at runtime).
- **`context.Context`** propagated through every layer for cancellation and deadlines.
- **No application-level mutexes.** They would mask correctness bugs and would be wrong under any future migration to a server database.

### 10.5 Database Choice Rationale

The reasoning:

- **Operational simplicity for the deliverable.** Reviewers can clone, run `go test ./...`, and exercise the full system without standing up a separate database server.
- **Single-file persistence.** The database is just `billing.db`; backup, replay, and inspection are trivial.
- **Pure-Go driver (`modernc.org/sqlite`).** No CGo, no platform-specific build flags — the binary cross-compiles cleanly.
- **Sufficient correctness guarantees.** ACID transactions, foreign keys, and unique constraints are all first-class. WAL + `BEGIN IMMEDIATE` provides the serialization the Loan aggregate needs.
- **Migration path is cheap.** All persistence is hidden behind the `LoanRepository` and `PaymentRepository` interfaces. Swapping to PostgreSQL is a new package implementing the same contracts. SQL dialect differences (`BEGIN IMMEDIATE` → `SELECT ... FOR UPDATE`) are isolated to the repository layer.

---

## 11. Open Questions / Stated Assumptions

These are confirmed-with-stakeholder. Surfacing them here demonstrates explicit awareness rather than implicit guessing.

1. **Delinquency threshold wording.** Spec contains both *"miss 2 continuous repayments"* and *"more than 2 weeks of non-payment"*. **Assumed:** ≥ 2 consecutive missed installments past `due_date` ⇒ delinquent.
2. **Interest interpretation.** Spec says *"flat 10% per annum"* with a 50-week term, but the example weekly amount (110,000) implies `interest = principal × rate` flat over the term, not pro-rated. **Assumed:** flat-over-term interpretation, matching the example.
3. **Week boundaries.** **Assumed:** weeks are calendar-based starting from `start_date`; not 7-day rolling windows.
4. **Timezone.** **Assumed:** Asia/Jakarta for `due_date` calendar logic; UTC for all timestamps.
5. **Disbursement timing.** **Assumed:** loan is treated as disbursed at `start_date`; outstanding at `start_date` is `total_amount`.
6. **Currency.** IDR only; all amounts in minor units (sen) to avoid float math.
7. **Borrower-level delinquency.** **Assumed:** `OR` over active loans — any one delinquent loan flags the borrower.
8. **Holidays / weekends.** Not considered; `due_date` falls on calendar days regardless.

---

*End of document.*
