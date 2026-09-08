package pgsql

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/kelindar/storage"
	"github.com/stretchr/testify/assert"
)

type convertRecord struct {
	storage.Meta `kind:"convert_record" json:",inline"`
	Name         string `json:"name"`
}

type indexedRecord struct {
	storage.Meta `kind:"indexed_record" json:",inline"`
	Name         string `json:"name"`
	Secret       string `json:"secret" search:"-"`
}

func (indexedRecord) Index() string { return "indexed" }

func TestConvert(t *testing.T) {
	registry := storage.NewRegistry()
	storage.MustRegister[*convertRecord](registry)

	value := &convertRecord{}
	withMeta(value, &value.Meta, "created", "updated", time.Unix(1, 2), time.Unix(3, 4))
	testConvertMetadata(t, value)
	testConvertHelpers(t, value)
	testConvertRead(t, registry)
	if got := metaOf(value); got != &value.Meta {
		t.Fatal("metaOf returned the wrong field")
	}
	if reflect.TypeFor[*convertRecord]().Kind() != reflect.Pointer {
		t.Fatal("unexpected test type")
	}
}

func testConvertMetadata(t *testing.T, value *convertRecord) {
	if value.CreatedBy != "created" || value.UpdatedBy != "updated" || value.CreatedAt != 1000000002 || value.UpdatedAt != 3000000004 {
		t.Fatalf("withMeta did not copy metadata: %#v", value.Meta)
	}
}

func testConvertHelpers(t *testing.T, value *convertRecord) {
	if got := snakeCase("HTTPServerID"); got != "http_server_id" {
		t.Fatalf("snakeCase = %q", got)
	}
	if got := tableOf("Thing"); got != `"thing"` {
		t.Fatalf("tableOf = %q", got)
	}
	if got := quoteIdent(`a"b`); got != `"a""b"` {
		t.Fatalf("quoteIdent = %q", got)
	}
	if got := indexOf(&indexedRecord{}); got != "indexed" {
		t.Fatalf("indexOf indexed = %q", got)
	}
	if got := indexOf(value); got != "" {
		t.Fatalf("indexOf plain = %q", got)
	}
	registry := storage.NewRegistry()
	searchType := storage.MustRegister[*indexedRecord](registry)
	assert.Equal(t, []string{"secret"}, searchType.SearchPaths)
	assert.Equal(t, "(data #- string_to_array('secret', '.'))::text", searchExpression("data", searchType.SearchPaths))
	assert.Equal(t, "data::text", searchExpression("data", nil))
}

func testConvertRead(t *testing.T, registry storage.Registry) {
	scan := func(dest ...any) error {
		values := []any{"id", "default", "active", []byte(`{"kind":"convert_record","tenant":"acme","namespace":"default","name":"stored"}`), "created", "updated", int64(1), int64(2), int64(3)}
		for i := range dest {
			switch dst := dest[i].(type) {
			case *string:
				*dst = values[i].(string)
			case *[]byte:
				*dst = values[i].([]byte)
			case *int64:
				*dst = values[i].(int64)
			}
		}
		return nil
	}
	readValue, err := read(scan, registry)
	if err != nil || readValue.URN().ID != "id" || readValue.Status() != "active" {
		t.Fatalf("read = %#v, %v", readValue, err)
	}
	if _, err := read(func(...any) error { return errors.New("scan failed") }, registry); err == nil {
		t.Fatal("read accepted a scan error")
	}
	if _, err := read(func(dest ...any) error {
		for _, value := range dest {
			if out, ok := value.(*[]byte); ok {
				*out = []byte(`{"kind":"missing"}`)
			}
		}
		return nil
	}, registry); err == nil {
		t.Fatal("read accepted an unknown kind")
	}
}
