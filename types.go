package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kelindar/storage/convert"
	"github.com/kelindar/storage/state"
)

var (
	typeResource = reflect.TypeFor[Meta]()
	typeEmbedded = reflect.TypeFor[Embed]()
)

var (
	ErrNotFound          = errors.New("storage: document was not found")
	ErrConflict          = errors.New("storage: write conflict")
	ErrInvalidTransition = errors.New("storage: invalid state transition")
	ErrDeleting          = errors.New("storage: content is deleting")
	ErrInvalid           = errors.New("storage: invalid input")
	ErrLockLost          = errors.New("storage: lock ownership lost")
)

// IsNotFound returns true if the specified error is a not found error.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}

// IsConflict returns true if the specified error is a conflict error.
func IsConflict(err error) bool {
	return errors.Is(err, ErrConflict)
}

// IsInvalidTransition checks whether the error is a state transition error.
func IsInvalidTransition(err error) bool {
	return errors.Is(err, ErrInvalidTransition)
}

// ---------------------------------- Contract ----------------------------------

// IsLockLost returns true if the specified error reports lost lock ownership.
func IsLockLost(err error) bool {
	return errors.Is(err, ErrLockLost)
}

// Storage represents a storage layer for records.
type Storage interface {
	io.Closer
	Registry() Registry
	Lock(ctx context.Context, name string) (context.Context, context.CancelFunc, error)
	Insert(ctx context.Context, v Object) (Object, error)
	Update(ctx context.Context, v Object) (Object, error)
	Delete(ctx context.Context, urn URN) (Object, error)
	Fetch(ctx context.Context, urn URN) (Object, error)
	Link(ctx context.Context, source URN) error
	Links(ctx context.Context, target URN) ([]Link, error)
	Search(ctx context.Context, kind Kind, query Query) (iter.Seq[Object], error)
	Count(ctx context.Context, kind Kind, query Query) (int, error)
	Changes(ctx context.Context, consumer string, kind Kind, after time.Time, handle func(context.Context, []Change) error) error
	Upload(ctx context.Context, scope URN, contentType string, data []byte) (*Blob, error)
	Next(ctx context.Context, name string) (uint32, error)
}

// Change describes a durable create, update, or delete mutation. It is valid
// only for the duration of the Changes callback and must not be retained or
// modified by the consumer.
type Change struct {
	URN    URN
	Action string
	At     time.Time
}

// ---------------------------------- Indexer ----------------------------------

// Indexer represents a resource that provides an index.
type Indexer interface {
	Index() string
}

// Embedded represents a generic embedded document for unmarshaling
type Embed struct {
	Value    Object `json:",inline"`
	Registry Registry
}

// UnmarshalJSON unmarshals the JSON into the embedded document
func (r *Embed) UnmarshalJSON(b []byte) error {
	if len(b) <= 4 { // "null" or empty
		return nil
	}

	o, err := FromJSON(r.Registry, b)
	if err != nil {
		return err
	}

	r.Value = o
	return nil
}

// MarshalJSON marshals the JSON from the embedded document
func (r Embed) MarshalJSON() ([]byte, error) {
	return json.Marshal(r.Value)
}

// walkEmbeds walks the struct and calls the specified function for each embedded resource
func walkEmbeds(r Registry, value any, fn func(reflect.Value)) error {
	v := reflect.ValueOf(value)
	if v.Kind() == reflect.Invalid || v.IsZero() {
		return nil
	}

	switch v.Type().Kind() {
	case reflect.Struct:
	case reflect.Pointer:
		v = reflect.Indirect(v)
		if v.Type().Kind() != reflect.Struct {
			return nil
		}
	default:
		return nil
	}

	rt := v.Type()
	for i := 0; i < v.NumField(); i++ {
		rv := v.Field(i)
		if rt.Field(i).Type == typeEmbedded {
			fn(rv)
			continue
		}

		// skip unexported fields
		if !rv.CanInterface() {
			continue
		}

		// Recursively walk the struct
		if err := walkEmbeds(r, rv.Interface(), fn); err != nil {
			return err
		}
	}

	return nil
}

// ---------------------------------- Options & Metadata ----------------------------------

