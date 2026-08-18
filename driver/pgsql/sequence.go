package pgsql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"

	"github.com/kelindar/storage"
)

const maxSequence = int64(math.MaxUint32)

// Next atomically advances the named sequence and returns its new value.
// The first value for a name is 1. Values increase through MaxUint32, then wrap to 0.
// Corrupted out-of-range stored values also wrap to 0. Only an empty name or a
// storage failure returns an error.
func (s *rds) Next(ctx context.Context, name string) (uint32, error) {
	if name == "" {
		return 0, fmt.Errorf("%w: sequence name is required", storage.ErrInvalid)
	}

	var value int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO sequences(name, value) VALUES (?, 1)
		ON CONFLICT(name) DO UPDATE SET value = CASE
			WHEN value < 0 OR value >= ? THEN 0
			ELSE value + 1
		END
		RETURNING value`, name, maxSequence).Scan(&value)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, fmt.Errorf("storage: sequence %q returned no value", name)
	case err != nil:
		return 0, fmt.Errorf("storage: sequence %q: %w", name, err)
	default:
		return uint32(value), nil
	}
}
