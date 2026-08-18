package storage

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kelindar/storage/convert"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"sigs.k8s.io/yaml"
)

type objectType struct {
	once   sync.Once
	meta   []int
	store  bool
	embeds bool
}

var objectTypes sync.Map // map[reflect.Type]*objectType

// Object represents an object in the system.
type Object interface {
	URN() URN                     // URN returns the uniform identifier of the object
	Status() string               // Status returns the current state
	Created() (string, time.Time) // Created returns createdBy and createdAt information
	Updated() (string, time.Time) // Updated returns updatedBy and updatedAt information
}

// Kind represents a resource Kind (e.g. "Document", "Sprite")
type Kind string

// String returns the string representation of the resource kind.
func (k Kind) String() string {
	return strings.ToLower(string(k))
}

// Meta represents a metadata of the object.
type Meta struct {
	ID        string `json:"id" form:"-"`                  // Globally unique identifier (e.g. "9m4e2mr0ui3e8a215n4g")
	Kind      Kind   `json:"kind" form:"-"`                // Meta kind (e.g. "deployment")
	Tenant    string `json:"tenant" form:"-"`              // Tenant slug (e.g. "acme")
	Namespace string `json:"namespace" form:"-"`           // Namespace of the object (e.g. "default")
	State     string `json:"state,omitempty"  form:"-"`    // State is the current state of the resource
	CreatedBy string `json:"createdBy,omitempty" form:"-"` // CreatedBy is the user who created the resource
	CreatedAt int64  `json:"createdAt,omitempty" form:"-"` // CreatedAt is the time when the resource was created
	UpdatedBy string `json:"updatedBy,omitempty" form:"-"` // UpdatedBy is the user who last updated the resource
	UpdatedAt int64  `json:"updatedAt,omitempty" form:"-"` // UpdatedAt is the time when the resource was last updated
	ExpiresAt int64  `json:"expiresAt,omitempty" form:"-"` // ExpiresAt is when the resource becomes eligible for deletion
}

// New creates a new instance of the specified resource kind.
func New[T Object](tenant, namespace string, funcs ...func(obj T) error) (T, error) {
	typ := typeOfT[T]()
	instance, err := NewByType(typ, tenant, namespace)
	if err != nil {
		return *new(T), err
	}

	// Apply the initializers
	for _, init := range funcs {
		if err := init(instance.(T)); err != nil {
			return *new(T), err
		}
	}

	return instance.(T), nil
}

// New creates a new instance of the specified resource kind.
func NewByType(typ reflect.Type, tenant, namespace string) (Object, error) {
	kind, err := KindOf(typ)
	if err != nil {
		return nil, err
	}

	urn, err := NewURN(tenant, namespace, kind)
	if err != nil {
		return nil, err
	}

	return newObject(typ, urn), nil
}

func newObject(typ reflect.Type, urn URN) Object {
	instance := reflect.New(typ)
	resource := metaValue(instance, objectTypeOf(typ))
	resource.Tenant = urn.Tenant
	resource.Namespace = urn.Namespace
	resource.Kind = urn.Kind
	resource.ID = urn.ID
	return instance.Interface().(Object)
}

// URN returns the URN of the object.
func (r *Meta) URN() URN {
	return URN{
		Tenant:    r.Tenant,
		Namespace: r.Namespace,
		Kind:      r.Kind,
		ID:        r.ID,
	}
}

// Created returns who created the resource and when.
func (r *Meta) Created() (string, time.Time) {
	return r.CreatedBy, time.Unix(0, r.CreatedAt)
}

// Updated returns who updated the resource and when.
func (r *Meta) Updated() (string, time.Time) {
	return r.UpdatedBy, time.Unix(0, r.UpdatedAt)
}

// Status returns the current state of the resource.
func (r *Meta) Status() string {
	return r.State
}

// assignID assigns a generated ID when a new object arrives without one.
func assignID(v Object) error {
	meta := metaOf(v)
	if meta.ID != "" {
		return nil
	}
	urn, err := NewURN(meta.Tenant, meta.Namespace, meta.Kind)
	if err != nil {
		return err
	}
	meta.ID = urn.ID
	return nil
}

// Title returns the title of the resource.
func (r *Meta) Title() string {
	return r.ID
}

// ---------------------------------- JSON ----------------------------------

// ToJSON encodes a resource for storage. Fields use their JSON name unless a
// store tag overrides it.
func ToJSON(v Object) ([]byte, error) {
	data, err := json.Marshal(v)
	switch {
	case err != nil:
		return nil, err
	case !hasStore(reflect.TypeOf(v)):
		return data, nil
	}
	err = walkStoreFields(reflect.ValueOf(v), "", "", func(storePath, jsonPath string, field reflect.Value) error {
		if jsonPath != "" && jsonPath != storePath {
			if data, err = sjson.DeleteBytes(data, jsonPath); err != nil {
				return err
			}
		}
		if storePath != "" {
			data, err = sjson.SetBytes(data, storePath, field.Interface())
		}
		return err
	})
	return data, err
}

