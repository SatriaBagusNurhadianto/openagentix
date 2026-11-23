# A2A caller. `make test` needs nothing but Go.

IMAGE ?= openagentix/a2a-call:dev

.PHONY: build test cover vet fmt docker

build:
	CGO_ENABLED=0 go build -trimpath -o bin/a2a-call ./cmd/a2a-call

test:
	go test ./... -race -count=1

cover:
	go test ./... -count=1 -cover

vet:
	go vet ./...

fmt:
	@test -z "$$(gofmt -l .)" || (gofmt -l .; exit 1)

docker:
	docker build -t $(IMAGE) .
