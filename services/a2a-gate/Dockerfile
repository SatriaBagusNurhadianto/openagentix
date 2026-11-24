# syntax=docker/dockerfile:1
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod ./
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/a2a-gate ./cmd/a2a-gate

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/a2a-gate /a2a-gate
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/a2a-gate"]
