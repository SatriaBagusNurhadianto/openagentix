# Control plane. `make test` needs Go; the CRD test also uses yq (skipped when missing), and `make golden`
# needs helm, yq and jq. The Kubernetes API is replaced by a local fake (internal/kubetest), and the module has no
# dependencies outside the standard library.

IMAGE ?= openagentix/control-plane:dev

.PHONY: build test cover vet fmt golden docker

build:
	CGO_ENABLED=0 go build -trimpath -o bin/control-plane ./cmd/control-plane

test:
	go test ./... -race -count=1

cover:
	go test ./... -count=1 -cover

vet:
	go vet ./...

fmt:
	@test -z "$$(gofmt -l .)" || (gofmt -l .; exit 1)

# Regenerate the Helm golden files the parity test compares with.
golden:
	./testdata/regen-golden.sh

docker:
	docker build -t $(IMAGE) .
