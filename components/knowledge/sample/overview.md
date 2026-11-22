# OpenAgentix overview

OpenAgentix is a Kubernetes-native platform for running AI agents. It puts existing open-source pieces behind one install command.

## Components

- Agent runtime: kagent 1.0 running on Agent Substrate, with every agent inside a gVisor sandbox.
- Model gateway: agentgateway serves an OpenAI-compatible API in front of the model providers.
- Tool layer: ToolHive aggregates MCP servers behind one virtual MCP server (the vMCP) with code mode.
- Skills: Agent Skills packaged as digest-pinned OCI images and mounted into the sandbox.
- Telegram bridge: a small Go service that long-polls Telegram and forwards each message to the agent over A2A.
- Knowledge base: a document store with hybrid search that agents query through MCP tools.

## How a message travels

A person writes to the Telegram bot. The bridge calls the kagent controller with an A2A SendMessage request. The controller wakes the agent in its sandbox. The agent asks the model gateway for a completion and calls tools through the vMCP. The answer goes back the same way.

## Installation

One script, install.sh, creates the kind cluster, installs the pinned upstream releases, creates Secrets from environment variables and runs Helm. Re-running it is safe.
