# syntax=docker/dockerfile:1

FROM golang:1.26.7 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/kinakomate ./cmd/kinakomate

# PostgreSQL's official image publishes both PG18 client executables from the
# same PostgreSQL apt distribution. Copy the ELF binaries and their dynamic
# dependencies into the distroless runtime; the image's entrypoint and server
# are not part of the application image.
FROM postgres:18-bookworm AS postgres-client
RUN set -eux; \
    mkdir -p /staging/usr/bin /staging/usr/lib/x86_64-linux-gnu; \
    for client in pg_dump psql; do \
        binary="/usr/lib/postgresql/18/bin/$client"; \
        test -x "$binary"; \
        cp "$binary" "/staging/usr/bin/$client"; \
        echo "staged $client from PostgreSQL 18"; \
        for lib in $(ldd "$binary" | awk '/=> \// {print $3}' | sort -u); do \
            base=$(basename "$lib"); \
            case "$base" in \
                libc.so.*|libm.so.*|libdl.so.*|libpthread.so.*|ld-linux-*.so.*|librt.so.*|libutil.so.*|libresolv.so.*) continue ;; \
            esac; \
            cp "$lib" /staging/usr/lib/x86_64-linux-gnu/; \
        done; \
    done; \
    /usr/lib/postgresql/18/bin/pg_dump --version; \
    /usr/lib/postgresql/18/bin/psql --version

# Both clients dynamically link against glibc. Start from a glibc-based
# distroless runtime, retaining its nonroot user, and copy in only the Go
# application and the PostgreSQL clients with their required libraries.
FROM gcr.io/distroless/base-debian13:nonroot AS runtime

COPY --from=build /out/kinakomate /usr/bin/kinakomate
COPY --from=postgres-client /staging/usr/bin/pg_dump /usr/bin/pg_dump
COPY --from=postgres-client /staging/usr/bin/psql /usr/bin/psql
COPY --from=postgres-client /staging/usr/lib/x86_64-linux-gnu/ /usr/lib/x86_64-linux-gnu/

ENTRYPOINT ["/usr/bin/kinakomate"]
