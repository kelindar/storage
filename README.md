<p align="center">
<img src="https://img.shields.io/github/go-mod/go-version/kelindar/storage" alt="Go Version">
<a href="https://pkg.go.dev/github.com/kelindar/storage"><img src="https://pkg.go.dev/badge/github.com/kelindar/storage" alt="PkgGoDev"></a>
<a href="https://opensource.org/licenses/MIT"><img src="https://img.shields.io/badge/License-MIT-blue.svg" alt="License"></a>
<a href="https://coveralls.io/github/kelindar/storage"><img src="https://coveralls.io/repos/github/kelindar/storage/badge.svg" alt="Coverage"></a>
</p>

## Typed object storage for Go

`storage` is a reflection-driven object store for Go applications. It provides
a small generic API for registering resource types, creating and querying
objects, storing links, tracking durable changes, and managing binary content.
SQLite and PostgreSQL are included durable backends.

## Features

- Generic object model with typed `URN` and `Kind` identifiers.
- Reflection-based registry with JSON and YAML encoding.
- Generic create, insert, update, patch, upsert, fetch, delete, search, and count helpers.
- Optimistic write conflicts, locks, sequences, durable changes, and expiration queries.
- Query filters, indexes, full-text matching, sorting, pagination, and time bounds.
- Tagged and programmatic links between objects.
- Immutable blobs with content validation, SHA-256 identity, optional zstd compression, and pluggable filesystems.
- Optional lifecycle state machines and field validation in subpackages.
- SQLite backend using a cgo-free driver.
- PostgreSQL backend using pgx.

## Quick start

Define an object by embedding `storage.Meta` and giving it a `kind` tag:

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
	Name         string `json:"name"`
}

func main() {
	ctx := context.Background()
	registry := storage.NewRegistry()
	storage.MustRegister[*Document](registry)

	db, err := sqlite.Open("storage.db", registry)
	if err != nil {
		panic(err)
	}
	defer db.Close()

	doc, err := storage.Create[*Document](ctx, db, func(doc *Document) error {
		doc.Name = "hello"
		return nil
	}, "acme", "default")
	if err != nil {
		panic(err)
	}

	got, err := storage.Fetch[*Document](ctx, db, doc.URN())
	if err != nil {
		panic(err)
	}
	fmt.Println(got.Name)
}
```

The generic helpers apply IDs, lifecycle defaults, optimistic-version checks,
and typed results. Use `db.Insert` and `db.Update` directly when you need the
lower-level backend contract.

## Querying

Queries are backend-independent:

```go
results, err := storage.Search[*Document](ctx, db, storage.Query{
		Namespaces: []string{"default"},
		Match:      "hello",
		SortBy:     []string{"name"},
		Limit:      20,
})
if err != nil {
	panic(err)
}

for doc := range results {
	fmt.Println(doc.Name)
}
```

## Blobs

Wrap a storage backend with `storage.Store` to enable blob uploads:

```go
files := &storage.Memory{}
store := storage.NewStore(db, files)

_, err := store.Upload(ctx,
	storage.URN{Tenant: "acme", Namespace: "default"},
		"text/plain",
		[]byte("hello"),
	)
```

`storage.Files` can be implemented for filesystem, object-storage, or test
backends. Blobs are content-addressed and immutable.

## Packages

- `github.com/kelindar/storage` — object model, registry, CRUD, queries, links, blobs, and generic helpers.
- `github.com/kelindar/storage/driver/sqlite` — SQLite implementation of the storage contract.
- `github.com/kelindar/storage/driver/pgsql` — PostgreSQL implementation of the storage contract.
- `github.com/kelindar/storage/state` — lifecycle state machines.
- `github.com/kelindar/storage/convert` — small conversion and labeling helpers.
- `github.com/kelindar/storage/validate` — reflection-based struct and field validation.

Reflection walking is kept under `internal/walk` because it is an implementation
detail of the SQLite and validation packages.

## Benchmarks

The benchmark harness is a separate Go module so benchmark dependencies do not
become dependencies of the library:

```sh
cd bench
go test ./...
go run .
```

Benchmark numbers depend on the Go version, database, filesystem, and machine.

## Development

```sh
go test ./...
go test -race ./...
go vet ./...

cd driver/sqlite && go test ./...
cd ../pgsql && go test ./...
cd ../../bench && go test ./...
```

## License

This project is licensed under the MIT License. See [LICENSE](LICENSE).
