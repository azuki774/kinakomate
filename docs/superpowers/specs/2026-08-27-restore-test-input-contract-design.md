# Design: restore-test-runner — Input Contract & Pre-flight Validation (Issue #3)

Date: 2026-08-27

## Goal

Define the input contract for the restore-test-runner as environment variables
and implement step 1: pre-flight validation that fails fast (non-zero exit)
before the runner touches any workload, database, or external resource.

## Scope

In scope:
- Environment-variable contract for the required core inputs.
- `S3_URI` parsing into bucket / prefix / endpoint.
- Pre-flight validation with no side effects ("validation before scale").
- Enforcing the workload-name invariant via an immutable config type.
- Structured logging of non-secret config values.

Out of scope (later issues):
- Actual dump fetch, restore, migration, readiness wait, checks.
- Phase timeouts, azkey API endpoint, checks directory (kept for later).
- Namespace resolution (resolved from the pod's own namespace later).

## Input contract

All values are injected explicitly via environment variables. No defaults: a
missing or empty value is a validation error.

| Field            | Env var     | Required | Secret | Notes                                            |
|------------------|-------------|----------|--------|--------------------------------------------------|
| workload         | `WORKLOAD`  | yes      | no     | Immutable K8s workload name (RFC 1123 label).    |
| S3 location      | `S3_URI`    | yes      | no     | `s3://bucket/prefix` or `https://endpoint/bucket/prefix`. |
| DB host          | `DB_HOST`   | yes      | no     |                                                  |
| DB port          | `DB_PORT`   | yes      | no     |                                                  |
| DB user          | `DB_USER`   | yes      | no     |                                                  |
| DB password      | `DB_PASS`   | yes      | yes    | Never logged.                                    |
| DB name          | —           | fixed    | no     | Hardcoded constant (`misskey`); not an input.    |
| namespace        | —           | resolved | no     | Pod's own namespace; resolved later, not input.  |

S3 credentials are intentionally out of this contract; the runner relies on
standard AWS SDK environment credentials (read-only, injected at deploy time).

### `S3_URI` parsing

- `s3://bucket/prefix` → AWS; `endpoint` empty (resolved by the SDK later),
  `bucket` from the authority, `prefix` from the path.
- `https://endpoint/bucket/prefix` (or `http://`) → S3-compatible;
  `endpoint` = `scheme://host`, `bucket` = first path segment, `prefix` = rest.
- Any other scheme (or a bucket-less value) is a validation error.

## Components

### `internal/config`

- `Config` struct: holds parsed inputs. No setters → immutable by construction,
  which enforces the workload-name invariant (the runner never derives or
  mutates these values).
- `LoadFromEnv() (*Config, error)`: reads the required env vars, validates
  presence/non-empty, validates `WORKLOAD` is an RFC 1123 label, parses
  `S3_URI`. Returns an error on the first failure. Performs no side effects.
- `Config.Loggable() map[string]any`: returns all non-secret fields for
  structured logging (omits `DB_PASS`).
- `DBName` constant (`misskey`).

### `internal/restore.Run` (step 1)

- Parses flags (reserved for later), then calls `config.LoadFromEnv()`.
- On error: returns the wrapped error so `main` exits non-zero. No workload or
  DB interaction occurs before this point ("validation before scale").
- On success: logs the config via the existing JSON `log.New()` logger
  (non-secret fields only).

## Error handling

- Missing/empty required input → `LoadFromEnv` error → non-zero exit.
- Invalid `WORKLOAD` (not an RFC 1123 label) → error.
- Unparseable / bucket-less `S3_URI` → error.

## Testing

- `internal/config/config_test.go`:
  - missing and empty required inputs → error.
  - invalid `WORKLOAD` → error.
  - `S3_URI` parsing for both supported forms and error cases.
  - `Loggable` omits `DB_PASS`.
- `internal/restore/restore_test.go`:
  - `Run` fails with no input.
  - `Run` succeeds with valid input (pre-flight only; no external calls).

## Acceptance-criteria mapping

- Required inputs detected / empty → fail non-zero: `LoadFromEnv` validation.
- Single parameter per resource, no defaults, explicit injection: env-var
  contract above.
- Validation before scale: `Run` validates before any workload/DB touch.
- Workload-name invariant: immutable `Config`, validated label.
- Non-secret config to structured log: `Loggable` + `logger.InfoContext`.
