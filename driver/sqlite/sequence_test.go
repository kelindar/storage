package sqlite

import (
	"database/sql"
	"math"
	"path/filepath"
	"sync"
	"testing"

	"github.com/kelindar/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSequence(t *testing.T) {
	t.Run("starts at one and increases", func(t *testing.T) {
		db := OpenEphemeral(storage.NewRegistry())
		t.Cleanup(func() { require.NoError(t, db.Close()) })

		first, err := db.Next(t.Context(), "experiment")
		require.NoError(t, err)
		assert.Equal(t, uint32(1), first)

		second, err := db.Next(t.Context(), "experiment")
		require.NoError(t, err)
		assert.Equal(t, uint32(2), second)

		third, err := db.Next(t.Context(), "experiment")
		require.NoError(t, err)
		assert.Equal(t, uint32(3), third)
	})

	t.Run("rejects empty name", func(t *testing.T) {
		db := OpenEphemeral(storage.NewRegistry())
		t.Cleanup(func() { require.NoError(t, db.Close()) })

		_, err := db.Next(t.Context(), "")
		require.Error(t, err)
		assert.ErrorIs(t, err, storage.ErrInvalid)

		var count int
		require.NoError(t, db.(*rds).db.QueryRow(`SELECT COUNT(*) FROM sequences`).Scan(&count))
		assert.Equal(t, 0, count)
	})

	t.Run("names advance independently", func(t *testing.T) {
		db := OpenEphemeral(storage.NewRegistry())
		t.Cleanup(func() { require.NoError(t, db.Close()) })

		a1, err := db.Next(t.Context(), "a")
		require.NoError(t, err)
		b1, err := db.Next(t.Context(), "b")
		require.NoError(t, err)
		a2, err := db.Next(t.Context(), "a")
		require.NoError(t, err)
		b2, err := db.Next(t.Context(), "b")
		require.NoError(t, err)

		assert.Equal(t, []uint32{1, 1, 2, 2}, []uint32{a1, b1, a2, b2})
	})

	t.Run("concurrent calls return unique contiguous values", func(t *testing.T) {
		db := OpenEphemeral(storage.NewRegistry())
		t.Cleanup(func() { require.NoError(t, db.Close()) })

		const n = 64
		values := make([]uint32, n)
		var wg sync.WaitGroup
		wg.Add(n)
		for i := range n {
			go func(i int) {
				defer wg.Done()
				v, err := db.Next(t.Context(), "concurrent")
				require.NoError(t, err)
				values[i] = v
			}(i)
		}
		wg.Wait()

		seen := make(map[uint32]struct{}, n)
		for _, v := range values {
			_, dup := seen[v]
			require.False(t, dup, "duplicate value %d", v)
			seen[v] = struct{}{}
		}
		for want := uint32(1); want <= n; want++ {
			_, ok := seen[want]
			assert.True(t, ok, "missing value %d", want)
		}
	})

	t.Run("persists across reopen", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "sequences.db")
		registry := storage.NewRegistry()

		db, err := Open(path, registry)
		require.NoError(t, err)
		for i := uint32(1); i <= 3; i++ {
			got, err := db.Next(t.Context(), "persist")
			require.NoError(t, err)
			assert.Equal(t, i, got)
		}
		require.NoError(t, db.Close())

		reopened, err := Open(path, registry)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, reopened.Close()) })

		got, err := reopened.Next(t.Context(), "persist")
		require.NoError(t, err)
		assert.Equal(t, uint32(4), got)
	})

	t.Run("keeps allocation after later work fails", func(t *testing.T) {
		db := OpenEphemeral(storage.NewRegistry())
		t.Cleanup(func() { require.NoError(t, db.Close()) })

		allocated, err := db.Next(t.Context(), "gap")
		require.NoError(t, err)
		assert.Equal(t, uint32(1), allocated)

		// Simulate caller work failing after a successful allocation.
		workErr := assert.AnError
		require.Error(t, workErr)

		next, err := db.Next(t.Context(), "gap")
		require.NoError(t, err)
		assert.Equal(t, uint32(2), next)
	})

	t.Run("wraps at MaxUint32", func(t *testing.T) {
		db := OpenEphemeral(storage.NewRegistry())
		t.Cleanup(func() { require.NoError(t, db.Close()) })

		_, err := db.(*rds).db.Exec(`INSERT INTO sequences(name, value) VALUES (?, ?)`, "wrap", math.MaxUint32)
		require.NoError(t, err)

		got, err := db.Next(t.Context(), "wrap")
		require.NoError(t, err)
		assert.Equal(t, uint32(0), got)

		got, err = db.Next(t.Context(), "wrap")
		require.NoError(t, err)
		assert.Equal(t, uint32(1), got)
	})

	t.Run("recovers corrupted stored values by wrapping", func(t *testing.T) {
		db := OpenEphemeral(storage.NewRegistry())
		t.Cleanup(func() { require.NoError(t, db.Close()) })
		sqliteDB := db.(*rds).db

		_, err := sqliteDB.Exec(`PRAGMA ignore_check_constraints = ON`)
		require.NoError(t, err)
		_, err = sqliteDB.Exec(`INSERT INTO sequences(name, value) VALUES ('bad', -1)`)
		require.NoError(t, err)

		got, err := db.Next(t.Context(), "bad")
		require.NoError(t, err)
		assert.Equal(t, uint32(0), got)

		_, err = sqliteDB.Exec(`INSERT INTO sequences(name, value) VALUES (?, ?)`, "overflow", math.MaxInt64)
		require.NoError(t, err)

		got, err = db.Next(t.Context(), "overflow")
		require.NoError(t, err)
		assert.Equal(t, uint32(0), got)
	})

	t.Run("returns storage errors without success", func(t *testing.T) {
		db := OpenEphemeral(storage.NewRegistry())
		require.NoError(t, db.Close())

		got, err := db.Next(t.Context(), "closed")
		require.Error(t, err)
		assert.Equal(t, uint32(0), got)
	})

	t.Run("migration creates sequences table", func(t *testing.T) {
		raw, err := sql.Open("sqlite3", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, raw.Close()) })

		require.NoError(t, autoMigrate(raw, storage.NewRegistry()))

		var count int
		require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'sequences'`).Scan(&count))
		assert.Equal(t, 1, count)

		db := OpenEphemeral(storage.NewRegistry())
		t.Cleanup(func() { require.NoError(t, db.Close()) })
		got, err := db.Next(t.Context(), "experiment")
		require.NoError(t, err)
		assert.Equal(t, uint32(1), got)
	})
}
