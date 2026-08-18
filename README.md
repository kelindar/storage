# Typed Object Storage for Go

<p align="center">
    <img src="https://img.shields.io/github/go-mod/go-version/kelindar/storage" alt="Go Version">
    <a href="https://pkg.go.dev/github.com/kelindar/storage"><img src="https://pkg.go.dev/badge/github.com/kelindar/storage" alt="PkgGoDev"></a>
    <a href="https://goreportcard.com/report/github.com/kelindar/storage"><img src="https://goreportcard.com/badge/github.com/kelindar/storage" alt="Go Report Card"></a>
    <a href="https://opensource.org/licenses/MIT"><img src="https://img.shields.io/badge/License-MIT-blue.svg" alt="License"></a>
    <a href="https://coveralls.io/github/kelindar/storage"><img src="https://coveralls.io/repos/github/kelindar/storage/badge.svg" alt="Coverage"></a>
</p>

This is a small, typed resource store for Go. It keeps application objects as JSON documents in SQLite or PostgreSQL and covers the pieces that tend to get rebuilt in every service: queries, links, optimistic updates, locks, change feeds, sequences, lifecycle state, validation, and blobs.

The idea is simple: define a struct, embed `Meta`, give it a kind, and register it. After that, the same API is used to create, query, link, expire, and delete resources.

Storage is not an ORM or an object database. The structs remain ordinary Go values. The drivers store their JSON documents alongside the fields needed for identity, state, timestamps, and indexes. In other words, objects in Go, documents on disk, and relational metadata around them.

There are no generated models, sessions, or `Save` methods. Create, fetch, update, and search are explicit operations.

- **Typed.** Resources are regular Go structs with a `storage.Meta` field.
- **Multi-tenant.** Every resource belongs to a tenant, namespace, kind, and generated ID.
- **Durable.** SQLite and PostgreSQL drivers provide transactions, migrations, locks, changes, and sequences.
- **Queryable.** Filter, sort, page, search, and count resources without writing SQL.
- **Composable.** Links, state machines, validation, conversion helpers, and blobs are optional.

# Installation

Requires Go 1.25 or newer. Install the core package and one of the drivers:

```sh
go get github.com/kelindar/storage
go get github.com/kelindar/storage/driver/sqlite
# or:
go get github.com/kelindar/storage/driver/pgsql
```

The SQLite and PostgreSQL drivers are separate Go modules. Each one depends directly on the core package, so it can be used without a `replace` directive.

# Quick start

This is the whole flow: define a resource, register it, open a driver, and use the generic helpers.

```go
package main

import (
	"context"
	"fmt"

	"github.com/kelindar/storage"
	"github.com/kelindar/storage/driver/sqlite"
)

type Document struct {
	storage.Meta `kind:"document" json:",inline"`
	Title        string `json:"title"`
}

func main() {
	registry := storage.NewRegistry()
	storage.MustRegister[*Document](registry)

	db, err := sqlite.Open("storage.db", registry)
	if err != nil {
		panic(err)
	}
	defer db.Close()

	ctx := context.Background()
	document, err := storage.Create[*Document](ctx, db, func(document *Document) error {
		document.Title = "Hello, storage"
		return nil
	}, "acme", "default")
	if err != nil {
		panic(err)
	}

	rows, err := storage.Search[*Document](ctx, db, storage.Query{
		Tenant: "acme",
		Limit:  20,
	})
	if err != nil {
		panic(err)
	}
	for document := range rows {
		fmt.Println(document.ID, document.Title)
	}
}
```

# Resources

Resources are plain Go values. Embedding `storage.Meta` gives them the `storage.Object` interface:

```go
type Object interface {
	URN() storage.URN
	Status() string
	Created() (string, time.Time)
	Updated() (string, time.Time)
}
```

`Meta` contains the fields shared by every resource:

- `ID`, `Kind`, `Tenant`, and `Namespace` identify it.
- `State` holds its current lifecycle state.
- `CreatedBy`, `CreatedAt`, `UpdatedBy`, and `UpdatedAt` record mutations.
- `ExpiresAt` is an optional Unix-nanosecond deadline.

`storage.New[T]` creates a new object with a generated ID. `NewByType`, `KindOf`, and `KindOfT` are useful when the concrete type is only known at runtime. IDs and kinds are also available through the object's `URN`.

## Registry and metadata

Register every resource type before opening a driver. A driver creates one table per registered kind during migration. `storage.Blob` must be registered too when the application stores blobs.

The registry can enumerate types, resolve a kind, and look up fields through `Type.Field`.

`storage.Options` holds application metadata for a resource type:

