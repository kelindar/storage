package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kelindar/storage"
	"github.com/kelindar/storage/driver/sqlite"
	"github.com/kelindar/storage/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChangesValidation(t *testing.T) {
	db := sqlite.OpenEphemeral(newRegistry())
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	t.Run("rejects invalid object", func(t *testing.T) {
		err := storage.Changes[*invalidObject](t.Context(), db, "consumer", time.Time{}, nil).Wait()
		require.Error(t, err)
	})
	t.Run("requires consumer", func(t *testing.T) {
		err := storage.Changes[*App](t.Context(), db, "", time.Time{}, nil).Wait()
		require.ErrorIs(t, err, storage.ErrInvalid)
	})
	t.Run("requires handler", func(t *testing.T) {
		err := storage.Changes[*App](t.Context(), db, "consumer", time.Time{}, nil).Wait()
		require.ErrorIs(t, err, storage.ErrInvalid)
	})
	t.Run("runs worker", func(t *testing.T) {
		err := storage.Changes[*App](t.Context(), changesStorage{Storage: db}, "consumer", time.Time{}, func(context.Context, []storage.Change) error {
			return nil
		}).Wait()
		require.ErrorContains(t, err, "changes failed")
	})
}

func TestStore(t *testing.T) {
	backend := sqlite.OpenEphemeral(newRegistry())
	t.Cleanup(func() { require.NoError(t, backend.Close()) })
	store := storage.NewStore(backend, &storage.Memory{})

	created, err := store.Upload(t.Context(), storage.URN{Tenant: "acme", Namespace: "default"}, "text/plain", []byte("bind me"))
	require.NoError(t, err)

	t.Run("fetch", func(t *testing.T) {
		object, err := store.Fetch(t.Context(), created.URN())
		require.NoError(t, err)
		blob, ok := object.(*storage.Blob)
		require.True(t, ok)
		data, err := blob.Read(t.Context())
		require.NoError(t, err)
		assert.Equal(t, []byte("bind me"), data)
	})

	t.Run("search", func(t *testing.T) {
		cursor, err := store.Search(t.Context(), storage.KindBlob, storage.Query{Filters: map[string][]string{"tenant": {"acme"}}})
		require.NoError(t, err)
		var found bool
		for object := range cursor {
			blob, ok := object.(*storage.Blob)
			require.True(t, ok)
			if blob.URN() != created.URN() {
				continue
			}
			data, err := blob.Read(t.Context())
			require.NoError(t, err)
			assert.Equal(t, []byte("bind me"), data)
			found = true
		}
		assert.True(t, found)
	})

	t.Run("update", func(t *testing.T) {
		current, err := storage.Fetch[*storage.Blob](t.Context(), store, created.URN())
		require.NoError(t, err)
		current.ContentType = "text/markdown"
		updated, err := store.Update(t.Context(), current)
		require.NoError(t, err)
		blob, ok := updated.(*storage.Blob)
		require.True(t, ok)
		assert.Equal(t, "text/markdown", blob.ContentType)
		data, err := blob.Read(t.Context())
		require.NoError(t, err)
		assert.Equal(t, []byte("bind me"), data)
	})

	t.Run("upsert", func(t *testing.T) {
		current, err := storage.Fetch[*storage.Blob](t.Context(), store, created.URN())
		require.NoError(t, err)
		upserted, err := storage.Upsert(t.Context(), store, current, func(blob *storage.Blob) error {
			blob.ContentType = "text/csv"
			return nil
		})
		require.NoError(t, err)
		blob := upserted
		assert.Equal(t, "text/csv", blob.ContentType)
		data, err := blob.Read(t.Context())
		require.NoError(t, err)
		assert.Equal(t, []byte("bind me"), data)
	})

	t.Run("deleteUnconfigured", func(t *testing.T) {
		_, err := (*storage.Store)(nil).Delete(t.Context(), created.URN())
		require.ErrorContains(t, err, "not configured")
	})
}

