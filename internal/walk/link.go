package walk

import (
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/kelindar/storage"
)

type linkType struct {
	once   sync.Once
	count  int
	fields []linkField
}

type linkField struct {
	index   int
	kind    storage.Kind
	name    string
	inline  bool
	nested  bool
	ignored bool
}

var linkTypes sync.Map // map[reflect.Type]*linkType
var urnType = reflect.TypeFor[storage.URN]()

// Links returns links declared by tags or by the object's Linker method.
func Links(obj storage.Object) ([]storage.Link, error) {
	links, err := taggedLinks(obj)
	if err != nil {
		return nil, err
	}
	if linker, ok := obj.(storage.Linker); ok {
		declared, err := linker.Links()
		if err != nil {
			return nil, err
		}
		links = append(links, declared...)
	}
	return links, nil
}

func taggedLinks(obj storage.Object) ([]storage.Link, error) {
	info := linkInfo(reflect.TypeOf(obj))
	if info == nil || info.count == 0 {
		return nil, nil
	}

	out := make([]storage.Link, 0, info.count)
	source := obj.URN()
	var path [256]byte
	if err := walkLinks(reflect.ValueOf(obj), path[:0], source, &out); err != nil {
		return nil, err
	}
	slices.SortFunc(out, compareLink)
	return out, nil
}

