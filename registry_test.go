package storage

import (
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRegistry(t *testing.T) {
	t.Run("invalidResource", func(t *testing.T) {
		r := NewRegistry()
		assert.NotNil(t, r)

		assert.Panics(t, func() {
			_, _ = Register[*struct {
				Meta
				Resource string `json:"xxx,inline"`
			}](r)
		})
	})

	t.Run("range", func(t *testing.T) {
		registry := NewRegistry()
		assert.NotNil(t, registry)
		assert.NotPanics(t, func() {
			_, err := Register[*Kind1](registry)
			assert.NoError(t, err)
		})

		r, err := New[*Kind1]("acme", "my_project")
		assert.NoError(t, err)
		assert.NotNil(t, r)

		count := 0
		for typ := range registry.Types() {
			assert.NotNil(t, typ)
			count++
		}
		assert.Equal(t, 1, count)

		typ, err := registry.Resolve("kind1")
		assert.NoError(t, err)
		assert.Equal(t, reflect.TypeFor[Kind1](), typ.Type)
	})

	t.Run("registerInvalid", func(t *testing.T) {
		r := NewRegistry()
		assert.NotNil(t, r)
		assert.Error(t, r.Register(Type{
			Kind: "kind1",
			Type: reflect.TypeFor[time.Time](),
		}))

		assert.Error(t, r.Register(Type{
			Kind: "kind1",
			Type: reflect.TypeFor[Kind5](),
		}))
	})
}

func TestRegistryGuards(t *testing.T) {
	registry := NewRegistry()
	_, err := registry.Resolve("missing")
	assert.ErrorIs(t, err, ErrKindNotFound)

	assert.Panics(t, func() { MustRegister[*invalidResource](registry) })

	type noMeta struct{}
	type noKind struct{ Meta }
	type badMeta struct{ Meta string }
	type badKind struct {
		Meta `kind:"1bad"`
	}
	for _, typ := range []reflect.Type{
		reflect.TypeOf(noMeta{}), reflect.TypeOf(noKind{}), reflect.TypeOf(badMeta{}), reflect.TypeOf(badKind{}),
	} {
		_, err := KindOf(typ)
		assert.Error(t, err)
	}

	tests := []struct {
		field  reflect.StructField
		name   string
		inline bool
	}{
		{field: reflect.StructField{Name: "Field", Tag: `json:"name"`}, name: "name"},
		{field: reflect.StructField{Name: "Field", Tag: `json:",inline"`}, inline: true},
		{field: reflect.StructField{Name: "Field", Tag: `json:"-"`}},
		{field: reflect.StructField{Name: "Field"}},
	}
	assert.Equal(t, reflect.TypeOf(Kind1{}), typeOf(Kind1{}))
	for _, tc := range tests {
		name, inline := jsonName(tc.field)
		assert.Equal(t, tc.name, name)
		assert.Equal(t, tc.inline, inline)
	}
}

// ---------------------------------- Test Types ----------------------------------

type Kind1 struct {
	Meta `kind:"kind1" json:",inline"`
	Name string `json:"name"`
	Link URN    `json:"link"`
}

type invalidResource struct {
	Meta
}

type Kind2 struct {
	Meta `kind:"kind2" json:",inline"`
	Name string
	Env  string `json:"env"`
	App  URN    `json:"app" kind:"kind1" query:"namespace=*;match=Inc"`
}

type Kind3 struct {
	Meta `kind:"kind3" json:",inline"`
}

type Kind4 struct {
	Meta   `kind:"kind4" json:",inline"`
	Name   string
	Before Embed `json:"before"`
	After  Embed `json:"after"`
}

type Kind5 struct {
	Meta `kind:"kind2" json:",inline"`
	Name string
	Env  string `json:"env"`
	App  URN    `json:"app" kind:"kind1" query:"xxx"`
}

func newRegistry() Registry {
	registry := NewRegistry()
	MustRegister[*Kind1](registry)
	MustRegister[*Kind2](registry)
	MustRegister[*Kind3](registry)
	MustRegister[*Kind4](registry)
	return registry
}

// ---------------------------------- Field Paths ----------------------------------

type Engine struct {
	Type  string `json:"type"`
	Power int    `json:"power"`
}

type Employee struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

type Department struct {
	Name      string     `json:"name"`
	Employees []Employee `json:"employees,omitempty"`
}

type Address struct {
	Street string `json:"street"`
	City   string `json:"city"`
}

type Company struct {
	Name        string       `json:"name"`
	Address     *Address     `json:"address,omitempty"`
	Departments []Department `json:"departments,omitempty"`
}

type Car struct {
	Meta        `kind:"car" json:",inline"`
	Type        string   `json:"type"`
	Year        int      `json:"year"`
	Model       string   `json:"model"`
	Description string   `json:"description"`
	Company     string   `json:"company"`
	Engine      *Engine  `json:"engine,omitempty"`
	Engines     []Engine `json:"engines,omitempty"`
	CompanyInfo *Company `json:"companyInfo,omitempty"`
}

func TestFieldsOf(t *testing.T) {
	registry := newRegistry()

	typ, err := registry.Resolve("kind1")
	assert.NoError(t, err)
	assert.NotNil(t, typ)

	f, ok := typ.Field("name")
	assert.True(t, ok)
	assert.Equal(t, "Name", f.Name)
	assert.Equal(t, 12, len(typ.fields))
}

func TestPathMeta(t *testing.T) {
	p := Path("name")
	assert.Equal(t, "name", p.String())
	assert.Equal(t, "prefix-6e616d65", p.ID("prefix"))
	assert.Equal(t, "Name", p.Label())
}

func TestFieldSlice(t *testing.T) {
	registry := newRegistry()
	MustRegister[*Car](registry)

	typ, err := registry.Resolve("car")
	assert.NoError(t, err)
	assert.NotNil(t, typ)

	t.Run("elemType", func(t *testing.T) {
		f, ok := typ.Field("engines.41354")
		assert.True(t, ok)
		assert.Equal(t, reflect.Slice, f.Type.Kind())
	})

	t.Run("slice", func(t *testing.T) {
		f, ok := typ.Field("engines")
		assert.True(t, ok)
		assert.Equal(t, reflect.Slice, f.Type.Kind())
	})

	t.Run("elemField", func(t *testing.T) {
		f, ok := typ.Field("engines.41354.type")
		assert.True(t, ok)
		assert.Equal(t, reflect.String, f.Type.Kind())
	})
}