func TestStorage(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			app, err := storage.Create[*App](t.Context(), db, func(obj *App) error {
				obj.State = "created"
				return nil
			}, "acme", "my_project")
			assert.NoError(t, err)
			assert.NotNil(t, app)
		})
	})

	t.Run("insert", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			app, err := storage.New[*App]("acme", "my_project")
			assert.NoError(t, err)

			out, err := db.Insert(t.Context(), app)
			assert.NoError(t, err)
			assert.Equal(t, app.URN(), out.URN())

			createdBy, createdAt := out.Created()
			assert.NotEmpty(t, createdAt)
			assert.Equal(t, storage.UnknownActor, createdBy)
		})
	})

	t.Run("insertAssignsID", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			app := &App{
				Meta: storage.Meta{
					Kind:      "app",
					Tenant:    "acme",
					Namespace: "my_project",
				},
			}

			out, err := storage.Insert(t.Context(), db, app)
			assert.NoError(t, err)
			assert.NotEmpty(t, out.URN().ID)
		})
	})

	t.Run("update", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			app, err := storage.New[*App]("acme", "my_project")
			assert.NoError(t, err)

			created, err := storage.Insert(t.Context(), db, app)
			assert.NoError(t, err)

			updated, err := storage.Update(t.Context(), db, created)
			assert.NoError(t, err)
			assert.Equal(t, app.URN(), updated.URN())
		})
	})

	t.Run("patch", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			app, err := storage.New[*App]("acme", "my_project")
			require.NoError(t, err)
			created, err := storage.Insert(t.Context(), db, app)
			require.NoError(t, err)

			patched, err := storage.Patch[*App](t.Context(), db, created.URN(), func(app *App) error {
				app.ExpiresAt = 42
				return nil
			})
			require.NoError(t, err)
			assert.Equal(t, int64(42), patched.ExpiresAt)

			stored, err := storage.Fetch[*App](t.Context(), db, created.URN())
			require.NoError(t, err)
			assert.Equal(t, int64(42), stored.ExpiresAt)
		})
	})

	t.Run("patchRetry", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			app, err := storage.New[*App]("acme", "my_project")
			require.NoError(t, err)
			created, err := storage.Insert(t.Context(), db, app)
			require.NoError(t, err)

			attempts := 0
			patched, err := storage.Patch[*App](t.Context(), db, created.URN(), func(current *App) error {
				attempts++
				if attempts == 1 {
					concurrent, err := storage.Fetch[*App](t.Context(), db, created.URN())
					if err != nil {
						return err
					}
					concurrent.ExpiresAt = 2
					if _, err := storage.Update(t.Context(), db, concurrent); err != nil {
						return err
					}
				}
				current.ExpiresAt++
				return nil
			})
			require.NoError(t, err)
			assert.Equal(t, 2, attempts)
			assert.Equal(t, int64(3), patched.ExpiresAt)

			stored, err := storage.Fetch[*App](t.Context(), db, created.URN())
			require.NoError(t, err)
			assert.Equal(t, int64(3), stored.ExpiresAt)
		})
	})

	t.Run("patchConflictLimit", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			app, err := storage.New[*App]("acme", "my_project")
			require.NoError(t, err)
			created, err := storage.Insert(t.Context(), db, app)
			require.NoError(t, err)

			attempts := 0
			_, err = storage.Patch[*App](t.Context(), db, created.URN(), func(current *App) error {
				attempts++
				if attempts > 10 {
					return errors.New("patch retry limit was not enforced")
				}
				current.UpdatedAt = 0
				return nil
			})
			assert.True(t, storage.IsConflict(err))
			assert.Equal(t, 10, attempts)
		})
	})

	t.Run("updateConflict", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			app, err := storage.New[*App]("acme", "my_project")
			assert.NoError(t, err)

			_, err = storage.Insert(t.Context(), db, app)
			assert.NoError(t, err)
			app.UpdatedAt = 0

			_, err = storage.Update(t.Context(), db, app)
			assert.True(t, storage.IsConflict(err))
		})
	})

	t.Run("delete", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			app, err := storage.New[*App]("acme", "my_project")
			assert.NoError(t, err)

			created, err := db.Insert(t.Context(), app)
			assert.NoError(t, err)

			deleted, err := storage.Delete[*App](t.Context(), db, created.URN())
			assert.NoError(t, err)
			assert.Equal(t, app.URN(), deleted.URN())
		})
	})

	t.Run("search", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			for range 10 {
				app, err := storage.New[*App]("acme", "my_project")
				assert.NoError(t, err)

				_, err = storage.Insert(t.Context(), db, app)
				assert.NoError(t, err)
			}

			results, err := storage.Search[*App](t.Context(), db, storage.Query{
				Namespaces: []string{"my_project"},
				Limit:      5,
			})
			assert.NoError(t, err)

			count := 0
			for range results {
				count++
			}
			assert.Equal(t, 5, count)
		})
	})

	t.Run("count", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			for range 10 {
				app, err := storage.New[*App]("acme", "my_project")
				assert.NoError(t, err)

				_, err = storage.Insert(t.Context(), db, app)
				assert.NoError(t, err)
			}

			count, err := storage.Count[*App](t.Context(), db, storage.Query{
				Namespaces: []string{"my_project"},
			})
			assert.NoError(t, err)
			assert.Equal(t, 10, count)
		})
	})
}

