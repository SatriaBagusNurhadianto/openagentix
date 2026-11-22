# Knowledge base service. `make test` needs nothing but Go. `make test-integration`
# starts throwaway PostgreSQL containers (pgvector, plus a plain one for the
# "pgvector missing" check), runs the PostgreSQL tests against them, and always
# stops them again (they are started with --rm, so stopping removes them).

IMAGE       ?= openagentix/kb:dev
PG_IMAGE    ?= pgvector/pgvector:pg17
PLAIN_IMAGE ?= postgres:17-alpine
PG_NAME     ?= kb-test-pg
PLAIN_NAME  ?= kb-test-pg-novec

.PHONY: build test test-integration cover vet fmt docker

build:
	CGO_ENABLED=0 go build -trimpath -o bin/kb ./cmd/kb

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

test-integration:
	@set -eu; \
	cleanup() { docker stop $(PG_NAME) $(PLAIN_NAME) >/dev/null 2>&1 || true; }; \
	trap cleanup EXIT INT TERM; \
	cleanup; \
	freeport() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])'; }; \
	P1=$$(freeport); P2=$$(freeport); \
	docker run -d --rm --name $(PG_NAME) -e POSTGRES_PASSWORD=kbtest -e POSTGRES_DB=kbtest -p 127.0.0.1:$$P1:5432 $(PG_IMAGE) >/dev/null; \
	docker run -d --rm --name $(PLAIN_NAME) -e POSTGRES_PASSWORD=kbtest -e POSTGRES_DB=kbtest -p 127.0.0.1:$$P2:5432 $(PLAIN_IMAGE) >/dev/null; \
	for n in $(PG_NAME) $(PLAIN_NAME); do \
	  i=0; until docker exec $$n psql -U postgres -d kbtest -tAc 'select 1' >/dev/null 2>&1; do \
	    i=$$((i+1)); [ $$i -lt 120 ] || { echo "$$n did not become ready" >&2; exit 1; }; sleep 1; done; \
	done; \
	KB_TEST_DATABASE_URL="postgres://postgres:kbtest@127.0.0.1:$$P1/kbtest?sslmode=disable" \
	KB_TEST_DATABASE_URL_NOVECTOR="postgres://postgres:kbtest@127.0.0.1:$$P2/kbtest?sslmode=disable" \
	go test ./internal/store/postgres/... ./cmd/kb/... -race -count=1 -v