- `Icon`, `Title`, `Plural`, and `Sort` describe how it is presented.
- `States` defines an optional lifecycle state machine.
- `Actions` contains permission names.
- `Workflows` contains application workflow names.

Actions and workflows are only metadata. Storage does not authorize an action or execute a workflow. When `Actions` is empty, `DefaultActions` is used.

## Actors

Put the actor responsible for a mutation in the context:

```go
ctx := storage.WithActor(context.Background(), "user:123")
created, err := storage.Insert(ctx, db, document)
```

`storage.Actor(ctx)` returns the value later. If no actor is set, it returns `storage.UnknownActor`; `storage.SystemActor` is available for background work.

# Drivers

Both drivers implement the same `storage.Storage` interface and auto-migrate their shared tables and the tables for registered resources.

## SQLite

The SQLite driver lives in `github.com/kelindar/storage/driver/sqlite`:

```go
db, err := sqlite.Open("app.db", registry)
if err != nil {
	return err
}
defer db.Close()

testDB := sqlite.OpenEphemeral(registry)
defer testDB.Close()
```

`sqlite.Open` uses the cgo-free `ncruces/go-sqlite3` driver. `OpenEphemeral` opens an in-memory database and is handy in tests.

When FTS5 is available, SQLite uses it for `Query.Match`. Otherwise, matching falls back to a case-insensitive substring search.

## PostgreSQL

The PostgreSQL driver lives in `github.com/kelindar/storage/driver/pgsql`:

```go
db, err := pgsql.Open("postgres://user:password@localhost/app?sslmode=disable", registry)
if err != nil {
	return err
}
defer db.Close()
```

`pgsql.Open` uses the pgx `database/sql` driver and owns the database handle. If the application already has a `*sql.DB`, use `pgsql.New` instead:

```go
sqlDB, err := sql.Open("pgx", dsn)
if err != nil {
	return err
}

db, err := pgsql.New(sqlDB, registry)
if err != nil {
	return err
}
defer db.Close() // does not close sqlDB
```

`pgsql.New` does not close the supplied `*sql.DB`. PostgreSQL `Query.Match` performs a case-insensitive substring search over the stored JSON; it does not provide SQLite-style FTS ranking.

The raw `Upload` method on either driver rejects blob content because a database backend does not include a file store. Wrap a driver with `storage.NewStore` when blobs are needed.

To write another backend, implement `storage.Storage`. It covers resource operations as well as links, search, changes, locks, uploads, and sequences. `storage.Store` adds a `storage.Files` implementation on top of it.

# Working with resources

## Create and update

Most applications only need a handful of operations:

- `storage.Create` constructs and inserts a resource.
- `storage.Insert` assigns a missing ID, applies the default state, and inserts it.
- `storage.Update` writes an existing resource with optimistic concurrency.
- `storage.Patch` fetches, changes, and updates a resource.
- `storage.Upsert` inserts a resource or patches the existing one after a conflict.
- `storage.Overwrite` refreshes the stored version before updating, so it can overwrite changes made by another writer.
- `storage.Fetch`, `storage.Delete`, `storage.Search`, and `storage.Count` are the direct read, delete, search, and count operations.

For example, create a document, then update it with a patch:

```go
document, err := storage.New[*Document]("acme", "default")
if err != nil {
	return err
}
document.Title = "First version"

document, err = storage.Insert(ctx, db, document)
if err != nil {
	return err
}

document, err = storage.Patch(ctx, db, document.URN(), func(document *Document) error {
	document.Title = "Updated version"
	return nil
})
```

Updates compare the `UpdatedAt` value read from the database. A stale object returns `storage.ErrConflict`.

`Patch` retries a conflict up to ten times. Its callback may run more than once, so it should only mutate the supplied object. Do not send mail, publish an event, or perform another external side effect from it. The same rule applies to the callback passed to `Upsert`.

Use the error helpers rather than matching error strings:

```go
if storage.IsNotFound(err) {
	// ...
}
if storage.IsConflict(err) {
	// refetch or retry
}
if storage.IsInvalidTransition(err) {
	// reject the requested state change
}
if storage.IsLockLost(err) {
	// stop work performed under the lease
}
```

`ErrInvalid`, `ErrDeleting`, and `ErrKindNotFound` are also available for `errors.Is` checks.

## Identity and targets

Every resource has a `storage.URN` in this form:

`urn:tenant:namespace:kind:id`

Use `NewURN` for a generated ID, `MakeURN` for a known ID, and `ParseURN` for an encoded value:

```go
urn, err := storage.NewURN("acme", "default", "document")
if err != nil {
	return err
}

parsed, err := storage.ParseURN(urn.String())
if err != nil {
	return err
}
_ = parsed.IsValid()
```

