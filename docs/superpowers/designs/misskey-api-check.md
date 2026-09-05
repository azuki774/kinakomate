# Misskey API restore verification design

## Goal

The restore runner must prove that a restored database is usable by the
application, not merely that PostgreSQL accepted the dump. It therefore starts
the web workload and checks the public Misskey API before cleanup.

## Dependency contract

The runner depends on the existing `MisskeyAPI` interface:

```go
type MisskeyAPI interface {
    WaitForReadiness(context.Context, *config.Config, time.Duration) error
    CheckGlobalTimeline(context.Context, *config.Config) error
}
```

Production wiring uses `newMisskeyAPI`; tests inject a recording fake.

## Workflow and failure policy

After preflight, connection checks, dump download, web scale-down, database
scale-up and waits, database reset, and restore, the runner performs:

1. Scale web to 1.
2. Wait for its actual replica count to reach 1, bounded by `scaleTimeout`.
3. Retry `GET /healthz` until readiness or `scaleTimeout`.
4. Call the global timeline once and validate 1 through 10 Notes.
5. Scale web to 0, then database to 0.

Every step fails fast with the step name wrapped around the underlying error.
On failure, deferred rollback scales web to 0 with a non-cancelled context.
Normal database cleanup is skipped, leaving it at 1 for investigation. Dump
cleanup is conditional so failures before download cannot dereference a nil
handle.

## Validation, logging, and security

`MISSKEY_BASE_URL` is required and accepts only an `http` or `https` origin
with a host and an optional root slash. Credentials, non-root paths, queries,
and fragments are rejected during preflight.

Readiness retries transient HTTP failures. The timeline request sends only
`{"limit":10}` and accepts a single JSON array containing 1 through 10 objects;
each must have a non-empty string `id` and an RFC3339 `createdAt`. Redirects are
not followed. Logs contain step status and the validated count, never response
content, Note fields, or credentials. The design introduces no hard-coded
deployment identifiers; configured workload names remain available in existing
audit logs.
