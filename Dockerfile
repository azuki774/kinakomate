# syntax=docker/dockerfile:1

FROM golang:1.26.7 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/kinakomate ./cmd/kinakomate

# Stage: install the PostgreSQL client in a Debian build base so we can copy
# the psql binary and its shared libraries into the distroless runtime.
FROM debian:bookworm-slim AS psql
RUN apt-get update \
    && apt-get install -y --no-install-recommends postgresql-client \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

# The runner invokes psql directly (ON_ERROR_STOP + single transaction), so the
# runtime image must include psql. We start from a glibc-based distroless base
# (psql is dynamically linked) and copy psql plus its shared library
# dependencies from the Debian stage.
FROM gcr.io/distroless/base-debian12:nonroot AS runtime

COPY --from=build /out/kinakomate /usr/bin/kinakomate
COPY --from=psql /usr/bin/psql /usr/bin/psql

# Shared library dependencies of psql / libpq on Debian 12.
COPY --from=psql /usr/lib/x86_64-linux-gnu/libpq.so* /usr/lib/x86_64-linux-gnu/
COPY --from=psql /usr/lib/x86_64-linux-gnu/libssl.so* /usr/lib/x86_64-linux-gnu/
COPY --from=psql /usr/lib/x86_64-linux-gnu/libcrypto.so* /usr/lib/x86_64-linux-gnu/
COPY --from=psql /usr/lib/x86_64-linux-gnu/libz.so* /usr/lib/x86_64-linux-gnu/
COPY --from=psql /usr/lib/x86_64-linux-gnu/libreadline.so* /usr/lib/x86_64-linux-gnu/
COPY --from=psql /usr/lib/x86_64-linux-gnu/libgssapi_krb5.so* /usr/lib/x86_64-linux-gnu/
COPY --from=psql /usr/lib/x86_64-linux-gnu/libkrb5.so* /usr/lib/x86_64-linux-gnu/
COPY --from=psql /usr/lib/x86_64-linux-gnu/libk5crypto.so* /usr/lib/x86_64-linux-gnu/
COPY --from=psql /usr/lib/x86_64-linux-gnu/libcom_err.so* /usr/lib/x86_64-linux-gnu/

ENV PATH="/usr/bin:${PATH}"
ENTRYPOINT ["/usr/bin/kinakomate"]
