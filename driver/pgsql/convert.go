package pgsql

import (
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/kelindar/storage"
)

type metaType struct {
	once  sync.Once
	index []int
}

var metaTypes sync.Map // map[reflect.Type]*metaType

// withMeta adds metadata to the record
func withMeta(v Record, meta *storage.Meta, createdBy, updatedBy string, createdAt, updatedAt time.Time) Record {
	meta.CreatedBy = createdBy
	meta.CreatedAt = createdAt.UnixNano()
	meta.UpdatedBy = updatedBy
	meta.UpdatedAt = updatedAt.UnixNano()
	return v
}

func read(scan func(...any) error, r storage.Registry) (Record, error) {
	var record struct {
		ID        string
		Namespace string
		State     string
		Data      []byte
		CreatedBy string
		UpdatedBy string
		CreatedAt int64
		UpdatedAt int64
		ExpiresAt int64
	}

	if err := scan(&record.ID,
		&record.Namespace,
		&record.State,
		&record.Data,
		&record.CreatedBy, &record.UpdatedBy,
		&record.CreatedAt, &record.UpdatedAt,
		&record.ExpiresAt,
	); err != nil {
		return nil, err
	}

	obj, err := storage.FromJSON(r, record.Data)
	if err != nil {
		return nil, err
	}
	meta := metaOf(obj)
	meta.ID = record.ID
	meta.Namespace = record.Namespace
	meta.State = record.State
	meta.ExpiresAt = record.ExpiresAt
	meta.CreatedBy = record.CreatedBy
	meta.UpdatedBy = record.UpdatedBy
	meta.CreatedAt = record.CreatedAt
	meta.UpdatedAt = record.UpdatedAt
	return obj, nil
}

func metaOf(v Record) *storage.Meta {
	typ := reflect.TypeOf(v)
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	value, ok := metaTypes.Load(typ)
	if !ok {
		info := new(metaType)
		actual, _ := metaTypes.LoadOrStore(typ, info)
		value = actual
	}
	info := value.(*metaType)
	info.once.Do(func() {
		field, _ := typ.FieldByName("Meta")
		info.index = field.Index
	})
	return reflect.ValueOf(v).Elem().FieldByIndex(info.index).Addr().Interface().(*storage.Meta)
}

// ---------------------------------- Text ----------------------------------

var (
	matchFirst = regexp.MustCompile("(.)([A-Z][a-z]+)")
	matchEvery = regexp.MustCompile("([a-z0-9])([A-Z])")
)

func snakeCase(str string) string {
	str = matchFirst.ReplaceAllString(str, "${1}_${2}")
	str = matchEvery.ReplaceAllString(str, "${1}_${2}")
	return strings.ToLower(str)
}

// tableOf returns the quoted table name for the specified resource kind.
func tableOf(kind storage.Kind) string {
	return quoteIdent(kind.String())
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// indexOf returns the index data of the record
func indexOf(r Record) string {
	if indexer, ok := r.(interface {
		Index() string
	}); ok {
		return indexer.Index()
	}

	return ""
}
