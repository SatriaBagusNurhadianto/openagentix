# A2A gate. `make test` needs nothing but Go: the Kubernetes TokenReview API and the kagent controller are
# replaced by local fakes, and the module has no dependencies outside the standard library.

IMAGE ?= openagentix/a2a-gate:dev

.PHONY: build test cover vet fmt docker

build:
	CGO_ENABLED=0 go build -trimpath -o bin/a2a-gate ./cmd/a2a-gate

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
