# Console. `make test` needs nothing but Go; `make web` needs Node 22 and pnpm.

IMAGE ?= openagentix/console:dev

.PHONY: build test cover vet fmt web docker demo

build:
	CGO_ENABLED=0 go build -trimpath -o bin/console ./cmd/console

test:
	go test ./... -race -count=1

cover:
	go test ./... -count=1 -cover

vet:
	go vet ./...

fmt:
	@test -z "$$(gofmt -l .)" || (gofmt -l .; exit 1)

web:
	./build-web.sh

demo: web
	CONSOLE_DEV_FAKE=1 go run ./cmd/console

docker:
	docker build -f Dockerfile -t $(IMAGE) ../..