// Options represents the options for a document
type Options struct {
	Icon      string        `json:"icon,omitempty"`      // Icon name from https://lucide.dev/icons
	Title     string        `json:"title,omitempty"`     // Title of the document (e.g. Person)
	Plural    string        `json:"plural,omitempty"`    // Plural name of the document (e.g. People)
	Sort      string        `json:"sort,omitempty"`      // Sort field
	States    state.Machine `json:"-"`                   // Optional lifecycle state machine
	Actions   []string      `json:"actions,omitempty"`   // Allowed permission actions for this kind
	Workflows []string      `json:"workflows,omitempty"` // Built-in workflows to run after saves
}

// DefaultActions lists standard permission actions used when Options.Actions is empty.
var DefaultActions = []string{
	"create", "read", "update", "delete", "search", "count", "run", "fail",
}

// defaultOptions returns the default options for the specified kind
func defaultOptions(kind Kind) Options {
	return Options{
		Icon:    "text_snippet",
		Title:   convert.TitleCase(string(kind)),
		Plural:  convert.TitleCase(string(kind)),
		Sort:    string(kind),
		Actions: slices.Clone(DefaultActions),
	}
}

// ---------------------------------- Query Parsing ----------------------------------

// Query represents a query to filter records.
type Query struct {
	Tenant        string              // Tenant limits results to one tenant.
	IDs           []string            // IDs limits results to the listed resource IDs.
	Namespaces    []string            // Namespaces limits results to the listed namespaces.
	States        []string            // States is a list of states to filter by
	Indexes       []string            // Indexes is a list of indexes to filter by
	Filters       map[string][]string // Filters is a map of filters to apply
	Match         string              // Match is the full-text search query
	SortBy        []string            // Sort is the set of fields to order by
	Offset        int                 // Offset is the number of records to skip
	Limit         int                 // Limit is the maximum number of records to return
	CreatedBefore time.Time           // CreatedBefore filters records created before this time
	UpdatedBefore time.Time           // UpdatedBefore filters records updated before this time
	UpdatedAfter  time.Time           // UpdatedAfter filters records updated after this time
}

// String returns the string representation of the query.
func (q *Query) String() string {
	var out strings.Builder

	if q.empty() {
		return "" // Skip empty queries
	}
	q.writeString(&out)
	return out.String()
}

func (q *Query) empty() bool {
	return q.Tenant == "" && len(q.IDs) == 0 && len(q.Namespaces) == 0 && len(q.States) == 0 && len(q.Indexes) == 0 && len(q.Filters) == 0 &&
		q.Match == "" && len(q.SortBy) == 0 && q.Offset == 0 && q.Limit == 0 &&
		q.CreatedBefore.IsZero() && q.UpdatedBefore.IsZero() && q.UpdatedAfter.IsZero()
}

func (q *Query) writeString(out *strings.Builder) {
	if q.Tenant != "" {
		out.WriteString("tenant=")
		out.WriteString(q.Tenant)
		out.WriteString(";")
	}
	if len(q.IDs) > 0 {
		out.WriteString("id=")
		out.WriteString(strings.Join(q.IDs, ","))
		out.WriteString(";")
	}

	if len(q.Namespaces) > 0 {
		out.WriteString("namespace=")
		out.WriteString(strings.Join(q.Namespaces, ","))
		out.WriteString(";")
	}

	if len(q.States) > 0 {
		out.WriteString("state=")
		out.WriteString(strings.Join(q.States, ","))
		out.WriteString(";")
	}

	if len(q.Indexes) > 0 {
		out.WriteString("index=")
		out.WriteString(strings.Join(q.Indexes, ","))
		out.WriteString(";")
	}

	if len(q.Filters) > 0 {
		out.WriteString("filter=")
		writeFilters(out, q.Filters)
		out.WriteString(";")
	}

	if q.Match != "" {
		out.WriteString("match=")
		out.WriteString(q.Match)
		out.WriteString(";")
	}

	if len(q.SortBy) > 0 {
		out.WriteString("sort=")
		out.WriteString(strings.Join(q.SortBy, ","))
		out.WriteString(";")
	}

	if q.Limit > 0 {
		out.WriteString("limit=")
		out.WriteString(strconv.Itoa(q.Limit))
		out.WriteString(";")
	}

	if q.Offset > 0 {
		out.WriteString("offset=")
		out.WriteString(strconv.Itoa(q.Offset))
		out.WriteString(";")
	}

	if !q.CreatedBefore.IsZero() {
		out.WriteString("createdBefore=")
		out.WriteString(q.CreatedBefore.Format(time.RFC3339Nano))
		out.WriteString(";")
	}

	if !q.UpdatedBefore.IsZero() {
		out.WriteString("updatedBefore=")
		out.WriteString(q.UpdatedBefore.Format(time.RFC3339Nano))
		out.WriteString(";")
	}

	if !q.UpdatedAfter.IsZero() {
		out.WriteString("updatedAfter=")
		out.WriteString(q.UpdatedAfter.Format(time.RFC3339Nano))
		out.WriteString(";")
	}
}