Tenants, namespaces, and kinds are normalized to lowercase and must use the package's slug format. IDs are 20-character xid-compatible values. URNs marshal to and from JSON strings.

`storage.Target` adds a selector to a URN. It supports `@draft`, `@latest`, and exact positive resource versions such as `@v3`:

```go
target, err := storage.ParseTarget(urn.String() + "@latest")
if err != nil {
	return err
}
fmt.Println(target.URN(), target.Ref(), target.IsLatest())
```

The target is only a selector; it does not create or store versions by itself.

## Queries

`storage.Query` filters and sorts a resource kind without exposing SQL to the caller:

```go
query := storage.Query{
	Tenant:     "acme",
	IDs:        []string{"id1", "id2"},
	Namespaces: []string{"default"},
	States:     []string{"active"},
	Indexes:    []string{"sample"},
	Filters: map[string][]string{
		"title":       {"release"},
		"description": {""}, // existence check
	},
	Match:        "deploy production",
	SortBy:       []string{"-updatedAt", "title"},
	Offset:       0,
	Limit:        50,
	UpdatedAfter: time.Now().Add(-time.Hour),
}
```

The fields cover the common cases:

- `Tenant`, `IDs`, and `Namespaces` scope the result set.
- `States` filters lifecycle states.
- `Indexes` filters the value returned by an optional `Index() string` method.
- `Filters` compare JSON paths.
- `Match` performs full-text or substring matching, depending on the driver.
- `SortBy` accepts `+field` for ascending and `-field` for descending order.
- `Offset` and `Limit` page the results.
- `CreatedBefore`, `UpdatedBefore`, and `UpdatedAfter` apply time bounds.

A filter with a value is an equality check. An empty value means “present and non-zero”: the field must exist and must not be empty, zero, or false. Nested fields can be addressed with paths such as `profile.email`.

`Search` returns an iterator. `Collect` drains it into a slice, while `Select` drains it and projects another value:

```go
rows, err := storage.Search[*Document](ctx, db, query)
if err != nil {
	return err
}

for document := range rows {
	fmt.Println(document.Title)
}

count, err := storage.Count[*Document](ctx, db, query)
if err != nil {
	return err
}

rows, _ = storage.Search[*Document](ctx, db, query)
titles := storage.Select(rows, func(document *Document) (string, bool) {
	return document.Title, document.Title != ""
})
```

`Count` does not accept sorting, offset, or limit.

### String queries

`ParseQuery` accepts semicolon-separated key/value pairs. This is useful when a query arrives from a URL or another text-based transport:

```go
query, err := storage.ParseQuery(
	"tenant=acme;namespace=default;state=active;"+
		"filter=title:release,description;"+
		"match={Title};sort=-updatedAt;limit=20;offset=0",
	document,
	storage.Query{},
)
```

The supported components are `tenant`, `id`, `namespace`, `state`, `index`, `filter`, `match`, `sort`, `limit`, `offset`, and `updatedAfter`. Multiple IDs, namespaces, states, and indexes are comma-separated. `namespace=*` removes the namespace restriction.

`match` can substitute fields from the supplied object with `{FieldName}`. Substitutions support strings, integers, floats, and booleans. Filters use `field:value` for equality or just `field` for existence. `Query.String()` produces a compact representation for logging and transport.

# JSON and YAML

The JSON helpers use the registry to find the concrete type from the `kind` field. The reader variants take an `io.Reader`:

```go
data, err := storage.ToJSON(document)
decoded, err := storage.FromJSON(registry, data)

decoded, err = storage.ReadJSON(registry, reader)
decoded, err = storage.FromYAML(registry, yamlData)
decoded, err = storage.ReadYAML(registry, reader)
```

`UnmarshalYAML` follows JSON struct tags. `ReadFile` reads from disk when its data argument is nil and chooses JSON, YAML, or YML from the file extension.

A `store` tag changes where a field is persisted without changing its public JSON shape:

```go
type SecretDocument struct {
	storage.Meta `kind:"secret_document" json:",inline"`
	Token        string `json:"-" store:"credentials.token"`
}
```

`ToJSON` writes `Token` at `credentials.token`, and `FromJSON` reads it from there. `store:"-"` removes a field from the stored representation. Nested structs, slices, arrays, and maps are supported.

For polymorphic embedded resources, use `storage.Embed`. It uses the registry and the embedded object's `kind` field to decode the concrete type.

# Links and lifecycle

## Links

A field tagged with `link:"kind"` produces dependency links to URNs or URN strings of that kind:

