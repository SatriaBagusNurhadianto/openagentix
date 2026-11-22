#!/usr/bin/env bash
# Plain-shell tests over `helm template` output. Needs: helm, yq (jq wrapper), jq.
# Usage: charts/openagentix/tests/run.sh   (exit 1 on any failure)
set -u
CHART="$(cd "$(dirname "$0")/.." && pwd)"
PASS=0; FAIL=0

# docs <helm args...> -> JSON array of all rendered documents ("[]" if helm fails)
docs() { helm template openagentix "$CHART" -n openagentix-system "$@" 2>/dev/null | yq -c 'select(.!=null)' 2>/dev/null | jq -s . 2>/dev/null || echo '[]'; }
# render_fails <helm args...> -> prints helm's error output (empty if it rendered)
render_err() { helm template openagentix "$CHART" -n openagentix-system "$@" 2>&1 >/dev/null; }

ok()   { PASS=$((PASS+1)); printf '  ok   %s\n' "$1"; }
bad()  { FAIL=$((FAIL+1)); printf '  FAIL %s\n       expected: %s\n       actual:   %s\n' "$1" "$2" "$3"; }
eq()   { [ "$2" = "$3" ] && ok "$1" || bad "$1" "$3" "$2"; }          # eq desc actual expected
# q <docs-json> <jq filter> -> raw result
q()    { jq -r "$2" <<<"$1"; }
count(){ q "$1" "[.[] | select($2)] | length"; }                      # count docs-json 'jq select expr'

echo "# defaults"
D=$(docs)
eq "default render is non-empty"                       "$(q "$D" 'length>0')" true
eq "bridge Deployment rendered once"                   "$(count "$D" '.kind=="Deployment" and .metadata.name=="telegram-bridge"')" 1
eq "bridge lives in openagentix-gateway"               "$(q "$D" '.[]|select(.kind=="Deployment")|.metadata.namespace')" openagentix-gateway
eq "bridge keeps legacy selector app=telegram-bridge"  "$(q "$D" '.[]|select(.kind=="Deployment")|.spec.selector.matchLabels.app')" telegram-bridge
eq "bridge strategy Recreate"                          "$(q "$D" '.[]|select(.kind=="Deployment")|.spec.strategy.type')" Recreate
eq "bridge image"                                      "$(q "$D" '.[]|select(.kind=="Deployment")|.spec.template.spec.containers[0].image')" openagentix/telegram-bridge:dev
eq "bridge A2A_URL derived from bridge.agent"          "$(q "$D" '.[]|select(.kind=="Deployment")|.spec.template.spec.containers[0].env[]|select(.name=="A2A_URL")|.value')" http://kagent-controller.kagent.svc.cluster.local:8083/agents/kagent/assistant
eq "bridge token comes from secretKeyRef"              "$(q "$D" '.[]|select(.kind=="Deployment")|.spec.template.spec.containers[0].env[]|select(.name=="TELEGRAM_BOT_TOKEN")|.valueFrom.secretKeyRef.name')" telegram-bridge
eq "bridge token env has no literal value"             "$(q "$D" '.[]|select(.kind=="Deployment")|.spec.template.spec.containers[0].env[]|select(.name=="TELEGRAM_BOT_TOKEN")|has("value")')" false
eq "allowlist env present by default, optional"        "$(q "$D" '.[]|select(.kind=="Deployment")|.spec.template.spec.containers[0].env[]|select(.name=="TELEGRAM_ALLOWED_CHAT_IDS")|.valueFrom.secretKeyRef.optional')" true
eq "bot is not open to everyone by default"            "$(q "$D" '[.[]|select(.kind=="Deployment")|.spec.template.spec.containers[0].env[]|select(.name=="TELEGRAM_ALLOW_ALL")]|length')" 0
eq "bridge liveness probe hits /healthz"               "$(q "$D" '.[]|select(.kind=="Deployment")|.spec.template.spec.containers[0].livenessProbe.httpGet|.path+":"+(.port|tostring)')" /healthz:health
eq "bridge exposes health port 8080"                   "$(q "$D" '.[]|select(.kind=="Deployment")|.spec.template.spec.containers[0].ports[]|select(.name=="health")|.containerPort')" 8080

