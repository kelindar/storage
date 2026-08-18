package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/kelindar/storage"
	"github.com/rs/xid"
)

func autoMigrate(db *sql.DB, registry storage.Registry) error {
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
			createSearchIndex(db, table, label),
			repairEmptyIDs(db, table),
		); err != nil {
			return err
		}
	}
	return nil
}

func createLinksTable(db *sql.DB) error {
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

func createSequencesTable(db *sql.DB) error {
	return execf(db, `CREATE TABLE IF NOT EXISTS sequences (
		name TEXT PRIMARY KEY NOT NULL,
		value INTEGER NOT NULL CHECK (value >= 0)
	)`)
}

func createChangesTable(db *sql.DB) error {
	return errors.Join(
		execf(db, `CREATE TABLE IF NOT EXISTS change_log (
			seq INTEGER PRIMARY KEY AUTOINCREMENT,
			created_at INTEGER NOT NULL,
			kind_hash INTEGER NOT NULL,
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

func createLocksTable(db *sql.DB) error {
	return execf(db, `CREATE TABLE IF NOT EXISTS locks (
		name TEXT PRIMARY KEY NOT NULL,
		owner TEXT NOT NULL CHECK (owner != ''),
		expires_at INTEGER NOT NULL
	)`)
}

func createTable(db *sql.DB, table, label string) error {
	if err := errors.Join(
		execf(db, `CREATE TABLE IF NOT EXISTS %s ( tenant TEXT NOT NULL, id TEXT PRIMARY KEY, namespace TEXT NOT NULL, state TEXT, data JSON, indexed_by TEXT, created_by TEXT, updated_by TEXT, created_at INTEGER, updated_at INTEGER, expires_at INTEGER NOT NULL DEFAULT 0)`, table),
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
		_, err = db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN expires_at INTEGER NOT NULL DEFAULT 0`)
	}
	return err
}

func columnExists(db *sql.DB, table, column string) (bool, error) {
	var exists bool
	err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM pragma_table_info(?) WHERE name = ?)`, table, column).Scan(&exists)
	return exists, err
}

func createExpirationIndex(db *sql.DB, table, label string) error {
	return execf(db, `CREATE INDEX IF NOT EXISTS %s ON %s(expires_at, id) WHERE expires_at != 0`,
		quoteIdent(label+"_idx_expiration"), table)
}

func fts5Enabled(db *sql.DB) bool {
	_, err := db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS __fts_probe USING fts5(id, data)`)
	if err != nil {
		return false
	}
	_, _ = db.Exec(`DROP TABLE IF EXISTS __fts_probe`)
	return true
}

func createSearchIndex(db *sql.DB, table, label string) error {
	if err := execf(db, `CREATE VIRTUAL TABLE IF NOT EXISTS %s USING fts5(id, data)`,
		quoteIdent(label+"_fts")); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "fts5") {
			return nil
		}
		return err
	}

	return errors.Join(
		execf(db, `CREATE TRIGGER IF NOT EXISTS %s BEFORE UPDATE ON %s BEGIN DELETE FROM %s WHERE rowid = old.rowid; END`,
			quoteIdent(label+"_fts_before_update"), table, quoteIdent(label+"_fts")),
		execf(db, `CREATE TRIGGER IF NOT EXISTS %s BEFORE DELETE ON %s BEGIN DELETE FROM %s WHERE rowid = old.rowid; END`,
			quoteIdent(label+"_fts_before_delete"), table, quoteIdent(label+"_fts")),
		execf(db, `CREATE TRIGGER IF NOT EXISTS %s AFTER UPDATE ON %s BEGIN INSERT INTO %s(rowid, id, data) VALUES (new.rowid, new.id, new.data); END`,
			quoteIdent(label+"_after_update"), table, quoteIdent(label+"_fts")),
		execf(db, `CREATE TRIGGER IF NOT EXISTS %s AFTER INSERT ON %s BEGIN INSERT INTO %s(rowid, id, data) VALUES (new.rowid, new.id, new.data); END`,
			quoteIdent(label+"_after_insert"), table, quoteIdent(label+"_fts")),
		execf(db, `INSERT INTO %s(rowid, id, data) SELECT rowid, id, data FROM %s WHERE rowid NOT IN (SELECT rowid FROM %s)`,
			quoteIdent(label+"_fts"), table, quoteIdent(label+"_fts")),
	)
}

func repairEmptyIDs(db *sql.DB, table string) error {
	rows, err := db.Query(`SELECT rowid FROM ` + table + ` WHERE id = '' OR json_extract(data, '$.id') IS NULL OR json_extract(data, '$.id') = ''`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var rowids []int64
	for rows.Next() {
		var rowid int64
		if err := rows.Scan(&rowid); err != nil {
			return err
		}
		rowids = append(rowids, rowid)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, rowid := range rowids {
		id := xid.New().String()
		if _, err := db.Exec(`UPDATE `+table+` SET id = ?, data = json_set(data, '$.id', ?) WHERE rowid = ?`, id, id, rowid); err != nil {
			return err
		}
	}
	return nil
}

func execf(db *sql.DB, sql string, args ...any) error {
	_, err := db.Exec(fmt.Sprintf(sql, args...))
	return err
}