```go
type Conversation struct {
	storage.Meta `kind:"conversation" json:",inline"`
	Attachments  []storage.URN `json:"attachments" link:"blob"`
}
```

The link walker understands pointers, nested structs, slices, arrays, and maps. Paths use JSON field names and include indexes or map keys, such as `attachments.0`. A field with `json:"-"` or `link:"-"` is ignored.

Call `storage.Links(obj)` to extract the links declared by an object.

For links that are not represented by a tagged field, implement `storage.Linker`:

```go
func (b *Bundle) Links() ([]storage.Link, error) {
	return []storage.Link{
		storage.Own(b.URN(), target, storage.Path("resources.0")),
	}, nil
}
```

Use `storage.Use` for a dependency and `storage.Own` for exclusive ownership. Drivers rebuild outgoing links during inserts and updates. Call `db.Link(ctx, source)` to rebuild them explicitly, and `db.Links(ctx, target)` to list incoming links.

Links must have valid URNs, a non-empty path, the matching source, and the same tenant on both sides. Ownership is unique per target. Blobs cannot own resources, and only bundles may own non-blob resources. `Path.String`, `Path.Label`, `Path.Index`, `Path.ID`, and `Path.Walk` expose the link path when it needs to be inspected.

## Lifecycle state

An optional `state.Machine` describes the transitions allowed for a resource:

```go
states := state.Machine{
	"create":  "* -> draft",
	"publish": "draft -> active",
	"archive": "active -> inactive",
}

storage.MustRegister[*Document](registry, storage.Options{States: states})
```

The wildcard source supplies the default state. `Insert` and `Upsert` assign it when no state is set. `Update` and `Patch` reject invalid transitions with `ErrInvalidTransition`.

The state package also provides shared state names—`Creating`, `Active`, `Inactive`, `Deleting`, and `Failed`—along with `Machine.TryAction`, `Machine.CanTransition`, `Machine.Default`, `Machine.States`, and `Edge.Value`.

## Expiration

Set `Meta.ExpiresAt` to a Unix-nanosecond deadline:

```go
document.ExpiresAt = time.Now().Add(time.Hour).UnixNano()
document, err = storage.Update(ctx, db, document)
```

The sweeper needs a callback because deleting a resource is application-specific:

```go
store.Start(ctx, func(ctx context.Context, urn storage.URN) error {
	_, err := store.Delete(ctx, urn)
	return err
})
```

Expiration is eventual. The sweeper scans expired resources in pages at a randomized interval between one and two hours. Failed or blocked deletions are logged and tried again later. `Store.Start` also enables change-log retention cleanup, and `Store.Close` stops the sweeper before closing the wrapped storage.

# Locks, changes, and sequences

## Named locks

`Storage.Lock` acquires a renewable named lease:

```go
lockCtx, release, err := db.Lock(ctx, "rebuild-index")
if err != nil {
	return err
}
defer release()

// Use lockCtx for work that must stop if the lease is lost.
if err := rebuild(lockCtx); err != nil {
	return err
}
if err := context.Cause(lockCtx); storage.IsLockLost(err) {
	return err
}
```

Only one live owner can hold a name. The returned context is canceled if renewal fails or ownership is lost. Always release the lease and use the returned context for work protected by it.

## Durable changes

`storage.Changes[T]` starts a named, persistent consumer for one resource kind:

```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

worker := storage.Changes[*Document](
	ctx,
	db,
	"search-indexer",
	time.Time{},
	func(ctx context.Context, batch []storage.Change) error {
		for _, change := range batch {
			fmt.Println(change.Action, change.URN, change.At)
		}
		return nil
	},
)

if err := worker.Wait(); err != nil {
	return err
}
```

The cursor is durable and keyed by consumer name and kind. Delivery is at least once: if the callback returns an error, the batch is retried until it succeeds or the context is canceled. Make the callback idempotent. Batches are borrowed only for the callback, so do not retain or modify them.

The `after` time selects the starting point for a new consumer. The sweeper removes change history older than seven days; pruned history is not replayed.

Changes describe creates, updates, and deletes. They identify the resource and the time of the mutation; they do not contain a copy of the document.

## Sequences

`storage.Next[T]` advances a sequence named by the resource kind. For an arbitrary name, call `db.Next` directly:

```go
sequence, err := storage.Next[*Document](ctx, db)
named, err := db.Next(ctx, "invoice")
```

Sequences are durable, atomic, and return `uint32` values.

# Blobs

Blobs keep binary content in a `storage.Files` backend and their metadata in the resource database. Register the blob type before opening the driver:

