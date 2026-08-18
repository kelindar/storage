package storage

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/*
cpu: 13th Gen Intel(R) Core(TM) i7-13700K
BenchmarkResource/new-24         	 3410836	       350.9 ns/op	     256 B/op	       3 allocs/op
*/
func BenchmarkResource(b *testing.B) {
	b.ReportAllocs()
	b.Run("new", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := New[*Kind1]("acme", "my_project"); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func TestNewWith(t *testing.T) {
	r, err := New("acme", "my_project", func(r *Kind1) error {
		r.Name = "my_name"
		return nil
	})

	assert.NoError(t, err)
	assert.NotNil(t, r)
	assert.NotNil(t, r.URN())
	assert.Equal(t, Kind("kind1"), r.Kind)
	assert.Equal(t, "acme", r.Tenant)
	assert.Equal(t, "my_project", r.Namespace)
	assert.NotEmpty(t, r.ID)
	assert.Equal(t, "my_name", r.Name)
}

func TestObjectGuards(t *testing.T) {
	_, err := New[*Kind1]("acme", "my_project", func(*Kind1) error { return assert.AnError })
	assert.ErrorIs(t, err, assert.AnError)

	_, err = New[*invalidResource]("acme", "my_project")
	assert.Error(t, err)
	_, err = NewByType(reflect.TypeOf(struct{}{}), "acme", "my_project")
	assert.Error(t, err)

	_, err = FromJSON(newRegistry(), []byte("{"))
	assert.Error(t, err)
	_, err = FromJSON(newRegistry(), []byte(`{"kind":"missing"}`))
	assert.ErrorIs(t, err, ErrKindNotFound)
	_, err = ReadJSON(newRegistry(), failingReader{})
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
	_, err = ReadYAML(newRegistry(), failingReader{})
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
	_, err = FromYAML(newRegistry(), []byte("["))
	assert.Error(t, err)
	assert.Error(t, ReadFile("missing.json", nil, &struct{}{}))
	assert.Equal(t, "persisted-id", (&Meta{ID: "persisted-id"}).Title())
}

func TestMarshal(t *testing.T) {
	registry := newRegistry()
	o, err := New[*Kind3]("acme", "my_project")
	assert.NoError(t, err)
	assert.NotNil(t, o)
	o.ID = "xxx"

	encoded, err := ToJSON(o)
	assert.NoError(t, err)
	assert.JSONEq(t, `{
		"kind": "kind3",
		"tenant": "acme",
		"namespace": "my_project",
		"id": "xxx"
	}`, string(encoded))

	t.Run("fromJSON", func(t *testing.T) {
		decoded, err := FromJSON(registry, encoded)
		assert.NoError(t, err)
		assert.NotNil(t, decoded)
		assert.Equal(t, o, decoded)
	})

	t.Run("readJSON", func(t *testing.T) {
		decoded, err := ReadJSON(registry, bytes.NewReader(encoded))
		assert.NoError(t, err)
		assert.NotNil(t, decoded)
		assert.Equal(t, o, decoded)
	})
}

func TestNestedStore(t *testing.T) {
	type nested struct {
		Public string `json:"public"`
		Alias  string `json:"alias" store:"stored"`
		Secret string `json:"-" store:"secret"`
	}
	type resource struct {
		Meta   `kind:"nestedstore" json:",inline"`
		Nested nested `json:"nested"`
	}

	registry := NewRegistry()
	MustRegister[*resource](registry)
	value, err := New[*resource]("acme", "default")
	require.NoError(t, err)
	value.Nested = nested{Public: "visible", Alias: "renamed", Secret: "stored"}

	data, err := ToJSON(value)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"id": "`+value.ID+`",
		"kind": "nestedstore",
		"tenant": "acme",
		"namespace": "default",
		"nested": {"public": "visible", "stored": "renamed", "secret": "stored"}
	}`, string(data))

	decoded, err := FromJSON(registry, data)
	require.NoError(t, err)
	assert.Equal(t, value.Nested, decoded.(*resource).Nested)
}

func TestStoreSlice(t *testing.T) {
	type nested struct {
		Name string `json:"name" store:"stored"`
	}
	type resource struct {
		Meta  `kind:"storeslice" json:",inline"`
		Items []nested `json:"items"`
	}

	registry := NewRegistry()
	MustRegister[*resource](registry)
	value, err := New[*resource]("acme", "default")
	require.NoError(t, err)
	value.Items = []nested{{Name: "one"}, {Name: "two"}}

	data, err := ToJSON(value)
	require.NoError(t, err)
	assert.JSONEq(t, `{"items":[{"stored":"one"},{"stored":"two"}],"id":"`+value.ID+`","kind":"storeslice","tenant":"acme","namespace":"default"}`, string(data))
	decoded, err := FromJSON(registry, data)
	require.NoError(t, err)
	assert.Equal(t, value.Items, decoded.(*resource).Items)
}

func TestObjectInternals(t *testing.T) {
	assert.False(t, scanStore(reflect.TypeOf(struct{}{})))
	assert.False(t, scanEmbed(reflect.TypeOf(struct{}{})))
	assert.Equal(t, reflect.TypeOf(Kind1{}), typeOf(Kind1{}))
	assert.Equal(t, "a.b\\.c", joinJSONPath("a", "b.c"))
	assert.Equal(t, "name", joinJSONPath("", "name"))
	assert.Equal(t, "prefix", joinJSONPath("prefix", ""))

	var visited int
	visit := func(string, string, reflect.Value) error {
		visited++
		return nil
	}
	assert.NoError(t, walkStoreFields(reflect.ValueOf((*Kind1)(nil)), "", "", visit))
	assert.NoError(t, walkStoreFields(reflect.ValueOf(1), "", "", visit))
	assert.Zero(t, visited)
}

func TestFromYAML(t *testing.T) {
	registry := newRegistry()
	o, err := New[*Kind3]("acme", "my_project")
	assert.NoError(t, err)
	assert.NotNil(t, o)
	o.ID = "xxx"

	decoded, err := FromYAML(registry, []byte(`
kind: kind3
tenant: acme
namespace: my_project
id: xxx
`))
	assert.NoError(t, err)
	assert.NotNil(t, decoded)
	assert.Equal(t, o, decoded)

	read, err := ReadYAML(registry, bytes.NewReader([]byte(`
kind: kind3
tenant: acme
namespace: my_project
id: xxx
`)))
	assert.NoError(t, err)
	assert.Equal(t, o, read)
}

func TestUnmarshalYAML(t *testing.T) {
	var out struct {
		Entry string `json:"entry"`
		Nodes []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"nodes"`
	}
	err := UnmarshalYAML([]byte(`
entry: start
nodes:
  - id: input
    type: input
`), &out)
	assert.NoError(t, err)
	assert.Equal(t, "start", out.Entry)
	require.Len(t, out.Nodes, 1)
	assert.Equal(t, "input", out.Nodes[0].ID)
}

