package pgsql

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kelindar/storage"
	"github.com/zeebo/xxh3"
)

const (
	changeCreate    = "create"
	changeUpdate    = "update"
	changeDelete    = "delete"
	changeMinTime   = int64(-1 << 63)
	changeStartSeq  = int64(-1)
	changeBatchSize = 256
)

var changePollInterval = time.Second

type changeCursor struct {
	createdAt int64
	seq       int64
}

var changeBuffers = sync.Pool{
	New: func() any {
		return make([]storage.Change, 0, changeBatchSize)
	},
}

func (s *rds) recordChange(ctx context.Context, tx *transaction, urn storage.URN, action string, at time.Time) error {
	code, err := changeActionCode(action)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO change_log(created_at, kind_hash, action, urn) VALUES (?, ?, ?, ?)`,
		at.UnixNano(), int64(xxh3.HashString(urn.Kind.String())), code, urn.String(),
	); err != nil {
		return fmt.Errorf("storage: record %s change: %w", action, err)
	}
	return nil
}

// Changes consumes a bounded, at-least-once wake-up feed for kind with a
// persistent cursor for consumer. Batches are borrowed until the callback
// returns and may be retried. Pruned history is intentionally not recovered.
func (s *rds) Changes(ctx context.Context, consumer string, kind storage.Kind, after time.Time, handle func(context.Context, []storage.Change) error) error {
	kind = storage.Kind(kind.String())
	cursor, err := s.loadChangeCursorWithRetry(ctx, consumer, kind, after)
	if err != nil {
		return ignoreChangeCancellation(ctx, err)
	}

	for {
		next, err := s.consumeChangeBatch(ctx, consumer, kind, cursor, handle)
		if err != nil {
			return ignoreChangeCancellation(ctx, err)
		}
		if next == cursor {
			if !waitForChanges(ctx) {
				return nil
			}
			continue
		}
		cursor = next
	}
}

func (s *rds) loadChangeCursorWithRetry(ctx context.Context, consumer string, kind storage.Kind, after time.Time) (changeCursor, error) {
	var cursor changeCursor
	err := retryChangeOperation(ctx, func() error {
		var err error
		cursor, err = s.loadChangeCursor(ctx, consumer, kind, after)
		return err
	})
	return cursor, err
}

func (s *rds) consumeChangeBatch(ctx context.Context, consumer string, kind storage.Kind, cursor changeCursor, handle func(context.Context, []storage.Change) error) (changeCursor, error) {
	batch, next := []storage.Change(nil), cursor
	defer func() { releaseChanges(batch) }()

	if err := retryChangeOperation(ctx, func() error {
		var err error
		batch, next, err = s.readChanges(ctx, kind, cursor)
		return err
	}); err != nil {
		return cursor, err
	}
	if ctx.Err() != nil {
		return cursor, ctx.Err()
	}
	if next == cursor {
		return cursor, nil
	}
	if len(batch) > 0 {
		if err := deliverChanges(ctx, handle, batch); err != nil {
			return cursor, err
		}
	}
	releaseChanges(batch)
	batch = nil
	if err := s.saveChangeCursor(ctx, consumer, kind, next); err != nil {
		return cursor, err
	}
	return next, nil
}

func (s *rds) loadChangeCursor(ctx context.Context, consumer string, kind storage.Kind, after time.Time) (changeCursor, error) {
	createdAt := changeMinTime
	if !after.IsZero() {
		createdAt = after.UnixNano()
	}
	key := string(kind)
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO change_cursors(consumer, kind, created_at, seq) VALUES (?, ?, ?, ?)`+
			` ON CONFLICT (consumer, kind) DO NOTHING`,
		consumer, key, createdAt, changeStartSeq,
	); err != nil {
		return changeCursor{}, fmt.Errorf("storage: initialize change cursor %q/%s: %w", consumer, kind, err)
	}

	var cursor changeCursor
	if err := s.db.QueryRowContext(ctx,
		`SELECT created_at, seq FROM change_cursors WHERE consumer = ? AND kind = ?`,
		consumer, key,
	).Scan(&cursor.createdAt, &cursor.seq); err != nil {
		return changeCursor{}, fmt.Errorf("storage: read change cursor %q/%s: %w", consumer, kind, err)
	}
	return cursor, nil
}

