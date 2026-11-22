# Decision record: kagent over Google AX as the agent runtime

## Decision

OpenAgentix uses kagent 1.0 on Agent Substrate as the runtime for chat and event-triggered agents. Google AX is not the MVP runtime.

## Why kagent

- Agents are declared as Kubernetes resources: a prompt, a model reference, a list of tools and a list of skills.
- A conversation keeps its history across turns through a context id.
- The whole thing installs from published Helm charts, which fits GitOps.

## Why not AX for now

The same agent was built on both runtimes. It worked on AX, with correct answers and no slower turns, but the agent loop, the conversation memory, the A2A front and the packaging all had to be written by hand. AX has no declarative agent kind and cannot send a message to a task that is already running.

## What stays open

The contract between the Telegram bridge and the runtime is the A2A protocol, so another backend can be added later without touching the channel code. AX remains a candidate for sandboxed batch and coding-agent work.
