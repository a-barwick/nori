package postgres

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// Releases run migrations before the new code serves traffic, and a revert sends traffic back
// to the previous code on the new schema. Migrations must therefore only add schema; removals,
// renames, and NOT NULL constraints without defaults ship later, in a migration named
// *_cleanup.sql, once no live code writes rows without them.
var destructiveMigration = regexp.MustCompile(`(?i)\b(DROP\s+(TABLE|COLUMN|SCHEMA)|RENAME\s+(TO|COLUMN)|ALTER\s+COLUMN\s+\S+\s+(SET\s+DATA\s+)?TYPE|ALTER\s+COLUMN\s+\S+\s+SET\s+NOT\s+NULL)\b`)
var addColumnStmt = regexp.MustCompile(`(?is)\bADD\s+COLUMN\b[^;]*`)
var notNullClause = regexp.MustCompile(`(?i)\bNOT\s+NULL\b`)
var defaultClause = regexp.MustCompile(`(?i)\bDEFAULT\b`)

var grandfatheredMigrations = map[string]bool{"004_index_and_ssi_tuning.sql": true}

func destructiveMigrationReason(body []byte) string {
	if match := destructiveMigration.Find(body); match != nil {
		return string(match)
	}
	for _, stmt := range addColumnStmt.FindAll(body, -1) {
		if notNullClause.Match(stmt) && !defaultClause.Match(stmt) {
			return strings.TrimSpace(string(stmt))
		}
	}
	return ""
}

func TestMigrationsOnlyAddSchema(t *testing.T) {
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil || len(names) == 0 {
		t.Fatalf("list migrations: %v (%d found)", err, len(names))
	}
	for _, path := range names {
		name := strings.TrimPrefix(path, "migrations/")
		if grandfatheredMigrations[name] || strings.HasSuffix(name, "_cleanup.sql") {
			continue
		}
		body, err := migrations.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if reason := destructiveMigrationReason(body); reason != "" {
			t.Errorf("%s contains %q; keep releases revertible by shipping removals or NOT NULL constraints without defaults in a later *_cleanup.sql migration", name, reason)
		}
	}
}

func TestDestructiveMigrationReason(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want bool
	}{
		{"drop column", "ALTER TABLE foo DROP COLUMN bar;", true},
		{"rename column", "ALTER TABLE foo RENAME COLUMN a TO b;", true},
		{"alter type", "ALTER TABLE foo ALTER COLUMN a TYPE bigint;", true},
		{"set not null", "ALTER TABLE foo ALTER COLUMN a SET NOT NULL;", true},
		{"add not null without default", "ALTER TABLE foo ADD COLUMN bar text NOT NULL;", true},
		{"add not null with default", "ALTER TABLE foo ADD COLUMN bar boolean NOT NULL DEFAULT false;", false},
		{"add nullable column", "ALTER TABLE foo ADD COLUMN bar text;", false},
		{"create table with not null", "CREATE TABLE foo (id text NOT NULL);", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := destructiveMigrationReason([]byte(tc.sql)) != ""
			if got != tc.want {
				t.Fatalf("destructiveMigrationReason(%q) = %v, want %v", tc.sql, got, tc.want)
			}
		})
	}
}
