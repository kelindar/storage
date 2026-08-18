package pgsql

import (
	"context"
	"testing"

	"github.com/kelindar/storage"
)

func TestStorageGuards(t *testing.T) {
	if _, err := New(nil, nil); err == nil {
		t.Fatal("nil database accepted")
	}
	store := &rds{}
	if _, err := store.Upload(context.Background(), storage.URN{}, "", nil); err == nil {
		t.Fatal("raw upload accepted")
	}
	if got := queryOrder("-updatedAt"); got != "updated_at DESC" {
		t.Fatalf("queryOrder = %q", got)
	}
	if got := queryOrder("+name"); got == "" {
		t.Fatal("ascending query order is empty")
	}
	if got := queryOrder(""); got != "" {
		t.Fatalf("empty query order = %q", got)
	}
	where, args := queryWhere(storage.Query{Tenant: "acme", IDs: []string{"one"}, States: []string{"active"}, Indexes: []string{"main"}, Filters: map[string][]string{"name": {"x"}}})
	if len(where) == 0 || len(args) == 0 {
		t.Fatal("queryWhere produced no clauses")
	}
	if got, _ := queryFilterByJSON("", []string{"x"}); got != "" {
		t.Fatal("empty filter path produced a clause")
	}
	if got, _ := queryFilterByJSON("name", nil); got != "" {
		t.Fatal("empty filter values produced a clause")
	}
	if clause, args := queryFilterByJSON("tenant", []string{"acme"}); clause == "" || len(args) != 1 {
		t.Fatalf("tenant filter = %q, %v", clause, args)
	}
	if clause, args := matchLikeClause("a% b_"); clause == "" || len(args) != 2 {
		t.Fatalf("match clause = %q, %v", clause, args)
	}
	if clause, _ := matchLikeClause(" "); clause != "" {
		t.Fatal("blank match produced a clause")
	}
	if got := escapeLike(`a%b_c\d`); got != `a\%b\_c\\d` {
		t.Fatalf("escapeLike = %q", got)
	}
}