func writeFilters(out *strings.Builder, filters map[string][]string) {
	for key, values := range filters {
		for i, value := range values {
			if i > 0 {
				out.WriteString(",")
			}
			out.WriteString(key)
			out.WriteString(":")
			out.WriteString(value)
		}
	}
}

/*
ParseQuery parses a string query into a Query struct. The query format is structured as a semicolon-separated list of key-value pairs.
Example query: "namespace=company;state=active;filter=age:30;match={Name}"
- The query is limited to `company` namespace.
- Only records with an `active` state will be considered.
- A filter is applied to only include records where `age` is `30`.
- It matches records containing the person's name from the placeholder `{Name}`.

 1. **namespace**: Specifies the namespaces to filter by. Multiple namespaces can be separated by commas.
    Example: `namespace=company,person`

 2. **state**: Indicates the states to filter by. Multiple states can be separated by commas.
    Example: `state=active,inactive`

 3. **filter**: Defines filters to apply. Each filter is specified as `field:value` for equality checks,
    or just `field` (without colon) for existence checks (non-nil and non-zero). Multiple filters can be
    separated by commas.
    Example: `filter=age:30,income:1000` (equality checks)
    Example: `filter=email` (existence check - matches records where email is set and non-empty)

 4. **match**: A full-text search query. This can include any search terms.
    Example: `match=software engineer`
*/
func ParseQuery(queryString string, object any, out Query) (Query, error) {
	if strings.TrimSpace(queryString) == "" {
		return out, nil
	}

	// Initialize the query with the default values
	if out.Filters == nil {
		out.Filters = make(map[string][]string)
	}

	// Split the query string by semicolon to handle each component
	parts := strings.SplitSeq(queryString, ";")
	for sub := range parts {
		if err := parseQueryToken(strings.TrimSpace(sub), &out, object); err != nil {
			return Query{}, err // Return the error for better debugging
		}
	}

	return out, nil
}

var (
	tenantRegex       = regexp.MustCompile(`^tenant=([^;]+)$`)
	idRegex           = regexp.MustCompile(`^id=([^;]+)$`)
	namespaceRegex    = regexp.MustCompile(`^namespace=([^;]+)$`)
	stateRegex        = regexp.MustCompile(`^state=([^;]+)$`)
	filterRegex       = regexp.MustCompile(`^filter=([^;]+)$`)
	matchRegex        = regexp.MustCompile(`^match=([^;]+)$`)
	sortRegex         = regexp.MustCompile(`^sort=([^;]+)$`)
	limitRegex        = regexp.MustCompile(`^limit=([^;]+)$`)
	offsetRegex       = regexp.MustCompile(`^offset=([^;]+)$`)
	indexRegex        = regexp.MustCompile(`^index=([^;]+)$`)
	updatedAfterRegex = regexp.MustCompile(`^updatedAfter=([^;]+)$`)
	variableRegex     = regexp.MustCompile(`{(\w+)}`)
)

// parseQueryToken parses each component of the query string.
func parseQueryToken(component string, query *Query, object any) error {
	switch {
	case component == "":
		return nil // Skip empty components
	case tenantRegex.MatchString(component):
		return parseTenant(component, query)
	case idRegex.MatchString(component):
		return parseIDs(component, query)
	case namespaceRegex.MatchString(component):
		return parseNamespace(component, query)
	case stateRegex.MatchString(component):
		return parseState(component, query)
	case filterRegex.MatchString(component):
		return parseFilter(component, query)
	case indexRegex.MatchString(component):
		return parseIndex(component, query)
	case matchRegex.MatchString(component):
		return parseMatch(component, query, object)
	case sortRegex.MatchString(component):
		return parseSort(component, query)
	case limitRegex.MatchString(component):
		return parseLimit(component, query)
	case offsetRegex.MatchString(component):
		return parseOffset(component, query)
	case updatedAfterRegex.MatchString(component):
		return parseUpdatedAfter(component, query)
	default:
		return fmt.Errorf("query: invalid component '%s'", component)
	}
}

