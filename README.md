# Storage: Typed Object Storage for Go

<p align="center">
    <img src="https://img.shields.io/github/go-mod/go-version/kelindar/storage" alt="Go Version">
    <a href="https://pkg.go.dev/github.com/kelindar/storage"><img src="https://pkg.go.dev/badge/github.com/kelindar/storage" alt="PkgGoDev"></a>
    <a href="https://goreportcard.com/report/github.com/kelindar/storage"><img src="https://goreportcard.com/badge/github.com/kelindar/storage" alt="Go Report Card"></a>
    <a href="https://opensource.org/licenses/MIT"><img src="https://img.shields.io/badge/License-MIT-blue.svg" alt="License"></a>
    <a href="https://coveralls.io/github/kelindar/storage"><img src="https://coveralls.io/repos/github/kelindar/storage/badge.svg" alt="Coverage"></a>
</p>

This repository contains a typed object store for Go applications. It stores resource metadata and JSON documents in a database, and provides the pieces that usually end up scattered across an application: queries, links, optimistic updates, locks, change feeds, sequences, lifecycle state, validation, and blobs.

The goal here is to keep the storage layer small enough to use directly, while still covering the common cases.

- **Typed.** Resources are regular Go structs with a storage.Meta field.
- **Multi-tenant.** Every resource belongs to a tenant, namespace, kind, and generated ID.
- **Durable.** SQLite and PostgreSQL drivers provide transactions, migrations, locks, changes, and sequences.
- **Queryable.** Filter, sort, page, search, and count resources without writing SQL.
- **Composable.** Links, state machines, validation, conversion helpers, and blobs are optional.
- **cgo-free SQLite.** The SQLite driver uses ncruces/go-sqlite3.

# Installation

Requires Go 1.25 or newer.

Install the root package and the driver you need:

```sh
go get github.com/kelindar/storage
go get github.com/kelindar/storage/driver/sqlite
# or:
go get github.com/kelindar/storage/driver/pgsql
```

The drivers are separate Go modules and depend directly on github.com/kelindar/storage.

# Resources

The main entry in the package is storage.Object. It is a regular Go struct which embeds storage.Meta and declares its kind:

```go
type Document struct {
	storage.Meta `kind:"document" json:",inline"`
	Title        string `json:"title"`
}
```

storage.Meta provides the Object interface:

```go
type Object interface {
	URN() storage.URN
	Status() string
	Created() (string, time.Time)
	Updated() (string, time.Time)
}
```

The metadata contains:

- ID, Kind, Tenant, and Namespace.
- State.
- CreatedBy, CreatedAt, UpdatedBy, and UpdatedAt.
- ExpiresAt, stored as Unix nanoseconds.

A resource kind is read from the kind tag. storage.New[T] creates a new object with a generated ID. storage.NewByType, storage.KindOf, and storage.KindOfT are the reflection helpers for code which does not know the concrete type at compile time.

Before opening a driver, register every resource type that the database will store:

```go
registry := storage.NewRegistry()
storage.MustRegister[*Document](registry)

db, err := sqlite.Open("storage.db", registry)
if err != nil {
	panic(err)
}
defer db.Close()
```

The driver creates one table for every registered kind during migration. This is why registration must happen first. If blobs are used, storage.Blob must be registered as well.

The registry also lets you enumerate registered types, resolve a kind, and inspect fields through Type.Field.

## Options

storage.Options adds application metadata when a type is registered:

- Icon, Title, Plural, and Sort describe the resource.
- States adds lifecycle rules.
- Actions lists permission names.
- Workflows lists application workflows.

Actions and Workflows are metadata only. Storage does not authorize actions or execute workflows. DefaultActions is used when Actions is empty.

## Actors

Attach the actor performing a mutation to the context:

```go
ctx := storage.WithActor(context.Background(), "user:123")
created, err := storage.Insert(ctx, db, document)
```

storage.Actor(ctx) returns the actor, or storage.UnknownActor when none is set. storage.SystemActor is available for system work.

## A complete example

Here is a small program which creates and searches a document:

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

# Drivers

## SQLite