func TestReadFile(t *testing.T) {
	var out struct {
		Name string `json:"name"`
	}
	err := ReadFile("workflow.yaml", []byte("name: nightly"), &out)
	require.NoError(t, err)
	assert.Equal(t, "nightly", out.Name)

	err = ReadFile("workflow.json", []byte(`{"name":"daily"}`), &out)
	require.NoError(t, err)
	assert.Equal(t, "daily", out.Name)

	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "workflow.json")
	require.NoError(t, os.WriteFile(jsonPath, []byte(`{"name":"from-disk"}`), 0o600))
	err = ReadFile(jsonPath, nil, &out)
	require.NoError(t, err)
	assert.Equal(t, "from-disk", out.Name)

	err = ReadFile("workflow.txt", []byte("name: bad"), &out)
	require.Error(t, err)
}

func TestFromJSON(t *testing.T) {
	t.Run("defaultNamespace", func(t *testing.T) {
		decoded, err := FromJSON(newRegistry(), []byte(`{
			"kind": "kind1",
			"tenant": "acme",
			"id": "persisted-id"
		}`))
		require.NoError(t, err)
		assert.Equal(t, "default", decoded.(*Kind1).Namespace)
	})

	t.Run("systemNamespace", func(t *testing.T) {
		type tenant struct {
			Meta `kind:"tenant" json:",inline"`
		}
		registry := NewRegistry()
		MustRegister[*tenant](registry)
		decoded, err := FromJSON(registry, []byte(`{
			"kind": "tenant",
			"tenant": "acme",
			"id": "persisted-id"
		}`))
		require.NoError(t, err)
		assert.Equal(t, "system", decoded.(*tenant).Namespace)
	})

	t.Run("malformedMetadata", func(t *testing.T) {
		tests := []string{
			`{"kind":"kind1","tenant":1,"namespace":"default","id":"id"}`,
			`{"kind":"kind1","tenant":"acme","namespace":"bad namespace","id":"id"}`,
			`{"kind":"kind1","tenant":"","namespace":"default","id":"id"}`,
		}
		for _, data := range tests {
			_, err := FromJSON(newRegistry(), []byte(data))
			assert.Error(t, err)
		}
	})

	t.Run("preservesPersistedID", func(t *testing.T) {
		registry := newRegistry()

		decoded, err := FromJSON(registry, []byte(`{
			"kind": "kind1",
			"tenant": "acme",
			"namespace": "my_project",
			"id": "persisted-id",
			"name": "example"
		}`))
		assert.NoError(t, err)
		assert.Equal(t, "persisted-id", decoded.(*Kind1).ID)
	})

	t.Run("preservesGeneratedID", func(t *testing.T) {
		registry := newRegistry()

		decoded, err := FromJSON(registry, []byte(`{
			"kind": "kind1",
			"tenant": "acme",
			"namespace": "my_project",
			"id": "",
			"name": "example"
		}`))
		assert.NoError(t, err)
		assert.NotNil(t, decoded)

		obj := decoded.(*Kind1)
		assert.NotEmpty(t, obj.ID)
		assert.Equal(t, "example", obj.Name)
	})
}

