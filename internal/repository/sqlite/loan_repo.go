package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/irhamdz/billing-engine/internal/domain"
	"github.com/irhamdz/billing-engine/internal/repository"
)

const dateFmt = "2006-01-02"

// loanRepo implements repository.LoanRepository on sqlite.
type loanRepo struct {
	db *DB
}

// NewLoanRepository constructs the sqlite-backed LoanRepository.
func NewLoanRepository(db *DB) repository.LoanRepository {
	return &loanRepo{db: db}
}

func (r *loanRepo) Create(ctx context.Context, loan *domain.Loan) error {
	tx, err := r.db.BeginImmediate(ctx)
	if err != nil {
		return err
	}
	ctxTx := tx.(*connTx)
	if _, err := ctxTx.exec(ctx, `
		INSERT INTO loans (id, borrower_id, principal, annual_interest_rate, term_weeks,
		                   total_amount, weekly_amount, start_date, status,
		                   created_at, closed_at, version, idempotency_key)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		loan.ID.String(), loan.BorrowerID.String(), loan.Principal, loan.AnnualInterestRate,
		loan.TermWeeks, loan.TotalAmount, loan.WeeklyAmount,
		loan.StartDate.Format(dateFmt), string(loan.Status),
		loan.CreatedAt.UTC().Format(time.RFC3339Nano), nullableTime(loan.ClosedAt), loan.Version,
		loan.IdempotencyKey,
	); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("insert loan: %w", err)
	}
	for _, it := range loan.Installments {
		if _, err := ctxTx.exec(ctx, `
			INSERT INTO installments (id, loan_id, week_number, amount, due_date,
			                          status, paid_at, payment_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			it.ID.String(), loan.ID.String(), it.WeekNumber, it.Amount,
			it.DueDate.Format(dateFmt), string(it.Status),
			nullableTime(it.PaidAt), nullableUUID(it.PaymentID),
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert installment: %w", err)
		}
	}
	return tx.Commit()
}

func (r *loanRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Loan, error) {
	row := r.db.sqlDB.QueryRowContext(ctx, loanSelectSQL, id.String())
	loan, err := scanLoan(row)
	if err != nil {
		return nil, err
	}
	if err := r.loadInstallments(ctx, r.db.sqlDB, loan); err != nil {
		return nil, err
	}
	if err := r.loadPayments(ctx, r.db.sqlDB, loan); err != nil {
		return nil, err
	}
	return loan, nil
}

func (r *loanRepo) GetByIDForUpdate(ctx context.Context, tx repository.Tx, id uuid.UUID) (*domain.Loan, error) {
	ctxTx := tx.(*connTx)
	row := ctxTx.queryRow(ctx, loanSelectSQL, id.String())
	loan, err := scanLoan(row)
	if err != nil {
		return nil, err
	}
	if err := r.loadInstallments(ctx, txQuerier{ctxTx}, loan); err != nil {
		return nil, err
	}
	if err := r.loadPayments(ctx, txQuerier{ctxTx}, loan); err != nil {
		return nil, err
	}
	return loan, nil
}

