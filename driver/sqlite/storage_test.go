package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/kelindar/storage"
	_ "github.com/ncruces/go-sqlite3/driver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSQLite(t *testing.T) {
	t.Run("cancels reads and scans", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			urn := storage.URN{Tenant: "acme", Namespace: "system", Kind: "app", ID: "d92il9hhq4uhlo6a5ucg"}

			_, err := db.Fetch(ctx, urn)
			assert.ErrorIs(t, err, context.Canceled)
			_, err = db.Search(ctx, "app", storage.Query{})
			assert.ErrorIs(t, err, context.Canceled)
			_, err = db.Count(ctx, "app", storage.Query{})
			assert.ErrorIs(t, err, context.Canceled)
			_, err = db.Links(ctx, urn)
			assert.ErrorIs(t, err, context.Canceled)
			_, err = db.Next(ctx, "app")
			assert.ErrorIs(t, err, context.Canceled)
			assert.ErrorIs(t, db.Link(ctx, urn), context.Canceled)
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

	t.Run("update", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			app, err := storage.New[*App]("acme", "my_project")
			assert.NoError(t, err)

			created, err := db.Insert(t.Context(), app)
			assert.NoError(t, err)

			updated, err := db.Update(t.Context(), created)
			assert.NoError(t, err)
			assert.Equal(t, app.URN(), updated.URN())
		})
	})

	t.Run("updateConflict", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			app, err := storage.New[*App]("acme", "my_project")
			assert.NoError(t, err)

			_, err = db.Insert(t.Context(), app)
			assert.NoError(t, err)
			app.UpdatedAt = 0

			_, err = db.Update(t.Context(), app)
			assert.True(t, storage.IsConflict(err))
		})
	})

	t.Run("delete", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			app, err := storage.New[*App]("acme", "my_project")
			assert.NoError(t, err)

			created, err := db.Insert(t.Context(), app)
			assert.NoError(t, err)

			deleted, err := db.Delete(t.Context(), created.URN())
			assert.NoError(t, err)
			assert.Equal(t, app.URN(), deleted.URN())
		})
	})

	t.Run("same ID is globally unique", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			acme, err := storage.New[*App]("acme", "system")
			require.NoError(t, err)
			acme.ID = "d92il9hhq4uhlo6a5ucg"
			acme.Name = "Acme"
			noeti, err := storage.New[*App]("noeti", "system")
			require.NoError(t, err)
			noeti.ID = acme.ID
			noeti.Name = "Noeti"

			_, err = db.Insert(t.Context(), acme)
			require.NoError(t, err)
			_, err = db.Insert(t.Context(), noeti)
			require.ErrorIs(t, err, storage.ErrConflict)
		})
	})

	t.Run("search", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			var firstID string
			for i := range 10 {
				v, err := storage.New[*App]("acme", "my_project")
				assert.NoError(t, err)
				if i == 0 {
					firstID = v.ID
				}

				_, err = db.Insert(t.Context(), v)
				assert.NoError(t, err)
			}

			results, err := db.Search(t.Context(), "App", storage.Query{
				Namespaces: []string{"my_project"},
				Offset:     1,
				Limit:      5,
			})
			assert.NoError(t, err)

			count := 0
			for range results {
				count++
			}
			assert.Equal(t, 5, count)

			results, err = db.Search(t.Context(), "App", storage.Query{Tenant: "acme", IDs: []string{firstID}})
			assert.NoError(t, err)
			assert.Len(t, storage.Collect(results, nil), 1)
		})
	})

	t.Run("empty namespace filter matches nothing", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			app, err := storage.New[*App]("acme", "my_project")
			require.NoError(t, err)
			_, err = db.Insert(t.Context(), app)
			require.NoError(t, err)

			results, err := db.Search(t.Context(), "App", storage.Query{Namespaces: []string{}})
			require.NoError(t, err)
			assert.Empty(t, storage.Collect(results, nil))

			count, err := db.Count(t.Context(), "App", storage.Query{Namespaces: []string{}})
			require.NoError(t, err)
			assert.Zero(t, count)
		})
	})

	t.Run("search parameterizes filters", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			for i, name := range []string{"Public", "Private"} {
				app, err := storage.New[*App]([]string{"acme", "other"}[i], "system")
				require.NoError(t, err)
				app.ID = fmt.Sprintf("%020d", i+1)
				app.Name = name
				_, err = db.Insert(t.Context(), app)
				require.NoError(t, err)
			}

			results, err := storage.Search[*App](t.Context(), db, storage.Query{Filters: map[string][]string{
				"tenant": {"acme"},
				"name":   {"x')) OR 1=1 -- "},
			}})
			require.NoError(t, err)
			assert.Empty(t, storage.Collect(results, nil))

			results, err = storage.Search[*App](t.Context(), db, storage.Query{Filters: map[string][]string{
				"tenant": {"acme"},
				"name":   {"Public"},
			}})
			require.NoError(t, err)
			require.Len(t, storage.Collect(results, nil), 1)
		})
	})

	t.Run("searchCreatedBefore", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			old, err := storage.New[*App]("acme", "my_project")
			assert.NoError(t, err)
			old.Name = "old"
			created, err := db.Insert(t.Context(), old)
			assert.NoError(t, err)

			recent, err := storage.New[*App]("acme", "my_project")
			assert.NoError(t, err)
			recent.Name = "recent"
			_, err = db.Insert(t.Context(), recent)
			assert.NoError(t, err)

			cutoff := time.Now().Add(-time.Hour)
			_, err = db.(*rds).db.Exec(`UPDATE "app" SET created_at = ? WHERE id = ?`, cutoff.Add(-time.Hour).UnixNano(), created.URN().ID)
			assert.NoError(t, err)

			results, err := db.Search(t.Context(), "App", storage.Query{CreatedBefore: cutoff})
			assert.NoError(t, err)

			var names []string
			for result := range results {
				names = append(names, result.(*App).Name)
			}
			assert.Equal(t, []string{"old"}, names)
		})
	})

	t.Run("searchUpdatedAfter", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			old, err := storage.New[*App]("acme", "my_project")
			assert.NoError(t, err)
			old.Name = "old"
			_, err = db.Insert(t.Context(), old)
			assert.NoError(t, err)

			recent, err := storage.New[*App]("acme", "my_project")
			assert.NoError(t, err)
			recent.Name = "recent"
			_, err = db.Insert(t.Context(), recent)
			assert.NoError(t, err)

			cutoff := time.Now().Add(-time.Hour)
			_, err = db.(*rds).db.Exec(`UPDATE "app" SET updated_at = ? WHERE id = ?`, cutoff.Add(-time.Hour).UnixNano(), old.ID)
			assert.NoError(t, err)

			results, err := db.Search(t.Context(), "App", storage.Query{UpdatedAfter: cutoff})
			assert.NoError(t, err)

			var names []string
			for result := range results {
				names = append(names, result.(*App).Name)
			}
			assert.Equal(t, []string{recent.Name}, names)
		})
	})

	t.Run("searchTableID", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			sqliteDB := db.(*rds)
			_, err := sqliteDB.db.Exec(
				`INSERT INTO "app" (tenant, id, namespace, state, indexed_by, data, created_by, updated_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				"acme", "legacy-id", "my_project", "", "Legacy", `{"kind":"app","tenant":"acme","namespace":"my_project","name":"Legacy"}`, "test", "test", int64(1), int64(1),
			)
			assert.NoError(t, err)

			results, err := db.Search(t.Context(), "app", storage.Query{Namespaces: []string{"my_project"}})
			assert.NoError(t, err)

			for result := range results {
				assert.Equal(t, "legacy-id", result.URN().ID)
				return
			}
			require.Fail(t, "expected legacy row")
		})
	})

	t.Run("searchReadError", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			sqliteDB := db.(*rds)
			_, err := sqliteDB.db.Exec(
				`INSERT INTO "app" (tenant, id, namespace, state, indexed_by, data, created_by, updated_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				"acme", "bad-id", "my_project", "", "", `{"kind":"missing","tenant":"acme","namespace":"my_project"}`, "test", "test", int64(1), int64(1),
			)
			assert.NoError(t, err)

			results, err := db.Search(t.Context(), "app", storage.Query{Namespaces: []string{"my_project"}})

			assert.Nil(t, results)
			assert.ErrorContains(t, err, "storage: unable to read")
		})
	})

	t.Run("searchWithoutFTS", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			s, ok := db.(*rds)
			if !ok || s.fts5 {
				t.Skip("requires sqlite without fts5")
			}

			for _, name := range []string{"Simple", "Simulator", "Other"} {
				app, err := storage.New[*App]("acme", "my_project")
				assert.NoError(t, err)
				app.Name = name
				_, err = db.Insert(t.Context(), app)
				assert.NoError(t, err)
			}

			results, err := db.Search(t.Context(), "App", storage.Query{
				Namespaces: []string{"my_project"},
				Match:      "Sim",
			})
			assert.NoError(t, err)

			var names []string
			for result := range results {
				names = append(names, result.(*App).Name)
			}
			assert.ElementsMatch(t, []string{"Simple", "Simulator"}, names)
		})
	})

	t.Run("searchFullText", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			for i := range 100 {
				v, _ := storage.New[*App]("acme", "my_project")
				v.Name = fmt.Sprintf("Application number %d", i)
				if i == 47 {
					v.Name = "Application special target"
				}
				_, err := db.Insert(t.Context(), v)
				assert.NoError(t, err)
			}

			results, err := db.Search(t.Context(), "App", storage.Query{
				Namespaces: []string{"my_project"},
				Match:      "appli special",
				Limit:      1,
			})
			assert.NoError(t, err)

			count := 0
			for result := range results {
				count++
				assert.Equal(t, "Application special target", result.(*App).Name)
			}

			assert.Equal(t, 1, count)
		})
	})

	t.Run("count", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			for range 10 {
				v, err := storage.New[*App]("acme", "my_project")
				assert.NoError(t, err)

				_, err = db.Insert(t.Context(), v)
				assert.NoError(t, err)
			}

			ct, err := db.Count(t.Context(), "App", storage.Query{
				Namespaces: []string{"my_project"},
			})
			assert.NoError(t, err)
			assert.Equal(t, 10, ct)
		})
	})

	t.Run("updateReturnsInput", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			app, err := storage.New[*App]("acme", "my_project")
			require.NoError(t, err)
			_, err = db.Insert(t.Context(), app)
			require.NoError(t, err)

			app.Name = "updated"
			updated, err := db.Update(t.Context(), app)
			require.NoError(t, err)
			assert.Same(t, app, updated)

			stored, err := db.Fetch(t.Context(), app.URN())
			require.NoError(t, err)
			assert.Equal(t, "updated", stored.(*App).Name)
		})
	})
}

