// Copyright (c) 2026, s0up and the autobrr contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package database

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpstreamMergePreservesForkSQLite(t *testing.T) {
	t.Parallel()
	conn, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "fork.db"))
	require.NoError(t, err)
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	db := &DB{writerConn: conn, dialect: DialectSQLite}
	checkForkUpgrade(t.Context(), t, db, migrationsFS, "migrations", "097_scope_client_settings_to_users.sql", db.migrate)
	rows, err := conn.QueryContext(t.Context(), "PRAGMA foreign_key_check")
	require.NoError(t, err)
	defer rows.Close()
	require.False(t, rows.Next(), "upgrade must not leave broken foreign keys")
	require.NoError(t, rows.Err())
}

func TestUpstreamMergePreservesForkPostgresIntegration(t *testing.T) {
	t.Parallel()
	ctx, dsn := openPostgresTestSchema(t)
	conn, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	db := &DB{writerConn: conn, dialect: DialectPostgres}
	checkForkUpgrade(ctx, t, db, postgresMigrationsFS, "postgres_migrations", "098_scope_client_settings_to_users.sql", db.migratePostgres)
}

func checkForkUpgrade(ctx context.Context, t *testing.T, db *DB, fsys fs.ReadDirFS, dir, cutoff string, migrate func() error) {
	t.Helper()
	conn := db.writerConn
	exec := func(query string) {
		t.Helper()
		_, err := conn.ExecContext(ctx, query)
		require.NoError(t, err, query)
	}
	// Recreate the schema shipped by this fork before the upstream merge.
	// Migration 010 rebuilds referenced tables with SQLite foreign keys off.
	if db.dialect == DialectSQLite {
		exec("PRAGMA foreign_keys = OFF")
	}
	exec("CREATE TABLE migrations (filename TEXT PRIMARY KEY, applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)")
	entries, err := fs.ReadDir(fsys, dir)
	require.NoError(t, err)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".sql") || name > cutoff {
			continue
		}
		body, err := fs.ReadFile(fsys, path.Join(dir, name))
		require.NoError(t, err)
		exec(string(body))
		exec(fmt.Sprintf("INSERT INTO migrations (filename) VALUES ('%s')", name))
	}
	if db.dialect == DialectSQLite {
		exec("PRAGMA foreign_keys = ON")
	}
	exec("INSERT INTO users (id, username, password_hash, role) VALUES (1, 'admin', 'hash-admin', 'admin'), (2, 'alice', 'hash-alice', 'user'), (3, 'bob', 'hash-bob', 'user')")
	exec("INSERT INTO string_pool (value) VALUES ('fork-fixture') ON CONFLICT (value) DO NOTHING")
	exec(`INSERT INTO instances (id, name_id, host_id, username_id, password_encrypted, client_type, owner_id, tls_skip_verify, has_local_filesystem_access)
		SELECT 1, id, id, id, 'encrypted-fixture', 'transmission', 2, TRUE, TRUE FROM string_pool WHERE value = 'fork-fixture'`)
	exec("INSERT INTO instance_shares (instance_id, user_id, created_by) VALUES (1, 3, 2)")
	exec("INSERT INTO user_permissions (user_id, permission) VALUES (2, 'manage_updates')")
	exec("INSERT INTO client_settings (user_id, key, value) VALUES (2, 'sidebar', '{\"hidden\":[\"rss\"]}'), (3, 'sidebar', '{\"hidden\":[]}'), (2, 'layout', '{\"sync\":true}')")
	exec("INSERT INTO api_keys (key_hash, name_id, user_id) SELECT 'fixture-key-hash', id, 2 FROM string_pool WHERE value = 'fork-fixture'")
	exec("INSERT INTO theme_settings (id, theme_id, mode, variation) VALUES (1, 'custom-fixture', 'dark', '') ON CONFLICT (id) DO UPDATE SET theme_id = excluded.theme_id, mode = excluded.mode")

	queries := []string{
		"SELECT id, username, password_hash, role FROM users ORDER BY id",
		"SELECT id, client_type, owner_id, password_encrypted, tls_skip_verify, has_local_filesystem_access FROM instances ORDER BY id",
		"SELECT instance_id, user_id, created_by FROM instance_shares ORDER BY instance_id, user_id",
		"SELECT user_id, permission FROM user_permissions ORDER BY user_id, permission",
		"SELECT user_id, key, value FROM client_settings ORDER BY user_id, key",
		"SELECT key_hash, user_id FROM api_keys ORDER BY id",
	}
	read := func(query string) [][]any {
		t.Helper()
		rows, err := conn.QueryContext(ctx, query)
		require.NoError(t, err)
		defer rows.Close()
		columns, err := rows.Columns()
		require.NoError(t, err)
		var result [][]any
		for rows.Next() {
			values := make([]any, len(columns))
			targets := make([]any, len(columns))
			for i := range values {
				targets[i] = &values[i]
			}
			require.NoError(t, rows.Scan(targets...))
			result = append(result, values)
		}
		require.NoError(t, rows.Err())
		return result
	}
	before := make([][][]any, len(queries))
	for i, query := range queries {
		before[i] = read(query)
	}
	// Run the real migrator twice: an upgrade and a subsequent restart.
	for range 2 {
		require.NoError(t, migrate())
		for i, query := range queries {
			require.Equal(t, before[i], read(query), query)
		}
		var clientType, themeID, mode, sshHost string
		var ownerID, sshPort int
		require.NoError(t, conn.QueryRowContext(ctx, "SELECT client_type, owner_id, ssh_host, ssh_port FROM instances_view WHERE id = 1").Scan(&clientType, &ownerID, &sshHost, &sshPort))
		require.Equal(t, "transmission", clientType)
		require.Equal(t, 2, ownerID)
		require.Empty(t, sshHost)
		require.Equal(t, 22, sshPort)
		require.NoError(t, conn.QueryRowContext(ctx, "SELECT theme_id, mode FROM theme_settings WHERE slot = 'default'").Scan(&themeID, &mode))
		require.Equal(t, "custom-fixture", themeID)
		require.Equal(t, "dark", mode)
	}
}