The SQLite driver is in github.com/kelindar/storage/driver/sqlite:

```go
db, err := sqlite.Open("app.db", registry)
if err != nil {
	return err
}
defer db.Close()

testDB := sqlite.OpenEphemeral(registry)
defer testDB.Close()
```

sqlite.Open uses the cgo-free ncruces/go-sqlite3 driver. OpenEphemeral opens an in-memory database and is useful for tests.

SQLite uses FTS5 for Query.Match when it is available. If FTS5 is not available, matching falls back to a case-insensitive substring search.

## PostgreSQL

The PostgreSQL driver is in github.com/kelindar/storage/driver/pgsql:

```go
db, err := pgsql.Open("postgres://user:password@localhost/app?sslmode=disable", registry)
if err != nil {
	return err
}
defer db.Close()
```

pgsql.Open uses the pgx database/sql driver and owns the database handle. If the application already has a *sql.DB, use pgsql.New:

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

pgsql.New does not close the supplied *sql.DB. PostgreSQL Query.Match performs case-insensitive substring matching over stored JSON text; it does not provide SQLite FTS ranking.

Both drivers auto-migrate their shared tables and registered resource tables. Their raw Upload method rejects blob content. Wrap a driver with storage.NewStore when blobs are needed.

A custom backend implements storage.Storage, which covers Close, Registry, Lock, CRUD, links, search, count, changes, uploads, and sequences. storage.Store decorates that backend with a storage.Files implementation.

# CRUD

The generic helpers are the normal way to work with typed resources:

- storage.Create constructs and inserts a resource.
- storage.Insert assigns a missing ID, applies the default state, and inserts it.
- storage.Update writes an existing resource with optimistic concurrency.
- storage.Patch fetches, changes, and updates a resource.
- storage.Upsert inserts a resource or patches the existing one after a conflict.
- storage.Overwrite refreshes the stored version before updating, so it can overwrite changes made by another writer.
- storage.Fetch, storage.Delete, storage.Search, and storage.Count do what their names suggest.

For example:

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

Updates compare the UpdatedAt value read from the database. A stale object returns storage.ErrConflict.

Patch retries a conflict up to ten times. Its callback may therefore run ten times, and must only mutate the supplied object. It must be safe to repeat and should not send mail, publish an event, or perform another external side effect. The same rule applies to the patch callback passed to Upsert.

Use the error helpers instead of matching strings:

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

The package also exposes ErrInvalid, ErrDeleting, and ErrKindNotFound for errors.Is checks.

# URNs and targets

A storage.URN has the following format:

`urn:tenant:namespace:kind:id`

Use NewURN for a generated ID, MakeURN for a known ID, and ParseURN for an encoded value:

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

storage.Target adds a selector to a URN. Supported references are @draft, @latest, and exact positive versions such as @v3:

```go
target, err := storage.ParseTarget(urn.String() + "@latest")
if err != nil {
	return err
}
fmt.Println(target.URN(), target.Ref(), target.IsLatest())
```

A target is a selector value. It does not create version storage by itself.

# Queries

storage.Query lets you filter and sort a resource kind without writing SQL:

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

The query fields are:

- Tenant, IDs, and Namespaces scope results.
- States filters lifecycle states.
- Indexes filters the value returned by an optional Index() string method.
- Filters compare JSON paths.
- Match performs full-text or substring matching, depending on the driver.
- SortBy accepts +field for ascending and -field for descending order.
- Offset and Limit page results.
- CreatedBefore, UpdatedBefore, and UpdatedAfter apply time bounds.

A filter with a value is an equality check. A filter with the empty string is an existence check: the field must be present and not be empty, zero, or false. Nested fields can be addressed with paths such as profile.email.

Search returns an iterator. Collect drains it into a slice, and Select drains it while projecting another value:

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

Count does not accept sorting, offset, or limit.

## String queries

ParseQuery accepts semicolon-separated key/value pairs:

```go
query, err := storage.ParseQuery(
	"tenant=acme;namespace=default;state=active;"+
		"filter=title:release,description;"+
		"match={Title};sort=-updatedAt;limit=20;offset=0",
	document,
	storage.Query{},
)
```

