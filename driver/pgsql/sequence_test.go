package pgsql

import (
	"context"
	"errors"
	"testing"

	"github.com/kelindar/storage"
)

func TestSequenceGuards(t *testing.T) {
	store := &rds{}
	if _, err := store.Next(context.Background(), ""); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("empty sequence error = %v", err)
	}
}