func TestStatefulStorage(t *testing.T) {
	t.Run("insertDefaultState", func(t *testing.T) {
		testStatefulStorage(func(db storage.Storage, _ storage.Registry) {
			app, err := storage.New[*StatefulApp]("acme", "my_project")
			assert.NoError(t, err)
			assert.Empty(t, app.State)

			created, err := storage.Insert(t.Context(), db, app)
			assert.NoError(t, err)
			assert.Equal(t, "requested", created.Status())
		})
	})

	t.Run("updateValidTransition", func(t *testing.T) {
		testStatefulStorage(func(db storage.Storage, _ storage.Registry) {
			app, err := storage.New[*StatefulApp]("acme", "my_project")
			assert.NoError(t, err)

			created, err := storage.Insert(t.Context(), db, app)
			assert.NoError(t, err)
			assert.Equal(t, "requested", created.Status())

			created.State = "approved"
			updated, err := storage.Update(t.Context(), db, created)
			assert.NoError(t, err)
			assert.Equal(t, "approved", updated.Status())
		})
	})

	t.Run("updateInvalidTransition", func(t *testing.T) {
		testStatefulStorage(func(db storage.Storage, _ storage.Registry) {
			app, err := storage.New[*StatefulApp]("acme", "my_project")
			assert.NoError(t, err)

			created, err := storage.Insert(t.Context(), db, app)
			assert.NoError(t, err)

			created.State = "draft"
			_, err = storage.Update(t.Context(), db, created)
			assert.True(t, storage.IsInvalidTransition(err))
		})
	})

	t.Run("upsertDefaultState", func(t *testing.T) {
		testStatefulStorage(func(db storage.Storage, _ storage.Registry) {
			app, err := storage.New[*StatefulApp]("acme", "my_project")
			assert.NoError(t, err)

			upserted, err := storage.Upsert(t.Context(), db, app, func(*StatefulApp) error {
				return nil
			})
			assert.NoError(t, err)
			assert.Equal(t, "requested", upserted.Status())
		})
	})

	t.Run("upsertPatchesExisting", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			app, err := storage.New[*App]("acme", "my_project")
			require.NoError(t, err)
			created, err := storage.Upsert(t.Context(), db, app, func(*App) error {
				return errors.New("create path must not patch")
			})
			require.NoError(t, err)

			patched, err := storage.Upsert(t.Context(), db, created, func(current *App) error {
				current.ExpiresAt = 42
				return nil
			})
			require.NoError(t, err)
			assert.Equal(t, int64(42), patched.ExpiresAt)
		})
	})

	t.Run("upsertRequiresPatch", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			app, err := storage.New[*App]("acme", "my_project")
			require.NoError(t, err)
			_, err = storage.Upsert(t.Context(), db, app, nil)
			assert.ErrorIs(t, err, storage.ErrInvalid)
		})
	})

	t.Run("upsertDoesNotPatchDifferentURN", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			first, err := storage.New[*App]("acme", "my_project")
			require.NoError(t, err)
			first, err = storage.Insert(t.Context(), db, first)
			require.NoError(t, err)

			second, err := storage.New[*App]("noeti", "my_project")
			require.NoError(t, err)
			second.ID = first.ID
			patched := false
			_, err = storage.Upsert(t.Context(), db, second, func(*App) error {
				patched = true
				return nil
			})
			assert.True(t, storage.IsConflict(err))
			assert.False(t, patched)
		})
	})
}