Supported components are tenant, id, namespace, state, index, filter, match, sort, limit, offset, and updatedAfter. Multiple IDs, namespaces, states, and indexes are comma-separated. namespace=* removes the namespace restriction.

match can substitute fields from the supplied object with {FieldName}. Substitutions support strings, integers, floats, and booleans. Filters use field:value for equality or just field for existence. Query.String() produces a compact representation for logging and transport.

# JSON and YAML

ToJSON serializes a resource. FromJSON reads the kind field, resolves the concrete type through the registry, and unmarshals it. The reader variants accept an io.Reader:

```go
data, err := storage.ToJSON(document)
decoded, err := storage.FromJSON(registry, data)

decoded, err = storage.ReadJSON(registry, reader)
decoded, err = storage.FromYAML(registry, yamlData)
decoded, err = storage.ReadYAML(registry, reader)
```

UnmarshalYAML uses JSON struct tags. ReadFile reads a file when its data argument is nil and chooses JSON, YAML, or YML from the extension.

A store tag changes where a field is persisted without changing its public JSON shape:

```go
type SecretDocument struct {
	storage.Meta `kind:"secret_document" json:",inline"`
	Token string `json:"-" store:"credentials.token"`
}
```

ToJSON writes Token at credentials.token, and FromJSON reads it from that path. store:"-" removes a field from the stored representation. Nested structs, slices, arrays, and maps are supported.

Use storage.Embed for polymorphic embedded resources. It stores an Object while using the registry and the embedded object's kind field to decode the concrete type.

# Links

A field tagged with link:"kind" produces dependency links to URNs or URN strings of that kind:

```go
type Conversation struct {
	storage.Meta `kind:"conversation" json:",inline"`
	Attachments  []storage.URN `json:"attachments" link:"blob"`
}
```

The link walker supports pointers, nested structs, slices, arrays, and maps. Paths use JSON field names and include indexes or map keys, for example attachments.0. A field with json:"-" or link:"-" is ignored.

For links that are not represented by a single tagged field, implement storage.Linker:

```go
func (b *Bundle) Links() ([]storage.Link, error) {
	return []storage.Link{
		storage.Own(b.URN(), target, storage.Path("resources.0")),
	}, nil
}
```

Use storage.Use for a dependency and storage.Own for exclusive ownership. Drivers rebuild outgoing links during inserts and updates. Call db.Link(ctx, source) to rebuild links explicitly, and db.Links(ctx, target) to list incoming links.

Link paths can be inspected with Path.String, Path.Label, Path.Index, Path.ID, and Path.Walk. Links must have valid URNs, a non-empty path, the matching source, and the same tenant on both sides. Ownership is unique per target; blobs cannot own resources, and only bundles may own non-blob resources.

# Locks, changes, and sequences

## Named locks

Storage.Lock acquires a renewable named lease:

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

Only one live owner can hold a name. The returned context is canceled if renewal fails or ownership is lost. Always release the lease and use the returned context for protected work.

## Durable changes

storage.Changes[T] starts a named, persistent consumer for one resource kind:

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

The consumer cursor is durable and keyed by consumer name and kind. Delivery is at least once. If the callback returns an error, the same batch is retried until it succeeds or the context is canceled. Batches are borrowed only for the callback, so do not retain or modify them. Actions are create, update, and delete.

The after time selects the starting point for a new consumer. The Store sweeper prunes change history older than seven days; pruned history is not replayed.

## Sequences

storage.Next[T] advances a sequence named by the resource kind. db.Next(ctx, name) supports an arbitrary sequence name:

```go
sequence, err := storage.Next[*Document](ctx, db)
named, err := db.Next(ctx, "invoice")
```

Sequences are durable and atomic and return uint32.

# Blobs

Blobs keep binary bytes in a storage.Files backend and metadata in the resource database. Register the blob type before opening the driver:

```go
storage.MustRegister[*storage.Blob](registry, storage.Options{
	States: state.Machine{
		"create": "* -> active",
		"delete": "active -> deleting",
	},
})
```

