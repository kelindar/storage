package storage

import (
	"reflect"
	"strings"
)

func searchFieldsOf(typ reflect.Type) []string {
	var paths []string
	walkSearchType(typ, "", &paths, false)
	return paths
}

func walkSearchType(typ reflect.Type, prefix string, paths *[]string, excluded bool) {
	for typ != nil && typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ == nil || typ.Kind() != reflect.Struct {
		return
	}

	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.PkgPath != "" {
			continue
		}

		name, inline := searchName(field)
		switch {
		case excluded && inline:
			walkSearchType(field.Type, prefix, paths, true)
		case excluded && name != "":
			*paths = append(*paths, joinSearchPath(prefix, name))
		case searchExcluded(field):
			if inline {
				walkSearchType(field.Type, prefix, paths, true)
			} else if name != "" {
				*paths = append(*paths, joinSearchPath(prefix, name))
			}
		case inline:
			walkSearchType(field.Type, prefix, paths, false)
		case name != "":
			walkSearchType(field.Type, joinSearchPath(prefix, name), paths, false)
		}
	}
}

func searchName(field reflect.StructField) (string, bool) {
	tag := strings.Split(field.Tag.Get("json"), ",")
	inline := field.Anonymous
	for _, option := range tag[1:] {
		inline = inline || option == "inline"
	}

	switch {
	case len(tag) > 0 && tag[0] == "-":
		return "", false
	case len(tag) > 0 && tag[0] != "":
		return tag[0], inline
	case inline:
		return "", true
	default:
		return field.Name, false
	}
}

func searchExcluded(field reflect.StructField) bool {
	tag := strings.Split(field.Tag.Get("search"), ",")
	return len(tag) > 0 && tag[0] == "-"
}

func joinSearchPath(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}
