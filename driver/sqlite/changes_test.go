package sqlite

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kelindar/storage"
	sqlite3 "github.com/ncruces/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zeebo/xxh3"
)

func TestChanges(t *testing.T) {
	t.Run("records CRUD in order", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			app, err := storage.New[*App]("acme", "system")
			require.NoError(t, err)
			created, err := db.Insert(t.Context(), app)
			require.NoError(t, err)
			updated, err := db.Update(t.Context(), created)
			require.NoError(t, err)
			_, err = db.Delete(t.Context(), updated.URN())
			require.NoError(t, err)

			changes := consumeOnce(t, db, "crud", storage.Kind("app"), time.Time{})
			require.Len(t, changes, 3)
			assert.Equal(t, []string{changeCreate, changeUpdate, changeDelete}, []string{
				changes[0].Action, changes[1].Action, changes[2].Action,
			})
			assertOrderedChanges(t, changes, app.URN())

			after := changes[1].At
			fromAfter := consumeOnce(t, db, "after", storage.Kind("app"), after)
			require.Len(t, fromAfter, 2)
			assert.Equal(t, []string{changeUpdate, changeDelete}, []string{fromAfter[0].Action, fromAfter[1].Action})
		})
	})

	t.Run("does not record a missing delete", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			app, err := storage.New[*App]("acme", "system")
			require.NoError(t, err)
			stored, err := db.Insert(t.Context(), app)
			require.NoError(t, err)
			_, err = db.Delete(t.Context(), stored.URN())
			require.NoError(t, err)
			_, err = db.Delete(t.Context(), stored.URN())
			require.ErrorIs(t, err, storage.ErrNotFound)

			changes := consumeOnce(t, db, "missing-delete", storage.Kind("app"), time.Time{})
			require.Len(t, changes, 2)
			assert.Equal(t, []string{changeCreate, changeDelete}, []string{changes[0].Action, changes[1].Action})
		})
	})

	t.Run("does not deduplicate URNs", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			app, err := storage.New[*App]("acme", "system")
			require.NoError(t, err)
			stored, err := db.Insert(t.Context(), app)
			require.NoError(t, err)
			_, err = db.Update(t.Context(), stored)
			require.NoError(t, err)

			changes := consumeOnce(t, db, "duplicates", storage.Kind("app"), time.Time{})
			require.Len(t, changes, 2)
			assert.Equal(t, changes[0].URN, changes[1].URN)
		})
	})

	t.Run("caps batches", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			insertApps(t, db, changeBatchSize+1)

			changes := consumeOnce(t, db, "bounded", storage.Kind("app"), time.Time{})
			assert.Len(t, changes, changeBatchSize)
		})
	})

	t.Run("normalizes kind at the boundary", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			app, err := storage.New[*App]("acme", "system")
			require.NoError(t, err)
			_, err = db.Insert(t.Context(), app)
			require.NoError(t, err)

			changes := consumeOnce(t, db, "uppercase", storage.Kind("APP"), time.Time{})
			require.Len(t, changes, 1)
			assert.Equal(t, app.URN(), changes[0].URN)
		})
	})

	t.Run("returns permanent change errors", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			at := time.Now().UTC().UnixNano()
			hash := int64(xxh3.HashString(storage.Kind("app").String()))
			_, err := db.(*rds).db.Exec(
				`INSERT INTO change_log(created_at, kind_hash, action, urn) VALUES (?, ?, ?, ?)`,
				at, hash, 1, "urn:acme:system:app:not-an-id",
			)
			require.NoError(t, err)

			err = db.Changes(t.Context(), "invalid", storage.Kind("app"), time.Time{}, func(context.Context, []storage.Change) error {
				t.Fatal("invalid change reached callback")
				return nil
			})
			require.ErrorContains(t, err, "parse change URN")
		})
	})

	t.Run("retries transient sqlite errors", func(t *testing.T) {
		assert.True(t, retryableChangeError(fmt.Errorf("busy: %w", sqlite3.BUSY)))
		assert.True(t, retryableChangeError(fmt.Errorf("locked: %w", sqlite3.LOCKED)))
		assert.False(t, retryableChangeError(errors.New("permanent")))
	})

	t.Run("does not regress a shared cursor", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			s := db.(*rds)
			_, err := s.db.Exec(
				`INSERT INTO change_cursors(consumer, kind, created_at, seq) VALUES (?, ?, ?, ?)`,
				"shared", "app", 0, 0,
			)
			require.NoError(t, err)

			newer := changeCursor{createdAt: 2, seq: 2}
			older := changeCursor{createdAt: 1, seq: 1}
			require.NoError(t, s.saveChangeCursor(t.Context(), "shared", storage.Kind("app"), newer))
			saveConcurrentCursors(t, s, older)

			var got changeCursor
			require.NoError(t, s.db.QueryRow(
				`SELECT created_at, seq FROM change_cursors WHERE consumer = ? AND kind = ?`,
				"shared", "app",
			).Scan(&got.createdAt, &got.seq))
			assert.Equal(t, newer, got)
		})
	})

	t.Run("retries a failed callback without advancing", func(t *testing.T) {
		previous := changePollInterval
		changePollInterval = 10 * time.Millisecond
		t.Cleanup(func() { changePollInterval = previous })

		testStorage(func(db storage.Storage, _ storage.Registry) {
			app, err := storage.New[*App]("acme", "system")
			require.NoError(t, err)
			_, err = db.Insert(t.Context(), app)
			require.NoError(t, err)

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var calls atomic.Int32
			var first, second []storage.Change
			done := make(chan struct{})
			errCh := make(chan error, 1)
			go func() {
				errCh <- db.Changes(ctx, "retry", storage.Kind("app"), time.Time{}, func(_ context.Context, batch []storage.Change) error {
					switch calls.Add(1) {
					case 1:
						first = append(first, batch...)
						return errors.New("try again")
					case 2:
						second = append(second, batch...)
						close(done)
					}
					return nil
				})
			}()
			<-done
			cancel()
			require.NoError(t, <-errCh)
			assert.Equal(t, int32(2), calls.Load())
			assert.Equal(t, first, second)
		})
	})

	t.Run("does not invoke an empty callback", func(t *testing.T) {
		previous := changePollInterval
		changePollInterval = time.Millisecond
		t.Cleanup(func() { changePollInterval = previous })

		var calls atomic.Int32
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		defer cancel()
		db := OpenEphemeral(newRegistry())
		defer db.Close()
		require.NoError(t, db.Changes(ctx, "empty", storage.Kind("app"), time.Time{}, func(_ context.Context, _ []storage.Change) error {
			calls.Add(1)
			return nil
		}))
		assert.Zero(t, calls.Load())
	})

	t.Run("persists independent consumer and kind cursors", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			app, err := storage.New[*App]("acme", "system")
			require.NoError(t, err)
			_, err = db.Insert(t.Context(), app)
			require.NoError(t, err)
			deployment, err := storage.New[*Deployment]("acme", "system")
			require.NoError(t, err)
			_, err = db.Insert(t.Context(), deployment)
			require.NoError(t, err)

			assert.Len(t, consumeOnce(t, db, "shared", storage.Kind("app"), time.Time{}), 1)
			assert.Len(t, consumeOnce(t, db, "shared", storage.Kind("deployment"), time.Time{}), 1)
			assert.Len(t, consumeOnce(t, db, "other", storage.Kind("app"), time.Time{}), 1)
			assert.Empty(t, consumeOnce(t, db, "shared", storage.Kind("app"), time.Time{}))
		})
	})

	t.Run("persists across reopen", func(t *testing.T) {
		path := t.TempDir() + "/changes.db"
		registry := newRegistry()
		first, err := Open(path, registry)
		require.NoError(t, err)
		app, err := storage.New[*App]("acme", "system")
		require.NoError(t, err)
		_, err = first.Insert(t.Context(), app)
		require.NoError(t, err)
		assert.Len(t, consumeOnce(t, first, "persistent", storage.Kind("app"), time.Time{}), 1)
		require.NoError(t, first.Close())

		second, err := Open(path, registry)
		require.NoError(t, err)
		defer second.Close()
		assert.Empty(t, consumeOnce(t, second, "persistent", storage.Kind("app"), time.Time{}))
	})

	t.Run("orders rows with the same timestamp by sequence", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			app, err := storage.New[*App]("acme", "system")
			require.NoError(t, err)
			at := time.Now().UTC().UnixNano()
			hash := int64(xxh3.HashString(app.Kind.String()))
			_, err = db.(*rds).db.Exec(
				`INSERT INTO change_log(created_at, kind_hash, action, urn) VALUES (?, ?, ?, ?), (?, ?, ?, ?)`,
				at, hash, 1, app.URN().String(), at, hash, 2, app.URN().String(),
			)
			require.NoError(t, err)

			changes := consumeOnce(t, db, "same-time", app.Kind, time.Unix(0, at))
			require.Len(t, changes, 2)
			assert.Equal(t, []string{changeCreate, changeUpdate}, []string{changes[0].Action, changes[1].Action})
		})
	})

	t.Run("cancellation ends normally", func(t *testing.T) {
		db := OpenEphemeral(newRegistry())
		defer db.Close()
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		require.NoError(t, db.Changes(ctx, "canceled", storage.Kind("app"), time.Time{}, func(context.Context, []storage.Change) error {
			t.Fatal("canceled consumer invoked callback")
			return nil
		}))
	})

	t.Run("failed transaction leaves no change", func(t *testing.T) {
		registry := storage.NewRegistry()
		storage.MustRegister[*brokenLinks](registry)
		db := OpenEphemeral(registry)
		defer db.Close()

		broken, err := storage.New[*brokenLinks]("acme", "system")
		require.NoError(t, err)
		_, err = db.Insert(t.Context(), broken)
		require.Error(t, err)
		assert.Empty(t, consumeOnce(t, db, "failed", broken.Kind, time.Time{}))
	})

	t.Run("prunes old rows", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			app, err := storage.New[*App]("acme", "system")
			require.NoError(t, err)
			_, err = db.Insert(t.Context(), app)
			require.NoError(t, err)
			require.NoError(t, db.(*rds).PruneChanges(t.Context(), time.Now().Add(time.Second)))
			assert.Empty(t, consumeOnce(t, db, "pruned", app.Kind, time.Time{}))
		})
	})

	t.Run("indexes pruning and retains the cutoff", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			s := db.(*rds)
			require.NoError(t, autoMigrate(s.db, s.registry))

			var indexCount int
			require.NoError(t, s.db.QueryRow(
				`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'change_log_created'`,
			).Scan(&indexCount))
			assert.Equal(t, 1, indexCount)

			app, err := storage.New[*App]("acme", "system")
			require.NoError(t, err)
			cutoff := time.Now().UTC().UnixNano()
			hash := int64(xxh3.HashString(app.Kind.String()))
			_, err = s.db.Exec(
				`INSERT INTO change_log(created_at, kind_hash, action, urn) VALUES (?, ?, ?, ?), (?, ?, ?, ?), (?, ?, ?, ?)`,
				cutoff-1, hash, 1, app.URN().String(),
				cutoff, hash, 2, app.URN().String(),
				cutoff+1, hash, 3, app.URN().String(),
			)
			require.NoError(t, err)

			require.NoError(t, s.PruneChanges(t.Context(), time.Unix(0, cutoff)))
			var remaining int
			require.NoError(t, s.db.QueryRow(
				`SELECT COUNT(*) FROM change_log WHERE created_at >= ?`, cutoff,
			).Scan(&remaining))
			assert.Equal(t, 2, remaining)
		})
	})
}