Wrap a driver with storage.NewStore:

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

storage.Memory is intended for tests. A custom Files implementation must also implement fs.FS, Write(context.Context, string, []byte), and Delete(context.Context, string).

Blob content is immutable. Uploads:

- Limit uncompressed data to storage.MaxSize (64 MiB).
- Detect and validate the MIME type.
- Record the original size, stored size, and SHA-256 digest.
- Compress text, JSON, XML, YAML, TOML, and selected vendor formats with zstd.
- Verify size, decompression, and SHA-256 on every read.

The persisted Blob.Compression value is either CompressionRaw or CompressionZstd. Updating a blob changes metadata only; it does not replace the bytes.

Raw drivers reject uploads because they have no file backend.

Blob deletion is two-phase. A referenced blob returns ErrConflict; otherwise it is first marked deleting, then its file and metadata are removed. If file deletion fails, the blob remains retryable in the deleting state. Store.Recover(ctx) takes the blob recovery lock and retries all deleting blobs.

# Expiration

Set Meta.ExpiresAt to a Unix-nanosecond deadline:

```go
document.ExpiresAt = time.Now().Add(time.Hour).UnixNano()
document, err = storage.Update(ctx, db, document)
```

Start the store sweeper with a deletion callback:

```go
store.Start(ctx, func(ctx context.Context, urn storage.URN) error {
	_, err := store.Delete(ctx, urn)
	return err
})
```

Expiration is eventual. The sweeper runs at a randomized interval between one and two hours, scans expired resources in pages, and invokes the callback. Failed or blocked deletions are logged and retried by a later sweep. Store.Start also enables change-log retention cleanup. Store.Close stops the sweeper and closes the wrapped storage.

# Lifecycle state

A state.Machine maps action names to edges:

```go
states := state.Machine{
	"create":  "* -> draft",
	"publish": "draft -> active",
	"archive": "active -> inactive",
}

storage.MustRegister[*Document](registry, storage.Options{States: states})
```

The wildcard source supplies the default state. The generic storage.Insert and storage.Upsert helpers assign it when no state is set. The generic storage.Update and storage.Patch helpers reject invalid transitions with ErrInvalidTransition.

The state package provides shared state names (Creating, Active, Inactive, Deleting, and Failed) plus Machine.TryAction, Machine.CanTransition, Machine.Default, Machine.States, and Edge.Value.

# Validation

github.com/kelindar/storage/validate validates nested structs, slices, arrays, maps, and pointers. Validation tags use is, not valid:

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

Struct requires a non-nil pointer to a struct and returns (bool, error). A failure may be a validate.Errors collection. Each validate.Error includes the field name, validator, nested path, and message.

The package includes validators for required values, strings, lengths, character classes, numbers, ranges, URLs, email, IP addresses, UUIDs, hashes, dates, encodings, and common identifiers. Register a custom validator with validate.Register; its negated !name form is registered automatically.

# Conversion helpers

github.com/kelindar/storage/convert contains small helpers:

- TitleCase, Label, and SlugLabel create display labels.
- Strings trims, removes empty values, deduplicates, and sorts.
- Int and Float parse strings with defaults.
- Int64, Uint64, and Float64 convert common Go and JSON values.
- ScheduleLabel creates a readable label from common five-field cron expressions.
- BuiltinID creates a stable tenant-specific 20-character ID.

# Package layout

```
/                 core storage API, registry, objects, queries, links, blobs
/state             lifecycle state machines
/validate          struct validation
/convert           labels, conversions, schedules, stable IDs
/driver/sqlite     standalone cgo-free SQLite module
/driver/pgsql      standalone PostgreSQL module using pgx
/bench             standalone benchmark module
/internal/walk     private reflection and link walker
```

internal/walk is an implementation detail and is not part of the public API.

# Benchmarks

The benchmark is a separate Go module under bench. It exercises resource creation, registry lookup, CRUD, search, count, changes, links, expiration scans, locks, sequences, blobs, and deletion.

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

Pull requests are welcome. Please keep changes focused and run the relevant tests before sending one.

# License

Storage is licensed under the [MIT License](LICENSE).
