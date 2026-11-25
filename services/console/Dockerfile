# syntax=docker/dockerfile:1
# Build context is the repository root: docker build -f services/console/Dockerfile .
FROM node:22 AS web
RUN corepack enable
WORKDIR /src/console
COPY console/package.json console/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY console/ ./
RUN pnpm build

FROM golang:1.25 AS build
WORKDIR /src
COPY services/console/ ./
COPY --from=web /src/console/build ./web/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/console ./cmd/console

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/console /console
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/console"]
