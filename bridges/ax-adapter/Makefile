# The module uses a local `replace` for github.com/google/ax, so the image is
# built from a prebuilt binary rather than inside Docker.
IMAGE ?= openagentix/ax-adapter:dev

.PHONY: test build image
test:
	go vet ./... && go test ./... -race -count=1

build:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o bin/ax-adapter ./cmd/ax-adapter

image: build
	docker build --platform linux/arm64 -t $(IMAGE) .
