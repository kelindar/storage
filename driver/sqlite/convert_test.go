package sqlite

import (
	"testing"

	"github.com/kelindar/storage"
	"github.com/stretchr/testify/assert"
)

func TestOrderBy(t *testing.T) {
	tests := map[string]string{
		"":           "",
		"created_by": "created_by",
		"updated_by": "updated_by",
		"created_at": "created_at",
		"updated_at": "updated_at",
		"createdBy":  "created_by",
		"updatedBy":  "updated_by",
		"createdAt":  "created_at",
		"updatedAt":  "updated_at",
		"-createdBy": "created_by DESC",
		"hello":      "json_extract(data, '$.hello')",
		"+hello":     "json_extract(data, '$.hello')",
		"-hello":     "json_extract(data, '$.hello') DESC",
	}

	for in, out := range tests {
		assert.Equal(t, out, queryOrder(in))
	}
}

func TestFilterJSON(t *testing.T) {
	tests := map[string]struct {
		clause string
		args   []any
	}{
		"":               {},
		"tenant":         {`(tenant IN (?))`, []any{"x"}},
		"test":           {`(json_extract(data, ?) IN (?))`, []any{"$.test", "x"}},
		"something.name": {`(json_extract(data, ?) IN (?))`, []any{"$.something.name", "x"}},
	}

	for in, expected := range tests {
		clause, args := queryFilterByJSON(in, []string{"x"})
		assert.Equal(t, expected.clause, clause)
		assert.Equal(t, expected.args, args)
	}
}

func TestSearchTag(t *testing.T) {
	registry := storage.NewRegistry()
	textType := storage.MustRegister[*searchRecord](registry)
	plainType := storage.MustRegister[*plainRecord](registry)

	assert.Equal(t, []string{"link", "nested.secret"}, textType.SearchPaths)
	assert.Equal(t, "json_remove(data, '$.link', '$.nested.secret')", searchExpression("data", textType.SearchPaths))
	assert.Empty(t, plainType.SearchPaths)
	assert.Equal(t, "data", searchExpression("data", plainType.SearchPaths))
}

type searchRecord struct {
	storage.Meta `kind:"text_record" json:",inline"`
	Title        string      `json:"title"`
	Link         storage.URN `json:"link" search:"-"`
	Nested       struct {
		Public string `json:"public"`
		Secret string `json:"secret" search:"-"`
	} `json:"nested"`
}

type plainRecord struct {
	storage.Meta `kind:"plain_record" json:",inline"`
}