// FromJSON parses a JSON file and returns a resource
func FromJSON(c Registry, data []byte) (Object, error) {
	var header struct {
		Kind Kind `json:"kind"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return nil, err
	}

	typ, err := c.Resolve(header.Kind)
	if err != nil {
		return nil, err
	}

	info := objectTypeOf(typ.Type)
	instance := newObject(typ.Type, URN{Kind: header.Kind})
	if info.embeds {
		if err := walkEmbeds(c, instance, func(rv reflect.Value) {
			rv.Set(reflect.ValueOf(Embed{Registry: c}))
		}); err != nil {
			return nil, err
		}
	}

	if err := json.Unmarshal(data, instance); err != nil {
		return nil, err
	}
	meta := metaValue(reflect.ValueOf(instance), info)
	if meta.Namespace == "" {
		meta.Namespace = defaultNamespace(header.Kind)
	}
	if meta.ID == "" {
		generated, err := NewURN(meta.Tenant, meta.Namespace, meta.Kind)
		if err != nil {
			return nil, err
		}
		meta.ID = generated.ID
	} else if _, err := MakeURN(meta.Tenant, meta.Namespace, meta.Kind, "00000000000000000000"); err != nil {
		return nil, err
	}
	if info.store {
		if err := walkStoreFields(reflect.ValueOf(instance), "", "", func(storePath, _ string, field reflect.Value) error {
			value := gjson.GetBytes(data, storePath)
			if storePath == "" || !value.Exists() {
				return nil
			}
			return json.Unmarshal([]byte(value.Raw), field.Addr().Interface())
		}); err != nil {
			return nil, err
		}
	}

	return instance, nil
}

func defaultNamespace(kind Kind) string {
	switch kind {
	case "tenant", "user", "group", "role", "namespace":
		return "system"
	default:
		return "default"
	}
}

func hasStore(typ reflect.Type) bool {
	return objectTypeOf(typ).store
}

func scanStore(typ reflect.Type) bool {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}

	switch typ.Kind() {
	case reflect.Struct:
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.PkgPath != "" {
				continue
			}
			if name, ok := tagName(field.Tag.Get("store")); ok && name != "" && name != "-" {
				return true
			}
			if scanStore(field.Type) {
				return true
			}
		}
	case reflect.Array, reflect.Map, reflect.Slice:
		return scanStore(typ.Elem())
	}
	return false
}

func scanEmbed(typ reflect.Type) bool {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}

	switch typ.Kind() {
	case reflect.Struct:
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.PkgPath != "" {
				continue
			}
			if field.Type == typeEmbedded || scanEmbed(field.Type) {
				return true
			}
		}
	case reflect.Array, reflect.Map, reflect.Slice:
		return scanEmbed(typ.Elem())
	}
	return false
}

func objectTypeOf(typ reflect.Type) *objectType {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if value, ok := objectTypes.Load(typ); ok {
		info := value.(*objectType)
		initObjectType(typ, info)
		return info
	}
	info := new(objectType)
	actual, _ := objectTypes.LoadOrStore(typ, info)
	info = actual.(*objectType)
	initObjectType(typ, info)
	return info
}

func initObjectType(typ reflect.Type, info *objectType) {
	info.once.Do(func() {
		field, _ := typ.FieldByName("Meta")
		info.meta = field.Index
		info.store = scanStore(typ)
		info.embeds = scanEmbed(typ)
	})
}

func metaValue(v reflect.Value, info *objectType) *Meta {
	return v.Elem().FieldByIndex(info.meta).Addr().Interface().(*Meta)
}

func walkStoreFields(v reflect.Value, storePrefix, jsonPrefix string, visit func(string, string, reflect.Value) error) error {
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}

	switch v.Kind() {
	case reflect.Struct:
		return walkStoreStruct(v, storePrefix, jsonPrefix, visit)
	case reflect.Slice, reflect.Array:
		return walkStoreSlice(v, storePrefix, jsonPrefix, visit)
	}
	return nil
}

func walkStoreStruct(v reflect.Value, storePrefix, jsonPrefix string, visit func(string, string, reflect.Value) error) error {
	typ := v.Type()
	for i := 0; i < typ.NumField(); i++ {
		field, value := typ.Field(i), v.Field(i)
		if field.PkgPath != "" {
			continue
		}
		if err := walkStoreField(value, field, storePrefix, jsonPrefix, visit); err != nil {
			return err
		}
	}
	return nil
}

func walkStoreField(value reflect.Value, field reflect.StructField, storePrefix, jsonPrefix string, visit func(string, string, reflect.Value) error) error {
	jsonName, inline := jsonName(field)
	storeName, override := tagName(field.Tag.Get("store"))
	switch {
	case override:
		return walkStoreOverride(value, storePrefix, jsonPrefix, jsonName, storeName, visit)
	case inline:
		return walkStoreFields(value, storePrefix, jsonPrefix, visit)
	case jsonName != "":
		storePath := joinJSONPath(storePrefix, jsonName)
		jsonPath := joinJSONPath(jsonPrefix, jsonName)
		return walkStoreFields(value, storePath, jsonPath, visit)
	default:
		return nil
	}
}

func walkStoreOverride(value reflect.Value, storePrefix, jsonPrefix, jsonName, storeName string, visit func(string, string, reflect.Value) error) error {
	storePath := joinJSONPath(storePrefix, storeName)
	jsonPath := ""
	if jsonName != "" {
		jsonPath = joinJSONPath(jsonPrefix, jsonName)
	}
	if storeName == "-" {
		storePath = ""
	}
	if err := visit(storePath, jsonPath, value); err != nil {
		return err
	}
	if storePath == "" {
		return nil
	}
	return walkStoreFields(value, storePath, jsonPath, visit)
}

func walkStoreSlice(v reflect.Value, storePrefix, jsonPrefix string, visit func(string, string, reflect.Value) error) error {
	for i := 0; i < v.Len(); i++ {
		index := strconv.Itoa(i)
		storePath := joinJSONPath(storePrefix, index)
		jsonPath := joinJSONPath(jsonPrefix, index)
		if err := walkStoreFields(v.Index(i), storePath, jsonPath, visit); err != nil {
			return err
		}
	}
	return nil
}

func tagName(tag string) (string, bool) {
	if tag == "" {
		return "", false
	}
	return strings.Split(tag, ",")[0], true
}

func joinJSONPath(prefix, name string) string {
	if name == "" {
		return prefix
	}
	name = jsonPathEscaper.Replace(name)
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

// ReadJSON reads a JSON file and returns a resource
func ReadJSON(c Registry, reader io.Reader) (Object, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}

	return FromJSON(c, data)
}

// UnmarshalYAML decodes YAML into v using json struct tags.
func UnmarshalYAML(data []byte, v any) error {
	return yaml.Unmarshal(data, v)
}

// FromYAML parses a YAML document and returns a resource.
func FromYAML(c Registry, data []byte) (Object, error) {
	jsonData, err := yaml.YAMLToJSON(data)
	if err != nil {
		return nil, err
	}
	return FromJSON(c, jsonData)
}

// ReadYAML reads a YAML document and returns a resource.
func ReadYAML(c Registry, reader io.Reader) (Object, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	return FromYAML(c, data)
}

// ReadFile decodes a file into v using its extension (.json, .yaml, .yml).
// When data is nil the file contents are read from path.
func ReadFile(path string, data []byte, v any) error {
	if data == nil {
		var err error
		data, err = os.ReadFile(path)
		if err != nil {
			return err
		}
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		return UnmarshalYAML(data, v)
	case ".json":
		return json.Unmarshal(data, v)
	default:
		return fmt.Errorf("storage: unsupported file extension %q", filepath.Ext(path))
	}
}

// ---------------------------------- Path ----------------------------------

// Regex for cleaning up the path
var rxPathToField = regexp.MustCompile(`\.\d+(\.|$)`)
var jsonPathEscaper = strings.NewReplacer(`\`, `\\`, `.`, `\.`, `*`, `\*`, `?`, `\?`)

// Path represents a rendering path for a particular field.
type Path string

// ID generates a unique ID for the path, encoded in hex.
func (p Path) ID(prefix string) string {
	id := hex.EncodeToString([]byte(p))
	return fmt.Sprintf("%s-%s", prefix, id)
}

// Label returns the label of the path.
func (p Path) Label() string {
	return convert.Label(string(p))
}

// String returns the string representation of the path.
func (p Path) String() string {
	return string(p)
}

// Index retrieves the index of the path, if it's a slice. Otherwise, returns -1.
func (p Path) Index() int {
	if i := strings.LastIndex(string(p), "."); i != -1 {
		return convert.Int(string(p[i+1:]), -1)
	}
	return -1
}

// removes all the full digits from the path, as they refer to a slice
// e.g. "engines.41354.type" -> "engines.type"
// e.g. "foo.1.bar.2.baz" -> "foo.bar.baz"
func (p Path) field() string {
	return rxPathToField.ReplaceAllString(string(p), "$1")
}

// Walk iterates over all sub-paths (e.g. "foo.bar.baz" -> "foo", "foo.bar", "foo.bar.baz")
func (p Path) Walk() iter.Seq[Path] {
	return func(yield func(Path) bool) {
		for i := 0; i < len(p); i++ {
			if p[i] == '.' {
				if !yield(Path(p[:i])) {
					return
				}
			}
		}
		yield(p)
	}
}