func walkLinks(v reflect.Value, path []byte, source storage.URN, out *[]storage.Link) error {
	for v.IsValid() && (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	if !v.IsValid() {
		return nil
	}

	switch v.Kind() {
	case reflect.Struct:
		return walkStructLinks(v, path, source, out)
	case reflect.Slice, reflect.Array:
		return walkSliceLinks(v, path, source, out)
	case reflect.Map:
		return walkMapLinks(v, path, source, out)
	}
	return nil
}

func walkStructLinks(v reflect.Value, path []byte, source storage.URN, out *[]storage.Link) error {
	info := linkInfo(v.Type())
	for _, field := range info.fields {
		value := v.Field(field.index)
		nested := field.nested && containsLinks(value)
		switch {
		case field.kind == "" && !nested:
			continue
		case field.kind != "" && !nested && (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface):
			continue
		case field.kind != "" && !nested && emptyLink(value):
			continue
		}

		fieldPath := path
		if !field.inline {
			fieldPath = appendPath(fieldPath, field.name)
		}
		if field.kind != "" && value.Kind() != reflect.Pointer && value.Kind() != reflect.Interface {
			if err := extractValue(value, fieldPath, source, field.kind, out); err != nil {
				return err
			}
		}
		if err := walkNestedLinks(nested, value, fieldPath, source, out); err != nil {
			return err
		}
	}
	return nil
}

func walkNestedLinks(nested bool, value reflect.Value, path []byte, source storage.URN, out *[]storage.Link) error {
	if !nested {
		return nil
	}
	return walkLinks(value, path, source, out)
}

func walkSliceLinks(v reflect.Value, path []byte, source storage.URN, out *[]storage.Link) error {
	for i := 0; i < v.Len(); i++ {
		if err := walkLinks(v.Index(i), appendIndex(path, i), source, out); err != nil {
			return err
		}
	}
	return nil
}

func walkMapLinks(v reflect.Value, path []byte, source storage.URN, out *[]storage.Link) error {
	keys := v.MapKeys()
	sort.Slice(keys, func(i, j int) bool {
		return mapKeyString(keys[i]) < mapKeyString(keys[j])
	})
	for _, key := range keys {
		if err := walkLinks(v.MapIndex(key), appendMapKey(path, key), source, out); err != nil {
			return err
		}
	}
	return nil
}

func containsLinks(v reflect.Value) bool {
	for v.IsValid() && v.Kind() == reflect.Interface {
		if v.IsNil() {
			return false
		}
		v = v.Elem()
	}
	switch {
	case !v.IsValid():
		return false
	case v.Kind() == reflect.Pointer && v.IsNil():
		return false
	}
	switch v.Kind() {
	case reflect.Struct, reflect.Pointer, reflect.Array, reflect.Map, reflect.Slice:
		if (v.Kind() == reflect.Array || v.Kind() == reflect.Map || v.Kind() == reflect.Slice) && v.Len() == 0 {
			return false
		}
		return hasLink(v.Type())
	default:
		return false
	}
}

func emptyLink(v reflect.Value) bool {
	switch {
	case v.Type() == urnType:
		return readURN(v) == (storage.URN{})
	case v.Kind() == reflect.String, v.Kind() == reflect.Array, v.Kind() == reflect.Map, v.Kind() == reflect.Slice:
		return v.Len() == 0
	default:
		return false
	}
}

func hasLink(typ reflect.Type) bool {
	info := linkInfo(typ)
	return info != nil && info.count > 0
}

func linkInfo(typ reflect.Type) *linkType {
	if typ == nil {
		return nil
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}

	value, ok := linkTypes.Load(typ)
	if ok {
		info := value.(*linkType)
		initLinkType(typ, info)
		return info
	}
	info := new(linkType)
	actual, _ := linkTypes.LoadOrStore(typ, info)
	info = actual.(*linkType)
	initLinkType(typ, info)
	return info
}

func initLinkType(typ reflect.Type, info *linkType) {
	info.once.Do(func() {
		info.count = scanLinks(typ, nil)
		if typ.Kind() == reflect.Struct {
			info.fields = scanFields(typ)
		}
	})
}

func scanFields(typ reflect.Type) []linkField {
	fields := make([]linkField, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.PkgPath != "" {
			continue
		}
		info := parseField(field)
		info.index = i
		info.nested = scanLinks(field.Type, nil) > 0
		switch {
		case info.ignored:
			continue
		case info.kind == "" && !info.nested:
			continue
		}
		fields = append(fields, info)
	}
	return fields
}

func scanLinks(typ reflect.Type, seen map[reflect.Type]struct{}) int {
	if typ == nil {
		return 0
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if _, ok := seen[typ]; ok {
		return 0
	}
	if seen == nil {
		seen = make(map[reflect.Type]struct{})
	}
	seen[typ] = struct{}{}

	switch typ.Kind() {
	case reflect.Struct:
		count := 0
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.PkgPath != "" {
				continue
			}
			info := parseField(field)
			if info.ignored {
				continue
			}
			if info.kind != "" {
				count++
			}
			count += scanLinks(field.Type, seen)
		}
		return count
	case reflect.Array, reflect.Map, reflect.Slice:
		return scanLinks(typ.Elem(), seen)
	case reflect.Interface:
		// The dynamic value may carry link tags.
		return 1
	}
	return 0
}

func parseField(field reflect.StructField) linkField {
	jsonTag := field.Tag.Get("json")
	jsonName, options := jsonTag, ""
	if comma := strings.IndexByte(jsonTag, ','); comma >= 0 {
		jsonName, options = jsonTag[:comma], jsonTag[comma+1:]
	}
	linkTag := field.Tag.Get("link")
	if jsonName == "-" || linkTag == "-" {
		return linkField{ignored: true}
	}
	if jsonName == "" {
		jsonName = field.Name
	}
	return linkField{
		kind:   storage.Kind(linkTag),
		name:   jsonName,
		inline: field.Anonymous || hasOption(options, "inline"),
	}
}

func hasOption(options, want string) bool {
	for {
		comma := strings.IndexByte(options, ',')
		if comma < 0 {
			return options == want
		}
		if options[:comma] == want {
			return true
		}
		options = options[comma+1:]
	}
}

func extractValue(v reflect.Value, path []byte, source storage.URN, kind storage.Kind, out *[]storage.Link) error {
	for v.IsValid() && (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	if !v.IsValid() {
		return nil
	}
	switch v.Kind() {
	case reflect.Slice, reflect.Array:
		return extractSlice(v, path, source, kind, out)
	case reflect.Map:
		return extractMap(v, path, source, kind, out)
	}

	var target storage.URN
	switch {
	case v.Type() == urnType:
		target = readURN(v)
		if target == (storage.URN{}) {
			return nil
		}
	case v.Kind() == reflect.String:
		if v.Len() == 0 {
			return nil
		}
		parsed, err := storage.ParseURN(v.String())
		if err != nil {
			return fmt.Errorf("storage: invalid link at %s: %w", path, err)
		}
		target = parsed
	default:
		return fmt.Errorf("storage: link at %s must be a URN or string", path)
	}
	if !target.IsValid() || target.Kind != kind {
		return fmt.Errorf("storage: invalid link at %s", path)
	}
	*out = append(*out, storage.Use(source, target, storage.Path(string(path))))
	return nil
}

func extractSlice(v reflect.Value, path []byte, source storage.URN, kind storage.Kind, out *[]storage.Link) error {
	for i := 0; i < v.Len(); i++ {
		if err := extractValue(v.Index(i), appendIndex(path, i), source, kind, out); err != nil {
			return err
		}
	}
	return nil
}

func extractMap(v reflect.Value, path []byte, source storage.URN, kind storage.Kind, out *[]storage.Link) error {
	keys := v.MapKeys()
	sort.Slice(keys, func(i, j int) bool {
		return mapKeyString(keys[i]) < mapKeyString(keys[j])
	})
	for _, key := range keys {
		if err := extractValue(v.MapIndex(key), appendMapKey(path, key), source, kind, out); err != nil {
			return err
		}
	}
	return nil
}

func extractTagged(v reflect.Value, path []string, source storage.URN, kind storage.Kind, out *[]storage.Link) error {
	var buf []byte
	for _, name := range path {
		buf = appendPath(buf, name)
	}
	return extractValue(v, buf, source, kind, out)
}

func appendPath(path []byte, name string) []byte {
	if len(path) > 0 {
		path = append(path, '.')
	}
	return append(path, name...)
}

func appendIndex(path []byte, index int) []byte {
	if len(path) > 0 {
		path = append(path, '.')
	}
	return strconv.AppendInt(path, int64(index), 10)
}

func appendMapKey(path []byte, key reflect.Value) []byte {
	if len(path) > 0 {
		path = append(path, '.')
	}
	switch key.Kind() {
	case reflect.String:
		return append(path, key.String()...)
	case reflect.Bool:
		return strconv.AppendBool(path, key.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.AppendInt(path, key.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.AppendUint(path, key.Uint(), 10)
	case reflect.Float32, reflect.Float64:
		return strconv.AppendFloat(path, key.Float(), 'g', -1, key.Type().Bits())
	default:
		return append(path, fmt.Sprint(key.Interface())...)
	}
}

func mapKeyString(key reflect.Value) string {
	switch key.Kind() {
	case reflect.String:
		return key.String()
	case reflect.Bool:
		return strconv.FormatBool(key.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(key.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(key.Uint(), 10)
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(key.Float(), 'g', -1, key.Type().Bits())
	default:
		return fmt.Sprint(key.Interface())
	}
}

func readURN(v reflect.Value) storage.URN {
	return storage.URN{
		Tenant:    v.Field(0).String(),
		Namespace: v.Field(1).String(),
		Kind:      storage.Kind(v.Field(2).String()),
		ID:        v.Field(3).String(),
	}
}

func compareLink(a, b storage.Link) int {
	if a.Path != b.Path {
		if a.Path < b.Path {
			return -1
		}
		return 1
	}
	return compareURN(a.Target, b.Target)
}

func compareURN(a, b storage.URN) int {
	switch {
	case a.Tenant != b.Tenant:
		return compareString(a.Tenant, b.Tenant)
	case a.Namespace != b.Namespace:
		return compareString(a.Namespace, b.Namespace)
	case a.Kind != b.Kind:
		return compareString(string(a.Kind), string(b.Kind))
	default:
		return compareString(a.ID, b.ID)
	}
}

func compareString(a, b string) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}
