package validate

import (
	"fmt"
	"reflect"
	"strconv"

	"github.com/kelindar/storage/internal/walk"
)

// Create validates form field access for a create payload. Value must be a
// non-nil pointer to a struct. Populated fields tagged form:"ro" are rejected;
// form:"rw" and form:"create" are allowed.
func Create(value any) error {
	if _, err := formStruct(value); err != nil {
		return err
	}

	var errs Errors
	_ = walk.Walk(value, func(v reflect.Value, field *reflect.StructField, path []string) error {
		if field != nil && field.Tag.Get("form") == "ro" && !walk.IsEmpty(v) {
			errs = append(errs, readOnlyError(field, path))
		}
		return nil
	})
	if len(errs) > 0 {
		return errs
	}
	return nil
}

// Update validates form field access for an update payload. Current and
// incoming must be non-nil pointers to the same struct type. Populated fields
// tagged form:"ro" or form:"create" must match the current value. On success,
// Update clears read-only fields that are empty in current from incoming as
// unstored projections.
func Update(current, incoming any) error {
	currentValue, err := formStruct(current)
	if err != nil {
		return fmt.Errorf("validate current: %w", err)
	}
	incomingValue, err := formStruct(incoming)
	if err != nil {
		return fmt.Errorf("validate incoming: %w", err)
	}
	if currentValue.Type() != incomingValue.Type() {
		return fmt.Errorf("validate update: current and incoming types differ: %s and %s", currentValue.Type(), incomingValue.Type())
	}

	var errs Errors
	var projections []reflect.Value
	_ = walk.Walk(incoming, func(v reflect.Value, field *reflect.StructField, path []string) error {
		switch {
		case field == nil || walk.IsEmpty(v):
			return nil
		case field.Tag.Get("form") != "ro" && field.Tag.Get("form") != "create":
			return nil
		}

		currentField, ok := formValueAt(currentValue, path)
		switch {
		case (!ok || walk.IsEmpty(currentField)) && v.CanSet():
			projections = append(projections, v)
			return nil
		case ok && sameFormValue(currentField, v):
			return nil
		default:
			errs = append(errs, readOnlyError(field, path))
			return nil
		}
	})
	if len(errs) > 0 {
		return errs
	}
	for _, projection := range projections {
		projection.SetZero()
	}
	return nil
}

func formStruct(value any) (reflect.Value, error) {
	v := reflect.ValueOf(value)
	if !v.IsValid() || v.Kind() != reflect.Pointer || v.IsNil() {
		return reflect.Value{}, fmt.Errorf("value must be a non-nil pointer to struct")
	}
	v = v.Elem()
	if v.Kind() != reflect.Struct {
		return reflect.Value{}, fmt.Errorf("value must point to a struct, got %s", v.Kind())
	}
	return v, nil
}

func readOnlyError(field *reflect.StructField, path []string) Error {
	return errorf(field, path, "readonly", "%s is read-only", nameOf(field))
}

func formValueAt(v reflect.Value, path []string) (reflect.Value, bool) {
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return reflect.Value{}, false
		}
		v = v.Elem()
	}
	if len(path) == 0 {
		return v, true
	}

	segment, rest := path[0], path[1:]
	switch v.Kind() {
	case reflect.Struct:
		field, ok := formField(v.Type(), segment)
		if !ok {
			return reflect.Value{}, false
		}
		return formValueAt(v.FieldByIndex(field.Index), rest)
	case reflect.Slice, reflect.Array:
		index, err := strconv.Atoi(segment)
		if err != nil || index < 0 || index >= v.Len() {
			return reflect.Value{}, false
		}
		return formValueAt(v.Index(index), rest)
	case reflect.Map:
		key, ok := formMapKey(v, segment)
		if !ok {
			return reflect.Value{}, false
		}
		return formValueAt(v.MapIndex(key), rest)
	default:
		return reflect.Value{}, false
	}
}

func formField(typ reflect.Type, name string) (reflect.StructField, bool) {
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.PkgPath == "" && nameOf(&field) == name {
			return field, true
		}
	}
	return reflect.StructField{}, false
}

func formMapKey(v reflect.Value, segment string) (reflect.Value, bool) {
	iter := v.MapRange()
	for iter.Next() {
		key := iter.Key()
		if fmt.Sprint(key.Interface()) == segment {
			return key, true
		}
	}
	return reflect.Value{}, false
}

func sameFormValue(a, b reflect.Value) bool {
	a, b = reflect.Indirect(a), reflect.Indirect(b)
	return a.IsValid() && b.IsValid() && reflect.DeepEqual(a.Interface(), b.Interface())
}
