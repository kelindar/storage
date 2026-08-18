package storage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"iter"
	"reflect"
	"time"

	"github.com/kelindar/async"
)

// Store joins resource storage with its file backend.
type Store struct {
	Storage
	files Files
	sweep sweeper
}

// NewStore joins resource storage with its file backend.
func NewStore(store Storage, files Files) *Store {
	return &Store{Storage: store, files: files}
}

// Start starts storage background processes.
func (s *Store) Start(ctx context.Context, deleteResource func(context.Context, URN) error) {
	if s == nil {
		return
	}
	s.sweep.start(ctx, s, deleteResource)
}

// Close stops background processes before closing storage.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	return s.sweep.stop(s.Storage)
}

type fileBinder interface {
	setFS(fs.FS)
}

// Fetch retrieves a resource and binds its file backend when needed.
func (s *Store) Fetch(ctx context.Context, urn URN) (Object, error) {
	object, err := s.Storage.Fetch(ctx, urn)
	if err != nil {
		return nil, err
	}
	return s.bind(object), nil
}

// Search retrieves resources and binds their file backend when needed.
func (s *Store) Search(ctx context.Context, kind Kind, query Query) (iter.Seq[Object], error) {
	objects, err := s.Storage.Search(ctx, kind, query)
	if err != nil {
		return nil, err
	}
	return func(yield func(Object) bool) {
		for object := range objects {
			if !yield(s.bind(object)) {
				return
			}
		}
	}, nil
}

// Insert stores a resource and binds its file backend when needed.
func (s *Store) Insert(ctx context.Context, object Object) (Object, error) {
	stored, err := s.Storage.Insert(ctx, object)
	if err != nil {
		return nil, err
	}
	return s.bind(stored), nil
}

// Update stores a resource and binds its file backend when needed.
func (s *Store) Update(ctx context.Context, object Object) (Object, error) {
	stored, err := s.Storage.Update(ctx, object)
	if err != nil {
		return nil, err
	}
	return s.bind(stored), nil
}

// Delete removes ordinary resources directly. Deleting a Blob first makes it
// unreadable, then removes its file and metadata. File failures leave a
// retryable deleting resource.
func (s *Store) Delete(ctx context.Context, urn URN) (Object, error) {
	switch {
	case s == nil || s.Storage == nil:
		return nil, errors.New("blob: store is not configured")
	case urn.Kind != KindBlob:
		return s.Storage.Delete(ctx, urn)
	}
	return s.deleteBlob(ctx, urn)
}

func (s *Store) bind(object Object) Object {
	if resource, ok := object.(fileBinder); ok {
		resource.setFS(s.files)
	}
	return object
}

// ---------------------------------- Generic ----------------------------------

// Create creates a new resource and inserts it into the storage.
func Create[T Object](ctx context.Context, db Storage, constructor func(obj T) error, tenant, namespace string) (T, error) {
	instance, err := New(tenant, namespace, constructor)
	if err != nil {
		return defaultOf[T](), err
	}

	return Insert(ctx, db, instance)
}

// Insert inserts a new resource into the storage.
func Insert[T Object](ctx context.Context, db Storage, v T) (T, error) {
	if err := assignID(v); err != nil {
		return defaultOf[T](), err
	}

	if err := setDefaultState(db, v); err != nil {
		return defaultOf[T](), err
	}

	out, err := db.Insert(ctx, v)
	if err != nil {
		return defaultOf[T](), err
	}

	return out.(T), nil
}

// Update updates an existing resource in the storage.
func Update[T Object](ctx context.Context, db Storage, v T) (T, error) {
	if err := canTransition(ctx, db, v); err != nil {
		return defaultOf[T](), err
	}

	out, err := db.Update(ctx, v)
	if err != nil {
		return defaultOf[T](), err
	}

	return out.(T), nil
}

// Patch fetches a resource, applies patch, and retries the update with a fresh
// version when a concurrent change wins, up to ten attempts. The patch callback
// may therefore run up to ten times and must only mutate the supplied object;
// it must be side-effect-free and safe to repeat.
func Patch[T Object](ctx context.Context, db Storage, urn URN, patch func(T) error) (T, error) {
	if patch == nil {
		return defaultOf[T](), fmt.Errorf("%w: patch function is required", ErrInvalid)
	}

	for attempts := 1; ; attempts++ {
		v, err := Fetch[T](ctx, db, urn)
		if err != nil {
			return defaultOf[T](), err
		}
		if err := patch(v); err != nil {
			return defaultOf[T](), err
		}
		if v.URN() != urn {
			return defaultOf[T](), fmt.Errorf("%w: patch cannot change object identity", ErrInvalid)
		}

		out, err := Update(ctx, db, v)
		if !IsConflict(err) || attempts == 10 {
			return out, err
		}
	}
}

// Overwrite updates using the latest stored version.
func Overwrite[T Object](ctx context.Context, db Storage, v T) (T, error) {
	current, err := db.Fetch(ctx, v.URN())
	if err != nil {
		return defaultOf[T](), err
	}
	copyVersion(v, current)
	return Update(ctx, db, v)
}