func parseIDs(text string, query *Query) error {
	matches := idRegex.FindStringSubmatch(text)
	if matches == nil || len(matches) != 2 {
		return fmt.Errorf("query: invalid id format '%s'", text)
	}
	query.IDs = strings.Split(matches[1], ",")
	for i := range query.IDs {
		query.IDs[i] = strings.TrimSpace(query.IDs[i])
		if query.IDs[i] == "" {
			return fmt.Errorf("query: empty id in '%s'", text)
		}
	}
	return nil
}

func parseTenant(text string, query *Query) error {
	matches := tenantRegex.FindStringSubmatch(text)
	if matches == nil || len(matches) != 2 {
		return fmt.Errorf("query: invalid tenant format '%s'", text)
	}
	query.Tenant = strings.TrimSpace(matches[1])
	if query.Tenant == "" {
		return fmt.Errorf("query: empty tenant in '%s'", text)
	}
	return nil
}

func parseUpdatedAfter(text string, query *Query) error {
	matches := updatedAfterRegex.FindStringSubmatch(text)
	if matches == nil || len(matches) != 2 {
		return fmt.Errorf("query: invalid updatedAfter format '%s'", text)
	}

	value, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(matches[1]))
	if err != nil {
		return fmt.Errorf("query: invalid updatedAfter '%s'", matches[1])
	}
	query.UpdatedAfter = value
	return nil
}

// parseNamespace parses the namespace from the component.
func parseNamespace(text string, query *Query) error {
	matches := namespaceRegex.FindStringSubmatch(text)
	if matches == nil || len(matches) != 2 {
		return fmt.Errorf("query: invalid namespace format '%s'", text)
	}

	ns := strings.TrimSpace(matches[1])
	switch ns {
	case "*", "":
		query.Namespaces = nil
	default:
		parts := strings.Split(ns, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		query.Namespaces = parts
	}
	return nil
}

// parseState parses the state from the component.
func parseState(text string, query *Query) error {
	matches := stateRegex.FindStringSubmatch(text)
	if matches == nil || len(matches) != 2 {
		return fmt.Errorf("query: invalid state format '%s'", text)
	}

	states := strings.Split(matches[1], ",")
	for i, state := range states {
		states[i] = strings.TrimSpace(state)
		if states[i] == "" {
			return fmt.Errorf("query: empty state in '%s'", text)
		}
	}
	query.States = states
	return nil
}

// parseIndex parses indexed_by filters from the component.
func parseIndex(text string, query *Query) error {
	matches := indexRegex.FindStringSubmatch(text)
	if matches == nil || len(matches) != 2 {
		return fmt.Errorf("query: invalid index format '%s'", text)
	}

	indexes := strings.Split(matches[1], ",")
	for i, index := range indexes {
		indexes[i] = strings.TrimSpace(index)
		if indexes[i] == "" {
			return fmt.Errorf("query: empty index in '%s'", text)
		}
	}
	query.Indexes = indexes
	return nil
}

// parseFilter parses the filter from the component.
func parseFilter(text string, query *Query) error {
	matches := filterRegex.FindStringSubmatch(text)
	if matches == nil || len(matches) != 2 {
		return fmt.Errorf("query: invalid filter format '%s'", text)
	}

	filterStr := matches[1]
	return populateFilters(query, filterStr)
}

// parseSort parses sort fields from the component.
func parseSort(text string, query *Query) error {
	matches := sortRegex.FindStringSubmatch(text)
	if matches == nil || len(matches) != 2 {
		return fmt.Errorf("query: invalid sort format '%s'", text)
	}

	fields := strings.Split(matches[1], ",")
	for i, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			return fmt.Errorf("query: empty sort field in '%s'", text)
		}
		fields[i] = field
	}
	query.SortBy = fields
	return nil
}

