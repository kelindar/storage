package pgsql

import (
	"context"
	"strings"
	"testing"

	"github.com/kelindar/storage"
	"github.com/stretchr/testify/assert"
)

func TestSelection(t *testing.T) {
	for _, tc := range []struct {
		name      string
		selection map[string][]string
		clause    string
		args      []any
	}{
		{"nil", nil, "tenant = ?", []any{"acme"}},
		{"empty", map[string][]string{}, "tenant = ? AND 1 = 0", []any{"acme"}},
		{"empty IDs", map[string][]string{"ns": {}}, "tenant = ? AND 1 = 0", []any{"acme"}},
		{"namespace", map[string][]string{"ns": nil}, "tenant = ? AND ((namespace = ?))", []any{"acme", "ns"}},
		{"exact", map[string][]string{"ns'": {"a", "a", "b'"}}, "tenant = ? AND ((namespace = ? AND id IN (?,?,?)))", []any{"acme", "ns'", "a", "a", "b'"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			where, args := queryWhere(storage.Query{Tenant: "acme", Selection: tc.selection})
			assert.Equal(t, tc.clause, strings.Join(where, " AND "))
			assert.Equal(t, tc.args, args)
		})
	}
	where, args := queryWhere(storage.Query{
		Tenant: "acme", Selection: map[string][]string{"whole": nil, "shared": {"id"}},
		Namespaces: []string{"shared"}, IDs: []string{"id"}, Filters: map[string][]string{"name": {"A"}},
	})
	assert.Len(t, where, 5)
	assert.True(t, strings.HasPrefix(where[2], "((") && strings.HasSuffix(where[2], "))"))
	assert.Contains(t, where[2], " OR ")
	assert.Equal(t, strings.Count(strings.Join(where, " AND "), "?"), len(args))
	assert.ElementsMatch(t, []any{"acme", "id", "whole", "shared", "id", "shared", "name", "A"}, args)
}

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
	if clause, args := matchLikeClause("data::text", "a% b_"); clause == "" || len(args) != 2 {
		t.Fatalf("match clause = %q, %v", clause, args)
	}
	if clause, _ := matchLikeClause("data::text", " "); clause != "" {
		t.Fatal("blank match produced a clause")
	}
	if got := escapeLike(`a%b_c\d`); got != `a\%b\_c\\d` {
		t.Fatalf("escapeLike = %q", got)
	}
}

func TestMatchLikeClause(t *testing.T) {
	clause, args := matchLikeClause("data::text", "secret")
	assert.Equal(t, "data::text ILIKE ? ESCAPE '\\'", clause)
	assert.Equal(t, []any{"%secret%"}, args)
}
