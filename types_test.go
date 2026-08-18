package storage

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmbed(t *testing.T) {
	r := newRegistry()

	// Create a new app
	app, err := New[*Kind3]("acme", "my_project")
	assert.NoError(t, err)

	// Create a new deployment with the associated app
	dep, err := New("acme", "my_project", func(r *Kind2) error {
		r.App = app.URN()
		return nil
	})
	assert.NoError(t, err)

	req, err := New("acme", "my_project", func(r *Kind4) error {
		r.After = Embed{Value: dep}
		return nil
	})
	assert.NoError(t, err)

	// Marshal the request
	data, err := ToJSON(req)
	assert.NoError(t, err)

	// Unmarshal the request
	out, err := FromJSON(r, data)
	assert.NoError(t, err)
	decoded := out.(*Kind4).After.Value.(*Kind2)
	assert.NotNil(t, decoded)
	assert.Equal(t, dep.ID, decoded.ID)

	var empty Embed
	assert.NoError(t, empty.UnmarshalJSON([]byte("null")))
	assert.Nil(t, empty.Value)
	empty.Registry = r
	assert.Error(t, empty.UnmarshalJSON([]byte(`{"kind":"missing"}`)))
}

func TestWalkEmbedsGuards(t *testing.T) {
	fn := func(reflect.Value) {}
	assert.NoError(t, walkEmbeds(newRegistry(), nil, fn))
	assert.NoError(t, walkEmbeds(newRegistry(), Kind3{}, fn))
	assert.NoError(t, walkEmbeds(newRegistry(), new(int), fn))
	assert.NoError(t, walkEmbeds(newRegistry(), 1, fn))
	type embedded struct {
		Value  Embed
		hidden int
	}
	assert.NoError(t, walkEmbeds(newRegistry(), embedded{}, fn))
}

func TestLockError(t *testing.T) {
	assert.True(t, IsLockLost(fmt.Errorf("wrapped: %w", ErrLockLost)))
	assert.False(t, IsLockLost(nil))
}

// MockObject is a sample struct for testing purposes.
type MockObject struct {
	Meta   `kind:"mock" json:",inline"`
	Name   string
	Age    int
	Income float64
}

func TestParseQuery(t *testing.T) {
	tests := map[string]struct {
		query   string
		object  Object
		expect  Query
		invalid bool
	}{
		"invalid namespace format": {query: "namespace=;state=active", invalid: true},
		"invalid filter key empty": {query: "namespace=company;filter=:value;state=active", invalid: true},
		"invalid match format":     {query: "namespace=company;match=;", invalid: true},
		"empty query string": {
			query:  "",
			expect: Query{},
		},
		"valid query with all components": {
			query: "tenant=acme;namespace=company;state=active;filter=age:30,income:1000;match={Name}",
			object: &MockObject{
				Name:   "Alice",
				Age:    30,
				Income: 50000.00,
			},
			expect: Query{
				Tenant:     "acme",
				Namespaces: []string{"company"},
				States:     []string{"active"},
				Filters: map[string][]string{
					"age":    {"30"},
					"income": {"1000"},
				},
				Match: "Alice",
			},
		},
		"single valid filter": {
			query: "namespace=company;state=active;filter=age:30;match={Name}",
			object: &MockObject{
				Name: "Alice",
			},
			expect: Query{
				Namespaces: []string{"company"},
				States:     []string{"active"},
				Filters: map[string][]string{
					"age": {"30"},
				},
				Match: "Alice",
			},
		},
		"existence check filter": {
			query: "filter=email",
			expect: Query{
				Filters: map[string][]string{
					"email": {""},
				},
			},
		},
		"pagination and sorting": {
			query: "match=alice;sort=-updatedAt,name;limit=20;offset=40",
			expect: Query{
				Filters: map[string][]string{},
				Match:   "alice",
				SortBy:  []string{"-updatedAt", "name"},
				Limit:   20,
				Offset:  40,
			},
		},
		"updated after": {
			query:  "updatedAfter=2025-01-02T03:04:05Z",
			expect: Query{Filters: map[string][]string{}, UpdatedAfter: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)},
		},
		"index filter": {
			query: "index=urn:acme:system:automation:demo",
			expect: Query{
				Filters: map[string][]string{},
				Indexes: []string{"urn:acme:system:automation:demo"},
			},
		},
		"id filter": {
			query:  "id=one,two",
			expect: Query{Filters: map[string][]string{}, IDs: []string{"one", "two"}},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if tt.object == nil {
				tt.object = &MockObject{}
			}

			result, err := ParseQuery(tt.query, tt.object, Query{})
			if tt.invalid {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expect, result)
			}
		})
	}
}

func TestEncodeQuery(t *testing.T) {
	tests := map[string]Query{
		"": {},
		"namespace=company;state=active;filter=age:30;match=Alice;": {
			Namespaces: []string{"company"},
			States:     []string{"active"},
			Filters: map[string][]string{
				"age": {"30"},
			},
			Match: "Alice",
		},
		"tenant=acme;": {Tenant: "acme"},
		"id=one,two;":  {IDs: []string{"one", "two"}},
		"namespace=company;state=active,inactive;filter=age:30;match=Alice;": {
			Namespaces: []string{"company"},
			States:     []string{"active", "inactive"},
			Filters: map[string][]string{
				"age": {"30"},
			},
			Match: "Alice",
		},
		"namespace=default;index=field1,field2;": {
			Namespaces: []string{"default"},
			Indexes:    []string{"field1", "field2"},
		},
		"match=alice;sort=-updatedAt,name;limit=20;offset=40;": {
			Match:  "alice",
			SortBy: []string{"-updatedAt", "name"},
			Limit:  20,
			Offset: 40,
		},
		"updatedAfter=2025-01-02T03:04:05Z;": {
			UpdatedAfter: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC),
		},
	}

	for expect, query := range tests {
		t.Run(expect, func(t *testing.T) {
			assert.Equal(t, expect, query.String())
		})
	}
}

