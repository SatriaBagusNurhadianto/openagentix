# Build the binary first: `make build` (CGO off, linux/arm64). It is not built
# in Docker because go.mod replaces github.com/google/ax with a local checkout.
FROM gcr.io/distroless/static:nonroot
COPY bin/ax-adapter /ax-adapter
USER nonroot:nonroot
ENTRYPOINT ["/ax-adapter"]
