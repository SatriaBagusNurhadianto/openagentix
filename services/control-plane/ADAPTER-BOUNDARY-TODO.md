# Adapter boundary: what still leaks (next pass)

Moving these changes the catalog schema, so it is not part of the review-fix pass. Until then the catalog and the policy
files keep their current shape.

- `internal/plan`: `Plan` carries `ModelConfigName`, `KeySecretKey`, `HarnessEnv` and `Memory.ModelConfig`, which are kagent
  and Kubernetes words. They belong in the runtime adapter's own section of the catalog, not in the platform-level plan.
- `internal/catalog`: `harnessImage`, `workerPool` and `snapshotLocation` are kagent / Agent Substrate settings at the top
  level of the catalog; they should live under an adapter-owned section (`runtime.kagent`).
- `internal/policy/policy.go`: hard-codes the tool name `execute_tool_script` and the vMCP wording of its messages; those
  are ToolHive facts and should come from the tools adapter (what a profile offers, why a tool is missing).
- `internal/keys` assumes the key lives in a Kubernetes Secret, and the gateway user equals the definition's name; both are
  adapter choices (the Secret naming with the UID is a core rule, the user id is the gateway adapter's).