func assertOrderedChanges(t *testing.T, changes []storage.Change, urn storage.URN) {
	t.Helper()
	for i := 1; i < len(changes); i++ {
		assert.False(t, changes[i].At.Before(changes[i-1].At))
	}
	for _, change := range changes {
		assert.Equal(t, urn, change.URN)
	}
}

func saveConcurrentCursors(t *testing.T, s *rds, cursor changeCursor) {
	t.Helper()
	errs := make(chan error, 32)
	for range cap(errs) {
		go func() {
			errs <- s.saveChangeCursor(t.Context(), "shared", storage.Kind("app"), cursor)
		}()
	}
	for range cap(errs) {
		require.NoError(t, <-errs)
	}
}

func consumeOnce(t *testing.T, db storage.Storage, consumer string, kind storage.Kind, after time.Time) []storage.Change {
	t.Helper()
	previous := changePollInterval
	changePollInterval = time.Millisecond
	defer func() { changePollInterval = previous }()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	errCh := make(chan error, 1)
	var changes []storage.Change
	var first sync.Once
	go func() {
		errCh <- db.Changes(ctx, consumer, kind, after, func(_ context.Context, batch []storage.Change) error {
			first.Do(func() {
				changes = append(changes, batch...)
				close(done)
			})
			return nil
		})
	}()
	timer := time.NewTimer(100 * time.Millisecond)
	select {
	case <-done:
	case err := <-errCh:
		require.NoError(t, err)
		return changes
	case <-timer.C:
		cancel()
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	if len(changes) > 0 {
		last := changes[len(changes)-1].At.UnixNano()
		require.Eventually(t, func() bool {
			var cursorAt int64
			err := db.(*rds).db.QueryRow(
				`SELECT created_at FROM change_cursors WHERE consumer = ? AND kind = ?`,
				consumer, kind.String(),
			).Scan(&cursorAt)
			return err == nil && cursorAt >= last
		}, time.Second, time.Millisecond)
	}
	cancel()
	require.NoError(t, <-errCh)
	return changes
}

type brokenLinks struct {
	storage.Meta `kind:"broken_links" json:",inline"`
}

func (*brokenLinks) Links() ([]storage.Link, error) {
	return nil, errors.New("link failure")
}