func TestExpirationPersistence(t *testing.T) {
	testStorage(func(db storage.Storage, _ storage.Registry) {
		app, err := storage.New[*App]("acme", "system")
		require.NoError(t, err)
		app.ExpiresAt = 10
		created, err := storage.Insert(t.Context(), db, app)
		require.NoError(t, err)
		assert.Equal(t, int64(10), created.ExpiresAt)

		created.ExpiresAt = 20
		updated, err := storage.Update(t.Context(), db, created)
		require.NoError(t, err)
		assert.Equal(t, int64(20), updated.ExpiresAt)

		fetched, err := storage.Fetch[*App](t.Context(), db, app.URN())
		require.NoError(t, err)
		assert.Equal(t, int64(20), fetched.ExpiresAt)

		results, err := storage.Search[*App](t.Context(), db, storage.Query{})
		require.NoError(t, err)
		for result := range results {
			if result.URN() == app.URN() {
				assert.Equal(t, int64(20), result.ExpiresAt)
				return
			}
		}
		require.Fail(t, "expected stored app")
	})
}

func TestExpired(t *testing.T) {
	testStorage(func(db storage.Storage, _ storage.Registry) {
		s := db.(*rds)
		now := time.Now().UnixNano()
		for i := range 103 {
			app, err := storage.New[*App]("acme", "system")
			require.NoError(t, err)
			app.ID = fmt.Sprintf("%020d", i)
			switch i {
			case 100:
				app.ExpiresAt = now - 1
			case 101:
				app.ExpiresAt = 0
			case 102:
				app.ExpiresAt = now + 1
			default:
				app.ExpiresAt = now
			}
			_, err = storage.Insert(t.Context(), db, app)
			require.NoError(t, err)
		}

		var expired []storage.URN
		for urn, err := range s.Expired(t.Context(), "app", now, 100) {
			require.NoError(t, err)
			expired = append(expired, urn)
		}
		require.Len(t, expired, 101)
		assert.Equal(t, "00000000000000000100", expired[0].ID)
		assert.Equal(t, "00000000000000000000", expired[1].ID)
		assert.Equal(t, "00000000000000000098", expired[99].ID)
		assert.Equal(t, "00000000000000000099", expired[100].ID)
	})
}

