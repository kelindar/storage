package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func BenchmarkURN(b *testing.B) {
	b.ReportAllocs()
	b.Run("new", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := NewURN("acme", "default", "namespace"); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("parse", func(b *testing.B) {
		urn, _ := NewURN("acme", "default", "namespace")
		txt := urn.String()
		for i := 0; i < b.N; i++ {
			_, err := ParseURN(txt)
			assert.NoError(b, err)
		}
	})
}

func TestNewURN(t *testing.T) {
	urn, err := NewURN("acme", "default", "namespace")
	assert.NoError(t, err)
	assert.NotEmpty(t, urn)
	assert.Equal(t, "acme", urn.Tenant)
	assert.Equal(t, "default", urn.Namespace)
	assert.Equal(t, "namespace", string(urn.Kind))
	assert.Len(t, urn.ID, 20)
	assert.NotEmpty(t, urn.String())
}

func TestMarshalURN(t *testing.T) {
	urn := "urn:acme:default:namespace:clmai59hq4ui597oseng"
	parsed, err := ParseURN(urn)
	assert.NoError(t, err)

	data, err := parsed.MarshalJSON()
	assert.NoError(t, err)

	var decoded URN
	assert.NoError(t, decoded.UnmarshalJSON(data))
	assert.Equal(t, parsed, decoded)
}

func TestMakeURN(t *testing.T) {
	urn, err := MakeURN("acme", "default", "namespace", "clmai59hq4ui597oseng")
	assert.NoError(t, err)
	assert.Equal(t, "urn:acme:default:namespace:clmai59hq4ui597oseng", urn.String())
}

func TestParseURN(t *testing.T) {
	t.Run("withID", func(t *testing.T) {
		urn := "urn:acme:default:namespace:clmai59hq4ui597oseng"
		parsed, err := ParseURN(urn)
		assert.NoError(t, err)
		assert.NotEmpty(t, parsed)
		assert.Equal(t, "acme", parsed.Tenant)
		assert.Equal(t, "default", parsed.Namespace)
		assert.Equal(t, "namespace", string(parsed.Kind))
		assert.Equal(t, "clmai59hq4ui597oseng", parsed.ID)
		assert.Equal(t, urn, parsed.String())
		assert.True(t, parsed.IsValid())
	})

	t.Run("withoutID", func(t *testing.T) {
		urn := "urn:acme:default:namespace:*"
		parsed, err := ParseURN(urn)
		assert.NoError(t, err)
		assert.NotEmpty(t, parsed)
		assert.Equal(t, "acme", parsed.Tenant)
		assert.Equal(t, "default", parsed.Namespace)
		assert.Equal(t, "namespace", string(parsed.Kind))
		assert.Len(t, parsed.ID, 20)
		assert.True(t, parsed.IsValid())
	})

	t.Run("shortest", func(t *testing.T) {
		urn := "urn:ab:cd:ef:*"
		parsed, err := ParseURN(urn)
		assert.NoError(t, err)
		assert.NotEmpty(t, parsed)
		assert.Equal(t, "ab", parsed.Tenant)
		assert.Equal(t, "cd", parsed.Namespace)
		assert.Equal(t, "ef", string(parsed.Kind))
		assert.Len(t, parsed.ID, 20)
		assert.True(t, parsed.IsValid())
	})

	t.Run("errors", func(t *testing.T) {
		tests := []string{
			"",
			"urn",
			"urn:",
			"urn:tenant",
			"urn:tenant:namespace",
			"urn:tenant:namespace:kind",
			"urn:tenant:namespace:kind:",
			"urn:tenant:namespace:kind:clmai59hq4ui597oseng:",
			"urn:tenant:namespace:kind:clmai59hq4ui597oseng:extra",
			"urn:tenant:namespace:kind:clmai59hq4ui597oseng1",
			"urn:tenant:123:kind:clmai59hq4ui597oseng",
			"urn:123:namespace:kind:clmai59hq4ui597oseng",
			"urn:tenant:namespace:123:clmai59hq4ui597oseng",
		}

		for _, test := range tests {
			t.Run(test, func(t *testing.T) {
				_, err := ParseURN(test)
				assert.Error(t, err)
			})
		}
	})
}

func TestNewURN_Errors(t *testing.T) {
	tests := []struct {
		Tenant    string
		Namespace string
		Kind      string
	}{
		{"", "", ""},
		{"tenant", "", ""},
		{"tenant", "namespace", ""},
		{"tenant", "namespace", "123"},
	}

	for _, test := range tests {
		t.Run(test.Tenant+":"+test.Namespace+":"+test.Kind, func(t *testing.T) {
			_, err := NewURN(test.Tenant, test.Namespace, Kind(test.Kind))
			assert.Error(t, err)
		})
	}
}

func TestEmptyURN(t *testing.T) {
	urn := URN{}
	assert.False(t, urn.IsValid())
	assert.Empty(t, urn.String())
	assert.Empty(t, urn.Tenant)
	assert.Empty(t, urn.Namespace)
	assert.Empty(t, string(urn.Kind))
	assert.Empty(t, urn.ID)
}