```go
storage.MustRegister[*storage.Blob](registry, storage.Options{
	States: state.Machine{
		"create": "* -> active",
		"delete": "active -> deleting",
	},
})
```

Wrap the driver with `storage.NewStore`:

```go
backend, err := sqlite.Open("app.db", registry)
if err != nil {
	return err
}

files := &storage.Memory{} // zero-value in-memory filesystem
store := storage.NewStore(backend, files)
defer store.Close()

scope := storage.URN{Tenant: "acme", Namespace: "default"}
blob, err := store.Upload(ctx, scope, "text/plain", []byte("hello"))
if err != nil {
	return err
}

data, err := blob.Read(ctx)
if err != nil {
	return err
}
_, err = blob.WriteTo(ctx, destination)
_ = data
```

`storage.Memory` is intended for tests. A custom `Files` implementation must also implement `fs.FS`, `Write(context.Context, string, []byte)`, and `Delete(context.Context, string)`.

Blob content is immutable. On upload, storage limits the uncompressed payload to `storage.MaxSize` (64 MiB), detects and validates the MIME type, records both sizes and a SHA-256 digest, and compresses text, JSON, XML, YAML, TOML, and selected vendor formats with zstd. Every read checks the size, decompression, and digest.

The persisted `Blob.Compression` is either `CompressionRaw` or `CompressionZstd`. Updating a blob changes its metadata only; it does not replace the bytes.

Blob deletion is two-phase. A referenced blob returns `ErrConflict`. Otherwise it is marked as deleting first, then its file and metadata are removed. If file deletion fails, the blob remains in the deleting state and can be retried. `Store.Recover(ctx)` takes the blob recovery lock and retries all deleting blobs.

# Supporting packages

## Validation

`github.com/kelindar/storage/validate` validates nested structs, slices, arrays, maps, and pointers. Validation tags use `is`:

```go
type Input struct {
	Email string `json:"email" is:"required,email"`
	Age   int    `json:"age" is:"min(18)"`
}

ok, err := validate.Struct(&Input{
	Email: "person@example.com",
	Age:   21,
})
```

`Struct` requires a non-nil pointer to a struct and returns `(bool, error)`. A failure may be a `validate.Errors` collection. Each `validate.Error` includes the field name, validator, nested path, and message.

The built-in validators cover required values, strings, lengths, character classes, numbers, ranges, URLs, email, IP addresses, UUIDs, hashes, dates, encodings, and common identifiers. Register another one with `validate.Register`; its negated `!name` form is registered automatically.

`Create` and `Update` enforce field access declared with `form` tags:

```go
type Document struct {
	Name    string `json:"name" form:"rw"`
	Type    string `json:"type" form:"create"`
	Version int    `json:"version" form:"ro"`
}

err := validate.Create(incoming)
err = validate.Update(current, incoming)
```

`Create` rejects populated `ro` fields. `Update` rejects changes to `ro` and `create` fields while allowing omitted or unchanged values. If a read-only field is empty in `current`, `Update` clears it from `incoming`; this supports round-tripping projections that are not stored.

## Conversion helpers

`github.com/kelindar/storage/convert` contains the small helpers that are useful around resource metadata and query input:

- `TitleCase`, `Label`, and `SlugLabel` create display labels.
- `Strings` trims, removes empty values, deduplicates, and sorts.
- `Int` and `Float` parse strings with defaults.
- `Int64`, `Uint64`, and `Float64` convert common Go and JSON values.
- `ScheduleLabel` creates a readable label from common five-field cron expressions.
- `BuiltinID` creates a stable tenant-specific 20-character ID.

## Package layout

```text
/                 core storage API, registry, objects, queries, links, blobs
/state             lifecycle state machines
/validate          struct validation
/convert           labels, conversions, schedules, stable IDs
/driver/sqlite     standalone cgo-free SQLite module
/driver/pgsql      standalone PostgreSQL module using pgx
/bench             standalone benchmark module
/internal/walk     private reflection walker
```

`internal/walk` is an implementation detail and is not part of the public API.

# Benchmarks

The benchmark is a separate Go module under `bench`. It exercises resource creation, registry lookup, CRUD, search, count, changes, links, expiration scans, locks, sequences, blobs, and deletion.

Run it with:

```sh
(cd bench && go run .)
```

# Development

Run the root tests and checks:

```sh
go test ./...
go test -race ./...
go vet ./...
```

The nested modules are tested separately:

```sh
(cd driver/sqlite && go test ./...)
(cd driver/pgsql && go test ./...)
(cd bench && go test ./...)
```

# Contributing

Keep changes focused and run the relevant tests before sending a pull request.

# License

Storage is licensed under the [MIT License](LICENSE).