func TestExpirationMigration(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = db.Exec(`CREATE TABLE app (
		tenant TEXT NOT NULL, id TEXT PRIMARY KEY, namespace TEXT NOT NULL, state TEXT,
		data JSON, indexed_by TEXT, created_by TEXT, updated_by TEXT, created_at INTEGER, updated_at INTEGER
	)`)
	require.NoError(t, err)

	registry := storage.NewRegistry()
	storage.MustRegister[*App](registry)
	require.NoError(t, autoMigrate(db, registry))

	var columnCount int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('app') WHERE name = 'expires_at' AND "notnull" = 1 AND dflt_value = '0'`).Scan(&columnCount))
	assert.Equal(t, 1, columnCount)
	var indexCount int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'app_idx_expiration'`).Scan(&indexCount))
	assert.Equal(t, 1, indexCount)
}

func TestSanitizeTerm(t *testing.T) {
	assert.Equal(t, "Sim*", sanitizeTerm("Sim"))
	assert.Equal(t, "NEAR(appli* 47*, 30)", sanitizeTerm("appli 47"))
	assert.Equal(t, "", sanitizeTerm("   "))
}

func TestLinkIndex(t *testing.T) {
	t.Run("tracks document mutations", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			first := testURN(t, "artifact", "00000000000000000000")
			second := testURN(t, "artifact", "00000000000000000001")
			app, err := storage.New[*App]("acme", "system")
			require.NoError(t, err)
			app.Artifact = first

			created, err := db.Insert(t.Context(), app)
			require.NoError(t, err)
			assert.Equal(t, []storage.Link{storage.Use(created.URN(), first, "artifact")}, linksFor(t, db, first))
			stale, err := db.Fetch(t.Context(), created.URN())
			require.NoError(t, err)

			created.(*App).Artifact = second
			updated, err := db.Update(t.Context(), created)
			require.NoError(t, err)
			assert.Empty(t, linksFor(t, db, first))
			assert.Equal(t, []storage.Link{storage.Use(updated.URN(), second, "artifact")}, linksFor(t, db, second))

			stale.(*App).Artifact = first
			_, err = db.Update(t.Context(), stale)
			require.True(t, storage.IsConflict(err))
			assert.Equal(t, []storage.Link{storage.Use(updated.URN(), second, "artifact")}, linksFor(t, db, second))

			_, err = db.Delete(t.Context(), updated.URN())
			require.NoError(t, err)
			assert.Empty(t, linksFor(t, db, second))
		})
	})

	t.Run("rolls back document when link indexing fails", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			target := testURN(t, "blob", "00000000000000000000")
			first := newOwner(t, target)
			_, err := db.Insert(t.Context(), first)
			require.NoError(t, err)

			second := newOwner(t, target)
			_, err = db.Insert(t.Context(), second)
			require.Error(t, err)
			_, err = db.Fetch(t.Context(), second.URN())
			require.True(t, storage.IsNotFound(err))
			assert.Equal(t, []storage.Link{storage.Own(first.URN(), target, "target")}, linksFor(t, db, target))
		})
	})

	t.Run("rejects tenant crossing", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			otherTenant := targetURN(t, "other", "artifact", "00000000000000000000")
			app, err := storage.New[*App]("system", "system")
			require.NoError(t, err)
			app.Extra = otherTenant
			_, err = db.Insert(t.Context(), app)
			require.ErrorContains(t, err, "crosses tenants")
		})
	})

	t.Run("merges typed and declared links and clears a deleted source", func(t *testing.T) {
		testStorage(func(db storage.Storage, _ storage.Registry) {
			target := testURN(t, "artifact", "00000000000000000000")
			app, err := storage.New[*App]("acme", "system")
			require.NoError(t, err)
			app.Artifact = target
			app.ArtifactText = target.String()
			app.Artifacts = []storage.URN{target}
			app.References = map[string]storage.URN{"primary": target}
			app.Reverse = target
			app.Extra = target
			created, err := db.Insert(t.Context(), app)
			require.NoError(t, err)
			source := created.URN()
			require.NoError(t, db.Link(t.Context(), source))
			got, err := db.Links(t.Context(), target)
			require.NoError(t, err)
			assert.Equal(t, []storage.Link{
				{Source: source, Target: target, Path: "artifact", Kind: storage.LinkDependency},
				{Source: source, Target: target, Path: "artifactText", Kind: storage.LinkDependency},
				{Source: source, Target: target, Path: "artifacts.0", Kind: storage.LinkDependency},
				{Source: source, Target: target, Path: "extra", Kind: storage.LinkDependency},
				{Source: source, Target: target, Path: "references.primary", Kind: storage.LinkDependency},
			}, got)

			_, err = db.Delete(t.Context(), source)
			require.NoError(t, err)
			require.NoError(t, db.Link(t.Context(), source))
			got, err = db.Links(t.Context(), target)
			require.NoError(t, err)
			assert.Empty(t, got)
		})
	})
}

