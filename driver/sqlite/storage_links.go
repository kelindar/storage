package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/kelindar/storage"
)

// Link replaces every outgoing link for source.
func (s *rds) Link(ctx context.Context, source storage.URN) error {
	if !source.IsValid() {
		return fmt.Errorf("storage: link source is invalid")
	}

	var links []storage.Link
	obj, err := s.Fetch(ctx, source)
	switch {
	case storage.IsNotFound(err):
	case err != nil:
		return err
	default:
		links, err = storage.Links(obj)
		if err != nil {
			return err
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin links: %w", err)
	}
	defer tx.Rollback()
	if err := replaceLinks(ctx, tx, source, links); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit links: %w", err)
	}
	return nil
}

// replaceLinks replaces every outgoing link for source in tx.
func replaceLinks(ctx context.Context, tx *sql.Tx, source storage.URN, links []storage.Link) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM links WHERE source_tenant = ? AND source_namespace = ? AND source_kind = ? AND source_id = ?`, source.Tenant, source.Namespace, source.Kind, source.ID); err != nil {
		return fmt.Errorf("storage: clear links: %w", err)
	}
	var seen map[storage.Link]struct{}
	if len(links) > 1 {
		seen = make(map[storage.Link]struct{}, len(links))
	}
	for _, link := range links {
		if err := validateLink(source, link, seen); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO links (source_tenant, source_namespace, source_kind, source_id, target_tenant, target_namespace, target_kind, target_id, path, kind) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, link.Source.Tenant, link.Source.Namespace, link.Source.Kind, link.Source.ID, link.Target.Tenant, link.Target.Namespace, link.Target.Kind, link.Target.ID, link.Path, link.Kind); err != nil {
			return fmt.Errorf("storage: insert link: %w", err)
		}
	}
	return nil
}

func validateLinks(source storage.URN, links []storage.Link) error {
	var seen map[storage.Link]struct{}
	if len(links) > 1 {
		seen = make(map[storage.Link]struct{}, len(links))
	}
	for _, link := range links {
		if err := validateLink(source, link, seen); err != nil {
			return err
		}
	}
	return nil
}

func validateLink(source storage.URN, link storage.Link, seen map[storage.Link]struct{}) error {
	switch {
	case link.Source != source:
		return fmt.Errorf("storage: link source mismatch")
	case !link.Target.IsValid():
		return fmt.Errorf("storage: link target is invalid")
	case link.Path == "":
		return fmt.Errorf("storage: link path is required")
	case link.Kind != storage.LinkOwnership && link.Kind != storage.LinkDependency:
		return fmt.Errorf("storage: link kind is invalid")
	case link.Source.Tenant != link.Target.Tenant:
		return fmt.Errorf("storage: link crosses tenants")
	case link.Kind == storage.LinkOwnership && link.Source.Kind == storage.Kind("blob"):
		return fmt.Errorf("storage: blob cannot own resources")
	case link.Kind == storage.LinkOwnership && link.Source.Kind != storage.Kind("bundle") && link.Target.Kind != storage.Kind("blob"):
		return fmt.Errorf("storage: only Bundles may own non-Blob resources")
	}
	if seen != nil {
		if _, ok := seen[link]; ok {
			return fmt.Errorf("storage: duplicate link")
		}
		seen[link] = struct{}{}
	}
	return nil
}

// Links returns incoming links for target in deterministic order.
func (s *rds) Links(ctx context.Context, target storage.URN) ([]storage.Link, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source_tenant, source_namespace, source_kind, source_id, path, kind FROM links WHERE target_tenant = ? AND target_namespace = ? AND target_kind = ? AND target_id = ? ORDER BY source_tenant, source_namespace, source_kind, source_id, path, kind`, target.Tenant, target.Namespace, target.Kind, target.ID)
	if err != nil {
		return nil, fmt.Errorf("storage: query links: %w", err)
	}
	defer rows.Close()

	var out []storage.Link
	for rows.Next() {
		var link storage.Link
		link.Target = target
		if err := rows.Scan(&link.Source.Tenant, &link.Source.Namespace, &link.Source.Kind, &link.Source.ID, &link.Path, &link.Kind); err != nil {
			return nil, fmt.Errorf("storage: read links: %w", err)
		}
		out = append(out, link)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: read links: %w", err)
	}
	return out, nil
}

var _ storage.Storage = (*rds)(nil)
