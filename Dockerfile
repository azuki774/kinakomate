# syntax=docker/dockerfile:1

FROM golang:1.26.7 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/kinakomate ./cmd/kinakomate

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/kinakomate /usr/bin/kinakomate
ENTRYPOINT ["/usr/bin/kinakomate"]
