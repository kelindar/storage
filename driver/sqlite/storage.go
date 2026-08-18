package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"iter"
	"strings"

	"github.com/kelindar/storage"
	_ "github.com/ncruces/go-sqlite3/driver" // cgo-free, uses wazero
)

// Record represents a stored resource document.
type Record = storage.Object

// rds represents a SQLite storage layer for resources.
type rds struct {
	db       *sql.DB
	registry storage.Registry
	leases   *leaser
	fts5     bool
}

// Open opens a storage database
func Open(dsn string, registry storage.Registry) (storage.Storage, error) {
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("storage: unable to open database: %w", err)
	}

	// Set max open connections for SQLite (as SQLite is not designed for many concurrent writes)
	db.SetMaxOpenConns(1)

	// Auto-create the tables
	if err := autoMigrate(db, registry); err != nil {
		_ = db.Close()
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &rds{
		db:       db,
		registry: registry,
		fts5:     fts5Enabled(db),
		leases:   &leaser{db: db, life: ctx, cancel: cancel, timing: defaultLockTiming},
	}, nil
}

// OpenEphemeral opens an ephemeral storage
func OpenEphemeral(registry storage.Registry) storage.Storage {
	s, err := Open(":memory:", registry)
	if err != nil {
		panic(err)
	}
	return s
}

// Close closes the storage gracefully.
func (s *rds) Close() error {
	if !s.leases.shutdown() {
		return nil
	}
	return s.db.Close()
}

// Registry returns the type registry associated with this storage.
func (s *rds) Registry() storage.Registry {
	return s.registry
}

// Expired returns overdue resource identities in materialized pages.
func (s *rds) Expired(ctx context.Context, kind storage.Kind, now int64, limit int) iter.Seq2[storage.URN, error] {
	return func(yield func(storage.URN, error) bool) {
		var afterAt int64
		var afterID string
		for {
			page, err := s.expiredPage(ctx, kind, now, afterAt, afterID, limit)
			switch {
			case err != nil:
				yield(storage.URN{}, err)
				return
			case len(page) == 0:
				return
			}
			last := page[len(page)-1]
			afterAt, afterID = last.at, last.ID
			for _, item := range page {
				if !yield(item.URN, nil) {
					return
				}
			}
			if len(page) < limit {
				return
			}
		}
	}
}

type expiredURN struct {
	storage.URN
	at int64
}

