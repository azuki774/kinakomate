# syntax=docker/dockerfile:1

FROM golang:1.26.7 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/kinakomate ./cmd/kinakomate

# Stage: install the PostgreSQL client in a Debian build base and stage the
# real psql binary plus every shared library it depends on. We discover the
# dependencies at build time with `ldd` (instead of hardcoding a list that can
# silently miss transitive libs) so the runtime image is complete.
FROM debian:bookworm-slim AS psql
RUN apt-get update \
    && apt-get install -y --no-install-recommends postgresql-client \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

# /usr/bin/psql in Debian is a pg_wrapper shell script that requires /bin/sh
# (absent in distroless). Resolve the real ELF binary that ships under
# /usr/lib/postgresql/<version>/bin/psql and stage it along with its complete
# set of shared library dependencies, preserving paths. Libraries the distroless
# base already provides (glibc, LD loader, libdl/libpthread/libm/...) are
# excluded to avoid version conflicts.
RUN set -eux; \
    PSQL=$(find /usr/lib/postgresql -path '*/bin/psql' -type f | sort -V | tail -1); \
    VERSION=$(echo "$PSQL" | sed -E 's#/usr/lib/postgresql/([0-9]+)/bin/psql#\1#'); \
    mkdir -p /staging/usr/bin /staging/usr/lib/x86_64-linux-gnu; \
    cp "$PSQL" /staging/usr/bin/psql; \
    echo "staged psql from PostgreSQL $VERSION"; \
    for lib in $(ldd "$PSQL" | awk '/=> \// {print $3}' | sort -u); do \
        base=$(basename "$lib"); \
        case "$base" in \
            libc.so.*|libm.so.*|libdl.so.*|libpthread.so.*|ld-linux-x86-64.so.*|librt.so.*|libutil.so.*|libresolv.so.*) continue ;; \
        esac; \
        cp "$lib" /staging/usr/lib/x86_64-linux-gnu/; \
    done

# The runner invokes psql directly (ON_ERROR_STOP + single transaction), so the
# runtime image must include psql. We start from a glibc-based distroless base
# (psql is dynamically linked) and copy the staged binary + libraries in.
FROM gcr.io/distroless/base-debian12:nonroot AS runtime

COPY --from=build /out/kinakomate /usr/bin/kinakomate
COPY --from=psql /staging/usr/bin/psql /usr/bin/psql
COPY --from=psql /staging/usr/lib/x86_64-linux-gnu/ /usr/lib/x86_64-linux-gnu/

ENTRYPOINT ["/usr/bin/kinakomate"]
