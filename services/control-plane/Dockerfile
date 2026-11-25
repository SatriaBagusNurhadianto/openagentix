# syntax=docker/dockerfile:1
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod ./
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/control-plane ./cmd/control-plane

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/control-plane /control-plane
USER nonroot:nonroot
EXPOSE 8081
ENTRYPOINT ["/control-plane"]
