module github.com/openagentix/openagentix/bridges/ax-adapter

go 1.27.1

// The AX checkout is not published as a stable module, so the stubs
// (github.com/google/ax/pkg/apis/v1alpha1) come from the local read-only
// checkout at commit 0b44e971. Because of this replace, the Docker image is
// built from a prebuilt binary (see Makefile and Dockerfile).
replace github.com/google/ax => /Users/gshah/work/agentix/ax

require (
	github.com/google/ax v0.0.0-00010101000000-000000000000
	google.golang.org/grpc v1.83.2
	google.golang.org/protobuf v1.36.12
)

require (
	go.ate.dev/substrate v1.0.0 // indirect
	golang.org/x/net v0.60.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260803160001-6ac0973c030d // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
