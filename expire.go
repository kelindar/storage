package storage

import (
	"context"
	"errors"
	"iter"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/kelindar/async"
)

const (
	expirationInterval = time.Hour
	expirationPageSize = 100
	changeRetention    = 7 * 24 * time.Hour
)

type expirationStorage interface {
	Expired(context.Context, Kind, int64, int) iter.Seq2[URN, error]
}

type changePruner interface {
	PruneChanges(context.Context, time.Time) error
}

type expirationSummary struct {
	Attempted int
	Deleted   int
	Blocked   int
	Failed    int
	Missing   int
}

// sweeper runs periodic resource cleanup for a Store.
type sweeper struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	done   async.Awaiter
}

func expirationDelay() time.Duration {
	return expirationInterval + time.Duration(rand.Int64N(int64(expirationInterval)))
}

func (s *sweeper) start(ctx context.Context, store *Store, deleteResource func(context.Context, URN) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done != nil {
		return
	}
	ctx, s.cancel = context.WithCancel(ctx)
	s.done = async.Repeat(ctx, expirationDelay(), func(ctx context.Context) {
		store.expire(ctx, time.Now(), deleteResource)
	})
}

func (s *sweeper) stop(store Storage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done != nil && s.cancel == nil {
		return s.done.Wait()
	}
	var err error
	if s.cancel != nil {
		s.cancel()
		err = s.done.Wait()
		if errors.Is(err, context.Canceled) {
			err = nil
		}
	}
	s.cancel = nil
	if store != nil {
		err = errors.Join(err, store.Close())
	}
	s.done = async.Failed[struct{}](err)
	return err
}

func (s *Store) expire(ctx context.Context, now time.Time, deleteResource func(context.Context, URN) error) expirationSummary {
	var summary expirationSummary
	if pruner, ok := s.Storage.(changePruner); ok {
		if err := pruner.PruneChanges(ctx, now.Add(-changeRetention)); err != nil && ctx.Err() == nil {
			slog.Error("storage change retention cleanup failed", "error", err)
		}
	}
	expirer, ok := s.Storage.(expirationStorage)
	if !ok || deleteResource == nil {
		return summary
	}
	started := time.Now()
	defer func() {
		slog.Info("resource expiration complete",
			"attempted", summary.Attempted,
			"deleted", summary.Deleted,
			"blocked", summary.Blocked,
			"failed", summary.Failed,
			"missing", summary.Missing,
			"duration", time.Since(started),
		)
	}()
	for typ := range s.Registry().Types() {
		for urn, err := range expirer.Expired(ctx, typ.Kind, now.UnixNano(), expirationPageSize) {
			if err != nil {
				slog.Error("resource expiration query failed", "kind", typ.Kind, "error", err)
				break
			}
			if ctx.Err() != nil {
				return summary
			}
			summary.Attempted++
			switch err := deleteResource(ctx, urn); {
			case err == nil:
				summary.Deleted++
			case IsNotFound(err):
				summary.Missing++
			case isBlocked(err):
				summary.Blocked++
			default:
				summary.Failed++
				slog.Error("resource expiration failed", "urn", urn, "error", err)
			}
		}
	}
	return summary
}

func isBlocked(err error) bool {
	var blocked interface{ Blocked() bool }
	return errors.As(err, &blocked) && blocked.Blocked()
}
