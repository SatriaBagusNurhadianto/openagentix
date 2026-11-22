# syntax=docker/dockerfile:1
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/bridge ./cmd/bridge

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/bridge /bridge
USER nonroot:nonroot
ENTRYPOINT ["/bridge"]
