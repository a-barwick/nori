package postgres

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// Releases run migrations before the new code serves traffic, and a revert sends traffic back
// to the previous code on the new schema. Migrations must therefore only add schema; removals
// and renames ship later, in a migration named *_cleanup.sql, once no live code uses them.
var destructiveMigration = regexp.MustCompile(`(?i)\b(DROP\s+(TABLE|COLUMN|SCHEMA)|RENAME\s+(TO|COLUMN)|ALTER\s+COLUMN\s+\S+\s+(SET\s+DATA\s+)?TYPE)\b`)

var grandfatheredMigrations = map[string]bool{"004_index_and_ssi_tuning.sql": true}

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
		if match := destructiveMigration.Find(body); match != nil {
			t.Errorf("%s contains %q; keep releases revertible by shipping removals in a later *_cleanup.sql migration", name, match)
		}
	}
}
