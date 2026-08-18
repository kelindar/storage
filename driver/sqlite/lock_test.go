package sqlite

import (
	"context"
	"crypto/rand"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/kelindar/async"
	"github.com/kelindar/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLock(t *testing.T) {
	t.Run("excludes contenders until release", func(t *testing.T) {
		first, second := openLockStores(t)
		_, unlock, err := first.Lock(t.Context(), "shared")
		require.NoError(t, err)

		acquired := make(chan context.CancelFunc, 1)
		go func() {
			_, next, _ := second.Lock(t.Context(), "shared")
			acquired <- next
		}()
		select {
		case <-acquired:
			t.Fatal("contender acquired a held lock")
		case <-time.After(25 * time.Millisecond):
		}

		unlock()
		select {
		case next := <-acquired:
			require.NotNil(t, next)
			next()
		case <-time.After(time.Second):
			t.Fatal("contender did not acquire released lock")
		}
	})

	t.Run("supports cancellation and independent names", func(t *testing.T) {
		first, second := openLockStores(t)
		_, unlock, err := first.Lock(t.Context(), "shared")
		require.NoError(t, err)
		defer unlock()

		_, other, err := second.Lock(t.Context(), "other")
		require.NoError(t, err)
		other()

		ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
		defer cancel()
		_, _, err = second.Lock(ctx, "shared")
		assert.ErrorIs(t, err, context.DeadlineExceeded)
		assert.False(t, storage.IsLockLost(err))
	})

	t.Run("rejects empty names", func(t *testing.T) {
		first, _ := openLockStores(t)
		_, _, err := first.Lock(t.Context(), "")
		assert.ErrorIs(t, err, storage.ErrInvalid)
	})

	t.Run("renews automatically", func(t *testing.T) {
		first, second := openLockStores(t)
		first.leases.timing.ttl = time.Second
		first.leases.timing.renewEvery = 50 * time.Millisecond
		first.leases.timing.loseAfter = 2 * time.Second
		second.leases.timing = first.leases.timing
		_, unlock, err := first.Lock(t.Context(), "renewed")
		require.NoError(t, err)
		defer unlock()

		original, err := lockExpiresAt(first, "renewed")
		require.NoError(t, err)

		var expires int64
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			expires, err = lockExpiresAt(first, "renewed")
			require.NoError(t, err)
			if expires > original {
				break
			}
			time.Sleep(first.leases.timing.renewEvery / 2)
		}
		assert.Greater(t, expires, original)

		ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
		defer cancel()
		_, _, err = second.Lock(ctx, "renewed")
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	})

	t.Run("stale owner cannot release successor", func(t *testing.T) {
		first, second := openLockStores(t)
		first.leases.timing.renewEvery = time.Hour
		_, old, err := first.Lock(t.Context(), "takeover")
		require.NoError(t, err)
		require.NoError(t, expireLock(second, "takeover"))

		_, current, err := second.Lock(t.Context(), "takeover")
		require.NoError(t, err)
		old()
		defer current()

		ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
		defer cancel()
		_, _, err = first.Lock(ctx, "takeover")
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	})

	t.Run("reports lost ownership", func(t *testing.T) {
		first, second := openLockStores(t)
		lock, unlock, err := first.Lock(t.Context(), "lost")
		require.NoError(t, err)
		require.NoError(t, expireLock(second, "lost"))

		_, next, err := second.Lock(t.Context(), "lost")
		require.NoError(t, err)
		defer next()

		select {
		case <-lock.Done():
			assert.True(t, storage.IsLockLost(context.Cause(lock)))
			assert.True(t, storage.IsLockLost(errors.Join(errors.New("wrapped"), context.Cause(lock))))
		case <-time.After(time.Second):
			t.Fatal("stale owner did not observe lock loss")
		}
		unlock()
	})

	t.Run("reports loss while renewal is blocked", func(t *testing.T) {
		first, _ := openLockStores(t)
		lock, unlock, err := first.Lock(t.Context(), "blocked")
		require.NoError(t, err)
		defer unlock()

		conn, err := first.db.Conn(t.Context())
		require.NoError(t, err)
		defer conn.Close()

		select {
		case <-lock.Done():
			assert.True(t, storage.IsLockLost(context.Cause(lock)))
		case <-time.After(time.Second):
			t.Fatal("blocked renewal exceeded the ownership window")
		}
	})

	t.Run("close drains active leases", func(t *testing.T) {
		first, _ := openLockStores(t)
		lock, unlock, err := first.Lock(t.Context(), "closing")
		require.NoError(t, err)
		require.NoError(t, first.Close())

		select {
		case <-lock.Done():
			assert.ErrorIs(t, context.Cause(lock), context.Canceled)
		default:
			t.Fatal("storage close did not cancel active lock")
		}
		unlock()
	})

	t.Run("close waits for acquired lease registration", func(t *testing.T) {
		first, second := openLockStores(t)
		lock, cancel := context.WithCancelCause(t.Context())
		stopClose := context.AfterFunc(first.leases.life, func() { cancel(context.Canceled) })
		owner := rand.Text()

		require.True(t, first.leases.register())
		acquired, err := first.leases.try(lock, "closing-acquire", owner)
		require.NoError(t, err)
		require.True(t, acquired)

		closed := make(chan error, 1)
		closer := async.Invoke(t.Context(), func(context.Context) (struct{}, error) {
			err := first.Close()
			closed <- err
			return struct{}{}, err
		})
		select {
		case <-closed:
			t.Fatal("storage close escaped the acquisition barrier")
		case <-time.After(20 * time.Millisecond):
		}

		first.leases.start(lock, "closing-acquire", owner, cancel, stopClose)
		require.NoError(t, <-closed)
		require.NoError(t, closer.Wait())

		ctx, stop := context.WithTimeout(t.Context(), time.Second)
		defer stop()
		_, unlock, err := second.Lock(ctx, "closing-acquire")
		require.NoError(t, err)
		unlock()
	})
}

func openLockStores(t *testing.T) (*rds, *rds) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "locks.db")
	registry := storage.NewRegistry()
	firstStore, err := Open(path, registry)
	require.NoError(t, err)
	secondStore, err := Open(path, registry)
	require.NoError(t, err)
	first := firstStore.(*rds)
	second := secondStore.(*rds)
	timing := lockTiming{
		ttl:            150 * time.Millisecond,
		renewEvery:     40 * time.Millisecond,
		renewRetry:     5 * time.Millisecond,
		loseAfter:      100 * time.Millisecond,
		acquireRetry:   5 * time.Millisecond,
		releaseTimeout: 100 * time.Millisecond,
	}
	first.leases.timing = timing
	second.leases.timing = timing
	t.Cleanup(func() {
		require.NoError(t, first.Close())
		require.NoError(t, second.Close())
	})
	return first, second
}

func expireLock(store *rds, name string) error {
	_, err := store.db.Exec(`UPDATE locks SET expires_at = 0 WHERE name = ?`, name)
	return err
}

func lockExpiresAt(store *rds, name string) (int64, error) {
	var expiresAt int64
	err := store.db.QueryRow(`SELECT expires_at FROM locks WHERE name = ?`, name).Scan(&expiresAt)
	return expiresAt, err
}
