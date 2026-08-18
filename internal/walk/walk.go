package walk

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
)

func Walk(value any, fn func(v reflect.Value, field *reflect.StructField, path []string) error) error {
	v := reflect.ValueOf(value)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return errors.New("value must be a non-nil pointer")
	}
	return walk(v, nil, nil, fn)
}

func walk(v reflect.Value, field *reflect.StructField, path []string, fn func(v reflect.Value, field *reflect.StructField, path []string) error) error {
	for {
		if err := fn(v, field, path); err != nil {
			return err
		}
		switch v.Kind() {
		case reflect.Pointer, reflect.Interface:
			if v.IsNil() {
				return nil
			}
			v = v.Elem()
		default:
			return walkValue(v, path, fn)
		}
	}
}

func walkValue(v reflect.Value, path []string, fn func(v reflect.Value, field *reflect.StructField, path []string) error) error {
	switch v.Kind() {
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			fieldInfo := v.Type().Field(i)
			if fieldInfo.PkgPath != "" {
				// Unexported field
				continue
			}

			fieldValue := v.Field(i)
			fieldPath := append(path, nameOf(&fieldInfo))
			// Call fn with field information
			if err := walk(fieldValue, &fieldInfo, fieldPath, fn); err != nil {
				return err
			}
		}

	// If the value is a slice or array, walk its elements
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			elemValue := v.Index(i)
			elemPath := append(path, strconv.Itoa(i))
			if err := walk(elemValue, nil, elemPath, fn); err != nil {
				return err
			}
		}

	// If the value is a map, walk its key-value pairs
	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			key := iter.Key()
			value := iter.Value()
			keyStr := fmt.Sprintf("%v", key.Interface())
			valuePath := append(path, keyStr)
			if err := walk(value, nil, valuePath, fn); err != nil {
				return err
			}
		}
	}
	return nil
}
