# Security model

## Credentials stay out of agents

An agent never holds the Anthropic API key. Each agent holds one gateway virtual key instead. The model gateway checks that key, applies a token budget per key, and adds the real provider key on its way upstream. Because every agent has its own virtual key, the gateway logs and metrics show which agent spent how many tokens.

## Tool layer identity

The vMCP accepts only requests that carry a valid Substrate actor JWT with the dedicated audience openagentix-vmcp. A request without a token gets HTTP 401. The sandbox does not call the vMCP itself: its traffic leaves through the Substrate egress proxy, which attaches the token.

## Network policy

NetworkPolicies restrict who can reach the MCP backends, the vMCP and the kagent controller. A pod in the default namespace times out when it tries to reach them. The knowledge base follows the same rule: only the vMCP pod may open its MCP endpoint.

## What is not protected

The kagent controller does not authenticate callers, so the NetworkPolicy is the only control in front of A2A. Substrate gives every session of every agent the same atespace, so tool authorization cannot tell one agent from another.

## Telegram allowlist

The bridge refuses to start unless it has a list of allowed chat ids. Messages from any other chat are ignored.
