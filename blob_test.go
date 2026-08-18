package storage_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kelindar/storage"
	"github.com/kelindar/storage/driver/sqlite"
	"github.com/kelindar/storage/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemory(t *testing.T) {
	t.Run("roundTrip", func(t *testing.T) {
		var files storage.Memory
		_, err := files.Write(t.Context(), "blobs/test", []byte("payload"))
		require.NoError(t, err)
		data, err := fs.ReadFile(&files, "blobs/test")
		require.NoError(t, err)
		require.Equal(t, []byte("payload"), data)
		require.NoError(t, files.Delete(t.Context(), "blobs/test"))
		_, err = fs.ReadFile(&files, "blobs/test")
		require.ErrorIs(t, err, fs.ErrNotExist)
		_, err = files.Write(t.Context(), ".", []byte("invalid"))
		require.ErrorIs(t, err, fs.ErrInvalid)
	})

	t.Run("canceledWrite", func(t *testing.T) {
		var files storage.Memory
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := files.Write(ctx, "blobs/test", []byte("payload"))
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("canceledDelete", func(t *testing.T) {
		var files storage.Memory
		_, err := files.Write(t.Context(), "blobs/test", []byte("payload"))
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		require.ErrorIs(t, files.Delete(ctx, "blobs/test"), context.Canceled)
	})
}

func TestBlobJSON(t *testing.T) {
	value := &storage.Blob{
		Meta: storage.Meta{
			ID: "blob-id", Kind: storage.KindBlob, Tenant: "acme", Namespace: "default",
		},
		ContentType: "text/plain", ObjectKey: "blobs/acme/default/blob-id",
		SHA256: "checksum", StoredSize: 7, Compression: storage.CompressionZstd,
	}
	public, err := json.Marshal(value)
	require.NoError(t, err)
	require.NotContains(t, string(public), "objectKey")
	require.NotContains(t, string(public), "sha256")
	require.NotContains(t, string(public), "storedSize")
	require.NotContains(t, string(public), "compression")
	stored, err := storage.ToJSON(value)
	require.NoError(t, err)
	decoded, err := storage.FromJSON(newRegistry(), stored)
	require.NoError(t, err)
	restored := decoded.(*storage.Blob)
	require.Equal(t, value.ObjectKey, restored.ObjectKey)
	require.Equal(t, value.Compression, restored.Compression)
	assert.Equal(t, value.ID, value.Title())
	assert.Equal(t, value.ContentType, value.Subtitle())
}

func TestStoreBlobLifecycle(t *testing.T) {
	files := &failingFiles{}
	backend := sqlite.OpenEphemeral(newRegistry())
	t.Cleanup(func() { require.NoError(t, backend.Close()) })
	store := storage.NewStore(backend, files)
	created, err := store.Upload(t.Context(), storage.URN{Tenant: "acme", Namespace: "default"}, "text/plain", []byte("hello blob"))
	require.NoError(t, err)
	require.Equal(t, storage.CompressionZstd, created.Compression)
	require.Equal(t, state.Active, created.State)
	fetched, err := storage.Fetch[*storage.Blob](t.Context(), store, created.URN())
	require.NoError(t, err)
	data, err := fetched.Read(t.Context())
	require.NoError(t, err)
	require.Equal(t, []byte("hello blob"), data)
	var written bytes.Buffer
	n, err := fetched.WriteTo(t.Context(), &written)
	require.NoError(t, err)
	require.EqualValues(t, len(data), n)
	require.Equal(t, data, written.Bytes())
	_, err = fetched.WriteTo(t.Context(), failWriter{})
	require.ErrorContains(t, err, "write failed")
	items, err := storage.Search[*storage.Blob](t.Context(), store, storage.Query{})
	require.NoError(t, err)
	for item := range items {
		if item.URN() == created.URN() {
			_, err = item.Read(t.Context())
			require.NoError(t, err)
		}
	}
	_, err = files.Write(t.Context(), created.ObjectKey, []byte("corrupt"))
	require.NoError(t, err)
	written.Reset()
	n, err = fetched.WriteTo(t.Context(), &written)
	require.Zero(t, n)
	require.Error(t, err)
	require.Empty(t, written.Bytes())

	files.deleteErr = errors.New("unavailable")
	_, err = store.Delete(t.Context(), created.URN())
	require.ErrorContains(t, err, "unavailable")
	deleting, err := storage.Fetch[*storage.Blob](t.Context(), store, created.URN())
	require.NoError(t, err)
	require.Equal(t, state.Deleting, deleting.State)
	_, err = deleting.Read(t.Context())
	require.ErrorContains(t, err, "deleting")
	updatedAt := deleting.UpdatedAt
	_, err = store.Delete(t.Context(), created.URN())
	require.ErrorContains(t, err, "unavailable")
	deleting, err = storage.Fetch[*storage.Blob](t.Context(), store, created.URN())
	require.NoError(t, err)
	require.Equal(t, updatedAt, deleting.UpdatedAt)

	files.deleteErr = nil
	require.NoError(t, store.Recover(t.Context()))
	_, err = storage.Fetch[*storage.Blob](t.Context(), store, created.URN())
	require.ErrorIs(t, err, storage.ErrNotFound)
}

func TestBlobReferences(t *testing.T) {
	backend := sqlite.OpenEphemeral(newRegistry())
	t.Cleanup(func() { require.NoError(t, backend.Close()) })
	store := storage.NewStore(backend, &storage.Memory{})
	blob, err := store.Upload(t.Context(), storage.URN{Tenant: "acme", Namespace: "default"}, "text/plain", []byte("linked"))
	require.NoError(t, err)
	_, err = storage.Create[*conversationObject](t.Context(), store, func(v *conversationObject) error {
		v.Attachments = []storage.URN{blob.URN()}
		return nil
	}, "acme", "default")
	require.NoError(t, err)
	_, err = store.Delete(t.Context(), blob.URN())
	require.ErrorIs(t, err, storage.ErrConflict)
}

func TestBlobDeleteCancellation(t *testing.T) {
	backend := sqlite.OpenEphemeral(newRegistry())
	t.Cleanup(func() { require.NoError(t, backend.Close()) })
	store := storage.NewStore(backend, &storage.Memory{})
	blob, err := store.Upload(t.Context(), storage.URN{Tenant: "acme", Namespace: "default"}, "text/plain", []byte("cancel me"))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err = store.Delete(ctx, blob.URN())

	require.ErrorIs(t, err, context.Canceled)
	deleting, err := storage.Fetch[*storage.Blob](t.Context(), store, blob.URN())
	require.NoError(t, err)
	require.Equal(t, state.Active, deleting.State)
}

func TestBlobValidation(t *testing.T) {
	backend := sqlite.OpenEphemeral(newRegistry())
	t.Cleanup(func() { require.NoError(t, backend.Close()) })
	store := storage.NewStore(backend, &storage.Memory{})

	t.Run("contentType", func(t *testing.T) {
		_, err := store.Upload(context.Background(), storage.URN{Tenant: "acme", Namespace: "default"}, "image/png", []byte("not an image"))
		require.ErrorContains(t, err, "does not match")
	})
	t.Run("invalidContentType", func(t *testing.T) {
		_, err := store.Upload(t.Context(), storage.URN{Tenant: "acme", Namespace: "default"}, "not a media type", []byte("x"))
		require.ErrorContains(t, err, "invalid content type")
	})
	t.Run("legacyContentType", func(t *testing.T) {
		blob, err := store.Upload(t.Context(), storage.URN{Tenant: "acme", Namespace: "default"}, "application/vnd.noeti.document", []byte("x"))
		require.NoError(t, err)
		assert.Equal(t, storage.CompressionZstd, blob.Compression)
	})
	t.Run("rawContent", func(t *testing.T) {
		blob, err := store.Upload(t.Context(), storage.URN{Tenant: "acme", Namespace: "default"}, "application/octet-stream", []byte{0, 1, 2})
		require.NoError(t, err)
		assert.Equal(t, storage.CompressionRaw, blob.Compression)
		data, err := blob.Read(t.Context())
		require.NoError(t, err)
		assert.Equal(t, []byte{0, 1, 2}, data)
	})
	t.Run("textual application content", func(t *testing.T) {
		_, err := store.Upload(t.Context(), storage.URN{Tenant: "acme", Namespace: "default"}, "application/yaml", []byte("name: Demo\n"))
		require.NoError(t, err)
	})
	t.Run("tooLarge", func(t *testing.T) {
		_, err := store.Upload(context.Background(), storage.URN{Tenant: "acme", Namespace: "default"}, "text/plain", bytes.Repeat([]byte{'x'}, storage.MaxSize+1))
		require.ErrorContains(t, err, "exceeds")
	})
	t.Run("nilStore", func(t *testing.T) {
		var nilStore *storage.Store
		_, err := nilStore.Upload(t.Context(), storage.URN{Tenant: "acme", Namespace: "default"}, "text/plain", []byte("x"))
		require.ErrorContains(t, err, "not configured")
	})
	t.Run("unconfigured", func(t *testing.T) {
		_, err := storage.NewStore(backend, nil).Upload(t.Context(), storage.URN{Tenant: "acme", Namespace: "default"}, "text/plain", []byte("x"))
		require.ErrorContains(t, err, "not configured")
	})
	t.Run("writeError", func(t *testing.T) {
		files := &failingFiles{writeErr: assert.AnError}
		store := storage.NewStore(backend, files)
		_, err := store.Upload(t.Context(), storage.URN{Tenant: "acme", Namespace: "default"}, "text/plain", []byte("x"))
		require.ErrorIs(t, err, assert.AnError)
	})
	t.Run("insertError", func(t *testing.T) {
		files := &storage.Memory{}
		wrapped := &insertErrorStorage{Storage: backend, err: assert.AnError}
		store := storage.NewStore(wrapped, files)
		_, err := store.Upload(t.Context(), storage.URN{Tenant: "acme", Namespace: "default"}, "text/plain", []byte("x"))
		require.ErrorIs(t, err, assert.AnError)
		_, err = fs.ReadFile(files, "blobs/acme/default/missing")
		assert.Error(t, err)
	})
}

func TestBlobRead(t *testing.T) {
	backend := sqlite.OpenEphemeral(newRegistry())
	t.Cleanup(func() { require.NoError(t, backend.Close()) })
	files := &storage.Memory{}
	store := storage.NewStore(backend, files)

	created, err := store.Upload(t.Context(), storage.URN{Tenant: "acme", Namespace: "default"}, "text/plain", []byte("hello blob"))
	require.NoError(t, err)

	t.Run("nilBlob", func(t *testing.T) {
		var blob *storage.Blob
		_, err := blob.Read(t.Context())
		require.ErrorIs(t, err, storage.ErrInvalid)
		assert.Contains(t, err.Error(), "blob is required")
	})

	t.Run("unconfigured", func(t *testing.T) {
		blob := &storage.Blob{Meta: created.Meta, ObjectKey: created.ObjectKey, Size: created.Size, StoredSize: created.StoredSize, SHA256: created.SHA256, Compression: created.Compression}
		_, err := blob.Read(t.Context())
		require.ErrorContains(t, err, "not configured")
	})

	t.Run("deleting", func(t *testing.T) {
		blob, err := storage.Fetch[*storage.Blob](t.Context(), store, created.URN())
		require.NoError(t, err)
		blob.State = state.Deleting
		_, err = blob.Read(t.Context())
		require.ErrorIs(t, err, storage.ErrDeleting)
	})

	t.Run("invalidSize", func(t *testing.T) {
		blob, err := storage.Fetch[*storage.Blob](t.Context(), store, created.URN())
		require.NoError(t, err)
		blob.Size = -1
		_, err = blob.Read(t.Context())
		require.ErrorContains(t, err, "invalid size metadata")

		blob, err = storage.Fetch[*storage.Blob](t.Context(), store, created.URN())
		require.NoError(t, err)
		blob.Size = storage.MaxSize + 1
		_, err = blob.Read(t.Context())
		require.ErrorContains(t, err, "invalid size metadata")

		blob, err = storage.Fetch[*storage.Blob](t.Context(), store, created.URN())
		require.NoError(t, err)
		blob.StoredSize = -1
		_, err = blob.Read(t.Context())
		require.ErrorContains(t, err, "invalid size metadata")
	})

	t.Run("storedSizeMismatch", func(t *testing.T) {
		blob, err := storage.Fetch[*storage.Blob](t.Context(), store, created.URN())
		require.NoError(t, err)
		blob.StoredSize = blob.StoredSize + 1
		_, err = blob.Read(t.Context())
		require.ErrorContains(t, err, "stored size mismatch")
	})

	t.Run("contentSizeMismatch", func(t *testing.T) {
		blob, err := storage.Fetch[*storage.Blob](t.Context(), store, created.URN())
		require.NoError(t, err)
		blob.Size = blob.Size + 1
		_, err = blob.Read(t.Context())
		require.ErrorContains(t, err, "content size mismatch")
	})

	t.Run("checksumMismatch", func(t *testing.T) {
		blob, err := storage.Fetch[*storage.Blob](t.Context(), store, created.URN())
		require.NoError(t, err)
		blob.SHA256 = hex.EncodeToString(sha256.New().Sum(nil))
		_, err = blob.Read(t.Context())
		require.ErrorContains(t, err, "checksum mismatch")
	})

	t.Run("missingContent", func(t *testing.T) {
		blob, err := storage.Fetch[*storage.Blob](t.Context(), store, created.URN())
		require.NoError(t, err)
		blob.ObjectKey = "missing"
		_, err = blob.Read(t.Context())
		require.ErrorContains(t, err, "open content")
	})

	t.Run("unsupportedCompression", func(t *testing.T) {
		blob, err := storage.Fetch[*storage.Blob](t.Context(), store, created.URN())
		require.NoError(t, err)
		blob.Compression = "unsupported"
		_, err = blob.Read(t.Context())
		require.ErrorContains(t, err, "unsupported compression")
	})

	t.Run("canceled", func(t *testing.T) {
		blob, err := storage.Fetch[*storage.Blob](t.Context(), store, created.URN())
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err = blob.Read(ctx)
		require.ErrorIs(t, err, context.Canceled)
	})
}

func TestBlobRecover(t *testing.T) {
	t.Run("unconfigured", func(t *testing.T) {
		require.ErrorContains(t, (*storage.Store)(nil).Recover(t.Context()), "not configured")
		backend := sqlite.OpenEphemeral(newRegistry())
		t.Cleanup(func() { require.NoError(t, backend.Close()) })
		require.ErrorContains(t, storage.NewStore(backend, nil).Recover(t.Context()), "not configured")
	})

	t.Run("deleting", func(t *testing.T) {
		backend := sqlite.OpenEphemeral(newRegistry())
		t.Cleanup(func() { require.NoError(t, backend.Close()) })
		store := storage.NewStore(backend, &storage.Memory{})
		created, err := store.Upload(t.Context(), storage.URN{Tenant: "acme", Namespace: "default"}, "text/plain", []byte("recover me"))
		require.NoError(t, err)

		next := *created
		next.State = state.Deleting
		_, err = storage.Update(t.Context(), store, &next)
		require.NoError(t, err)

		require.NoError(t, store.Recover(t.Context()))
		_, err = storage.Fetch[*storage.Blob](t.Context(), store, created.URN())
		require.ErrorIs(t, err, storage.ErrNotFound)
	})

	t.Run("recoveryError", func(t *testing.T) {
		backend := sqlite.OpenEphemeral(newRegistry())
		t.Cleanup(func() { require.NoError(t, backend.Close()) })
		files := &failingFiles{deleteErr: assert.AnError}
		store := storage.NewStore(backend, files)
		created, err := store.Upload(t.Context(), storage.URN{Tenant: "acme", Namespace: "default"}, "text/plain", []byte("recover error"))
		require.NoError(t, err)
		next := *created
		next.State = state.Deleting
		_, err = storage.Update(t.Context(), store, &next)
		require.NoError(t, err)
		assert.ErrorIs(t, store.Recover(t.Context()), assert.AnError)
	})

	t.Run("serializes recovery", func(t *testing.T) {
		backend := sqlite.OpenEphemeral(newRegistry())
		t.Cleanup(func() { require.NoError(t, backend.Close()) })
		files := &blockingFiles{entered: make(chan struct{}), release: make(chan struct{})}
		store := storage.NewStore(backend, files)
		created, err := store.Upload(t.Context(), storage.URN{Tenant: "acme", Namespace: "default"}, "text/plain", []byte("recover once"))
		require.NoError(t, err)
		next := *created
		next.State = state.Deleting
		_, err = storage.Update(t.Context(), store, &next)
		require.NoError(t, err)

		first := make(chan error, 1)
		second := make(chan error, 1)
		go func() { first <- store.Recover(t.Context()) }()
		<-files.entered
		go func() { second <- store.Recover(t.Context()) }()
		select {
		case <-second:
			t.Fatal("second recovery completed while the first held the lease")
		case <-time.After(25 * time.Millisecond):
		}

		close(files.release)
		require.NoError(t, <-first)
		require.NoError(t, <-second)
		assert.Equal(t, int32(1), files.calls.Load())
	})
}

type failingFiles struct {
	storage.Memory
	deleteErr error
	writeErr  error
}

type insertErrorStorage struct {
	storage.Storage
	err error
}

func (s *insertErrorStorage) Insert(context.Context, storage.Object) (storage.Object, error) {
	return nil, s.err
}

type blockingFiles struct {
	storage.Memory
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (f *blockingFiles) Open(key string) (fs.File, error) { return f.Memory.Open(key) }

func (f *blockingFiles) Delete(ctx context.Context, key string) error {
	if f.calls.Add(1) == 1 {
		close(f.entered)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-f.release:
		}
	}
	return f.Memory.Delete(ctx, key)
}

func (f *failingFiles) Open(key string) (fs.File, error) { return f.Memory.Open(key) }

func (f *failingFiles) Write(ctx context.Context, key string, data []byte) (string, error) {
	if f.writeErr != nil {
		return "", f.writeErr
	}
	return f.Memory.Write(ctx, key, data)
}

func (f *failingFiles) Delete(ctx context.Context, key string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	return f.Memory.Delete(ctx, key)
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }
