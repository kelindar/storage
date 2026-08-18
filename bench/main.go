package main

import (
	"context"
	"errors"
	"iter"
	"strconv"
	"time"

	"github.com/kelindar/bench"
	"github.com/kelindar/storage"
	"github.com/kelindar/storage/driver/sqlite"
	"github.com/rs/xid"
)

const (
	tenant      = "acme"
	namespace   = "default"
	searchRows  = 256
	changeRows  = searchRows + 1
	deleteBatch = 20_000
)

var sink struct {
	object   storage.Object
	blob     *storage.Blob
	links    []storage.Link
	registry storage.Registry
	count    int
	sequence uint32
}

type suite struct {
	ctx context.Context

	db      storage.Storage
	changes storage.Storage
	insert  storage.Storage
	delete  storage.Storage
	expired storage.Storage
	blobs   *storage.Store

	update         *automation
	upsert         *automation
	search         storage.Query
	count          storage.Query
	source         storage.URN
	target         storage.URN
	expires        int64
	changeConsumer uint64

	insertTemplate *namespaceObject
	deleteRecords  []storage.URN
	deleteIndex    int
	uploadScope    storage.URN
	uploadPayload  []byte
}

type expirer interface {
	Expired(context.Context, storage.Kind, int64, int) iter.Seq2[storage.URN, error]
}

func main() {
	s := newSuite(context.Background())
	defer s.close()

	bench.Run(func(b *bench.B) {
		runBenchmarks(b, s)
	}, bench.WithSamples(20), bench.WithDuration(200*time.Millisecond))
}

func runBenchmarks(b *bench.B, s *suite) {
	expired := s.expired.(expirer)

	b.Run("new", func(int) {
		sink.object = newAutomation("benchmark automation")
	})

	b.Run("registry", func(int) {
		sink.registry = s.db.Registry()
	})

	b.Run("insert", func(int) {
		v := *s.insertTemplate
		v.ID = xid.New().String()
		sink.object = must(storage.Insert(s.ctx, s.insert, &v))
	})

	b.Run("update", func(int) {
		s.update.Desc = "updated benchmark automation"
		s.update = must(storage.Update(s.ctx, s.db, s.update))
		sink.object = s.update
	})

	b.Run("upsert", func(int) {
		s.upsert.Desc = "upserted benchmark automation"
		s.upsert = must(storage.Upsert(s.ctx, s.db, s.upsert, func(current *automation) error {
			current.Desc = s.upsert.Desc
			return nil
		}))
		sink.object = s.upsert
	})

	b.Run("fetch", func(int) {
		sink.object = must(storage.Fetch[*automation](s.ctx, s.db, s.target))
	})

	b.Run("search", func(int) {
		seq := must(storage.Search[*automation](s.ctx, s.db, s.search))
		count := 0
		for range seq {
			count++
		}
		sink.count = count
	})

	b.Run("count", func(int) {
		sink.count = must(storage.Count[*automation](s.ctx, s.db, s.count))
	})

	b.Run("changes", func(_ int) {
		ctx, cancel := context.WithCancel(s.ctx)
		seen := make(chan struct{})
		batches := 0
		s.changeConsumer++
		worker := storage.Changes[*automation](ctx, s.changes, "benchmark-"+strconv.FormatUint(s.changeConsumer, 10), time.Time{}, func(_ context.Context, batch []storage.Change) error {
			sink.count = len(batch)
			batches++
			if batches == 2 {
				close(seen)
			}
			return nil
		})
		<-seen
		cancel()
		if err := worker.Wait(); err != nil && !errors.Is(err, context.Canceled) {
			check(err)
		}
	})

	b.Run("link", func(int) {
		check(s.db.Link(s.ctx, s.source))
	})

	b.Run("links", func(int) {
		sink.links = must(s.db.Links(s.ctx, s.target))
	})

	b.Run("expired", func(int) {
		count := 0
		for _, err := range expired.Expired(s.ctx, kindAutomation, s.expires, searchRows) {
			check(err)
			count++
		}
		sink.count = count
	})

	b.Run("next", func(int) {
		sink.sequence = must(storage.Next[*automation](s.ctx, s.db))
	})

	b.Run("lock", func(int) {
		lockCtx, cancel, err := s.db.Lock(s.ctx, "benchmark")
		check(err)
		_ = lockCtx
		cancel()
	})

	b.Run("upload", func(int) {
		sink.blob = must(s.blobs.Upload(s.ctx, s.uploadScope, "application/octet-stream", s.uploadPayload))
	})

	b.Run("delete", func(int) {
		// ponytail: finite delete pool; replenish only when exhausted to avoid
		// timing an Insert alongside every Delete. Increase deleteBatch or add
		// per-sample setup support if delete-only timing needs tighter isolation.
		if s.deleteIndex == len(s.deleteRecords) {
			s.resetDelete()
		}
		sink.object = must(storage.Delete[*namespaceObject](s.ctx, s.delete, s.deleteRecords[s.deleteIndex]))
		s.deleteIndex++
	})
}

