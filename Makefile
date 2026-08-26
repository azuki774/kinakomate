.PHONY: build test vet lint docker-build docker-push clean

BINARY := kinakomate
IMAGE_REPO := ghcr.io/azuki774/kinakomate
TAG := $(shell git rev-parse --short HEAD)

build:
	CGO_ENABLED=0 go build -trimpath -o bin/$(BINARY) ./cmd/kinakomate

test:
	go test ./...

vet:
	go vet ./...

lint:
	golangci-lint run ./...

docker-build:
	docker build -t $(IMAGE_REPO):$(TAG) .

docker-push: docker-build
	docker push $(IMAGE_REPO):$(TAG)

clean:
	rm -rf bin
