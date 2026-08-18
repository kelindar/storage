package pgsql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"iter"
	"time"

	"github.com/kelindar/storage"
)

const recordColumns = "id, namespace, state, data, created_by, updated_by, created_at, updated_at, expires_at"

// Insert inserts a new resource into the storage.
func (s *rds) Insert(ctx context.Context, v Record) (Record, error) {
	createdBy := storage.Actor(ctx)
	state := v.Status()
	meta := metaOf(v)
	meta.State = state
	data, err := storage.ToJSON(v)
	if err != nil {
		return nil, err
	}

	urn := v.URN()
	now := time.Now().UTC()
	query := `INSERT INTO ` + tableOf(urn.Kind) +
		` (tenant, id, namespace, state, indexed_by, data, created_by, updated_by, created_at, updated_at, expires_at)` +
		` VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("storage: begin insert: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, query, urn.Tenant, urn.ID, urn.Namespace, state, indexOf(v), data, createdBy, createdBy, now.UnixNano(), now.UnixNano(), meta.ExpiresAt); err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%w (%v)", storage.ErrConflict, urn.String())
		}
		return nil, fmt.Errorf("storage: unable to insert, %w", err)
	}
	links, err := storage.Links(v)
	if err != nil {
		return nil, err
	}
	if err := replaceLinks(ctx, tx, urn, links); err != nil {
		return nil, err
	}
	if err := s.recordChange(ctx, tx, urn, changeCreate, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("storage: commit insert: %w", err)
	}
	created := withMeta(v, meta, createdBy, createdBy, now, now)
	return created, nil
}

// Update updates an existing resource in the storage.
func (s *rds) Update(ctx context.Context, v Record) (Record, error) {
	updatedBy := storage.Actor(ctx)
	old, err := s.Fetch(ctx, v.URN())
	if err != nil {
		return nil, err
	}

	state := v.Status()
	meta := metaOf(v)
	meta.State = state
	data, err := storage.ToJSON(v)
	if err != nil {
		return nil, err
	}

	_, version := v.Updated()
	urn := v.URN()
	now := time.Now().UTC()
	query := `UPDATE ` + tableOf(urn.Kind) +
		` SET state = ?, indexed_by = ?, data = ?, updated_by = ?, updated_at = ?, expires_at = ?` +
		` WHERE tenant = ? AND namespace = ? AND id = ? AND updated_at = ?`
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("storage: begin update: %w", err)
	}
	defer tx.Rollback()
	r, err := tx.ExecContext(ctx, query, state, indexOf(v), data, updatedBy, now.UnixNano(), meta.ExpiresAt, urn.Tenant, urn.Namespace, urn.ID, version.UnixNano())
	if err != nil {
		return nil, fmt.Errorf("storage: unable to update, %w", err)
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("%w (%v)", storage.ErrConflict, urn.String())
	}
	createdBy, createdAt := old.Created()
	links, err := storage.Links(v)
	if err != nil {
		return nil, err
	}
	if err := replaceLinks(ctx, tx, urn, links); err != nil {
		return nil, err
	}
	if err := s.recordChange(ctx, tx, urn, changeUpdate, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("storage: commit update: %w", err)
	}
	updated := withMeta(v, meta, createdBy, updatedBy, createdAt, now)
	return updated, nil
}

// Fetch retrieves a resource by URN.
func (s *rds) Fetch(ctx context.Context, urn storage.URN) (Record, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+recordColumns+` FROM `+tableOf(urn.Kind)+` WHERE tenant = ? AND namespace = ? AND id = ?`, urn.Tenant, urn.Namespace, urn.ID)
	obj, err := read(row.Scan, s.registry)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("%w (%v)", storage.ErrNotFound, urn.String())
	case err != nil:
		return nil, fmt.Errorf("storage: unable to fetch, %w", err)
	default:
		return obj, nil
	}
}

// Delete deletes a resource from the storage.
func (s *rds) Delete(ctx context.Context, urn storage.URN) (Record, error) {
	deleted, err := s.Fetch(ctx, urn)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("storage: begin delete: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM `+tableOf(urn.Kind)+` WHERE tenant = ? AND namespace = ? AND id = ?`, urn.Tenant, urn.Namespace, urn.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to delete record: %w", err)
	}
	if rows, err := result.RowsAffected(); err != nil {
		return nil, fmt.Errorf("failed to inspect deleted record: %w", err)
	} else if rows == 0 {
		return nil, fmt.Errorf("%w (%v)", storage.ErrNotFound, urn.String())
	}
	if err := replaceLinks(ctx, tx, urn, nil); err != nil {
		return nil, err
	}
	if err := s.recordChange(ctx, tx, urn, changeDelete, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("storage: commit delete: %w", err)
	}
	return deleted, nil
}

// Search performs a query against the storage layer and calls the specified
// function for each retrieved object.
func (s *rds) Search(ctx context.Context, kind storage.Kind, q storage.Query) (iter.Seq[Record], error) {
	rows, err := s.query(ctx, "SELECT "+recordColumns, kind, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	capacity := q.Limit
	if capacity <= 0 {
		capacity = 16
	}
	out := make([]Record, 0, capacity)
	for rows.Next() {
		obj, err := read(rows.Scan, s.registry)
		if err != nil {
			return nil, fmt.Errorf("storage: unable to read, %w", err)
		}
		out = append(out, obj)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: unable to read rows, %w", err)
	}

	return func(yield func(Record) bool) {
		for _, obj := range out {
			if !yield(obj) {
				return
			}
		}
	}, nil
}

// Count returns the number of records that match the specified query.
func (s *rds) Count(ctx context.Context, kind storage.Kind, q storage.Query) (int, error) {
	switch {
	case q.Limit != 0:
		return 0, fmt.Errorf("storage: count does not support limit")
	case q.Offset != 0:
		return 0, fmt.Errorf("storage: count does not support offset")
	case q.SortBy != nil:
		return 0, fmt.Errorf("storage: count does not support sorting")
	}

	rows, err := s.query(ctx, "SELECT COUNT(*)", kind, q)
	if err != nil {
		return 0, err
	}

	defer rows.Close()
	if !rows.Next() {
		return 0, nil
	}

	var count int
	if err := rows.Scan(&count); err != nil {
		return 0, fmt.Errorf("storage: unable to read count, %w", err)
	}

	return count, nil
}