func (s *rds) saveChangeCursor(ctx context.Context, consumer string, kind storage.Kind, cursor changeCursor) error {
	return retryChangeOperation(ctx, func() error {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE change_cursors SET created_at = ?, seq = ?`+
				` WHERE consumer = ? AND kind = ?`+
				` AND (created_at < ? OR (created_at = ? AND seq < ?))`,
			cursor.createdAt, cursor.seq, consumer, string(kind),
			cursor.createdAt, cursor.createdAt, cursor.seq,
		); err != nil {
			return fmt.Errorf("storage: save change cursor %q/%s: %w", consumer, kind, err)
		}
		return nil
	})
}

func (s *rds) readChanges(ctx context.Context, kind storage.Kind, cursor changeCursor) ([]storage.Change, changeCursor, error) {
	hash, pattern, err := changeFilter(kind)
	if err != nil {
		return nil, changeCursor{}, err
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT seq, urn, action, created_at FROM change_log`+
			` WHERE kind_hash = ? AND urn LIKE ? ESCAPE '\'`+
			` AND (created_at > ? OR (created_at = ? AND seq > ?))`+
			` ORDER BY created_at, seq LIMIT ?`,
		hash, pattern, cursor.createdAt, cursor.createdAt, cursor.seq, changeBatchSize,
	)
	if err != nil {
		return nil, changeCursor{}, fmt.Errorf("storage: query changes %s: %w", kind, err)
	}
	defer rows.Close()

	out := changeBuffers.Get().([]storage.Change)[:0]
	next := cursor
	for rows.Next() {
		var rawURN string
		var action, createdAt, seq int64
		if err := rows.Scan(&seq, &rawURN, &action, &createdAt); err != nil {
			releaseChanges(out)
			return nil, changeCursor{}, fmt.Errorf("storage: read changes %s: %w", kind, err)
		}
		next = changeCursor{createdAt: createdAt, seq: seq}
		urn, err := storage.ParseURN(rawURN)
		if err != nil {
			releaseChanges(out)
			return nil, changeCursor{}, fmt.Errorf("storage: parse change URN: %w", err)
		}
		if urn.Kind != kind {
			continue
		}
		name, ok := changeActionName(action)
		if !ok {
			releaseChanges(out)
			return nil, changeCursor{}, fmt.Errorf("storage: invalid change action %d", action)
		}
		out = append(out, storage.Change{URN: urn, Action: name, At: time.Unix(0, createdAt).UTC()})
	}
	if err := rows.Err(); err != nil {
		releaseChanges(out)
		return nil, changeCursor{}, fmt.Errorf("storage: read changes %s: %w", kind, err)
	}
	return out, next, nil
}

func waitForChanges(ctx context.Context) bool {
	timer := time.NewTimer(changePollInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func retryChangeOperation(ctx context.Context, operation func() error) error {
	for {
		err := operation()
		switch {
		case err == nil:
			return nil
		case ctx.Err() != nil:
			return ctx.Err()
		case !retryableChangeError(err):
			return err
		case !waitForChanges(ctx):
			return ctx.Err()
		}
	}
}

func deliverChanges(ctx context.Context, handle func(context.Context, []storage.Change) error, batch []storage.Change) error {
	for {
		if err := handle(ctx, batch); err == nil {
			return nil
		}
		if !waitForChanges(ctx) {
			return ctx.Err()
		}
	}
}

func ignoreChangeCancellation(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func retryableChangeError(err error) bool {
	return retryablePostgresError(err)
}

// PruneChanges removes change rows older than before. It is an internal
// maintenance hook used by storage.Store's existing sweeper.
func (s *rds) PruneChanges(ctx context.Context, before time.Time) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM change_log WHERE created_at < ?`, before.UnixNano()); err != nil {
		return fmt.Errorf("storage: prune changes: %w", err)
	}
	return nil
}

func changeFilter(kind storage.Kind) (int64, string, error) {
	if kind == "" {
		return 0, "", fmt.Errorf("%w: kind is required", storage.ErrInvalid)
	}
	return int64(xxh3.HashString(string(kind))), kindPattern(kind), nil
}

func kindPattern(kind storage.Kind) string {
	value := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(string(kind))
	return "urn:%:%:" + value + ":%"
}

func changeActionCode(action string) (int64, error) {
	switch action {
	case changeCreate:
		return 1, nil
	case changeUpdate:
		return 2, nil
	case changeDelete:
		return 3, nil
	default:
		return 0, fmt.Errorf("%w: unsupported change action %q", storage.ErrInvalid, action)
	}
}

func changeActionName(action int64) (string, bool) {
	switch action {
	case 1:
		return changeCreate, true
	case 2:
		return changeUpdate, true
	case 3:
		return changeDelete, true
	default:
		return "", false
	}
}

func releaseChanges(changes []storage.Change) {
	if changes == nil {
		return
	}
	clear(changes[:cap(changes)])
	changeBuffers.Put(changes[:0])
}
