.PHONY: build run test vet fmt lint shell clean

build:
	nix build .

run:
	nix run .

test:
	nix develop -c go test ./...

vet:
	nix develop -c go vet ./...

fmt:
	nix develop -c gofmt -w cmd/kinakomate/main.go

lint: vet

shell:
	nix develop

clean:
	rm -f result
