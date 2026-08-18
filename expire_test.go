package storage

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type expiringObject struct {
	Meta `kind:"expiring" json:",inline"`
}

type expirationBackend struct {
	Storage
	registry   Registry
	urns       []URN
	limit      int
	expiredErr error
	pruneErr   error
	closed     atomic.Bool
	closes     atomic.Int32
	closeErr   error
}

type expirationBlockedError struct{ error }

func (expirationBlockedError) Blocked() bool { return true }

func (s *expirationBackend) Registry() Registry {
	return s.registry
}

func (s *expirationBackend) Expired(_ context.Context, _ Kind, _ int64, limit int) iter.Seq2[URN, error] {
	s.limit = limit
	return func(yield func(URN, error) bool) {
		for _, urn := range s.urns {
			if !yield(urn, nil) {
				return
			}
		}
		if s.expiredErr != nil {
			yield(URN{}, s.expiredErr)
		}
	}
}

func (s *expirationBackend) PruneChanges(context.Context, time.Time) error {
	return s.pruneErr
}

func (s *expirationBackend) Close() error {
	s.closed.Store(true)
	s.closes.Add(1)
	return s.closeErr
}

func TestExpiration(t *testing.T) {
	t.Run("summary", func(t *testing.T) {
		registry := NewRegistry()
		_, err := Register[*expiringObject](registry)
		require.NoError(t, err)
		backend := &expirationBackend{registry: registry}
		for _, id := range []string{"deleted", "missing", "blocked", "bundle-root", "failed"} {
			backend.urns = append(backend.urns, URN{Tenant: "acme", Namespace: "system", Kind: "expiring", ID: id})
		}
		store := NewStore(backend, nil)
		summary := store.expire(t.Context(), time.Now(), func(_ context.Context, urn URN) error {
			switch urn.ID {
			case "deleted":
				return nil
			case "missing":
				return fmt.Errorf("%w: raced", ErrNotFound)
			case "blocked":
				return expirationBlockedError{errors.New("blocked")}
			case "bundle-root":
				return fmt.Errorf("wrapped: %w", expirationBlockedError{errors.New("blocked")})
			default:
				return assert.AnError
			}
		})

		assert.Equal(t, expirationPageSize, backend.limit)
		assert.Equal(t, expirationSummary{Attempted: 5, Deleted: 1, Missing: 1, Blocked: 2, Failed: 1}, summary)
		assert.Equal(t, summary.Attempted, summary.Deleted+summary.Missing+summary.Blocked+summary.Failed)
	})

	t.Run("continues past the first page", func(t *testing.T) {
		registry := NewRegistry()
		_, err := Register[*expiringObject](registry)
		require.NoError(t, err)
		backend := &expirationBackend{registry: registry}
		for i := range expirationPageSize + 50 {
			backend.urns = append(backend.urns, URN{
				Tenant: "acme", Namespace: "system", Kind: "expiring",
				ID: fmt.Sprintf("%020d", i),
			})
		}
		store := NewStore(backend, nil)
		var deleted atomic.Int32
		summary := store.expire(t.Context(), time.Now(), func(context.Context, URN) error {
			deleted.Add(1)
			return nil
		})
		assert.Equal(t, expirationPageSize+50, summary.Attempted)
		assert.Equal(t, expirationPageSize+50, summary.Deleted)
		assert.Equal(t, int32(expirationPageSize+50), deleted.Load())
	})

	t.Run("delay is bounded", func(t *testing.T) {
		for range 100 {
			delay := expirationDelay()
			assert.GreaterOrEqual(t, delay, expirationInterval)
			assert.Less(t, delay, 2*expirationInterval)
		}
	})

	t.Run("close cancels background work before storage", func(t *testing.T) {
		registry := NewRegistry()
		_, err := Register[*expiringObject](registry)
		require.NoError(t, err)
		backend := &expirationBackend{registry: registry}
		store := NewStore(backend, nil)
		store.Start(context.Background(), func(context.Context, URN) error { return nil })
		require.NoError(t, store.Close())
		assert.True(t, backend.closed.Load())
	})

	t.Run("start and close are concurrent-safe", func(t *testing.T) {
		registry := NewRegistry()
		_, err := Register[*expiringObject](registry)
		require.NoError(t, err)
		backend := &expirationBackend{registry: registry}
		store := NewStore(backend, nil)
		var wg sync.WaitGroup
		for range 10 {
			wg.Go(func() {
				store.Start(context.Background(), func(context.Context, URN) error { return nil })
			})
			wg.Go(func() {
				_ = store.Close()
			})
		}
		wg.Wait()
		require.NoError(t, store.Close())
		assert.Equal(t, int32(1), backend.closes.Load())
	})

	t.Run("close preserves its error", func(t *testing.T) {
		backend := &expirationBackend{closeErr: assert.AnError}
		store := NewStore(backend, nil)
		require.ErrorIs(t, store.Close(), assert.AnError)
		require.ErrorIs(t, store.Close(), assert.AnError)
		assert.Equal(t, int32(1), backend.closes.Load())
	})

	t.Run("retriesAfterFailure", func(t *testing.T) {
		registry := NewRegistry()
		_, err := Register[*expiringObject](registry)
		require.NoError(t, err)
		urn := URN{Tenant: "acme", Namespace: "system", Kind: "expiring", ID: "retry"}
		backend := &expirationBackend{registry: registry, urns: []URN{urn}}
		store := NewStore(backend, nil)
		var attempts atomic.Int32
		deleteResource := func(context.Context, URN) error {
			if attempts.Add(1) == 1 {
				return assert.AnError
			}
			return nil
		}
		now := time.Now()
		summary := store.expire(t.Context(), now, deleteResource)
		assert.Equal(t, expirationSummary{Attempted: 1, Failed: 1}, summary)

		summary = store.expire(t.Context(), now, deleteResource)
		assert.Equal(t, expirationSummary{Attempted: 1, Deleted: 1}, summary)
		assert.Equal(t, int32(2), attempts.Load())
	})

	t.Run("queryAndContextGuards", func(t *testing.T) {
		registry := NewRegistry()
		_, err := Register[*expiringObject](registry)
		require.NoError(t, err)
		backend := &expirationBackend{
			registry:   registry,
			urns:       []URN{{Tenant: "acme", Namespace: "system", Kind: "expiring", ID: "one"}},
			expiredErr: assert.AnError,
			pruneErr:   assert.AnError,
		}
		store := NewStore(backend, nil)
		summary := store.expire(t.Context(), time.Now(), func(context.Context, URN) error { return nil })
		assert.Equal(t, expirationSummary{Attempted: 1, Deleted: 1}, summary)

		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		backend.expiredErr = nil
		summary = store.expire(ctx, time.Now(), func(context.Context, URN) error { return nil })
		assert.Zero(t, summary.Attempted)
		assert.Zero(t, store.expire(t.Context(), time.Now(), nil))
	})
}
