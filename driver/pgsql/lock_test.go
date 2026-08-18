package pgsql

import (
	"context"
	"errors"
	"testing"

	"github.com/kelindar/storage"
)

func TestLockGuards(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	l := &leaser{life: ctx, cancel: cancel}
	if _, _, err := l.lock(context.Background(), ""); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("empty lock error = %v", err)
	}
	cancel()
	if _, _, err := l.lock(context.Background(), "name"); err != context.Canceled {
		t.Fatalf("canceled lock error = %v", err)
	}
	if l.shutdown() {
		t.Fatal("shutdown should report an already canceled lease")
	}
}
