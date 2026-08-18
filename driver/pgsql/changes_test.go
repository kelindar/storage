package pgsql

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kelindar/storage"
)

func TestChangesHelpers(t *testing.T) {
	testChangeActions(t)
	testChangeRetries(t)
}

func testChangeActions(t *testing.T) {
	if _, _, err := changeFilter(""); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("empty filter error = %v", err)
	}
	if _, pattern, err := changeFilter("kind_%"); err != nil || pattern != `urn:%:%:kind\_\%:%` {
		t.Fatalf("escaped filter = %q, %v", pattern, err)
	}
	for _, action := range []string{changeCreate, changeUpdate, changeDelete} {
		if code, err := changeActionCode(action); err != nil || code < 1 || code > 3 {
			t.Fatalf("action code %q = %d, %v", action, code, err)
		}
	}
	if _, err := changeActionCode("other"); err == nil {
		t.Fatal("unsupported action accepted")
	}
	for _, code := range []int64{1, 2, 3} {
		if name, ok := changeActionName(code); !ok || name == "" {
			t.Fatalf("action name %d = %q, %v", code, name, ok)
		}
	}
	if _, ok := changeActionName(0); ok {
		t.Fatal("invalid action had a name")
	}
	if kindPattern("a\\b") != `urn:%:%:a\\b:%` {
		t.Fatal("kind pattern did not escape backslashes")
	}
}

func testChangeRetries(t *testing.T) {
	if retryableChangeError(errors.New("plain")) {
		t.Fatal("plain error was retryable")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if waitForChanges(ctx) {
		t.Fatal("canceled wait completed")
	}
	if got := ignoreChangeCancellation(ctx, errors.New("ignored")); got != nil {
		t.Fatal("canceled error was not ignored")
	}
	if got := ignoreChangeCancellation(context.Background(), errors.New("kept")); got == nil {
		t.Fatal("live error was ignored")
	}
	if err := retryChangeOperation(context.Background(), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := retryChangeOperation(context.Background(), func() error { return errors.New("stop") }); err == nil {
		t.Fatal("non-retryable error was lost")
	}
	if err := deliverChanges(ctx, func(context.Context, []storage.Change) error { return errors.New("retry") }, nil); err != context.Canceled {
		t.Fatalf("canceled delivery error = %v", err)
	}
	changes := []storage.Change{{At: time.Now()}}
	releaseChanges(changes)
	releaseChanges(nil)
}
