package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"strings"
	"sync"
	"testing/fstest"
	"time"

	"github.com/kelindar/storage/state"
	"github.com/klauspost/compress/zstd"
)

const (
	// KindBlob identifies binary resources.
	KindBlob Kind = "blob"
)

// Compression describes the persisted encoding of a Blob.
type Compression string

const (
	CompressionRaw  Compression = "raw"
	CompressionZstd Compression = "zstd"
)

// Blob is immutable binary content stored outside the resource database.
// Storage details are retained only in the persisted representation.
type Blob struct {
	Meta        `kind:"blob" json:",inline"`
	ContentType string      `json:"contentType" form:"ro"`
	Size        int64       `json:"size" form:"ro"`
	ObjectKey   string      `json:"-" store:"objectKey" form:"-"`
	SHA256      string      `json:"-" store:"sha256" form:"-"`
	StoredSize  int64       `json:"-" store:"storedSize" form:"-"`
	Compression Compression `json:"-" store:"compression" form:"-"`
	files       fs.FS       `json:"-"`
}

func (b *Blob) Title() string    { return b.ID }
func (b *Blob) Subtitle() string { return b.ContentType }

func (b *Blob) setFS(files fs.FS) { b.files = files }

// Files is the object filesystem used for Blob content.
type Files interface {
	fs.FS
	Write(context.Context, string, []byte) (string, error)
	Delete(context.Context, string) error
}

// Memory is a zero-value in-memory object filesystem for tests.
type Memory struct {
	mu    sync.RWMutex
	files fstest.MapFS
}

func (m *Memory) Open(name string) (fs.File, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.files.Open(name)
}

func (m *Memory) Write(ctx context.Context, name string, data []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !fs.ValidPath(name) || name == "." {
		return "", &fs.PathError{Op: "write", Path: name, Err: fs.ErrInvalid}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.files == nil {
		m.files = make(fstest.MapFS)
	}
	m.files[name] = &fstest.MapFile{Data: bytes.Clone(data)}
	return "", nil
}

func (m *Memory) Delete(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.files, name)
	return nil
}

// MaxSize is the largest uncompressed Blob payload.
const MaxSize = 64 << 20

// Upload stores immutable content.
func (s *Store) Upload(ctx context.Context, scope URN, contentType string, data []byte) (*Blob, error) {
	switch {
	case s == nil || s.Storage == nil || s.files == nil:
		return nil, errors.New("blob: store is not configured")
	case len(data) > MaxSize:
		return nil, fmt.Errorf("%w: content exceeds %d bytes", ErrInvalid, MaxSize)
	}
	contentType, err := normalizeContentType(contentType, data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	blob, err := New[*Blob](scope.Tenant, scope.Namespace)
	if err != nil {
		return nil, err
	}
	stored, compression, err := encode(contentType, data)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	blob.ContentType = contentType
	blob.Size = int64(len(data))
	blob.ObjectKey = blobKey(blob.URN())
	blob.SHA256 = hex.EncodeToString(sum[:])
	blob.StoredSize = int64(len(stored))
	blob.Compression = compression
	if _, err := s.files.Write(ctx, blob.ObjectKey, stored); err != nil {
		return nil, fmt.Errorf("blob: store content: %w", err)
	}
	created, err := Insert(ctx, s, blob)
	if err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		cleanup := s.files.Delete(cleanupCtx, blob.ObjectKey)
		return nil, errors.Join(err, cleanup)
	}
	created.setFS(s.files)
	return created, nil
}

