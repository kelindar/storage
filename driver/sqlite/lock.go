package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/kelindar/async"
	"github.com/kelindar/storage"
)

const databaseMillis = `CAST(unixepoch('subsec') * 1000 AS INTEGER)`

type leaser struct {
	db     *sql.DB
	life   context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	active sync.WaitGroup
	timing lockTiming
}

type lockTiming struct {
	ttl            time.Duration
	renewEvery     time.Duration
	renewRetry     time.Duration
	loseAfter      time.Duration
	acquireRetry   time.Duration
	releaseTimeout time.Duration
}

var defaultLockTiming = lockTiming{
	ttl:            time.Minute,
	renewEvery:     20 * time.Second,
	renewRetry:     time.Second,
	loseAfter:      40 * time.Second,
	acquireRetry:   50 * time.Millisecond,
	releaseTimeout: 5 * time.Second,
}

// Lock acquires a named advisory lease or waits until ctx is canceled.
func (s *rds) Lock(ctx context.Context, name string) (context.Context, context.CancelFunc, error) {
	return s.leases.lock(ctx, name)
}

func (l *leaser) lock(ctx context.Context, name string) (context.Context, context.CancelFunc, error) {
	if name == "" {
		return nil, nil, fmt.Errorf("%w: lock name is required", storage.ErrInvalid)
	}

	lockCtx, cancel := context.WithCancelCause(ctx)
	stopClose := context.AfterFunc(l.life, func() {
		cancel(context.Canceled)
	})
	if !l.register() {
		stopClose()
		cancel(context.Canceled)
		return nil, nil, context.Canceled
	}
	registered := true
	defer func() {
		if registered {
			l.active.Done()
		}
	}()

	owner := rand.Text()
	for {
		acquired, err := l.try(lockCtx, name, owner)
		var task async.Task[struct{}]
		if err == nil && acquired {
			task = l.start(lockCtx, name, owner, cancel, stopClose)
			registered = false
		}
		switch {
		case err != nil:
			stopClose()
			cancel(err)
			return nil, nil, err
		case acquired:
			return lockCtx, func() {
				cancel(context.Canceled)
				_ = task.Wait()
			}, nil
		}

		timer := time.NewTimer(l.timing.acquireRetry)
		select {
		case <-lockCtx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			stopClose()
			return nil, nil, context.Cause(lockCtx)
		case <-timer.C:
		}
	}
}

func (l *leaser) register() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.life.Err() != nil {
		return false
	}
	l.active.Add(1)
	return true
}

func (l *leaser) start(ctx context.Context, name, owner string, cancel context.CancelCauseFunc, stopClose func() bool) async.Task[struct{}] {
	return async.Invoke(context.WithoutCancel(ctx), func(context.Context) (struct{}, error) {
		defer l.active.Done()
		defer stopClose()
		l.renewLoop(ctx, name, owner, cancel)
		return struct{}{}, nil
	})
}

func (l *leaser) try(ctx context.Context, name, owner string) (bool, error) {
	var acquired string
	err := l.db.QueryRowContext(ctx, `
		INSERT INTO locks(name, owner, expires_at)
		VALUES (?, ?, `+databaseMillis+` + ?)
		ON CONFLICT(name) DO UPDATE SET
			owner = excluded.owner,
			expires_at = excluded.expires_at
		WHERE locks.expires_at <= `+databaseMillis+`
		RETURNING owner`,
		name, owner, l.timing.ttl.Milliseconds(),
	).Scan(&acquired)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil && ctx.Err() != nil:
		return false, context.Cause(ctx)
	case err != nil:
		return false, fmt.Errorf("storage: acquire lock %q: %w", name, err)
	default:
		return acquired == owner, nil
	}
}

func (l *leaser) renewLoop(ctx context.Context, name, owner string, cancel context.CancelCauseFunc) {
	lastSuccess := time.Now()
	timer := time.NewTimer(l.timing.renewEvery)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			l.release(name, owner)
			return
		case <-timer.C:
		}

		renewCtx, stop := context.WithTimeout(ctx, max(0, l.timing.loseAfter-time.Since(lastSuccess)))
		owned, err := l.renew(renewCtx, name, owner)
		stop()
		switch {
		case err == nil && owned:
			lastSuccess = time.Now()
			timer.Reset(l.timing.renewEvery)
		case err == nil:
			cancel(storage.ErrLockLost)
			l.release(name, owner)
			return
		case time.Since(lastSuccess) >= l.timing.loseAfter:
			cancel(fmt.Errorf("%w: renew %q: %v", storage.ErrLockLost, name, err))
			l.release(name, owner)
			return
		default:
			timer.Reset(l.timing.renewRetry)
		}
	}
}

func (l *leaser) renew(ctx context.Context, name, owner string) (bool, error) {
	result, err := l.db.ExecContext(ctx, `
		UPDATE locks
		SET expires_at = `+databaseMillis+` + ?
		WHERE name = ? AND owner = ? AND expires_at > `+databaseMillis,
		l.timing.ttl.Milliseconds(), name, owner,
	)
	if err != nil {
		return false, err
	}
	updated, err := result.RowsAffected()
	return updated == 1, err
}

func (l *leaser) release(name, owner string) {
	ctx, cancel := context.WithTimeout(context.Background(), l.timing.releaseTimeout)
	defer cancel()
	_, _ = l.db.ExecContext(ctx, `DELETE FROM locks WHERE name = ? AND owner = ?`, name, owner)
}

// shutdown cancels the storage lifetime once and waits for active leases.
// It reports whether the caller should close the database.
func (l *leaser) shutdown() bool {
	l.mu.Lock()
	if l.life.Err() != nil {
		l.mu.Unlock()
		return false
	}
	l.cancel()
	l.mu.Unlock()
	l.active.Wait()
	return true
}
