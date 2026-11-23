# Notify service. `make test` needs nothing but Go: every platform (Telegram,
# Slack, webhook) is tested against local httptest fakes, never the real APIs.

IMAGE ?= openagentix/notify:dev

.PHONY: build test cover vet fmt docker

build:
	CGO_ENABLED=0 go build -trimpath -o bin/notify ./cmd/notify

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
