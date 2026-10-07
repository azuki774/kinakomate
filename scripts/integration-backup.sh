#!/bin/sh
set -eu

SCRIPT_DIR=$(CDPATH= cd "$(dirname "$0")" && pwd)
ROOT=$(CDPATH= cd "$SCRIPT_DIR/.." && pwd)
ENGINE=${CONTAINER_ENGINE:-docker}
# Share the database container's isolated network namespace. This avoids
# container DNS and does not expose the fixture services on the host.
case "$ENGINE" in
    *podman) BASE_NETWORK=slirp4netns ;;
    *) BASE_NETWORK=bridge ;;
esac
POSTGRES_IMAGE=${POSTGRES_IMAGE:-docker.io/library/postgres:18-bookworm}
RUSTFS_IMAGE=${RUSTFS_IMAGE:-docker.io/rustfs/rustfs@sha256:1803faef57627e2d9c2e7d89d655d712ddded5389040054987163043fecb6a3c}
AWS_CLI_IMAGE=${AWS_CLI_IMAGE:-public.ecr.aws/aws-cli/aws-cli@sha256:11b9f131024b5a799aa25a10a8146f1a1e3221eb9904da7e4b788040ff005377}

for utility in mktemp chmod gzip cmp diff sed wc tr sleep; do
    if ! command -v "$utility" >/dev/null 2>&1; then
        printf 'required command not found: %s\n' "$utility" >&2
        exit 127
    fi
done
if ! command -v "$ENGINE" >/dev/null 2>&1; then
    printf 'container engine not found: %s\n' "$ENGINE" >&2
    exit 127
fi
if ! "$ENGINE" info >/dev/null 2>&1; then
    printf 'container engine is unavailable: %s\n' "$ENGINE" >&2
    exit 1
fi

TMP_ROOT=${TMPDIR:-/tmp}
TMP_ROOT=$(CDPATH= cd "$TMP_ROOT" && pwd)
WORK_DIR=$(mktemp -d "$TMP_ROOT/kinakomate-backup-integration.XXXXXX")
chmod 0755 "$WORK_DIR"
mkdir -m 0777 "$WORK_DIR/aws" "$WORK_DIR/tmp"
chmod 0777 "$WORK_DIR/aws" "$WORK_DIR/tmp"
PREFIX=kinakomate-backup-it-$$
PG_CONTAINER=$PREFIX-postgres
NETWORK=container:$PG_CONTAINER
RUSTFS_CONTAINER=$PREFIX-rustfs
APP_IMAGE=kinakomate-backup-integration:$$

DB_USER=backup_it
DB_PASS=integration-only-password
DB_NAME='backup integration host=198.51.100.42 port=1'
RESTORE_DB=backup_restore_it
S3_BUCKET=backup-integration
S3_KEY=integration/postgres.sql.gz
S3_REGION=us-east-1
AWS_ACCESS_KEY_ID=integrationaccess
AWS_SECRET_ACCESS_KEY=integrationsecret
S3_ENDPOINT=http://127.0.0.1:9000
printf '[default]\nregion = %s\ns3 =\n    addressing_style = path\n' "$S3_REGION" > "$WORK_DIR/aws/config"

cleanup() {
    result=$?
    trap - 0 HUP INT TERM
    "$ENGINE" rm --force --volumes "$RUSTFS_CONTAINER" "$PG_CONTAINER" >/dev/null 2>&1 || true
    "$ENGINE" image rm "$APP_IMAGE" >/dev/null 2>&1 || true
    rm -rf "$WORK_DIR"
    exit "$result"
}
trap cleanup 0
trap 'exit 130' INT
trap 'exit 143' TERM

aws_cli() {
    "$ENGINE" run --rm --network "$NETWORK" \
        --volume "$WORK_DIR/aws:/aws" \
        --env "AWS_ACCESS_KEY_ID=$AWS_ACCESS_KEY_ID" \
        --env "AWS_SECRET_ACCESS_KEY=$AWS_SECRET_ACCESS_KEY" \
        --env "AWS_DEFAULT_REGION=$S3_REGION" \
        --env AWS_PAGER= \
        --env AWS_CONFIG_FILE=/aws/config \
        "$AWS_CLI_IMAGE" --endpoint-url "$S3_ENDPOINT" --region "$S3_REGION" "$@"
}

wait_for_postgres() {
    attempt=0
    while [ "$attempt" -lt 60 ]; do
        if "$ENGINE" exec "$PG_CONTAINER" pg_isready -q -U postgres -d postgres >/dev/null 2>&1; then
            return 0
        fi
        attempt=$((attempt + 1))
        sleep 1
    done
    printf 'PostgreSQL did not become ready\n' >&2
    "$ENGINE" logs "$PG_CONTAINER" >&2 || true
    return 1
}

