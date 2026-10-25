#!/bin/sh
# Adapted from temporalio/samples-server/compose/scripts/setup-postgres.sh.
# Databases are created by Postgres init; vendor schema operations are repeatable.
set -eu
: "${POSTGRES_SEEDS:?POSTGRES_SEEDS is required}"
: "${POSTGRES_USER:?POSTGRES_USER is required}"

for database in temporal temporal_visibility; do
  temporal-sql-tool --plugin postgres12 --ep "$POSTGRES_SEEDS" \
    -u "$POSTGRES_USER" -p 5432 --db "$database" setup-schema -v 0.0
  case "$database" in
    temporal) schema=temporal ;;
    temporal_visibility) schema=visibility ;;
  esac
  temporal-sql-tool --plugin postgres12 --ep "$POSTGRES_SEEDS" \
    -u "$POSTGRES_USER" -p 5432 --db "$database" update-schema \
    -d "/etc/temporal/schema/postgresql/v12/$schema/versioned"
done
