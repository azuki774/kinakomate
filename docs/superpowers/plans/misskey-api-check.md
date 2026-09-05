# Misskey API restore verification implementation plan

## Scope

Wire the already implemented Misskey HTTP client into the restore runner while
preserving all pre-restore behavior and the existing failure cleanup policy.

## Implementation

1. Replace the placeholder `Checks` dependency with `MisskeyAPI`, and construct
   it with `newMisskeyAPI` in production.
2. Extend runner tests first to require the exact post-restore sequence: web
   scale to 1, actual replica wait, API readiness wait, one global timeline
   check, then web/database cleanup.
3. Cover web wait, readiness, and timeline failures. Each must stop later steps,
   roll web back to 0, and omit database scale-down. Cover failure before dump
   download to require nil-safe cleanup.
4. Add the minimal runner steps with `scaleTimeout` for both the web replica
   wait and API readiness wait. Remove the no-op checks implementation.
5. Add `MISSKEY_BASE_URL` to the command fixture and document its safe origin
   constraints, the API verification sequence, and rollback behavior.

## Verification

Format modified Go files, inspect the diff, and run:

```text
env -u GOROOT GOCACHE=/tmp/kinakomate-go-cache go test ./...
env -u GOROOT GOCACHE=/tmp/kinakomate-go-cache go vet ./...
golangci-lint run ./...  # when installed
```

Before committing, inspect status, diff, and recent log; stage only this task's
files and use a Conventional Commits message.