echo "# bridge toggles"
D=$(docs --set bridge.enabled=false)
eq "bridge.enabled=false renders no Deployment"        "$(count "$D" '.kind=="Deployment"')" 0
D=$(docs --set bridge.allowlist.enabled=false)
eq "allowlist.enabled=false drops the env var"         "$(q "$D" '[.[]|select(.kind=="Deployment")|.spec.template.spec.containers[0].env[]|select(.name=="TELEGRAM_ALLOWED_CHAT_IDS")]|length')" 0
eq "allowlist.enabled=false opens the bot explicitly"  "$(q "$D" '.[]|select(.kind=="Deployment")|.spec.template.spec.containers[0].env[]|select(.name=="TELEGRAM_ALLOW_ALL")|.value')" true
D=$(docs --set bridge.existingSecret=my-secret --set bridge.tokenKey=TOK --set bridge.a2aUrl=http://x/y)
eq "bridge.existingSecret honoured"                    "$(q "$D" '.[]|select(.kind=="Deployment")|.spec.template.spec.containers[0].env[]|select(.name=="TELEGRAM_BOT_TOKEN")|.valueFrom.secretKeyRef|.name+"/"+.key')" my-secret/TOK
eq "bridge.a2aUrl overrides derived url"               "$(q "$D" '.[]|select(.kind=="Deployment")|.spec.template.spec.containers[0].env[]|select(.name=="A2A_URL")|.value')" http://x/y

echo "# model gateway"
D=$(docs)
eq "one Gateway"                                       "$(count "$D" '.kind=="Gateway"')" 1
eq "Gateway class agentgateway"                        "$(q "$D" '.[]|select(.kind=="Gateway")|.spec.gatewayClassName')" agentgateway
eq "Gateway ns"                                        "$(q "$D" '.[]|select(.kind=="Gateway")|.metadata.namespace')" agentgateway-system
eq "listener allows AgentgatewayModel"                 "$(q "$D" '.[]|select(.kind=="Gateway")|[.spec.listeners[0].allowedRoutes.kinds[].kind]|index("AgentgatewayModel")!=null')" true
eq "default model claude-haiku"                        "$(q "$D" '.[]|select(.kind=="AgentgatewayModel")|.metadata.name')" claude-haiku
eq "claude-haiku provider Anthropic"                   "$(q "$D" '.[]|select(.kind=="AgentgatewayModel")|.spec.provider')" Anthropic
eq "claude-haiku auth secret"                          "$(q "$D" '.[]|select(.kind=="AgentgatewayModel")|.spec.policies.auth.secretRef.name')" anthropic-api-key
eq "claude-haiku upstream model rewrite"               "$(q "$D" '.[]|select(.kind=="AgentgatewayModel")|.spec.policies.transformations[0].expression')" '"claude-haiku-5-5"'
eq "model parentRef is the gateway"                    "$(q "$D" '.[]|select(.kind=="AgentgatewayModel")|.spec.parentRefs[0].name')" model-gateway
eq "exactly ONE AgentgatewayPolicy"                    "$(count "$D" '.kind=="AgentgatewayPolicy"')" 1
eq "policy: strict api-key auth"                       "$(q "$D" '.[]|select(.kind=="AgentgatewayPolicy")|.spec.traffic.apiKeyAuthentication.mode')" Strict
eq "policy: per-user token budget"                     "$(q "$D" '.[]|select(.kind=="AgentgatewayPolicy")|.spec.traffic.rateLimit.local[0].key')" apiKey.user_id
eq "policy: token budget default 1000000 per hour"     "$(q "$D" '.[]|select(.kind=="AgentgatewayPolicy")|.spec.traffic.rateLimit.local[0]|"\(.tokens) \(.unit)"')" "1000000 Hours"
eq "policy: retry attempts"                            "$(q "$D" '.[]|select(.kind=="AgentgatewayPolicy")|.spec.traffic.retry.attempts')" 2
eq "policy: access log attributes user_id"             "$(q "$D" '.[]|select(.kind=="AgentgatewayPolicy")|.spec.frontend.accessLog.attributes.add[0].name')" user_id
eq "policy: metrics attribute user_id"                 "$(q "$D" '.[]|select(.kind=="AgentgatewayPolicy")|.spec.frontend.metrics.attributes.add[0].name')" user_id
D=$(docs --set gateway.enabled=false)
eq "gateway.enabled=false: no Gateway/Model/Policy"    "$(count "$D" '.kind=="Gateway" or .kind=="AgentgatewayModel" or .kind=="AgentgatewayPolicy"')" 0
cat > "$(dirname "$0")/.extra-models.yaml" <<'YAML'
gateway:
  models:
    - name: claude-haiku
      provider: Anthropic
      authSecret: anthropic-api-key
      upstreamModel: claude-haiku-5-5
    - name: gemini
      provider: Gemini
      authSecret: gemini-api-key
      upstreamModel: gemini-3.5-flash-lite
    - name: auto
      failover: [gemini, claude-haiku]
YAML
D=$(docs -f "$(dirname "$0")/.extra-models.yaml")
eq "models come from values (3 now)"                   "$(count "$D" '.kind=="AgentgatewayModel"')" 3
eq "failover model renders virtualModel targets"       "$(q "$D" '.[]|select(.kind=="AgentgatewayModel" and .metadata.name=="auto")|[.spec.virtualModel.failover.targets[]|.modelRef.name+":"+(.priority|tostring)]|join(",")')" gemini:0,claude-haiku:1
rm -f "$(dirname "$0")/.extra-models.yaml"

echo "# MCP tool layer"
D=$(docs)
eq "one MCPGroup"                                      "$(count "$D" '.kind=="MCPGroup"')" 1
eq "two MCPServers by default"                         "$(count "$D" '.kind=="MCPServer"')" 2
eq "MCPServers reference the group"                    "$(q "$D" '[.[]|select(.kind=="MCPServer")|.spec.groupRef.name]|unique|join(",")')" tools
eq "one VirtualMCPServer"                              "$(count "$D" '.kind=="VirtualMCPServer"')" 1
eq "codeMode enabled by default"                       "$(q "$D" '.[]|select(.kind=="VirtualMCPServer")|.spec.config.codeMode.enabled')" true
eq "tool name prefixing kept"                          "$(q "$D" '.[]|select(.kind=="VirtualMCPServer")|.spec.config.aggregation.conflictResolution')" prefix
D=$(docs --set mcp.virtual.codeMode.enabled=false)
eq "codeMode toggle off omits codeMode"                "$(q "$D" '.[]|select(.kind=="VirtualMCPServer")|.spec.config|has("codeMode")')" false
D=$(docs --set mcp.enabled=false)
eq "mcp.enabled=false: no ToolHive resources"          "$(count "$D" '.kind=="MCPGroup" or .kind=="MCPServer" or .kind=="VirtualMCPServer"')" 0
cat > "$(dirname "$0")/.servers.yaml" <<'YAML'
mcp:
  servers:
    - {name: fetch, image: "ghcr.io/stackloklabs/gofetch/server:2.0.1", port: 8080}
    - {name: osv, image: "ghcr.io/stackloklabs/osv-mcp/server:0.2.1", port: 8080}
    - {name: extra, image: "example.com/x:1", port: 9000}
YAML
D=$(docs -f "$(dirname "$0")/.servers.yaml"); rm -f "$(dirname "$0")/.servers.yaml"
eq "MCPServers come from a values list (3)"            "$(count "$D" '.kind=="MCPServer"')" 3
D=$(docs)
eq "RemoteMCPServer url derived from vMCP name+ns"     "$(q "$D" '.[]|select(.kind=="RemoteMCPServer")|.spec.url')" http://vmcp-tools.mcp-tools.svc.cluster.local:4483/mcp
eq "RemoteMCPServer lives in kagent"                   "$(q "$D" '.[]|select(.kind=="RemoteMCPServer")|.metadata.namespace')" kagent

echo "# kagent agents"
eq "one Agent by default"                              "$(count "$D" '.kind=="Agent"')" 1
eq "one AgentTemplate by default"                      "$(count "$D" '.kind=="AgentTemplate"')" 1
eq "one Harness by default"                            "$(count "$D" '.kind=="Harness"')" 1
eq "Agent refs template and harness"                   "$(q "$D" '.[]|select(.kind=="Agent")|.spec.template.ref.name+"/"+.spec.harnessRef.name')" assistant-template/assistant-harness
eq "template systemPrompt set"                         "$(q "$D" '.[]|select(.kind=="AgentTemplate")|.spec.systemPrompt|length>20')" true
eq "template modelConfig ref"                          "$(q "$D" '.[]|select(.kind=="AgentTemplate")|.spec.modelConfig.name')" gateway-claude-haiku
eq "mcp binding kind"                                  "$(q "$D" '.[]|select(.kind=="AgentTemplate")|.spec.tools[0].mcp.server.kind')" RemoteMCPServer
eq "mcp binding has NO apiGroup (webhook rejects it)"  "$(q "$D" '.[]|select(.kind=="AgentTemplate")|.spec.tools[0].mcp.server|has("apiGroup")')" false
eq "mcp binding server name"                           "$(q "$D" '.[]|select(.kind=="AgentTemplate")|.spec.tools[0].mcp.server.name')" toolhive-vmcp
eq "mcp binding tool allowlist (5)"                    "$(q "$D" '.[]|select(.kind=="AgentTemplate")|.spec.tools[0].mcp.tools|length')" 5
eq "harness uses substrate worker pool"                "$(q "$D" '.[]|select(.kind=="Harness")|.spec.substrate.workerPoolRef.name')" kagent-default
eq "harness image is digest pinned"                    "$(q "$D" '.[]|select(.kind=="Harness")|.spec.workload.image|test("@sha256:[0-9a-f]{64}$")')" true
eq "skill source is a digest-pinned oci ref"           "$(q "$D" '.[]|select(.kind=="AgentTemplate")|.spec.skills[0].source.oci|test("@sha256:[0-9a-f]{64}$")')" true
eq "default skill ref has no hard-coded node IP"       "$(q "$D" '.[]|select(.kind=="AgentTemplate")|.spec.skills[0].source.oci|test("^172\\.")')" false
eq "skill name"                                        "$(q "$D" '.[]|select(.kind=="AgentTemplate")|.spec.skills[0].name')" openagentix-canary
E=$(render_err --set skills.openagentix-canary.oci=registry.example/skills/x:latest)
eq "unpinned skill ref fails the render"               "$([ -n "$E" ] && echo failed || echo rendered)" failed
D=$(docs --set kagent.enabled=false)
eq "kagent.enabled=false: no kagent resources"         "$(count "$D" '.kind=="Agent" or .kind=="AgentTemplate" or .kind=="Harness" or .kind=="ModelConfig" or .kind=="RemoteMCPServer"')" 0

cat > "$(dirname "$0")/.two-agents.yaml" <<'YAML'
agents:
  - name: alpha
    model: claude-haiku
    systemPrompt: "You are alpha, a terse assistant for testing."
    tools: []
    skills: []
  - name: beta
    model: claude-haiku
    systemPrompt: "You are beta, a terse assistant for testing."
    tools:
      - tools: [fetch_fetch]
    skills: []
YAML
D=$(docs -f "$(dirname "$0")/.two-agents.yaml")
eq "2 agents -> 2 Agent"                               "$(count "$D" '.kind=="Agent"')" 2
eq "2 agents -> 2 AgentTemplate"                       "$(count "$D" '.kind=="AgentTemplate"')" 2
eq "2 agents -> 2 Harness"                             "$(count "$D" '.kind=="Harness"')" 2
eq "2 agents -> 2 ModelConfig"                         "$(count "$D" '.kind=="ModelConfig"')" 2
eq "agent without tools renders no tools key"          "$(q "$D" '.[]|select(.kind=="AgentTemplate" and .metadata.name=="alpha-template")|has("spec") and (.spec|has("tools"))')" false
eq "tools entry defaults server to the vMCP RemoteMCPServer" "$(q "$D" '.[]|select(.kind=="AgentTemplate" and .metadata.name=="beta-template")|.spec.tools[0].mcp.server.name')" toolhive-vmcp
eq "agent without skills renders no skills key"        "$(q "$D" '.[]|select(.kind=="AgentTemplate" and .metadata.name=="alpha-template")|.spec|has("skills")')" false

echo "# per-agent virtual keys"
eq "alpha ModelConfig uses alpha-virtual-key"          "$(q "$D" '.[]|select(.kind=="ModelConfig" and .metadata.name=="gateway-alpha")|.spec.apiKeySecret')" alpha-virtual-key
eq "beta ModelConfig uses beta-virtual-key"            "$(q "$D" '.[]|select(.kind=="ModelConfig" and .metadata.name=="gateway-beta")|.spec.apiKeySecret')" beta-virtual-key
eq "virtual key secret key"                            "$(q "$D" '[.[]|select(.kind=="ModelConfig")|.spec.apiKeySecretKey]|unique|join(",")')" OPENAI_API_KEY
eq "ModelConfig points at the gateway FQDN"            "$(q "$D" '.[]|select(.kind=="ModelConfig" and .metadata.name=="gateway-alpha")|.spec.openAI.baseUrl')" http://model-gateway.agentgateway-system.svc.cluster.local/v1
eq "ModelConfig model is the gateway model name"       "$(q "$D" '.[]|select(.kind=="ModelConfig" and .metadata.name=="gateway-alpha")|.spec.model')" claude-haiku
eq "ModelConfig annotated with its agent (install.sh reads it)" "$(q "$D" '[.[]|select(.kind=="ModelConfig")|.metadata.annotations["openagentix.io/agent"]]|sort|join(",")')" alpha,beta
eq "each template uses its own ModelConfig"            "$(q "$D" '[.[]|select(.kind=="AgentTemplate")|.spec.modelConfig.name]|sort|join(",")')" gateway-alpha,gateway-beta
rm -f "$(dirname "$0")/.two-agents.yaml"
E=$(render_err --set 'agents[0].model=nope')
eq "agent referencing an unknown gateway model fails"  "$([ -n "$E" ] && echo failed || echo rendered)" failed

echo "# secrets hygiene"
D=$(docs --set bridge.token=SENTINEL-TOKEN-123 --set gateway.apiKey=SENTINEL-TOKEN-123)
eq "chart renders no Secret objects"                   "$(count "$D" '.kind=="Secret"')" 0
eq "no stringData/data anywhere"                       "$(q "$D" '[.[]|select(has("data") or has("stringData"))]|length')" 0
eq "stray secret-looking values are never rendered"    "$(q "$D" 'tostring|contains("SENTINEL-TOKEN-123")')" false
eq "no Secret-valued env var has a literal value"      "$(q "$D" '[.[]|select(.kind=="Deployment")|.spec.template.spec.containers[].env[]|select((.name|test("TOKEN|KEY|CHAT_IDS")) and has("value"))]|length')" 0

echo
echo "passed=$PASS failed=$FAIL"
[ "$FAIL" -eq 0 ]