func linksFor(t *testing.T, db storage.Storage, target storage.URN) []storage.Link {
	t.Helper()
	links, err := db.Links(t.Context(), target)
	require.NoError(t, err)
	return links
}

func testURN(t *testing.T, kind storage.Kind, id string) storage.URN {
	t.Helper()
	return targetURN(t, "acme", kind, id)
}

func targetURN(t *testing.T, tenant string, kind storage.Kind, id string) storage.URN {
	t.Helper()
	urn, err := storage.MakeURN(tenant, "system", kind, id)
	require.NoError(t, err)
	return urn
}

func TestMatchLikeClause(t *testing.T) {
	clause, args := matchLikeClause("Sim")
	assert.Equal(t, "CAST(data AS TEXT) LIKE ? ESCAPE '\\'", clause)
	assert.Equal(t, []any{"%Sim%"}, args)

	clause, args = matchLikeClause("appli 47")
	assert.Equal(t, "CAST(data AS TEXT) LIKE ? ESCAPE '\\' AND CAST(data AS TEXT) LIKE ? ESCAPE '\\'", clause)
	assert.Equal(t, []any{"%appli%", "%47%"}, args)
}

// ---------------------------------- Storage Test ----------------------------------

func testStorage(fn func(db storage.Storage, registry storage.Registry)) {
	r := newRegistry()
	s := OpenEphemeral(r)
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
	Name         string                 `json:"name"`
	Artifact     storage.URN            `json:"artifact" link:"artifact"`
	ArtifactText string                 `json:"artifactText,omitempty" link:"artifact"`
	Artifacts    []storage.URN          `json:"artifacts,omitempty" link:"artifact"`
	References   map[string]storage.URN `json:"references,omitempty" link:"artifact"`
	Reverse      storage.URN            `json:"reverse"`
	Extra        storage.URN            `json:"extra"`
}

type Owner struct {
	storage.Meta `kind:"owner" json:",inline"`
	Target       storage.URN `json:"target"`
}

func (o *Owner) Links() ([]storage.Link, error) {
	return []storage.Link{storage.Own(o.URN(), o.Target, "target")}, nil
}

func newOwner(t *testing.T, target storage.URN) *Owner {
	t.Helper()
	owner, err := storage.New[*Owner]("acme", "system")
	require.NoError(t, err)
	owner.Target = target
	return owner
}

func (a *App) Links() ([]storage.Link, error) {
	if !a.Extra.IsValid() {
		return nil, nil
	}
	return []storage.Link{storage.Use(a.URN(), a.Extra, "extra")}, nil
}

func newRegistry() storage.Registry {
	registry := storage.NewRegistry()
	storage.MustRegister[*Artifact](registry)
	storage.MustRegister[*Deployment](registry)
	storage.MustRegister[*App](registry)
	storage.MustRegister[*Owner](registry)
	return registry
}