func (s *rds) expiredPage(ctx context.Context, kind storage.Kind, now, afterAt int64, afterID string, limit int) ([]expiredURN, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT tenant, namespace, id, expires_at FROM `+tableOf(kind)+
			` WHERE expires_at != 0 AND expires_at <= ?`+
			` AND (? = 0 OR expires_at > ? OR (expires_at = ? AND id > ?))`+
			` ORDER BY expires_at, id LIMIT ?`,
		now, afterAt, afterAt, afterAt, afterID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("storage: query expired %s: %w", kind, err)
	}
	defer rows.Close()

	out := make([]expiredURN, 0, limit)
	for rows.Next() {
		item := expiredURN{URN: storage.URN{Kind: kind}}
		if err := rows.Scan(&item.Tenant, &item.Namespace, &item.ID, &item.at); err != nil {
			return nil, fmt.Errorf("storage: read expired %s: %w", kind, err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: read expired %s: %w", kind, err)
	}
	return out, nil
}

// Upload rejects Blob writes; wrap with storage.NewStore to enable uploads.
func (s *rds) Upload(context.Context, storage.URN, string, []byte) (*storage.Blob, error) {
	return nil, errors.New("blob: store is not configured")
}

// ---------------------------------- Query ----------------------------------

// query creates a query for the specified resource kind
func (s *rds) query(ctx context.Context, projection string, kind storage.Kind, q storage.Query) (*sql.Rows, error) {
	defaultSort := q.SortBy == nil
	switch {
	case q.SortBy == nil:
		q.SortBy = []string{"id"}
	case q.Limit == 0:
		q.Limit = 1000
	}

	// Validate the query
	switch {
	case kind == "":
		return nil, fmt.Errorf("storage: kind is required")
	case q.Limit < 0:
		return nil, fmt.Errorf("storage: invalid limit %d", q.Limit)
	case q.Offset < 0:
		return nil, fmt.Errorf("storage: invalid offset %d", q.Offset)
	}

	where, args := queryWhere(q)
	rankOrder := s.matchWhere(kind, q.Match, defaultSort, &where, &args)

	// Build the SQL query string
	querySQL := fmt.Sprintf("%s FROM %s", projection, tableOf(kind))
	if len(where) > 0 {
		querySQL += ` WHERE ` + strings.Join(where, " AND ")
	}

	querySQL, args = appendOrder(querySQL, q, rankOrder, args)

	if q.Limit > 0 {
		querySQL += fmt.Sprintf(" LIMIT %d", q.Limit)
	}

	if q.Offset > 0 {
		querySQL += fmt.Sprintf(" OFFSET %d", q.Offset)
	}

	return s.db.QueryContext(ctx, querySQL, args...)
}

func queryWhere(q storage.Query) ([]string, []any) {
	where, args := queryScope(q)
	switch {
	case q.Namespaces == nil:
	case len(q.Namespaces) == 0:
		return []string{"1 = 0"}, nil
	default:
		placeholders := make([]string, len(q.Namespaces))
		for i, namespace := range q.Namespaces {
			placeholders[i] = "?"
			args = append(args, namespace)
		}
		where = append(where, fmt.Sprintf("namespace IN (%s)", strings.Join(placeholders, ",")))
	}
	if len(q.States) > 0 {
		placeholders := make([]string, len(q.States))
		for i, status := range q.States {
			placeholders[i] = "?"
			args = append(args, status)
		}
		where = append(where, fmt.Sprintf("state IN (%s)", strings.Join(placeholders, ",")))
	}
	if len(q.Indexes) > 0 {
		placeholders := make([]string, len(q.Indexes))
		for i, index := range q.Indexes {
			placeholders[i] = "?"
			args = append(args, index)
		}
		where = append(where, fmt.Sprintf("indexed_by IN (%s)", strings.Join(placeholders, ",")))
	}
	if !q.CreatedBefore.IsZero() {
		where = append(where, "created_at < ?")
		args = append(args, q.CreatedBefore.UnixNano())
	}
	if !q.UpdatedBefore.IsZero() {
		where = append(where, "updated_at < ?")
		args = append(args, q.UpdatedBefore.UnixNano())
	}
	if !q.UpdatedAfter.IsZero() {
		where = append(where, "updated_at >= ?")
		args = append(args, q.UpdatedAfter.UnixNano())
	}
	for path, filter := range q.Filters {
		if path != "" {
			clause, filterArgs := queryFilterByJSON(path, filter)
			if clause != "" {
				where = append(where, clause)
				args = append(args, filterArgs...)
			}
		}
	}
	return where, args
}

func queryScope(q storage.Query) ([]string, []any) {
	where := make([]string, 0, 2)
	args := make([]any, 0, len(q.IDs)+1)
	if q.Tenant != "" {
		where = append(where, "tenant = ?")
		args = append(args, q.Tenant)
	}
	if len(q.IDs) == 0 {
		return where, args
	}
	placeholders := make([]string, len(q.IDs))
	for i, id := range q.IDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	where = append(where, fmt.Sprintf("id IN (%s)", strings.Join(placeholders, ",")))
	return where, args
}

func (s *rds) matchWhere(kind storage.Kind, match string, defaultSort bool, where *[]string, args *[]any) string {
	if match == "" {
		return ""
	}
	if !s.fts5 {
		if clause, likeArgs := matchLikeClause(match); clause != "" {
			*where = append(*where, clause)
			*args = append(*args, likeArgs...)
		}
		return ""
	}
	fts := quoteIdent(kind.String() + "_fts")
	*where = append(*where, fmt.Sprintf(`rowid IN (SELECT rowid FROM %s WHERE data match ?)`, fts))
	*args = append(*args, sanitizeTerm(match))
	if defaultSort {
		return fmt.Sprintf(`(SELECT bm25(%s) FROM %s WHERE %s.rowid = %s.rowid AND data MATCH ?)`, fts, fts, fts, tableOf(kind))
	}
	return ""
}

func appendOrder(querySQL string, q storage.Query, rankOrder string, args []any) (string, []any) {
	if rankOrder != "" {
		return querySQL + " ORDER BY " + rankOrder, append(args, sanitizeTerm(q.Match))
	}
	if len(q.SortBy) == 0 {
		return querySQL, args
	}
	sortFields := make([]string, 0, len(q.SortBy))
	for _, field := range q.SortBy {
		sortFields = append(sortFields, queryOrder(field))
	}
	return fmt.Sprintf("%s ORDER BY %s", querySQL, strings.Join(sortFields, ", ")), args
}

// queryOrder converts the sort field to a query string
func queryOrder(field string) string {
	if len(field) == 0 {
		return ""
	}

	var suffix string
	switch field[0] {
	case '-':
		suffix = " DESC"
		field = field[1:]
	case '+':
		field = field[1:]
	}

	switch field {
	case "id",
		"state",
		"createdBy", "createdAt",
		"updatedBy", "updatedAt",
		"updated_by", "updated_at",
		"created_by", "created_at":
		return snakeCase(field) + suffix
	default:
		return "json_extract(data, '$." + field + "')" + suffix
	}
}

// queryFilterByJSON returns a parameterized filter for the specified JSON path and values.
func queryFilterByJSON(path string, values []string) (string, []any) {
	if len(path) == 0 || len(values) == 0 {
		return "", nil
	}

	expression := "json_extract(data, ?)"
	pathArgs := func(count int) []any {
		args := make([]any, count)
		for i := range args {
			args[i] = "$." + path
		}
		return args
	}
	switch path {
	case "tenant":
		expression = "tenant"
		pathArgs = func(int) []any { return nil }
	}

	if len(values) == 1 && values[0] == "" {
		return fmt.Sprintf("(%[1]s IS NOT NULL AND %[1]s != '' AND %[1]s != 0 AND %[1]s != false)", expression), pathArgs(4)
	}

	placeholders := make([]string, len(values))
	args := pathArgs(1)
	for i, value := range values {
		placeholders[i] = "?"
		args = append(args, value)
	}
	return fmt.Sprintf("(%s IN (%s))", expression, strings.Join(placeholders, ",")), args
}

// matchLikeClause builds a case-insensitive JSON substring match for each token.
func matchLikeClause(query string) (string, []any) {
	tokens := strings.Fields(strings.TrimSpace(query))
	if len(tokens) == 0 {
		return "", nil
	}

	parts := make([]string, len(tokens))
	args := make([]any, len(tokens))
	for i, token := range tokens {
		parts[i] = "CAST(data AS TEXT) LIKE ? ESCAPE '\\'"
		args[i] = "%" + escapeLike(token) + "%"
	}
	return strings.Join(parts, " AND "), args
}

func escapeLike(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch r {
		case '%', '_', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// sanitizeTerm tokenizes and prepares the query for FTS5 with prefix matching.
func sanitizeTerm(query string) string {
	tokens := strings.Fields(query)
	if len(tokens) == 0 {
		return ""
	}

	for i, token := range tokens {
		tokens[i] = token + "*"
	}

	if len(tokens) == 1 {
		return tokens[0]
	}

	// Wrap with NEAR to match any of the tokens 30 words apart
	return "NEAR(" + strings.Join(tokens, " ") + ", 30)"
}