func TestNext(t *testing.T) {
	testStorage(func(db storage.Storage, _ storage.Registry) {
		first, err := storage.Next[*App](t.Context(), db)
		require.NoError(t, err)
		assert.Equal(t, uint32(1), first)

		second, err := storage.Next[*App](t.Context(), db)
		require.NoError(t, err)
		assert.Equal(t, uint32(2), second)

		// Kind string is the sequence name.
		direct, err := db.Next(t.Context(), "app")
		require.NoError(t, err)
		assert.Equal(t, uint32(3), direct)

		other, err := storage.Next[*Artifact](t.Context(), db)
		require.NoError(t, err)
		assert.Equal(t, uint32(1), other)
	})
}

// ---------------------------------- Storage Test ----------------------------------

func testStorage(fn func(db storage.Storage, registry storage.Registry)) {
	r := newRegistry()
	s := sqlite.OpenEphemeral(r)
	defer s.Close()
	fn(s, r)
}

func testStatefulStorage(fn func(db storage.Storage, registry storage.Registry)) {
	r := newStatefulRegistry()
	s := sqlite.OpenEphemeral(r)
	defer s.Close()
	fn(s, r)
}

// ---------------------------------- Test Types ----------------------------------

type Artifact struct {
	storage.Meta `kind:"artifact" json:",inline"`
	Deployment   storage.URN `json:"deployment"`
}

type Deployment struct {
	storage.Meta `kind:"deployment" json:",inline"`
	Env          string      `json:"env"`
	App          storage.URN `json:"app"`
}

type App struct {
	storage.Meta `kind:"app" json:",inline"`
}

type StatefulApp struct {
	storage.Meta `kind:"stateful_app" json:",inline"`
}

type invalidObject struct {
	storage.Meta
}

type changesStorage struct {
	storage.Storage
}

func (changesStorage) Changes(context.Context, string, storage.Kind, time.Time, func(context.Context, []storage.Change) error) error {
	return errors.New("changes failed")
}

func newRegistry() storage.Registry {
	registry := storage.NewRegistry()
	storage.MustRegister[*Artifact](registry)
	storage.MustRegister[*Deployment](registry)
	storage.MustRegister[*App](registry)
	storage.MustRegister[*storage.Blob](registry, storage.Options{
		States: state.Machine{
			"create": "* -> active",
			"delete": "active -> deleting",
		},
	})
	storage.MustRegister[*conversationObject](registry)
	return registry
}

type conversationObject struct {
	storage.Meta `kind:"conversation" json:",inline"`
	Attachments  []storage.URN `json:"attachments" link:"blob"`
}

func newStatefulRegistry() storage.Registry {
	registry := storage.NewRegistry()
	storage.MustRegister[*StatefulApp](registry, storage.Options{
		States: state.Machine{
			"create": "* -> requested",
			"update": "requested -> approved",
			"delete": "requested -> rejected",
		},
	})
	return registry
}