func (r *loanRepo) Save(ctx context.Context, tx repository.Tx, loan *domain.Loan) error {
	ctxTx := tx.(*connTx)

	// Optimistic version bump: WHERE id=? AND version=loan.Version-1 (the
	// in-memory Version reflects the post-mutation count; we increment in
	// SQL to keep storage authoritative).
	prevVersion := loan.Version - 1
	res, err := ctxTx.exec(ctx, `
		UPDATE loans
		   SET status = ?, closed_at = ?, version = ?
		 WHERE id = ? AND version = ?`,
		string(loan.Status), nullableTime(loan.ClosedAt), loan.Version,
		loan.ID.String(), prevVersion,
	)
	if err != nil {
		return fmt.Errorf("update loan: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return domain.ErrVersionConflict
	}

	// Persist any newly-PAID installments and the payments that paid them.
	for _, it := range loan.Installments {
		if it.Status != domain.InstallmentPaid {
			continue
		}
		// UPDATE only if the row is still PENDING; idempotent for replays.
		if _, err := ctxTx.exec(ctx, `
			UPDATE installments
			   SET status = 'PAID', paid_at = ?, payment_id = ?
			 WHERE id = ? AND status = 'PENDING'`,
			nullableTime(it.PaidAt), nullableUUID(it.PaymentID), it.ID.String(),
		); err != nil {
			return fmt.Errorf("update installment: %w", err)
		}
	}
	for _, p := range loan.Payments {
		// INSERT OR IGNORE — duplicates are fine because of the UNIQUE
		// constraint and our domain replay handling.
		if _, err := ctxTx.exec(ctx, `
			INSERT OR IGNORE INTO payments
			       (id, loan_id, installment_id, amount, idempotency_key, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			p.ID.String(), p.LoanID.String(), p.InstallmentID.String(),
			p.Amount, p.IdempotencyKey, p.CreatedAt.UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("insert payment: %w", err)
		}
	}
	return nil
}

// scanning helpers ----------------------------------------------------

const loanSelectSQL = `
	SELECT id, borrower_id, principal, annual_interest_rate, term_weeks,
	       total_amount, weekly_amount, start_date, status,
	       created_at, closed_at, version, idempotency_key
	  FROM loans
	 WHERE id = ?`

type rowScanner interface{ Scan(dest ...any) error }

func scanLoan(row rowScanner) (*domain.Loan, error) {
	var (
		idStr, borrowerStr, startStr, statusStr, createdStr string
		closedStr, idempKeyStr                              sql.NullString
		l                                                   domain.Loan
	)
	err := row.Scan(
		&idStr, &borrowerStr, &l.Principal, &l.AnnualInterestRate, &l.TermWeeks,
		&l.TotalAmount, &l.WeeklyAmount, &startStr, &statusStr,
		&createdStr, &closedStr, &l.Version, &idempKeyStr,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrLoanNotFound
	}
	if err != nil {
		return nil, err
	}
	l.ID = uuid.MustParse(idStr)
	l.BorrowerID = uuid.MustParse(borrowerStr)
	if l.StartDate, err = time.ParseInLocation(dateFmt, startStr, time.UTC); err != nil {
		return nil, err
	}
	l.Status = domain.LoanStatus(statusStr)
	if l.CreatedAt, err = time.Parse(time.RFC3339Nano, createdStr); err != nil {
		return nil, err
	}
	if closedStr.Valid {
		t, err := time.Parse(time.RFC3339Nano, closedStr.String)
		if err != nil {
			return nil, err
		}
		l.ClosedAt = &t
	}
	if idempKeyStr.Valid {
		l.IdempotencyKey = idempKeyStr.String
	}
	return &l, nil
}

type querier interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
}

type txQuerier struct{ tx *connTx }

func (q txQuerier) QueryContext(ctx context.Context, sql string, args ...any) (*sql.Rows, error) {
	return q.tx.query(ctx, sql, args...)
}

func (r *loanRepo) loadInstallments(ctx context.Context, q querier, loan *domain.Loan) error {
	rows, err := q.QueryContext(ctx, `
		SELECT id, week_number, amount, due_date, status, paid_at, payment_id
		  FROM installments
		 WHERE loan_id = ?
		 ORDER BY week_number`, loan.ID.String())
	if err != nil {
		return err
	}
	defer rows.Close()
	loan.Installments = loan.Installments[:0]
	for rows.Next() {
		var (
			idStr, dueStr, statusStr string
			paidAt, paymentID        sql.NullString
			it                       domain.Installment
		)
		if err := rows.Scan(&idStr, &it.WeekNumber, &it.Amount,
			&dueStr, &statusStr, &paidAt, &paymentID); err != nil {
			return err
		}
		it.ID = uuid.MustParse(idStr)
		it.LoanID = loan.ID
		it.Status = domain.InstallmentStatus(statusStr)
		if it.DueDate, err = time.ParseInLocation(dateFmt, dueStr, time.UTC); err != nil {
			return err
		}
		if paidAt.Valid {
			t, err := time.Parse(time.RFC3339Nano, paidAt.String)
			if err != nil {
				return err
			}
			it.PaidAt = &t
		}
		if paymentID.Valid {
			id := uuid.MustParse(paymentID.String)
			it.PaymentID = &id
		}
		loan.Installments = append(loan.Installments, it)
	}
	return rows.Err()
}

func (r *loanRepo) loadPayments(ctx context.Context, q querier, loan *domain.Loan) error {
	rows, err := q.QueryContext(ctx, `
		SELECT id, loan_id, installment_id, amount, idempotency_key, created_at
		  FROM payments
		 WHERE loan_id = ?
		 ORDER BY created_at, id`, loan.ID.String())
	if err != nil {
		return err
	}
	defer rows.Close()
	loan.Payments = loan.Payments[:0]
	for rows.Next() {
		var idStr, loanStr, instStr, key, createdStr string
		var p domain.Payment
		if err := rows.Scan(&idStr, &loanStr, &instStr, &p.Amount, &key, &createdStr); err != nil {
			return err
		}
		p.ID = uuid.MustParse(idStr)
		p.LoanID = uuid.MustParse(loanStr)
		p.InstallmentID = uuid.MustParse(instStr)
		p.IdempotencyKey = key
		if p.CreatedAt, err = time.Parse(time.RFC3339Nano, createdStr); err != nil {
			return err
		}
		loan.Payments = append(loan.Payments, p)
	}
	return rows.Err()
}

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func nullableUUID(id *uuid.UUID) any {
	if id == nil {
		return nil
	}
	return id.String()
}

const loanByKeySQL = `
	SELECT id, borrower_id, principal, annual_interest_rate, term_weeks,
	       total_amount, weekly_amount, start_date, status,
	       created_at, closed_at, version, idempotency_key
	  FROM loans
	 WHERE idempotency_key = ?`

func (r *loanRepo) GetByIdempotencyKey(ctx context.Context, key string) (*domain.Loan, error) {
	row := r.db.sqlDB.QueryRowContext(ctx, loanByKeySQL, key)
	loan, err := scanLoan(row)
	if err != nil {
		if errors.Is(err, domain.ErrLoanNotFound) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	if err := r.loadInstallments(ctx, r.db.sqlDB, loan); err != nil {
		return nil, err
	}
	if err := r.loadPayments(ctx, r.db.sqlDB, loan); err != nil {
		return nil, err
	}
	return loan, nil
}