wait_for_rustfs() {
    attempt=0
    while [ "$attempt" -lt 60 ]; do
        if aws_cli s3api list-buckets >/dev/null 2>&1; then
            return 0
        fi
        attempt=$((attempt + 1))
        sleep 1
    done
    printf 'RustFS S3 API did not become ready\n' >&2
    "$ENGINE" logs "$RUSTFS_CONTAINER" >&2 || true
    return 1
}

run_backup() {
    password=$1
    "$ENGINE" run --rm --network "$NETWORK" \
        --volume "$WORK_DIR/tmp:/tmp" \
        --env "TMPDIR=/tmp" \
        --env DB_HOST=127.0.0.1 \
        --env DB_PORT=5432 \
        --env "DB_USER=$DB_USER" \
        --env "DB_PASS=$password" \
        --env "DB_NAME=$DB_NAME" \
        --env "S3_REGION=$S3_REGION" \
        --env "S3_BUCKET=$S3_BUCKET" \
        --env "S3_KEY=$S3_KEY" \
        --env "S3_ENDPOINT=$S3_ENDPOINT" \
        --env "AWS_ACCESS_KEY_ID=$AWS_ACCESS_KEY_ID" \
        --env "AWS_SECRET_ACCESS_KEY=$AWS_SECRET_ACCESS_KEY" \
        "$APP_IMAGE" backup
}

# Build and exercise the exact distroless image that is published for runtime.
"$ENGINE" build --tag "$APP_IMAGE" "$ROOT"
"$ENGINE" run --detach --name "$PG_CONTAINER" --network "$BASE_NETWORK" \
    --env POSTGRES_USER=postgres \
    --env POSTGRES_PASSWORD=postgres-integration-password \
    --env POSTGRES_DB=postgres \
    --env POSTGRES_INITDB_ARGS=--auth-host=scram-sha-256 \
    "$POSTGRES_IMAGE" >/dev/null
"$ENGINE" run --detach --name "$RUSTFS_CONTAINER" --network "$NETWORK" \
    --env "RUSTFS_ACCESS_KEY=$AWS_ACCESS_KEY_ID" \
    --env "RUSTFS_SECRET_KEY=$AWS_SECRET_ACCESS_KEY" \
    --env RUSTFS_ADDRESS=:9000 \
    --env RUSTFS_CONSOLE_ADDRESS=:9001 \
    --env RUSTFS_CONSOLE_ENABLE=true \
    "$RUSTFS_IMAGE" /data >/dev/null

wait_for_postgres
wait_for_rustfs
aws_cli s3api create-bucket --bucket "$S3_BUCKET" >/dev/null

# This database name intentionally resembles libpq connection-string options.
# It must remain a single database name rather than being re-parsed as conninfo.
"$ENGINE" exec -i "$PG_CONTAINER" psql -X -v ON_ERROR_STOP=1 -U postgres -d postgres <<'SQL'
CREATE ROLE backup_it LOGIN PASSWORD 'integration-only-password';
CREATE DATABASE "backup integration host=198.51.100.42 port=1" OWNER backup_it;
CREATE DATABASE backup_restore_it OWNER backup_it;
SQL

"$ENGINE" exec -i \
    --env PGHOST=127.0.0.1 \
    --env "PGDATABASE=$DB_NAME" \
    --env "PGPASSWORD=$DB_PASS" \
    "$PG_CONTAINER" psql -X -v ON_ERROR_STOP=1 -U "$DB_USER" <<'SQL'
CREATE SCHEMA "app schema";
CREATE TYPE "app schema".record_state AS ENUM ('pending', 'complete');
CREATE TABLE "app schema".entries (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    state "app schema".record_state NOT NULL,
    payload text NOT NULL CHECK (length(payload) = 768),
    created_at timestamptz NOT NULL
);
CREATE INDEX entries_state_created_idx ON "app schema".entries (state, created_at);
CREATE VIEW "app schema".entry_totals AS
    SELECT state, count(*) AS row_count
    FROM "app schema".entries
    GROUP BY state;
INSERT INTO "app schema".entries (state, payload, created_at)
SELECT (CASE WHEN i % 2 = 0 THEN 'complete' ELSE 'pending' END)::"app schema".record_state,
       (
           SELECT string_agg(md5(i::text || ':' || j::text || ':' || random()::text), '' ORDER BY j)
           FROM generate_series(1, 24) AS parts(j)
       ),
       TIMESTAMPTZ '2025-01-01 00:00:00+00' + i * INTERVAL '1 second'