func newSuite(ctx context.Context) *suite {
	db := sqlite.OpenEphemeral(newRegistry())
	automations := seedAutomations(ctx, db, searchRows)

	changesDB := sqlite.OpenEphemeral(newRegistry())
	seedAutomations(ctx, changesDB, changeRows)

	bundle := newBundle(automations[:4])
	_ = must(db.Insert(ctx, bundle))

	update := must(storage.Fetch[*automation](ctx, db, automations[0]))
	upsert := must(storage.Fetch[*automation](ctx, db, automations[1]))

	insertDB := sqlite.OpenEphemeral(newRegistry())
	insertTemplate := must(storage.New[*namespaceObject](tenant, namespace))
	insertTemplate.Name = "benchmark namespace"

	deleteDB := sqlite.OpenEphemeral(newRegistry())
	deleteRecords := seedNamespaces(ctx, deleteDB, deleteBatch)

	expiredDB := sqlite.OpenEphemeral(newRegistry())
	seedExpired(ctx, expiredDB, searchRows)

	blobDB := sqlite.OpenEphemeral(newRegistry())
	payload := make([]byte, 4<<10)
	for i := range payload {
		payload[i] = byte(i*31 + 17)
	}

	return &suite{
		ctx:            ctx,
		db:             db,
		changes:        changesDB,
		insert:         insertDB,
		delete:         deleteDB,
		expired:        expiredDB,
		blobs:          storage.NewStore(blobDB, &storage.Memory{}),
		update:         update,
		upsert:         upsert,
		search:         storage.Query{Namespaces: []string{namespace}, Match: "benchmark", SortBy: []string{"name"}, Limit: 25},
		count:          storage.Query{Namespaces: []string{namespace}, Match: "benchmark"},
		source:         bundle.URN(),
		target:         automations[0],
		expires:        time.Now().UnixNano(),
		insertTemplate: insertTemplate,
		deleteRecords:  deleteRecords,
		uploadScope:    storage.URN{Tenant: tenant, Namespace: namespace},
		uploadPayload:  payload,
	}
}

func (s *suite) resetDelete() {
	check(s.delete.Close())
	s.delete = sqlite.OpenEphemeral(newRegistry())
	s.deleteRecords = seedNamespaces(s.ctx, s.delete, deleteBatch)
	s.deleteIndex = 0
}

func (s *suite) close() {
	if s.blobs != nil {
		check(s.blobs.Close())
		s.blobs = nil
	}
	for _, db := range []storage.Storage{s.db, s.changes, s.insert, s.delete, s.expired} {
		if db != nil {
			check(db.Close())
		}
	}
}

func seedAutomations(ctx context.Context, db storage.Storage, n int) []storage.URN {
	out := make([]storage.URN, n)
	for i := range n {
		v := newAutomation("benchmark automation")
		stored := must(db.Insert(ctx, v))
		out[i] = stored.URN()
	}
	return out
}

func seedExpired(ctx context.Context, db storage.Storage, n int) {
	for range n {
		v := newAutomation("expired benchmark automation")
		v.ExpiresAt = 1
		_ = must(db.Insert(ctx, v))
	}
}

func seedNamespaces(ctx context.Context, db storage.Storage, n int) []storage.URN {
	out := make([]storage.URN, n)
	for i := range n {
		v := must(storage.New[*namespaceObject](tenant, namespace))
		v.Name = "benchmark namespace"
		stored := must(db.Insert(ctx, v))
		out[i] = stored.URN()
	}
	return out
}

func newAutomation(name string) *automation {
	v := must(storage.New[*automation](tenant, namespace))
	v.Name = name
	v.Desc = "benchmark automation"
	v.Type = automationWorkflow
	v.Workflow.Version = 1
	return v
}

func newBundle(resources []storage.URN) *bundle {
	v := must(storage.New[*bundle](tenant, namespace))
	v.Name = "benchmark bundle"
	v.Resources = append([]storage.URN(nil), resources...)
	return v
}

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}
