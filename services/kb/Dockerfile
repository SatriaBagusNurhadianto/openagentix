# syntax=docker/dockerfile:1
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/kb ./cmd/kb

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/kb /kb
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/kb"]
