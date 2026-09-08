package storage

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
)

type searchDocument struct {
	SearchEmbedded `json:",inline" search:"-"`
	Name           string `json:"name"`
	Link           string `json:"link" search:"-"`
	Nested         struct {
		Public string `json:"public"`
		Secret string `json:"secret" search:"-"`
	} `json:"nested"`
}

type SearchEmbedded struct {
	Public string `json:"public"`
	Secret string `json:"secret"`
}

func TestSearchFields(t *testing.T) {
	assert.Equal(t, []string{"public", "secret", "link", "nested.secret"}, searchFieldsOf(reflect.TypeFor[searchDocument]()))
	assert.Empty(t, searchFieldsOf(reflect.TypeFor[SearchEmbedded]()))
	assert.Empty(t, searchFieldsOf(reflect.TypeFor[string]()))
}
