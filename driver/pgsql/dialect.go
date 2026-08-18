package pgsql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// database and transaction keep the storage implementation's SQL readable
// while translating its driver-neutral '?' placeholders to PostgreSQL's $N
// placeholders at the database boundary.
type database struct{ *sql.DB }

type transaction struct{ *sql.Tx }

func (d *database) Exec(query string, args ...any) (sql.Result, error) {
	return d.DB.Exec(bind(query), args...)
}

func (d *database) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return d.DB.ExecContext(ctx, bind(query), args...)
}

func (d *database) Query(query string, args ...any) (*sql.Rows, error) {
	return d.DB.Query(bind(query), args...)
}

func (d *database) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return d.DB.QueryContext(ctx, bind(query), args...)
}

func (d *database) QueryRow(query string, args ...any) *sql.Row {
	return d.DB.QueryRow(bind(query), args...)
}

func (d *database) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return d.DB.QueryRowContext(ctx, bind(query), args...)
}

func (d *database) BeginTx(ctx context.Context, opts *sql.TxOptions) (*transaction, error) {
	tx, err := d.DB.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &transaction{Tx: tx}, nil
}

func (t *transaction) Exec(query string, args ...any) (sql.Result, error) {
	return t.Tx.Exec(bind(query), args...)
}

func (t *transaction) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.Tx.ExecContext(ctx, bind(query), args...)
}

func (t *transaction) Query(query string, args ...any) (*sql.Rows, error) {
	return t.Tx.Query(bind(query), args...)
}

func (t *transaction) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return t.Tx.QueryContext(ctx, bind(query), args...)
}

func (t *transaction) QueryRow(query string, args ...any) *sql.Row {
	return t.Tx.QueryRow(bind(query), args...)
}

func (t *transaction) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return t.Tx.QueryRowContext(ctx, bind(query), args...)
}

// bind converts unquoted '?' markers to PostgreSQL positional parameters.
func bind(query string) string {
	var out strings.Builder
	out.Grow(len(query) + 8)
	index := 1
	var quote byte
	for i := 0; i < len(query); i++ {
		ch := query[i]
		if quote != 0 {
			out.WriteByte(ch)
			if ch == quote {
				if i+1 < len(query) && query[i+1] == quote {
					out.WriteByte(query[i+1])
					i++
					continue
				}
				quote = 0
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			out.WriteByte(ch)
			continue
		}
		if ch == '?' {
			out.WriteString(fmt.Sprintf("$%d", index))
			index++
			continue
		}
		out.WriteByte(ch)
	}
	return out.String()
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func retryablePostgresError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	switch pgErr.Code {
	case "40001", // serialization_failure
		"40P01", // deadlock_detected
		"55P03": // lock_not_available
		return true
	default:
		return false
	}
}