func TestPath(t *testing.T) {
	t.Run("field", func(t *testing.T) {
		tests := map[string]string{
			"engines.41354.type": "engines.type",
			"engines.41354":      "engines",
			"engines":            "engines",
			"foo.1.bar.2.baz":    "foo.bar.baz",
		}

		for path, expected := range tests {
			p := Path(path)
			assert.Equal(t, expected, p.field())
		}
	})

	t.Run("walk", func(t *testing.T) {
		tests := map[Path][]string{
			"foo.bar.baz":   {"foo", "foo.bar", "foo.bar.baz"},
			"foo.bar":       {"foo", "foo.bar"},
			"foo":           {"foo"},
			"foo.41354":     {"foo", "foo.41354"},
			"foo.41354.bar": {"foo", "foo.41354", "foo.41354.bar"},
		}

		for path, expected := range tests {
			var out []string
			for v := range path.Walk() {
				out = append(out, v.String())
			}

			assert.Equal(t, expected, out)
		}
	})

	t.Run("index", func(t *testing.T) {
		tests := map[string]int{
			"engines.41354.type": -1,
			"engines.41354":      41354,
			"engines":            -1,
			"foo.1.bar.2":        2,
		}

		for path, expected := range tests {
			p := Path(path)
			assert.Equal(t, expected, p.Index())
		}
	})
}

func TestCollect(t *testing.T) {
	items := []*Kind1{
		{Meta: Meta{Tenant: "acme", Namespace: "ns", Kind: "kind1", ID: "00000000000000000001"}, Name: "keep"},
		{Meta: Meta{Tenant: "acme", Namespace: "ns", Kind: "kind1", ID: "00000000000000000002"}, Name: "drop"},
		{Meta: Meta{Tenant: "acme", Namespace: "ns", Kind: "kind1", ID: "00000000000000000003"}, Name: "keep"},
	}

	t.Run("nil where keeps all", func(t *testing.T) {
		got := Collect(slices.Values(items), nil)
		assert.Equal(t, items, got)
		assert.GreaterOrEqual(t, cap(got), 64)
	})

	t.Run("filters with predicate", func(t *testing.T) {
		got := Collect(slices.Values(items), func(item *Kind1) bool {
			return item.Name == "keep"
		})
		require.Len(t, got, 2)
		assert.Equal(t, "keep", got[0].Name)
		assert.Equal(t, "keep", got[1].Name)
		assert.GreaterOrEqual(t, cap(got), 64)
	})

	t.Run("empty sequence", func(t *testing.T) {
		got := Collect(slices.Values([]*Kind1{}), nil)
		assert.Empty(t, got)
		assert.GreaterOrEqual(t, cap(got), 64)
	})
}

func TestSelect(t *testing.T) {
	items := []*Kind1{
		{Meta: Meta{Tenant: "acme", Namespace: "ns", Kind: "kind1", ID: "00000000000000000001"}, Name: "alpha"},
		{Meta: Meta{Tenant: "acme", Namespace: "ns", Kind: "kind1", ID: "00000000000000000002"}, Name: "skip"},
		{Meta: Meta{Tenant: "acme", Namespace: "ns", Kind: "kind1", ID: "00000000000000000003"}, Name: "beta"},
	}

	t.Run("projects matching items", func(t *testing.T) {
		got := Select(slices.Values(items), func(item *Kind1) (string, bool) {
			if item.Name == "skip" {
				return "", false
			}
			return item.Name, true
		})
		assert.Equal(t, []string{"alpha", "beta"}, got)
		assert.GreaterOrEqual(t, cap(got), 64)
	})

	t.Run("empty sequence", func(t *testing.T) {
		got := Select(slices.Values([]*Kind1{}), func(item *Kind1) (string, bool) {
			return item.Name, true
		})
		assert.Empty(t, got)
		assert.GreaterOrEqual(t, cap(got), 64)
	})
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
