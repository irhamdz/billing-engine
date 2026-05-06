package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/irhamdz/billing-engine/internal/domain"
	"github.com/irhamdz/billing-engine/internal/repository"
)

type paymentRepo struct{ db *DB }

// NewPaymentRepository constructs the sqlite-backed PaymentRepository.
func NewPaymentRepository(db *DB) repository.PaymentRepository {
	return &paymentRepo{db: db}
}

func (r *paymentRepo) GetByIdempotencyKey(ctx context.Context, loanID uuid.UUID, key string) (*domain.Payment, error) {
	row := r.db.sqlDB.QueryRowContext(ctx, `
		SELECT id, loan_id, installment_id, amount, idempotency_key, created_at
		  FROM payments
		 WHERE loan_id = ? AND idempotency_key = ?`,
		loanID.String(), key)
	var idStr, loanStr, instStr, ikey, createdStr string
	var p domain.Payment
	err := row.Scan(&idStr, &loanStr, &instStr, &p.Amount, &ikey, &createdStr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p.ID = uuid.MustParse(idStr)
	p.LoanID = uuid.MustParse(loanStr)
	p.InstallmentID = uuid.MustParse(instStr)
	p.IdempotencyKey = ikey
	p.CreatedAt, err = time.Parse(time.RFC3339Nano, createdStr)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *paymentRepo) ListByLoan(ctx context.Context, loanID uuid.UUID) ([]domain.Payment, error) {
	rows, err := r.db.sqlDB.QueryContext(ctx, `
		SELECT id, loan_id, installment_id, amount, idempotency_key, created_at
		  FROM payments
		 WHERE loan_id = ?
		 ORDER BY created_at, id`, loanID.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Payment
	for rows.Next() {
		var idStr, loanStr, instStr, ikey, createdStr string
		var p domain.Payment
		if err := rows.Scan(&idStr, &loanStr, &instStr, &p.Amount, &ikey, &createdStr); err != nil {
			return nil, err
		}
		p.ID = uuid.MustParse(idStr)
		p.LoanID = uuid.MustParse(loanStr)
		p.InstallmentID = uuid.MustParse(instStr)
		p.IdempotencyKey = ikey
		if p.CreatedAt, err = time.Parse(time.RFC3339Nano, createdStr); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