FROM generate_series(1, 30000) AS records(i);
SQL

run_backup "$DB_PASS"
aws_cli s3 cp "s3://$S3_BUCKET/$S3_KEY" /aws/backup-before.sql.gz >/dev/null
OBJECT_SIZE=$(wc -c < "$WORK_DIR/aws/backup-before.sql.gz" | tr -d '[:space:]')
if [ "$OBJECT_SIZE" -le 5242880 ]; then
    printf 'compressed object is %s bytes; multipart verification needs more than 5 MiB\n' "$OBJECT_SIZE" >&2
    exit 1
fi
gzip -t "$WORK_DIR/aws/backup-before.sql.gz"
printf 'Uploaded compressed backup size: %s bytes (multipart threshold exceeded)\n' "$OBJECT_SIZE"

# A failed pg_dump must not replace an object that was already committed.
if run_backup 'deliberately-wrong-password'; then
    printf 'backup unexpectedly succeeded with invalid database credentials\n' >&2
    exit 1
fi
aws_cli s3 cp "s3://$S3_BUCKET/$S3_KEY" /aws/backup-after-failed-dump.sql.gz >/dev/null
if ! cmp -s "$WORK_DIR/aws/backup-before.sql.gz" "$WORK_DIR/aws/backup-after-failed-dump.sql.gz"; then
    printf 'failed dump changed the previously stored object\n' >&2
    exit 1
fi

# Restore through the psql binary inside the same distroless application image
# into a separate, initially empty database.
gzip -dc "$WORK_DIR/aws/backup-before.sql.gz" > "$WORK_DIR/restore.sql"
chmod 0644 "$WORK_DIR/restore.sql"
"$ENGINE" run --rm --network "$NETWORK" \
    --volume "$WORK_DIR:/work:ro" \
    --entrypoint /usr/bin/psql \
    --env PGHOST=127.0.0.1 \
    --env PGPORT=5432 \
    --env "PGUSER=$DB_USER" \
    --env "PGPASSWORD=$DB_PASS" \
    --env "PGDATABASE=$RESTORE_DB" \
    "$APP_IMAGE" -X -v ON_ERROR_STOP=1 -f /work/restore.sql

RESTORED_ROWS=$("$ENGINE" exec \
    --env PGHOST=127.0.0.1 \
    --env "PGDATABASE=$RESTORE_DB" \
    --env "PGPASSWORD=$DB_PASS" \
    "$PG_CONTAINER" psql -XAt -U "$DB_USER" -c 'SELECT count(*) FROM "app schema".entries')
if [ "$RESTORED_ROWS" != 30000 ]; then
    printf 'restored database has %s rows; expected 30000\n' "$RESTORED_ROWS" >&2
    exit 1
fi

dump_normalized() {
    dump_db=$1
    dump_mode=$2
    dump_label=$3
    dump_raw="$WORK_DIR/$dump_label.raw.sql"
    dump_normalized="$WORK_DIR/$dump_label.sql"
    if ! "$ENGINE" exec \
        --env PGHOST=127.0.0.1 \
        --env "PGDATABASE=$dump_db" \
        --env "PGPASSWORD=$DB_PASS" \
        "$PG_CONTAINER" pg_dump -U "$DB_USER" --no-owner --no-acl "--$dump_mode" > "$dump_raw"; then
        printf 'could not produce %s dump for database %s\n' "$dump_mode" "$dump_db" >&2
        exit 1
    fi
    # pg_dump includes version comments and a per-run psql \restrict token;
    # neither is database schema or data, so remove those volatile lines before
    # comparing otherwise identical plain SQL output.
    sed -e '/^--/d' -e '/^\\restrict /d' -e '/^\\unrestrict /d' "$dump_raw" > "$dump_normalized"
}

for mode in schema-only data-only; do
    dump_normalized "$DB_NAME" "$mode" "source-$mode"
    dump_normalized "$RESTORE_DB" "$mode" "restored-$mode"
    if ! cmp -s "$WORK_DIR/source-$mode.sql" "$WORK_DIR/restored-$mode.sql"; then
        printf 'restored %s differs from the source database\n' "$mode" >&2
        diff -u "$WORK_DIR/source-$mode.sql" "$WORK_DIR/restored-$mode.sql" || true
        exit 1
    fi
done

printf 'Backup integration passed: dump, multipart upload, failed-dump object preservation, restore, schema, and data match.\n'
