-- 001_init.sql
-- Schema for the Billing Engine. All money in minor units (sen, IDR).

CREATE TABLE IF NOT EXISTS loans (
  id                   TEXT    PRIMARY KEY,
  borrower_id          TEXT    NOT NULL,
  principal            INTEGER NOT NULL,
  annual_interest_rate REAL    NOT NULL,
  term_weeks           INTEGER NOT NULL,
  total_amount         INTEGER NOT NULL,
  weekly_amount        INTEGER NOT NULL,
  start_date           TEXT    NOT NULL,                 -- YYYY-MM-DD (Asia/Jakarta)
  status               TEXT    NOT NULL CHECK (status IN ('ACTIVE','CLOSED')),
  created_at           TEXT    NOT NULL,                 -- RFC3339 UTC
  closed_at            TEXT,
  version              INTEGER NOT NULL DEFAULT 0,
  idempotency_key      TEXT    UNIQUE                    -- nullable; set by CreateLoan caller
);

CREATE TABLE IF NOT EXISTS installments (
  id          TEXT    PRIMARY KEY,
  loan_id     TEXT    NOT NULL REFERENCES loans(id),
  week_number INTEGER NOT NULL,
  amount      INTEGER NOT NULL,
  due_date    TEXT    NOT NULL,                          -- YYYY-MM-DD
  status      TEXT    NOT NULL CHECK (status IN ('PENDING','PAID')),
  paid_at     TEXT,
  payment_id  TEXT,
  UNIQUE(loan_id, week_number)
);

CREATE INDEX IF NOT EXISTS idx_installments_loan_pending
  ON installments(loan_id, week_number) WHERE status = 'PENDING';

CREATE TABLE IF NOT EXISTS payments (
  id              TEXT    PRIMARY KEY,
  loan_id         TEXT    NOT NULL REFERENCES loans(id),
  installment_id  TEXT    NOT NULL REFERENCES installments(id),
  amount          INTEGER NOT NULL,
  idempotency_key TEXT    NOT NULL,
  created_at      TEXT    NOT NULL,                      -- RFC3339 UTC
  UNIQUE(loan_id, idempotency_key)
);

CREATE INDEX IF NOT EXISTS idx_payments_loan
  ON payments(loan_id, created_at);