func TestQueryStringAll(t *testing.T) {
	q := Query{
		Tenant:        "acme",
		IDs:           []string{"one", "two"},
		Namespaces:    []string{"default"},
		States:        []string{"active"},
		Indexes:       []string{"main"},
		Filters:       map[string][]string{"name": {"one", "two"}},
		Match:         "deploy",
		SortBy:        []string{"-updatedAt", "name"},
		Limit:         10,
		Offset:        2,
		CreatedBefore: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC),
		UpdatedBefore: time.Date(2025, 2, 2, 3, 4, 5, 0, time.UTC),
		UpdatedAfter:  time.Date(2025, 3, 2, 3, 4, 5, 0, time.UTC),
	}
	encoded := q.String()
	for _, part := range []string{
		"tenant=acme;", "id=one,two;", "namespace=default;", "state=active;", "index=main;",
		"name:one,name:two;", "match=deploy;", "sort=-updatedAt,name;", "limit=10;", "offset=2;",
		"createdBefore=2025-01-02T03:04:05Z;", "updatedBefore=2025-02-02T03:04:05Z;",
		"updatedAfter=2025-03-02T03:04:05Z;",
	} {
		assert.Contains(t, encoded, part)
	}
}

func TestQueryGuards(t *testing.T) {
	tests := []struct {
		name string
		call func(*Query) error
	}{
		{"ids", func(q *Query) error { return parseIDs("id=one,", q) }},
		{"invalid ids", func(q *Query) error { return parseIDs("bad", q) }},
		{"tenant", func(q *Query) error { return parseTenant("tenant=", q) }},
		{"invalid tenant", func(q *Query) error { return parseTenant("bad", q) }},
		{"updated after", func(q *Query) error { return parseUpdatedAfter("updatedAfter=bad", q) }},
		{"invalid updated after", func(q *Query) error { return parseUpdatedAfter("bad", q) }},
		{"namespace", func(q *Query) error { return parseNamespace("namespace=", q) }},
		{"invalid namespace", func(q *Query) error { return parseNamespace("bad", q) }},
		{"state", func(q *Query) error { return parseState("state=,", q) }},
		{"invalid state", func(q *Query) error { return parseState("bad", q) }},
		{"index", func(q *Query) error { return parseIndex("index=,", q) }},
		{"invalid index", func(q *Query) error { return parseIndex("bad", q) }},
		{"filter", func(q *Query) error { return parseFilter("filter=:value", q) }},
		{"filter value", func(q *Query) error { return parseFilter("filter=key:", q) }},
		{"invalid filter", func(q *Query) error { return parseFilter("bad", q) }},
		{"sort", func(q *Query) error { return parseSort("sort=,", q) }},
		{"invalid sort", func(q *Query) error { return parseSort("bad", q) }},
		{"limit", func(q *Query) error { return parseLimit("limit=bad", q) }},
		{"invalid limit", func(q *Query) error { return parseLimit("bad", q) }},
		{"negative limit", func(q *Query) error { return parseLimit("limit=-1", q) }},
		{"offset", func(q *Query) error { return parseOffset("offset=bad", q) }},
		{"invalid offset", func(q *Query) error { return parseOffset("bad", q) }},
		{"negative offset", func(q *Query) error { return parseOffset("offset=-1", q) }},
		{"match", func(q *Query) error { return parseMatch("match={Missing}", q, &MockObject{}) }},
		{"invalid match", func(q *Query) error { return parseMatch("bad", q, &MockObject{}) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Error(t, tc.call(&Query{Filters: map[string][]string{}}))
		})
	}

	_, err := ParseQuery("unknown=value", &MockObject{}, Query{})
	assert.Error(t, err)
}

func TestQueryVariables(t *testing.T) {
	object := struct {
		Name  string
		Count int
		Ratio float64
		Ready bool
	}{Name: "Alice", Count: 3, Ratio: 1.25, Ready: true}
	got, err := replaceVariables("{Name}/{Count}/{Ratio}/{Ready}", object)
	require.NoError(t, err)
	assert.Equal(t, "Alice/3/1.25/true", got)
	got, err = replaceVariables("plain", object)
	require.NoError(t, err)
	assert.Equal(t, "plain", got)
	assert.NoError(t, populateFilters(&Query{Filters: map[string][]string{}}, ",,"))

	for _, name := range []string{"Missing", "Name"} {
		if name == "Name" {
			continue
		}
		_, err = loadField(object, name)
		assert.Error(t, err)
	}
	var pointer *MockObject
	_, err = loadField(&pointer, "Name")
	assert.Error(t, err)
	_, err = loadField(42, "Name")
	assert.Error(t, err)
	_, err = loadField(object, "Missing")
	assert.Error(t, err)
	_, err = loadField(struct{ hidden int }{1}, "hidden")
	assert.Error(t, err)
	_, err = loadField(struct{ Values []int }{}, "Values")
	assert.Error(t, err)
}
