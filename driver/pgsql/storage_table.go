package pgsql

import (
	"errors"
	"fmt"

	"github.com/kelindar/storage"
	"github.com/rs/xid"
)

func autoMigrate(db *database, registry storage.Registry) error {
	if err := createLinksTable(db); err != nil {
		return err
	}
	if err := createSequencesTable(db); err != nil {
		return err
	}
	if err := createChangesTable(db); err != nil {
		return err
	}
	if err := createLocksTable(db); err != nil {
		return err
	}
	for t := range registry.Types() {
		label := t.Kind.String()
		table := quoteIdent(label)
		if err := errors.Join(
			createTable(db, table, label),
			createExpirationIndex(db, table, label),
			repairEmptyIDs(db, table),
		); err != nil {
			return err
		}
	}
	return nil
}

func createLinksTable(db *database) error {
	return errors.Join(
		execf(db, `CREATE TABLE IF NOT EXISTS links (
			source_tenant TEXT NOT NULL, source_namespace TEXT NOT NULL, source_kind TEXT NOT NULL, source_id TEXT NOT NULL,
			target_tenant TEXT NOT NULL, target_namespace TEXT NOT NULL, target_kind TEXT NOT NULL, target_id TEXT NOT NULL,
			path TEXT NOT NULL, kind INTEGER NOT NULL,
			PRIMARY KEY (source_tenant, source_namespace, source_kind, source_id, target_tenant, target_namespace, target_kind, target_id, path, kind)
		)`),
		execf(db, `CREATE INDEX IF NOT EXISTS links_target ON links(target_tenant, target_namespace, target_kind, target_id, kind)`),
		execf(db, `CREATE INDEX IF NOT EXISTS links_source ON links(source_tenant, source_namespace, source_kind, source_id)`),
		execf(db, `CREATE UNIQUE INDEX IF NOT EXISTS links_owner ON links(target_tenant, target_namespace, target_kind, target_id) WHERE kind = 1`),
	)
}

func createSequencesTable(db *database) error {
	return execf(db, `CREATE TABLE IF NOT EXISTS sequences (
		name TEXT PRIMARY KEY NOT NULL,
		value INTEGER NOT NULL CHECK (value >= 0)
	)`)
}

func createChangesTable(db *database) error {
	return errors.Join(
		execf(db, `CREATE TABLE IF NOT EXISTS change_log (
			seq BIGSERIAL PRIMARY KEY,
			created_at BIGINT NOT NULL,
			kind_hash BIGINT NOT NULL,
			action INTEGER NOT NULL CHECK (action IN (1, 2, 3)),
			urn TEXT NOT NULL
		)`),
		execf(db, `CREATE INDEX IF NOT EXISTS change_log_kind_created ON change_log(kind_hash, created_at, seq)`),
		execf(db, `CREATE INDEX IF NOT EXISTS change_log_created ON change_log(created_at, seq)`),
		execf(db, `CREATE TABLE IF NOT EXISTS change_cursors (
			consumer TEXT NOT NULL,
			kind TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			seq INTEGER NOT NULL,
			PRIMARY KEY (consumer, kind)
		)`),
	)
}

func createLocksTable(db *database) error {
	return execf(db, `CREATE TABLE IF NOT EXISTS locks (
		name TEXT PRIMARY KEY NOT NULL,
		owner TEXT NOT NULL CHECK (owner != ''),
		expires_at BIGINT NOT NULL
	)`)
}

func createTable(db *database, table, label string) error {
	if err := errors.Join(
		execf(db, `CREATE TABLE IF NOT EXISTS %s ( tenant TEXT NOT NULL, id TEXT PRIMARY KEY, namespace TEXT NOT NULL, state TEXT, data JSONB, indexed_by TEXT, created_by TEXT, updated_by TEXT, created_at BIGINT, updated_at BIGINT, expires_at BIGINT NOT NULL DEFAULT 0)`, table),
		execf(db, `CREATE INDEX IF NOT EXISTS %s ON %s(tenant, namespace)`, quoteIdent(label+"_idx_tenant_namespace"), table),
		execf(db, `CREATE INDEX IF NOT EXISTS %s ON %s(namespace)`, quoteIdent(label+"_idx_namespace"), table),
		execf(db, `CREATE INDEX IF NOT EXISTS %s ON %s(state)`, quoteIdent(label+"_idx_state"), table),
		execf(db, `CREATE INDEX IF NOT EXISTS %s ON %s(indexed_by)`, quoteIdent(label+"_idx_index"), table),
	); err != nil {
		return err
	}
	exists, err := columnExists(db, label, "expires_at")
	switch {
	case err != nil:
		return err
	case !exists:
		_, err = db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN expires_at BIGINT NOT NULL DEFAULT 0`)
	}
	return err
}

func columnExists(db *database, table, column string) (bool, error) {
	var exists bool
	err := db.QueryRow(`SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = ? AND column_name = ?
	)`, table, column).Scan(&exists)
	return exists, err
}

func createExpirationIndex(db *database, table, label string) error {
	return execf(db, `CREATE INDEX IF NOT EXISTS %s ON %s(expires_at, id) WHERE expires_at != 0`,
		quoteIdent(label+"_idx_expiration"), table)
}

func repairEmptyIDs(db *database, table string) error {
	rows, err := db.Query(`SELECT ctid::text FROM ` + table + ` WHERE id = '' OR data ->> 'id' IS NULL OR data ->> 'id' = ''`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var ctids []string
	for rows.Next() {
		var ctid string
		if err := rows.Scan(&ctid); err != nil {
			return err
		}
		ctids = append(ctids, ctid)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, ctid := range ctids {
		id := xid.New().String()
		if _, err := db.Exec(`UPDATE `+table+` SET id = ?, data = jsonb_set(COALESCE(data, '{}'::jsonb), '{id}', to_jsonb(?::text), true) WHERE ctid = ?::tid`, id, id, ctid); err != nil {
			return err
		}
	}
	return nil
}

func execf(db *database, query string, args ...any) error {
	_, err := db.Exec(fmt.Sprintf(query, args...))
	return err
}