// parseLimit parses the page size from the component.
func parseLimit(text string, query *Query) error {
	matches := limitRegex.FindStringSubmatch(text)
	if matches == nil || len(matches) != 2 {
		return fmt.Errorf("query: invalid limit format '%s'", text)
	}

	limit, err := strconv.Atoi(strings.TrimSpace(matches[1]))
	if err != nil || limit < 0 {
		return fmt.Errorf("query: invalid limit '%s'", matches[1])
	}
	query.Limit = limit
	return nil
}

// parseOffset parses the page offset from the component.
func parseOffset(text string, query *Query) error {
	matches := offsetRegex.FindStringSubmatch(text)
	if matches == nil || len(matches) != 2 {
		return fmt.Errorf("query: invalid offset format '%s'", text)
	}

	offset, err := strconv.Atoi(strings.TrimSpace(matches[1]))
	if err != nil || offset < 0 {
		return fmt.Errorf("query: invalid offset '%s'", matches[1])
	}
	query.Offset = offset
	return nil
}

// parseMatch parses the match from the component.
func parseMatch(text string, query *Query, object any) error {
	matches := matchRegex.FindStringSubmatch(text)
	if matches == nil || len(matches) != 2 {
		return fmt.Errorf("query: invalid match format '%s'", text)
	}

	matchValue, err := replaceVariables(matches[1], object)
	if err != nil {
		return fmt.Errorf("query: error replacing placeholders in match: %v", err)
	}
	query.Match = matchValue
	return nil
}

// populateFilters processes the filters and adds them to the Query struct.
// Filters can be in the format "key:value" for equality checks or just "key" for existence checks.
func populateFilters(query *Query, filterStr string) error {
	filters := strings.SplitSeq(filterStr, ",")
	for filter := range filters {
		filter = strings.TrimSpace(filter)
		if filter == "" {
			continue // Skip empty filters
		}

		kv := strings.SplitN(filter, ":", 2)
		key := strings.TrimSpace(kv[0])
		if key == "" {
			return fmt.Errorf("query: empty key in filter '%s'", filter)
		}

		// If no value is provided, it's an existence check (empty string signals this)
		var value string
		if len(kv) == 2 {
			value = strings.TrimSpace(kv[1])
			if value == "" {
				return fmt.Errorf("query: empty value in filter '%s'", filter)
			}
		}

		query.Filters[key] = append(query.Filters[key], value)
	}
	return nil
}

// replaceVariables replaces placeholders in the string with actual field values from the object.
func replaceVariables(value string, object any) (string, error) {
	var out strings.Builder
	var idx int

	for _, match := range variableRegex.FindAllStringSubmatchIndex(value, -1) {
		if len(match) != 4 {
			continue // Safety check for malformed match
		}

		i0, i1, f0, f1 := match[0], match[1], match[2], match[3]

		// Append the text before the placeholder
		out.WriteString(value[idx:i0])

		fieldName := value[f0:f1]
		fieldValue, err := loadField(object, fieldName)
		if err != nil {
			return "", err
		}

		out.WriteString(fieldValue) // Append field value
		idx = i1
	}

	// Append the rest of the string
	out.WriteString(value[idx:])
	return out.String(), nil
}

// loadField retrieves the value of a specified field from the object.
func loadField(object any, fieldName string) (string, error) {
	rv := reflect.Indirect(reflect.ValueOf(object))
	switch {
	case rv.Kind() == reflect.Pointer && rv.IsNil():
		return "", errors.New("object is nil")
	case rv.Kind() == reflect.Pointer:
		rv = rv.Elem()
	case rv.Kind() != reflect.Struct:
		return "", errors.New("object is not a struct or pointer to struct")
	}

	fv := rv.FieldByName(fieldName)
	switch {
	case !fv.IsValid():
		return "", fmt.Errorf("field '%s' does not exist", fieldName)
	case !fv.CanInterface():
		return "", fmt.Errorf("field '%s' cannot be accessed", fieldName)
	}

	// Handle different types with formatting
	switch fv.Kind() {
	case reflect.String:
		return fv.String(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return fmt.Sprintf("%d", fv.Int()), nil
	case reflect.Float32, reflect.Float64:
		return fmt.Sprintf("%.2f", fv.Float()), nil
	case reflect.Bool:
		return fmt.Sprintf("%t", fv.Bool()), nil
	default:
		return "", fmt.Errorf("unsupported field type: %s", fv.Kind())
	}
}