// Read returns verified original bytes.
func (b *Blob) Read(ctx context.Context) ([]byte, error) {
	switch {
	case b == nil:
		return nil, fmt.Errorf("%w: blob is required", ErrInvalid)
	case b.files == nil:
		return nil, errors.New("blob: store is not configured")
	case b.State == state.Deleting:
		return nil, ErrDeleting
	case b.Size < 0 || b.Size > MaxSize || b.StoredSize < 0:
		return nil, errors.New("blob: invalid size metadata")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := b.files.Open(b.ObjectKey)
	if err != nil {
		return nil, fmt.Errorf("blob: open content: %w", err)
	}
	defer file.Close()
	stored, err := io.ReadAll(io.LimitReader(file, b.StoredSize+1))
	switch {
	case err != nil:
		return nil, fmt.Errorf("blob: read content: %w", err)
	case int64(len(stored)) != b.StoredSize:
		return nil, errors.New("blob: stored size mismatch")
	}
	data, err := decode(b.Compression, stored)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	switch {
	case int64(len(data)) != b.Size:
		return nil, errors.New("blob: content size mismatch")
	case !strings.EqualFold(hex.EncodeToString(sum[:]), b.SHA256):
		return nil, errors.New("blob: checksum mismatch")
	}
	return data, nil
}

// WriteTo writes verified original bytes to dst.
func (b *Blob) WriteTo(ctx context.Context, dst io.Writer) (int64, error) {
	data, err := b.Read(ctx)
	if err != nil {
		return 0, err
	}
	return io.Copy(dst, bytes.NewReader(data))
}

func (s *Store) deleteBlob(ctx context.Context, urn URN) (Object, error) {
	if s == nil || s.Storage == nil || s.files == nil {
		return nil, errors.New("blob: store is not configured")
	}
	links, err := s.Links(ctx, urn)
	switch {
	case err != nil:
		return nil, err
	case len(links) != 0:
		return nil, fmt.Errorf("%w: blob is referenced", ErrConflict)
	}
	if err := s.markDeleting(ctx, urn); err != nil {
		return nil, err
	}
	return s.remove(ctx, urn)
}

func (s *Store) markDeleting(ctx context.Context, urn URN) error {
	blob, err := Fetch[*Blob](ctx, s.Storage, urn)
	switch {
	case err != nil:
		return err
	case blob.State == state.Deleting:
		return nil
	}
	_, err = Patch[*Blob](ctx, s.Storage, urn, func(blob *Blob) error {
		blob.State = state.Deleting
		return nil
	})
	return err
}

func (s *Store) remove(ctx context.Context, urn URN) (Object, error) {
	blob, err := Fetch[*Blob](ctx, s.Storage, urn)
	switch {
	case err != nil:
		return nil, err
	case blob.State != state.Deleting:
		return nil, fmt.Errorf("blob: %s is not deleting", urn)
	}
	if err := s.files.Delete(ctx, blob.ObjectKey); err != nil {
		return nil, fmt.Errorf("blob: delete content: %w", err)
	}
	return s.Storage.Delete(ctx, urn)
}

// Recover removes files and metadata for Blobs left in the deleting state.
func (s *Store) Recover(ctx context.Context) error {
	if s == nil || s.Storage == nil || s.files == nil {
		return errors.New("blob: store is not configured")
	}
	lock, unlock, err := s.Lock(ctx, "blob-recovery")
	if err != nil {
		return err
	}
	defer unlock()
	ctx = lock

	cursor, err := Search[*Blob](ctx, s.Storage, Query{States: []string{state.Deleting}})
	if err != nil {
		return fmt.Errorf("blob: recover list: %w", err)
	}
	var errs []error
	for _, blob := range Collect(cursor, nil) {
		if _, err := s.remove(ctx, blob.URN()); err != nil {
			errs = append(errs, fmt.Errorf("blob: recover %s: %w", blob.URN(), err))
		}
	}
	return errors.Join(errs...)
}

func blobKey(urn URN) string {
	return "blobs/" + urn.Tenant + "/" + urn.Namespace + "/" + urn.ID
}

func normalizeContentType(declared string, data []byte) (string, error) {
	declared = strings.TrimSpace(strings.ToLower(declared))
	if declared != "" {
		parsed, _, err := mime.ParseMediaType(declared)
		if err != nil || parsed == "" {
			return "", errors.New("blob: invalid content type")
		}
		declared = parsed
	}
	detected := strings.ToLower(http.DetectContentType(data))
	detected, _, _ = mime.ParseMediaType(detected)
	switch {
	case declared == "":
		return detected, nil
	case detected != "application/octet-stream" && !compatibleContentTypes(declared, detected):
		return "", fmt.Errorf("blob: declared content type %q does not match content %q", declared, detected)
	}
	return declared, nil
}

func compatibleContentTypes(declared, detected string) bool {
	return declared == detected || declared == "application/octet-stream" ||
		strings.HasPrefix(declared, "application/vnd.noeti.") ||
		(detected == "text/plain" && compressible(declared)) ||
		(strings.HasPrefix(declared, "text/") && strings.HasPrefix(detected, "text/"))
}

func encode(contentType string, data []byte) ([]byte, Compression, error) {
	if !compressible(contentType) {
		return bytes.Clone(data), CompressionRaw, nil
	}
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		return nil, "", err
	}
	defer encoder.Close()
	return encoder.EncodeAll(data, nil), CompressionZstd, nil
}

func decode(compression Compression, stored []byte) ([]byte, error) {
	switch compression {
	case CompressionRaw:
		return bytes.Clone(stored), nil
	case CompressionZstd:
		decoder, err := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(MaxSize))
		if err != nil {
			return nil, err
		}
		defer decoder.Close()
		data, err := decoder.DecodeAll(stored, nil)
		switch {
		case err != nil:
			return nil, fmt.Errorf("blob: decompress content: %w", err)
		case len(data) > MaxSize:
			return nil, fmt.Errorf("blob: content exceeds %d bytes", MaxSize)
		}
		return data, nil
	default:
		return nil, fmt.Errorf("blob: unsupported compression %q", compression)
	}
}

func compressible(contentType string) bool {
	return strings.HasPrefix(contentType, "text/") || contentType == "application/json" ||
		strings.HasSuffix(contentType, "+json") || strings.HasSuffix(contentType, "+xml") ||
		contentType == "application/xml" || contentType == "application/yaml" ||
		contentType == "application/toml" || strings.HasPrefix(contentType, "application/vnd.noeti.")
}