// Upsert inserts a resource, or patches the existing resource with the same
// URN when insertion conflicts. The patch callback is not called when the
// insert succeeds and may run more than once when it patches an existing row.
func Upsert[T Object](ctx context.Context, db Storage, v T, patch func(T) error) (T, error) {
	if patch == nil {
		return defaultOf[T](), fmt.Errorf("%w: upsert patch function is required", ErrInvalid)
	}

	inserted, err := Insert(ctx, db, v)
	switch {
	case err == nil:
		return inserted, nil
	case !IsConflict(err):
		return defaultOf[T](), err
	}

	updated, patchErr := Patch(ctx, db, v.URN(), patch)
	if IsNotFound(patchErr) {
		return defaultOf[T](), err
	}
	return updated, patchErr
}

// Delete deletes a resource from the storage and returns the deleted object.
func Delete[T Object](ctx context.Context, db Storage, urn URN) (T, error) {
	out, err := db.Delete(ctx, urn)
	if err != nil {
		return defaultOf[T](), err
	}

	return out.(T), nil
}

// Fetch attempts to find a specific document in the storage layer.
func Fetch[T Object](ctx context.Context, db Storage, urn URN) (T, error) {
	v, err := db.Fetch(ctx, urn)
	if err != nil {
		return defaultOf[T](), err
	}

	return v.(T), nil
}

// Search performs a query against the storage layer.
func Search[T Object](ctx context.Context, db Storage, q Query) (iter.Seq[T], error) {
	kind, err := KindOfT[T]()
	if err != nil {
		return nil, err
	}

	cursor, err := db.Search(ctx, kind, q)
	if err != nil {
		return nil, err
	}

	return func(yield func(T) bool) {
		for v := range cursor {
			if next := yield(v.(T)); !next {
				return
			}
		}
	}, nil
}

// Changes starts bounded, durable changes for T with the named persistent
// consumer. Wait observes its terminal error. The callback is retried until it
// succeeds or ctx is canceled.
func Changes[T Object](ctx context.Context, db Storage, consumer string, after time.Time, handle func(context.Context, []Change) error) async.Awaiter {
	kind, err := KindOfT[T]()
	if err != nil {
		return async.Failed[struct{}](err)
	}
	kind = Kind(kind.String())
	switch {
	case consumer == "":
		return async.Failed[struct{}](fmt.Errorf("%w: change consumer is required", ErrInvalid))
	case kind == "":
		return async.Failed[struct{}](fmt.Errorf("%w: kind is required", ErrInvalid))
	case handle == nil:
		return async.Failed[struct{}](fmt.Errorf("%w: change handler is required", ErrInvalid))
	}
	return async.Invoke(ctx, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, db.Changes(ctx, consumer, kind, after, handle)
	})
}

// Collect drains a search iterator, keeping items for which where returns true.
// A nil where keeps every item. Use this before running nested queries on
// SQLite, which only allows one open cursor at a time.
func Collect[T Object](seq iter.Seq[T], where func(T) bool) []T {
	return Select(seq, func(item T) (T, bool) {
		return item, where == nil || where(item)
	})
}

// Select drains a search iterator, projecting items for which where returns true.
func Select[T Object, P any](seq iter.Seq[T], where func(T) (P, bool)) []P {
	out := make([]P, 0, 64)
	for item := range seq {
		if projected, ok := where(item); ok {
			out = append(out, projected)
		}
	}
	return out
}

// Count returns the number of records that match the specified query.
func Count[T Object](ctx context.Context, db Storage, q Query) (int, error) {
	kind, err := KindOfT[T]()
	if err != nil {
		return 0, err
	}

	return db.Count(ctx, kind, q)
}

// Next advances the sequence named by T's resource kind and returns its new value.
func Next[T Object](ctx context.Context, db Storage) (uint32, error) {
	kind, err := KindOfT[T]()
	if err != nil {
		return 0, err
	}
	return db.Next(ctx, string(kind))
}

// defaultOf returns the default value for the specified type
func defaultOf[T any]() T {
	var v T
	return v
}

func metaOf(v Object) *Meta {
	return metaValue(reflect.ValueOf(v), objectTypeOf(reflect.TypeOf(v)))
}
func copyVersion(dst, src Object) {
	dstMeta := metaOf(dst)
	srcMeta := metaOf(src)
	dstMeta.CreatedBy = srcMeta.CreatedBy
	dstMeta.CreatedAt = srcMeta.CreatedAt
	dstMeta.UpdatedBy = srcMeta.UpdatedBy
	dstMeta.UpdatedAt = srcMeta.UpdatedAt
}

func setDefaultState(db Storage, v Object) error {
	typ, err := db.Registry().Resolve(v.URN().Kind)
	if err != nil || len(typ.States) == 0 {
		return nil
	}

	meta := metaOf(v)
	if meta.State == "" {
		meta.State = typ.States.Default()
	}
	return nil
}

func canTransition(ctx context.Context, db Storage, v Object) error {
	typ, err := db.Registry().Resolve(v.URN().Kind)
	if err != nil || len(typ.States) == 0 {
		return nil
	}

	current, err := db.Fetch(ctx, v.URN())
	if err != nil {
		return err
	}

	from, to := current.Status(), v.Status()
	switch {
	case from == to || typ.States.CanTransition(from, to):
		return nil
	default:
		return fmt.Errorf("%w: %q -> %q", ErrInvalidTransition, from, to)
	}
}
