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

func TestSearchBy(t *testing.T) {
	registry := storage.NewRegistry()
	textType := storage.MustRegister[*searchRecord](registry)
	plainType := storage.MustRegister[*plainRecord](registry)
	emptyType := storage.MustRegister[*emptySearchRecord](registry)

	assert.Equal(t, searchConfig{paths: []string{"title", "nested.summary"}, selected: true}, searchConfigOf(textType))
	assert.Equal(t, "(COALESCE(json_extract(data, '$.title'), '') || ' ' || COALESCE(json_extract(data, '$.nested.summary'), ''))", searchExpression("data", searchConfigOf(textType)))
	assert.Equal(t, "data", searchExpression("data", searchConfigOf(plainType)))
	assert.Equal(t, "''", searchExpression("data", searchConfigOf(emptyType)))
}

type searchRecord struct {
	storage.Meta `kind:"text_record" json:",inline"`
}

func (*searchRecord) SearchBy() []string { return []string{"title", "nested.summary"} }

type emptySearchRecord struct {
	storage.Meta `kind:"empty_search_record" json:",inline"`
}

func (*emptySearchRecord) SearchBy() []string { return nil }

type plainRecord struct {
	storage.Meta `kind:"plain_record" json:",inline"`
}
