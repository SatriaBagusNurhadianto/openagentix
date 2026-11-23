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

# the default agents list with one yq edit applied (lists are replaced, not merged, by helm --set)
with_agents() { local e=$1; shift; yq -y "{agents: (.agents|$e)}" "$CHART/values.yaml" > "$(dirname "$0")/.agents-variant.yaml"; docs -f "$(dirname "$0")/.agents-variant.yaml" "$@"; rm -f "$(dirname "$0")/.agents-variant.yaml"; }
echo "# defaults"
D=$(docs)
eq "default render is non-empty"                       "$(q "$D" 'length>0')" true
eq "bridge Deployment rendered once"                   "$(count "$D" '.kind=="Deployment" and .metadata.name=="telegram-bridge"')" 1
eq "bridge lives in openagentix-gateway"               "$(q "$D" '.[]|select(.kind=="Deployment" and .metadata.name=="telegram-bridge")|.metadata.namespace')" openagentix-gateway
eq "bridge keeps legacy selector app=telegram-bridge"  "$(q "$D" '.[]|select(.kind=="Deployment" and .metadata.name=="telegram-bridge")|.spec.selector.matchLabels.app')" telegram-bridge
eq "bridge strategy Recreate"                          "$(q "$D" '.[]|select(.kind=="Deployment" and .metadata.name=="telegram-bridge")|.spec.strategy.type')" Recreate
eq "bridge image"                                      "$(q "$D" '.[]|select(.kind=="Deployment" and .metadata.name=="telegram-bridge")|.spec.template.spec.containers[0].image')" openagentix/telegram-bridge:dev
eq "bridge A2A_URL derived from bridge.agent"          "$(q "$D" '.[]|select(.kind=="Deployment" and .metadata.name=="telegram-bridge")|.spec.template.spec.containers[0].env[]|select(.name=="A2A_URL")|.value')" http://kagent-controller.kagent.svc.cluster.local:8083/agents/kagent/assistant
eq "bridge token comes from secretKeyRef"              "$(q "$D" '.[]|select(.kind=="Deployment" and .metadata.name=="telegram-bridge")|.spec.template.spec.containers[0].env[]|select(.name=="TELEGRAM_BOT_TOKEN")|.valueFrom.secretKeyRef.name')" telegram-bridge
eq "bridge token env has no literal value"             "$(q "$D" '.[]|select(.kind=="Deployment" and .metadata.name=="telegram-bridge")|.spec.template.spec.containers[0].env[]|select(.name=="TELEGRAM_BOT_TOKEN")|has("value")')" false
eq "allowlist env present by default, optional"        "$(q "$D" '.[]|select(.kind=="Deployment" and .metadata.name=="telegram-bridge")|.spec.template.spec.containers[0].env[]|select(.name=="TELEGRAM_ALLOWED_CHAT_IDS")|.valueFrom.secretKeyRef.optional')" true
eq "bot is not open to everyone by default"            "$(q "$D" '[.[]|select(.kind=="Deployment" and .metadata.name=="telegram-bridge")|.spec.template.spec.containers[0].env[]|select(.name=="TELEGRAM_ALLOW_ALL")]|length')" 0
eq "bridge liveness probe hits /healthz"               "$(q "$D" '.[]|select(.kind=="Deployment" and .metadata.name=="telegram-bridge")|.spec.template.spec.containers[0].livenessProbe.httpGet|.path+":"+(.port|tostring)')" /healthz:health
eq "bridge exposes health port 8080"                   "$(q "$D" '.[]|select(.kind=="Deployment" and .metadata.name=="telegram-bridge")|.spec.template.spec.containers[0].ports[]|select(.name=="health")|.containerPort')" 8080

echo "# bridge toggles"
D=$(docs --set bridge.enabled=false)
eq "bridge.enabled=false renders no bridge Deployment" "$(count "$D" '.kind=="Deployment" and .metadata.name=="telegram-bridge"')" 0
D=$(docs --set bridge.allowlist.enabled=false)
eq "allowlist.enabled=false drops the env var"         "$(q "$D" '[.[]|select(.kind=="Deployment" and .metadata.name=="telegram-bridge")|.spec.template.spec.containers[0].env[]|select(.name=="TELEGRAM_ALLOWED_CHAT_IDS")]|length')" 0
eq "allowlist.enabled=false opens the bot explicitly"  "$(q "$D" '.[]|select(.kind=="Deployment" and .metadata.name=="telegram-bridge")|.spec.template.spec.containers[0].env[]|select(.name=="TELEGRAM_ALLOW_ALL")|.value')" true
D=$(docs --set bridge.existingSecret=my-secret --set bridge.tokenKey=TOK --set bridge.a2aUrl=http://x/y)
eq "bridge.existingSecret honoured"                    "$(q "$D" '.[]|select(.kind=="Deployment" and .metadata.name=="telegram-bridge")|.spec.template.spec.containers[0].env[]|select(.name=="TELEGRAM_BOT_TOKEN")|.valueFrom.secretKeyRef|.name+"/"+.key')" my-secret/TOK
eq "bridge.a2aUrl overrides derived url"               "$(q "$D" '.[]|select(.kind=="Deployment")|.spec.template.spec.containers[0].env[]|select(.name=="A2A_URL")|.value')" http://x/y

echo "# model gateway"
D=$(docs)
eq "one Gateway"                                       "$(count "$D" '.kind=="Gateway"')" 1
eq "Gateway class agentgateway"                        "$(q "$D" '.[]|select(.kind=="Gateway")|.spec.gatewayClassName')" agentgateway
eq "Gateway ns"                                        "$(q "$D" '.[]|select(.kind=="Gateway")|.metadata.namespace')" agentgateway-system
eq "listener allows AgentgatewayModel"                 "$(q "$D" '.[]|select(.kind=="Gateway")|[.spec.listeners[0].allowedRoutes.kinds[].kind]|index("AgentgatewayModel")!=null')" true
eq "default model claude-haiku"                        "$(q "$D" '.[]|select(.kind=="AgentgatewayModel" and .metadata.name=="claude-haiku")|.metadata.name')" claude-haiku
eq "claude-haiku provider Anthropic"                   "$(q "$D" '.[]|select(.kind=="AgentgatewayModel" and .metadata.name=="claude-haiku")|.spec.provider')" Anthropic
eq "claude-haiku auth secret"                          "$(q "$D" '.[]|select(.kind=="AgentgatewayModel" and .metadata.name=="claude-haiku")|.spec.policies.auth.secretRef.name')" anthropic-api-key
eq "claude-haiku upstream model rewrite"               "$(q "$D" '.[]|select(.kind=="AgentgatewayModel" and .metadata.name=="claude-haiku")|.spec.policies.transformations[0].expression')" '"claude-haiku-5-5"'
eq "model parentRef is the gateway"                    "$(q "$D" '.[]|select(.kind=="AgentgatewayModel" and .metadata.name=="claude-haiku")|.spec.parentRefs[0].name')" model-gateway
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
eq "models come from values (3 chat models + embed-default + embed-memory)" "$(count "$D" '.kind=="AgentgatewayModel"')" 5
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
eq "mcp binding tool allowlist (5 + 3 kb tools)"                    "$(q "$D" '.[]|select(.kind=="AgentTemplate")|.spec.tools[0].mcp.tools|length')" 8
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

echo "# network policies (stage 1)"
D=$(docs)
VP='.[]|select(.kind=="NetworkPolicy" and .metadata.name=="openagentix-vmcp-ingress")'
CP='.[]|select(.kind=="NetworkPolicy" and .metadata.name=="openagentix-kagent-controller")'
BP='.[]|select(.kind=="NetworkPolicy" and .metadata.name=="openagentix-mcp-tools-default")'
eq "3 platform NetworkPolicies by default (knowledge ns excluded)" "$(count "$D" '.kind=="NetworkPolicy" and .metadata.namespace!="openagentix-knowledge" and .metadata.namespace!="openagentix-memory"')" 3
eq "mcp-tools default policy lives in mcp-tools"       "$(q "$D" "$BP|.metadata.namespace")" mcp-tools
eq "mcp-tools default policy selects every pod"        "$(q "$D" "$BP|.spec.podSelector|length")" 0
eq "mcp-tools default policy is ingress-only"          "$(q "$D" "$BP|.spec.policyTypes|join(\",\")")" Ingress
eq "backends: allowed from own namespace and operator ns" "$(q "$D" "$BP|[.spec.ingress[].from[]|(.namespaceSelector.matchLabels[\"kubernetes.io/metadata.name\"]//\"same-ns\")]|sort|join(\",\")")" same-ns,toolhive-system
eq "vMCP policy selects the vMCP pod"                  "$(q "$D" "$VP|.spec.podSelector.matchLabels|\"\\(.app)/\\(.[\"toolhive-name\"])\"")" virtualmcpserver/tools
eq "vMCP policy only on the vMCP port"                 "$(q "$D" "$VP|[.spec.ingress[].ports[].port]|unique|join(\",\")")" 4483
eq "vMCP: sources are egress proxy and kagent controller" "$(q "$D" "$VP|[.spec.ingress[].from[]|.podSelector.matchLabels|to_entries[0].value]|sort|join(\",\")")" atenet-egress,controller
eq "vMCP: egress source scoped to ate-system namespace" "$(q "$D" "$VP|[.spec.ingress[].from[]|select(.podSelector.matchLabels.app==\"atenet-egress\")|.namespaceSelector.matchLabels[\"kubernetes.io/metadata.name\"]]|join(\",\")")" ate-system
eq "controller policy lives in kagent"                 "$(q "$D" "$CP|.metadata.namespace")" kagent
eq "controller policy selects the controller pod"      "$(q "$D" "$CP|.spec.podSelector.matchLabels[\"app.kubernetes.io/component\"]")" controller
eq "controller: 8083 sources are bridge pod, kagent ns, egress proxy" "$(q "$D" "$CP|[.spec.ingress[]|select(any(.ports[]?;.port==8083))|.from[]|(.podSelector.matchLabels|if .==null then \"any-pod\" else to_entries[0].value end)]|sort|join(\",\")")" any-pod,atenet-egress,telegram-bridge
eq "controller: bridge source is pod-scoped (ns AND pod label in one entry)" "$(q "$D" "$CP|[.spec.ingress[].from[]?|select(.podSelector.matchLabels.app==\"telegram-bridge\")|.namespaceSelector.matchLabels[\"kubernetes.io/metadata.name\"]]|join(\",\")")" openagentix-gateway
eq "controller: ports other than 8083 stay open"       "$(q "$D" "$CP|[.spec.ingress[]|select(has(\"from\")|not)|.ports[]|(.port|tostring)+\"-\"+(.endPort|tostring)]|sort|join(\",\")")" 1-8082,8084-65535
D=$(docs --set networkPolicy.enabled=false)
eq "networkPolicy.enabled=false renders none (also in the knowledge ns)" "$(count "$D" '.kind=="NetworkPolicy"')" 0
D=$(docs --set mcp.enabled=false)
eq "mcp.enabled=false: only the controller policy"     "$(q "$D" '[.[]|select(.kind=="NetworkPolicy" and .metadata.namespace!="openagentix-knowledge" and .metadata.namespace!="openagentix-memory")|.metadata.name]|join(",")')" openagentix-kagent-controller
D=$(docs --set bridge.enabled=false)
eq "bridge.enabled=false drops the bridge source"      "$(q "$D" "$CP|[.spec.ingress[].from[]?|select(.podSelector.matchLabels.app==\"telegram-bridge\")]|length")" 0
D=$(docs --set kagent.enabled=false)
eq "kagent.enabled=false: no controller policy, vMCP policy keeps ns sources" "$(q "$D" '[.[]|select(.kind=="NetworkPolicy" and .metadata.namespace!="openagentix-knowledge" and .metadata.namespace!="openagentix-memory")|.metadata.name]|sort|join(",")')" openagentix-mcp-tools-default,openagentix-vmcp-ingress

echo "# tool-layer identity (stage 2)"
D=$(docs)
V='.[]|select(.kind=="VirtualMCPServer")'
O='.[]|select(.kind=="MCPOIDCConfig")'
eq "vMCP incoming auth is oidc by default"             "$(q "$D" "$V|.spec.incomingAuth.type")" oidc
eq "vMCP references the MCPOIDCConfig"                 "$(q "$D" "$V|.spec.incomingAuth.oidcConfigRef.name")" substrate-actor-jwt
eq "vMCP expects the dedicated audience"               "$(q "$D" "$V|.spec.incomingAuth.oidcConfigRef.audience")" openagentix-vmcp
eq "Cedar policy is still rendered for authenticated callers" "$(q "$D" "$V|.spec.incomingAuth.authzConfig.inline.policies|length")" 1
eq "one MCPOIDCConfig in mcp-tools"                    "$(q "$D" "[$O|select(.metadata.namespace==\"mcp-tools\")]|length")" 1
eq "MCPOIDCConfig is inline"                           "$(q "$D" "$O|.spec.type")" inline
eq "issuer is the Substrate IdP"                       "$(q "$D" "$O|.spec.inline.issuer")" https://idp.ate-system.svc
eq "explicit jwksUrl (no reliance on discovery)"       "$(q "$D" "$O|.spec.inline.jwksUrl")" https://idp.ate-system.svc/openid/v1/jwks
eq "CA bundle from a ConfigMap"                        "$(q "$D" "$O|.spec.inline.caBundleRef.configMapRef|.name+\"/\"+.key")" substrate-idp-ca/ca.crt
eq "jwksAllowPrivateIP is on by default (the in-cluster IdP is a ClusterIP)" "$(q "$D" "$O|.spec.inline.jwksAllowPrivateIP")" true
eq "no insecure flags on the OIDC config"              "$(q "$D" "$O|.spec.inline|has(\"insecureAllowHTTP\")")" false
eq "RemoteMCPServer asks for an actor JWT with the audience" "$(q "$D" '.[]|select(.kind=="RemoteMCPServer")|.spec.actorJwt.audiences|join(",")')" openagentix-vmcp
eq "actor JWT lifetime is set"                         "$(q "$D" '.[]|select(.kind=="RemoteMCPServer")|.spec.actorJwt.expirationSeconds')" 3600
D=$(docs --set mcp.virtual.incomingAuth.type=anonymous)
eq "anonymous: no MCPOIDCConfig"                       "$(count "$D" '.kind=="MCPOIDCConfig"')" 0
eq "anonymous: no oidcConfigRef"                       "$(q "$D" "$V|.spec.incomingAuth|has(\"oidcConfigRef\")")" false
eq "anonymous: no actorJwt on the RemoteMCPServer"     "$(q "$D" '.[]|select(.kind=="RemoteMCPServer")|.spec|has("actorJwt")')" false
E=$(render_err --set mcp.virtual.incomingAuth.type=bogus)
eq "unknown incomingAuth.type fails the render"        "$([ -n "$E" ] && echo failed || echo rendered)" failed
D=$(docs --set mcp.virtual.incomingAuth.oidc.audience=other-aud --set mcp.virtual.incomingAuth.oidc.jwksAllowPrivateIP=false)
eq "audience value is shared by vMCP and RemoteMCPServer" "$(q "$D" "[($V|.spec.incomingAuth.oidcConfigRef.audience), (.[]|select(.kind==\"RemoteMCPServer\")|.spec.actorJwt.audiences[0])]|unique|join(\",\")")" other-aud
eq "jwksAllowPrivateIP follows values (false omits it)" "$(q "$D" "$O|.spec.inline|has(\"jwksAllowPrivateIP\")")" false

echo "# tool authorization keyed on the actor identity"
D=$(docs)
eq "default: Cedar stays permit-all (one policy)"      "$(q "$D" "$V|.spec.incomingAuth.authzConfig.inline.policies|join(\"|\")")" 'permit(principal, action, resource);'
cat > "$(dirname "$0")/.toolaccess.yaml" <<'YAML'
mcp:
  virtual:
    incomingAuth:
      toolAccess:
        - atespace: kagent
          tools: [fetch_fetch, osv_get_vulnerability]
        - atespace: other
          tools: [fetch_fetch]
YAML
D=$(docs -f "$(dirname "$0")/.toolaccess.yaml"); rm -f "$(dirname "$0")/.toolaccess.yaml"
eq "toolAccess renders one permit per atespace+tool (3)" "$(q "$D" "$V|.spec.incomingAuth.authzConfig.inline.policies|length")" 3
eq "toolAccess replaces the permit-all policy"         "$(q "$D" "$V|[.spec.incomingAuth.authzConfig.inline.policies[]|select(.==\"permit(principal, action, resource);\")]|length")" 0
eq "permit is keyed on the atespace claim and the tool" "$(q "$D" "$V|.spec.incomingAuth.authzConfig.inline.policies[0]")" 'permit(principal, action == Action::"call_tool", resource == Tool::"fetch_fetch") when { context["claim_actor.ate.dev"].atespace == "kagent" };'
eq "second atespace is rendered too"                   "$(q "$D" "$V|.spec.incomingAuth.authzConfig.inline.policies[2]|test(\"atespace == \\\"other\\\"\")")" true
E=$(render_err --set mcp.virtual.incomingAuth.type=anonymous --set 'mcp.virtual.incomingAuth.toolAccess[0].atespace=x' --set 'mcp.virtual.incomingAuth.toolAccess[0].tools[0]=fetch_fetch')
eq "toolAccess without oidc fails (anonymous has no identity)" "$([ -n "$E" ] && echo failed || echo rendered)" failed

echo "# knowledge base (kb + pgvector + local embeddings)"
D=$(docs)
KNS=openagentix-knowledge
inns() { count "$1" ".metadata.namespace==\"$KNS\" and ($2)"; }
KB='.[]|select(.kind=="Deployment" and .metadata.name=="kb")'
KC="$KB|.spec.template.spec.containers[0]"
KE="$KC|.env[]"
eq "knowledge block renders into its own namespace"    "$(q "$D" '[.[]|select(.metadata.namespace=="openagentix-knowledge")|.kind]|sort|join(",")')" Deployment,Deployment,NetworkPolicy,NetworkPolicy,NetworkPolicy,PersistentVolumeClaim,Service,Service,Service,StatefulSet
eq "kb Deployment, kb image"                           "$(q "$D" "$KC|.image")" openagentix/kb:dev
eq "kb uses the postgres store"                        "$(q "$D" "$KE|select(.name==\"KB_STORE\")|.value")" postgres
eq "kb database URL comes from a Secret"               "$(q "$D" "$KE|select(.name==\"KB_DATABASE_URL\")|.valueFrom.secretKeyRef|.name+\"/\"+.key")" kb-database/KB_DATABASE_URL
eq "kb database URL has no literal value"              "$(q "$D" "$KE|select(.name==\"KB_DATABASE_URL\")|has(\"value\")")" false
eq "kb admin token comes from a Secret"                "$(q "$D" "$KE|select(.name==\"KB_ADMIN_TOKEN\")|.valueFrom.secretKeyRef|.name+\"/\"+.key")" kb-admin/KB_ADMIN_TOKEN
eq "kb admin token is NOT optional (no silent open/closed API)" "$(q "$D" "$KE|select(.name==\"KB_ADMIN_TOKEN\")|.valueFrom.secretKeyRef.optional // false")" false
eq "kb embeds through the model gateway"               "$(q "$D" "$KE|select(.name==\"KB_EMBEDDINGS_URL\")|.value")" http://model-gateway.agentgateway-system.svc.cluster.local/v1
eq "kb embeddings model is the gateway alias"          "$(q "$D" "$KE|select(.name==\"KB_EMBEDDINGS_MODEL\")|.value")" embed-default
eq "kb embeddings dimension"                           "$(q "$D" "$KE|select(.name==\"KB_EMBEDDINGS_DIM\")|.value")" 384
eq "kb embeddings key is its own virtual key (Secret)" "$(q "$D" "$KE|select(.name==\"KB_EMBEDDINGS_API_KEY\")|.valueFrom.secretKeyRef|.name+\"/\"+.key")" kb-virtual-key/OPENAI_API_KEY
eq "kb pod is annotated for install.sh virtual key creation" "$(q "$D" "$KB|.metadata.annotations[\"openagentix.io/gateway-user\"]+\"/\"+.metadata.annotations[\"openagentix.io/gateway-key-secret\"]")" kb/kb-virtual-key
eq "kb runs non-root, read-only rootfs, no capabilities" "$(q "$D" "$KB|.spec.template.spec|[.securityContext.runAsNonRoot,.containers[0].securityContext.readOnlyRootFilesystem,.containers[0].securityContext.allowPrivilegeEscalation,(.containers[0].securityContext.capabilities.drop|join(\",\"))]|map(tostring)|join(\" \")")" "true true false ALL"
eq "kb probes: /healthz liveness, /readyz readiness"   "$(q "$D" "$KC|[.livenessProbe.httpGet.path,.readinessProbe.httpGet.path]|join(\",\")")" /healthz,/readyz
eq "kb has requests and limits"                        "$(q "$D" "$KC|.resources|(.requests.memory!=null) and (.limits.memory!=null) and (.requests.cpu!=null)")" true
eq "kb waits for its postgres in an initContainer (no crash-restart on first start)" "$(q "$D" "$KB|.spec.template.spec.initContainers|map(.name)|join(\",\")")" wait-for-postgres
eq "kb chunk size is tuned for the small embedding model (600 / overlap 100)" "$(q "$D" "[$KE|select(.name==\"KB_CHUNK_MAX_CHARS\" or .name==\"KB_CHUNK_OVERLAP\")|.value]|join(\"/\")")" 600/100
eq "kb pod template has no source-hash annotation by default" "$(q "$D" "$KB|.spec.template.metadata.annotations|has(\"openagentix.io/source-hash\")")" false
D3=$(docs --set-string knowledge.kb.sourceHash=abc123)
eq "kb sourceHash becomes a pod annotation (rolls the pod when the code changes)" "$(q "$D3" "$KB|.spec.template.metadata.annotations[\"openagentix.io/source-hash\"]")" abc123
eq "kb Service exposes 8080 only"                      "$(q "$D" '.[]|select(.kind=="Service" and .metadata.name=="kb")|[.spec.ports[].port]|join(",")')" 8080
PG='.[]|select(.kind=="StatefulSet" and .metadata.name=="kb-postgres")'
PC="$PG|.spec.template.spec.containers[0]"
eq "postgres image pinned by tag and digest"           "$(q "$D" "$PC|.image|test(\"^pgvector/pgvector:pg17@sha256:[0-9a-f]{64}\$\")")" true
eq "postgres password from a Secret, no literal"       "$(q "$D" "$PC|.env[]|select(.name==\"POSTGRES_PASSWORD\")|(.valueFrom.secretKeyRef.name+\"/\"+.valueFrom.secretKeyRef.key)+\" literal=\"+(has(\"value\")|tostring)")" "kb-postgres/POSTGRES_PASSWORD literal=false"
eq "postgres has a volumeClaimTemplate"                "$(q "$D" "$PG|.spec.volumeClaimTemplates|length")" 1
eq "postgres non-root with fsGroup"                    "$(q "$D" "$PG|.spec.template.spec.securityContext|(.runAsNonRoot|tostring)+\" \"+(.fsGroup|tostring)")" "true 999"
eq "postgres drops all capabilities"                   "$(q "$D" "$PC|.securityContext.capabilities.drop|join(\",\")")" ALL
eq "postgres readiness uses pg_isready"                "$(q "$D" "$PC|.readinessProbe.exec.command|join(\" \")|test(\"pg_isready\")")" true
eq "postgres has requests and limits"                  "$(q "$D" "$PC|.resources|(.requests.memory!=null) and (.limits.memory!=null)")" true
eq "postgres Service on 5432"                          "$(q "$D" '.[]|select(.kind=="Service" and .metadata.name=="kb-postgres")|.spec.ports[0].port')" 5432
EM='.[]|select(.kind=="Deployment" and .metadata.name=="kb-embeddings")'
EC="$EM|.spec.template.spec.containers[0]"
eq "embedding server image pinned by tag and digest"   "$(q "$D" "$EC|.image|test(\"^ghcr.io/ggml-org/llama.cpp:server-b[0-9]+@sha256:[0-9a-f]{64}\$\")")" true
eq "embedding server serves the default model (bge-small)" "$(q "$D" "$EC|.args|join(\" \")|test(\"bge-small-en-v1.5\")")" true
eq "embedding server runs in embeddings mode with cls pooling" "$(q "$D" "$EC|.args|join(\" \")|test(\"--embeddings\") and test(\"--pooling cls\")")" true
eq "embedding readiness is /health (200 only once the model is loaded)" "$(q "$D" "$EC|.readinessProbe.httpGet.path")" /health
eq "embedding server has a startup probe (model download)" "$(q "$D" "$EC|.startupProbe.httpGet.path")" /health
eq "embedding model cache is a PVC"                    "$(q "$D" "$EM|.spec.template.spec.volumes[]|select(.name==\"models\")|.persistentVolumeClaim.claimName")" kb-embeddings-models
eq "embedding server non-root, no capabilities"        "$(q "$D" "$EM|.spec.template.spec|[.securityContext.runAsNonRoot,(.containers[0].securityContext.capabilities.drop|join(\",\"))]|map(tostring)|join(\" \")")" "true ALL"
eq "embedding server has requests and limits"          "$(q "$D" "$EC|.resources|(.requests.memory!=null) and (.limits.memory!=null)")" true
eq "embedding strategy Recreate (RWO volume)"          "$(q "$D" "$EM|.spec.strategy.type")" Recreate
EG='.[]|select(.kind=="AgentgatewayModel" and .metadata.name=="embed-default")'
eq "gateway model embed-default exists in the gateway ns" "$(q "$D" "$EG|.metadata.namespace")" agentgateway-system
eq "embed-default is an OpenAI-compatible provider at the embedding Service" "$(q "$D" "$EG|.spec.provider+\" \"+.spec.baseURL")" "OpenAI http://kb-embeddings.openagentix-knowledge.svc.cluster.local:8080/v1"
eq "embed-default attaches to the model gateway"       "$(q "$D" "$EG|.spec.parentRefs[0].name")" model-gateway
eq "embed-default carries no provider credential"      "$(q "$D" "$EG|.spec|has(\"policies\")")" false
eq "still exactly ONE AgentgatewayPolicy"              "$(count "$D" '.kind=="AgentgatewayPolicy"')" 1
ME='.[]|select(.kind=="MCPServerEntry")'
eq "kb is one more backend of the vMCP group"          "$(q "$D" "$ME|.spec.groupRef.name+\" \"+.metadata.namespace")" "tools mcp-tools"
eq "MCPServerEntry points at the kb /mcp endpoint"     "$(q "$D" "$ME|.spec.remoteUrl")" http://kb.openagentix-knowledge.svc.cluster.local:8080/mcp
eq "MCPServerEntry transport streamable-http"          "$(q "$D" "$ME|.spec.transport")" streamable-http
eq "MCPServerEntry allows the in-cluster endpoint"     "$(q "$D" "$ME|.spec.allowPrivateEndpoint")" true
eq "MCPServerEntry references an unauthenticated outgoing config" "$(q "$D" "[$ME|.spec.externalAuthConfigRef.name]|join(\"\")")" "$(q "$D" '.[]|select(.kind=="MCPExternalAuthConfig")|.metadata.name')"
eq "outgoing auth config type is unauthenticated"      "$(q "$D" '.[]|select(.kind=="MCPExternalAuthConfig")|.spec.type')" unauthenticated
eq "MCPServers are unchanged (still 2)"                "$(count "$D" '.kind=="MCPServer"')" 2
KP='.[]|select(.kind=="NetworkPolicy" and .metadata.name=="kb-ingress")'
eq "kb policy selects the kb pod"                      "$(q "$D" "$KP|.spec.podSelector.matchLabels.app")" kb
eq "kb policy is ingress-only on 8080"                 "$(q "$D" "$KP|(.spec.policyTypes|join(\",\"))+\" \"+([.spec.ingress[].ports[].port]|unique|map(tostring)|join(\",\"))")" "Ingress 8080"
eq "kb MCP source is the vMCP pod in mcp-tools (pod-scoped)" "$(q "$D" "[$KP|.spec.ingress[].from[]|select(.podSelector.matchLabels.\"toolhive-name\"==\"tools\")|.namespaceSelector.matchLabels[\"kubernetes.io/metadata.name\"]+\"/\"+.podSelector.matchLabels.app]|join(\",\")")" mcp-tools/virtualmcpserver
eq "kb policy: no namespace-wide or any-pod source by default" "$(q "$D" "[$KP|.spec.ingress[].from[]|select((.podSelector.matchLabels==null) or (.namespaceSelector==null))]|length")" 0
eq "kb policy: exactly one source by default (ingest is by port-forward)" "$(q "$D" "$KP|[.spec.ingress[].from[]]|length")" 1
PP='.[]|select(.kind=="NetworkPolicy" and .metadata.name=="kb-postgres-ingress")'
eq "postgres accepts only the kb pod on 5432"          "$(q "$D" "$PP|([.spec.ingress[].from[]|.podSelector.matchLabels.app]|join(\",\"))+\" \"+([.spec.ingress[].ports[].port]|map(tostring)|join(\",\"))")" "kb 5432"
EP='.[]|select(.kind=="NetworkPolicy" and .metadata.name=="kb-embeddings-ingress")'
eq "embeddings accept only the model gateway proxy pod" "$(q "$D" "[$EP|.spec.ingress[].from[]|.namespaceSelector.matchLabels[\"kubernetes.io/metadata.name\"]+\"/\"+.podSelector.matchLabels[\"gateway.networking.k8s.io/gateway-name\"]]|join(\",\")")" agentgateway-system/model-gateway
eq "embeddings policy only on 8080"                    "$(q "$D" "$EP|[.spec.ingress[].ports[].port]|map(tostring)|join(\",\")")" 8080
echo "# knowledge base: agent wiring"
AT='.[]|select(.kind=="AgentTemplate" and .metadata.name=="assistant-template")'
eq "assistant gets the kb tools next to the existing ones" "$(q "$D" "$AT|.spec.tools[0].mcp.tools|length")" 8
eq "assistant kb tool names (prefix + kb tool)"        "$(q "$D" "$AT|[.spec.tools[0].mcp.tools[]|select(test(\"kb_\"))]|sort|join(\",\")")" knowledge_kb_get_document,knowledge_kb_list_collections,knowledge_kb_search
eq "assistant keeps the original tools"                "$(q "$D" "$AT|[.spec.tools[0].mcp.tools[]|select(test(\"^(execute_tool_script|fetch_fetch|osv_)\"))]|length")" 5
eq "system prompt tells it to search the knowledge base and cite titles" "$(q "$D" "$AT|.spec.systemPrompt|test(\"knowledge base\") and test(\"knowledge_kb_search\") and test(\"title\")")" true
eq "original prompt text is kept"                      "$(q "$D" "$AT|.spec.systemPrompt|test(\"say when you used one\")")" true
cat > "$(dirname "$0")/.noknow-agent.yaml" <<'YAML'
agents:
  - name: plain
    model: claude-haiku
    systemPrompt: "You are plain."
    tools: [{tools: [fetch_fetch]}]
YAML
D2=$(docs -f "$(dirname "$0")/.noknow-agent.yaml"); rm -f "$(dirname "$0")/.noknow-agent.yaml"
eq "agent without knowledge:true is not given kb tools" "$(q "$D2" '.[]|select(.kind=="AgentTemplate")|.spec.tools[0].mcp.tools|join(",")')" fetch_fetch
eq "agent without knowledge:true keeps its prompt"     "$(q "$D2" '.[]|select(.kind=="AgentTemplate")|.spec.systemPrompt')" "You are plain."
echo "# knowledge base: switches"
D=$(with_agents '.[0].memory.enabled=false' --set knowledge.enabled=false)
eq "knowledge.enabled=false renders nothing in the knowledge ns" "$(count "$D" '.metadata.namespace=="openagentix-knowledge"')" 0
eq "knowledge.enabled=false: no embed-default, no MCPServerEntry, no MCPExternalAuthConfig" "$(count "$D" '.kind=="MCPServerEntry" or .kind=="MCPExternalAuthConfig" or (.kind=="AgentgatewayModel" and .metadata.name=="embed-default")')" 0
eq "knowledge.enabled=false: assistant keeps its original 5 tools" "$(q "$D" '.[]|select(.kind=="AgentTemplate")|.spec.tools[0].mcp.tools|length')" 5
eq "knowledge.enabled=false: prompt has no knowledge text" "$(q "$D" '.[]|select(.kind=="AgentTemplate")|.spec.systemPrompt|test("knowledge")')" false
eq "knowledge.enabled=false: platform NetworkPolicies unchanged (3)" "$(count "$D" '.kind=="NetworkPolicy"')" 3
eq "knowledge.enabled=false: one model, one policy, 2 MCPServers" "$(count "$D" '.kind=="AgentgatewayModel" or .kind=="AgentgatewayPolicy" or .kind=="MCPServer"')" 4
eq "knowledge.enabled=false: no Deployment but the bridge" "$(count "$D" '.kind=="Deployment" or .kind=="StatefulSet"')" 1
D=$(docs --set mcp.enabled=false)
eq "mcp.enabled=false: no MCPServerEntry, kb still deployed" "$(q "$D" '[(.[]|select(.kind=="MCPServerEntry")),(.[]|select(.kind=="Deployment" and .metadata.name=="kb"))]|map(.kind)|join(",")')" Deployment
eq "mcp.enabled=false: agent keeps its 5 tools"      "$(q "$D" '.[]|select(.kind=="AgentTemplate")|.spec.tools[0].mcp.tools|length')" 5
eq "mcp.enabled=false: kb policy has no tool-gateway source" "$(q "$D" '[.[]|select(.kind=="NetworkPolicy" and .metadata.name=="kb-ingress")|.spec.ingress[].from[]?]|length')" 0
D=$(docs --set networkPolicy.enabled=false)
eq "networkPolicy.enabled=false: no knowledge policies" "$(inns "$D" '.kind=="NetworkPolicy"')" 0
D=$(docs --set gateway.enabled=false)
eq "gateway.enabled=false: no embed-default model"     "$(count "$D" '.kind=="AgentgatewayModel"')" 0
D=$(docs --set knowledge.embeddings.viaGateway=false)
eq "direct mode: kb calls the embedding Service"       "$(q "$D" "$KE|select(.name==\"KB_EMBEDDINGS_URL\")|.value")" http://kb-embeddings.openagentix-knowledge.svc.cluster.local:8080/v1
eq "direct mode: model is the served model name"       "$(q "$D" "$KE|select(.name==\"KB_EMBEDDINGS_MODEL\")|.value")" bge-small-en-v1.5
eq "direct mode: no API key env, no gateway model"     "$(q "$D" "[$KE|select(.name==\"KB_EMBEDDINGS_API_KEY\")]|length")/$(count "$D" '.kind=="AgentgatewayModel" and .metadata.name=="embed-default"')" 0/0
eq "direct mode: embeddings accept only the kb pod"    "$(q "$D" "[$EP|.spec.ingress[].from[]|.podSelector.matchLabels.app]|join(\",\")")" kb
eq "direct mode: kb has no virtual-key annotation"     "$(q "$D" "$KB|.metadata.annotations|has(\"openagentix.io/gateway-user\")")" false
D=$(docs --set knowledge.embeddings.enabled=false)
eq "embeddings.enabled=false: lexical-only, no embedding objects" "$(q "$D" "[$KE|select(.name|test(\"^KB_EMBEDDINGS\"))]|length")/$(inns "$D" '.metadata.name|test("embeddings")')/$(count "$D" '.kind=="AgentgatewayModel" and .metadata.name=="embed-default"')" 0/0/0
D=$(docs --set knowledge.embeddings.enabled=false --set knowledge.kb.embeddings.url=https://api.example.com/v1 --set knowledge.kb.embeddings.model=text-embedding-3-small --set knowledge.kb.embeddings.dim=1536 --set knowledge.kb.embeddings.apiKeySecret.name=hosted-key --set knowledge.kb.embeddings.apiKeySecret.key=KEY)
eq "hosted embedder: url, model, dim from values"      "$(q "$D" "[$KE|select(.name==\"KB_EMBEDDINGS_URL\" or .name==\"KB_EMBEDDINGS_MODEL\" or .name==\"KB_EMBEDDINGS_DIM\")|.value]|join(\" \")")" "https://api.example.com/v1 text-embedding-3-small 1536"
eq "hosted embedder: key from the named Secret"        "$(q "$D" "$KE|select(.name==\"KB_EMBEDDINGS_API_KEY\")|.valueFrom.secretKeyRef|.name+\"/\"+.key")" hosted-key/KEY
D=$(docs --set knowledge.postgres.enabled=false)
eq "external database: no StatefulSet/Service/policy for postgres" "$(inns "$D" '.metadata.name|test("postgres")')" 0
eq "external database: no wait-for-postgres initContainer" "$(q "$D" "$KB|.spec.template.spec|has(\"initContainers\")")" false
eq "external database: kb still reads the URL from its Secret" "$(q "$D" "$KE|select(.name==\"KB_DATABASE_URL\")|.valueFrom.secretKeyRef.name")" kb-database
D=$(docs --set 'knowledge.kb.ingestFrom[0].namespace=ci' --set 'knowledge.kb.ingestFrom[0].podLabels.app=ingest')
eq "ingestFrom adds a pod-scoped source for in-cluster ingestion" "$(q "$D" "[$KP|.spec.ingress[].from[]|select(.podSelector.matchLabels.app==\"ingest\")|.namespaceSelector.matchLabels[\"kubernetes.io/metadata.name\"]]|join(\",\")")" ci
eq "dimension above pgvector's HNSW limit (2000) fails the render" "$([ -n "$(render_err --set knowledge.embeddings.model.dim=4096)" ] && echo failed || echo rendered)" failed
eq "embeddings url without model fails the render"     "$([ -n "$(render_err --set knowledge.kb.embeddings.url=http://x/v1 --set knowledge.embeddings.enabled=false)" ] && echo failed || echo rendered)" failed

echo "# long-term memory (kagent Harness spec.kagent.memory)"
D=$(docs)
HM='.[]|select(.kind=="Harness" and .metadata.name=="assistant-harness")'
MC='.[]|select(.kind=="ModelConfig" and .metadata.name=="gateway-embed-memory")'
MD='.[]|select(.kind=="Deployment" and .metadata.name=="memory-embeddings")'
eq "assistant opts in: harness has memory.modelConfigRef"  "$(q "$D" "$HM|.spec.kagent.memory.modelConfigRef|.name+\"/\"+.namespace")" gateway-embed-memory/kagent
eq "assistant memory ttlDays from values"              "$(q "$D" "$HM|.spec.kagent.memory.ttlDays")" 30
eq "embedding ModelConfig rendered exactly once"       "$(count "$D" '.kind=="ModelConfig" and .metadata.name=="gateway-embed-memory"')" 1
eq "embedding ModelConfig: provider OpenAI, model embed-memory" "$(q "$D" "$MC|.spec.provider+\"/\"+.spec.model")" OpenAI/embed-memory
eq "embedding ModelConfig has its own virtual key secret" "$(q "$D" "$MC|.spec.apiKeySecret+\"/\"+.spec.apiKeySecretKey")" memory-virtual-key/OPENAI_API_KEY
eq "embedding ModelConfig annotated so install.sh creates its key (user memory)" "$(q "$D" "$MC|.metadata.annotations[\"openagentix.io/agent\"]")" memory
eq "embedding baseUrl is a DIFFERENT origin than the chat ModelConfig (kagent binds one credential per origin)" "$(q "$D" "$MC|.spec.openAI.baseUrl")" http://model-gateway-memory.agentgateway-system.svc.cluster.local/v1
eq "chat ModelConfig origin unchanged"                 "$(q "$D" '.[]|select(.kind=="ModelConfig" and .metadata.name=="gateway-claude-haiku")|.spec.openAI.baseUrl')" http://model-gateway.agentgateway-system.svc.cluster.local/v1
eq "alias Service is ExternalName to the gateway"      "$(q "$D" '.[]|select(.kind=="Service" and .metadata.name=="model-gateway-memory")|.spec.type+"/"+.spec.externalName')" ExternalName/model-gateway.agentgateway-system.svc.cluster.local
eq "gateway model embed-memory -> memory-embeddings (OpenAI-compatible)" "$(q "$D" '.[]|select(.kind=="AgentgatewayModel" and .metadata.name=="embed-memory")|.spec.provider+" "+.spec.baseURL')" "OpenAI http://memory-embeddings.openagentix-memory.svc.cluster.local:8080/v1"
eq "embedding server is a 768-dimension model (kagent fixes 768)" "$(q "$D" "$MD|.spec.template.spec.containers[0].args|index(\"CompendiumLabs/bge-base-en-v1.5-gguf:Q8_0\")!=null")" true
eq "embedding server image is digest pinned"           "$(q "$D" "$MD|.spec.template.spec.containers[0].image|test(\"@sha256:[0-9a-f]{64}\$\")")" true
eq "embedding server lives in openagentix-memory"      "$(q "$D" "$MD|.metadata.namespace")" openagentix-memory
eq "embedding server has resource limits"              "$(q "$D" "$MD|.spec.template.spec.containers[0].resources.limits.memory")" 512Mi
eq "embedding server ingress only from the gateway proxy" "$(q "$D" '.[]|select(.kind=="NetworkPolicy" and .metadata.name=="memory-embeddings-ingress")|[.spec.ingress[].from[].podSelector.matchLabels["gateway.networking.k8s.io/gateway-name"]]|join(",")')" model-gateway
eq "memory objects carry no Secret"                    "$(count "$D" '.kind=="Secret"')" 0
cat > "$(dirname "$0")/.mem-agents.yaml" <<'YAML'
agents:
  - name: alpha
    model: claude-haiku
    systemPrompt: "You are alpha, a terse assistant for testing."
    memory: {enabled: true}
  - name: beta
    model: claude-haiku
    systemPrompt: "You are beta, a terse assistant for testing."
  - name: gamma
    model: claude-haiku
    systemPrompt: "You are gamma, a terse assistant for testing."
    memory: {enabled: false, ttlDays: 5}
YAML
D=$(docs -f "$(dirname "$0")/.mem-agents.yaml"); rm -f "$(dirname "$0")/.mem-agents.yaml"
eq "only the opted-in agent has memory"                "$(q "$D" '[.[]|select(.kind=="Harness" and (.spec.kagent|has("memory")))|.metadata.name]|join(",")')" alpha-harness
eq "agents without the block render spec.kagent: {}"   "$(q "$D" '[.[]|select(.kind=="Harness" and .metadata.name!="alpha-harness")|.spec.kagent|length]|join(",")')" 0,0
eq "ttlDays omitted when not set (kagent default)"     "$(q "$D" '.[]|select(.kind=="Harness" and .metadata.name=="alpha-harness")|.spec.kagent.memory|has("ttlDays")')" false
eq "one embedding stack for any number of agents"      "$(count "$D" '(.kind=="ModelConfig" and .metadata.name=="gateway-embed-memory") or (.kind=="Deployment" and .metadata.name=="memory-embeddings")')" 2
D=$(with_agents '.[0].memory.enabled=false')
eq "variant renders (guards the tests below against vacuous passes)" "$(q "$D" 'length>0')" true
eq "memory disabled on every agent: no memory objects at all" "$(count "$D" '.metadata.namespace=="openagentix-memory" or .metadata.name=="gateway-embed-memory" or .metadata.name=="embed-memory" or .metadata.name=="model-gateway-memory" or (.kind=="Harness" and (.spec.kagent|has("memory")))')" 0
D=$(with_agents 'map(del(.memory))')
eq "variant without the block renders"                 "$(q "$D" 'length>0')" true
eq "no memory block: no memory objects at all"         "$(count "$D" '.metadata.namespace=="openagentix-memory" or .metadata.name=="gateway-embed-memory" or .metadata.name=="embed-memory" or .metadata.name=="model-gateway-memory" or (.kind=="Harness" and (.spec.kagent|has("memory")))')" 0
D=$(docs --set kagent.enabled=false)
eq "kagent.enabled=false: no memory objects"           "$(count "$D" '.metadata.namespace=="openagentix-memory" or .metadata.name=="embed-memory"')" 0
D=$(docs --set knowledge.enabled=false)
eq "memory is independent of knowledge.enabled"        "$(count "$D" '.kind=="Deployment" and .metadata.name=="memory-embeddings"')" 1
D=$(docs --set networkPolicy.enabled=false)
eq "networkPolicy.enabled=false: no memory policy"     "$(count "$D" '.kind=="NetworkPolicy" and .metadata.namespace=="openagentix-memory"')" 0
eq "embedding dimension other than 768 fails the render (kagent stores vector(768))" "$([ -n "$(render_err --set memory.embeddings.model.dim=384)" ] && echo failed || echo rendered)" failed
eq "memory without the gateway fails the render"       "$([ -n "$(render_err --set gateway.enabled=false)" ] && echo failed || echo rendered)" failed
eq "memory prompt hint is appended for opted-in agents" "$(q "$(docs)" '.[]|select(.kind=="AgentTemplate")|.spec.systemPrompt|test("save_memory")')" true

echo "# bridge user identity and rollout"
D=$(docs)
BE='.[]|select(.kind=="Deployment" and .metadata.name=="telegram-bridge")|.spec.template.spec.containers[0].env[]'
eq "bridge: no A2A_USER_ID_PREFIX env by default (the bridge default telegram: applies)" "$(q "$D" "[$BE|select(.name==\"A2A_USER_ID_PREFIX\")]|length")" 0
D=$(docs --set-string bridge.userIdPrefix=botB:)
eq "bridge.userIdPrefix becomes A2A_USER_ID_PREFIX"    "$(q "$D" "$BE|select(.name==\"A2A_USER_ID_PREFIX\")|.value")" botB:
BT='.[]|select(.kind=="Deployment" and .metadata.name=="telegram-bridge")|.spec.template.metadata'
D=$(docs)
eq "bridge pod template has no source-hash annotation by default" "$(q "$D" "$BT|(.annotations//{})|has(\"openagentix.io/source-hash\")")" false
D=$(docs --set-string bridge.sourceHash=abc123)
eq "bridge sourceHash becomes a pod annotation (rolls the pod when bridge code changes, tag stays dev)" "$(q "$D" "$BT|.annotations[\"openagentix.io/source-hash\"]")" abc123

echo "# extra A2A callers of the kagent controller"
D=$(docs)
eq "extraA2ACallers defaults to empty"                 "$(yq -c .networkPolicy.extraA2ACallers "$CHART/values.yaml")" '[]'
EX="$(dirname "$0")/.extra-callers.yaml"
cat > "$EX" <<'YAML'
networkPolicy:
  extraA2ACallers:
    - namespace: agent-kourier
      podLabels: {app.kubernetes.io/name: agent-kourier}
    - namespace: other-ns
YAML
D2=$(docs -f "$EX"); rm -f "$EX"
eq "default render: controller policy has 3 sources on 8083" "$(q "$D" "$CP|[.spec.ingress[]|select(any(.ports[]?;.port==8083))|.from[]]|length")" 3
eq "extraA2ACallers adds two sources to 8083"          "$(q "$D2" "$CP|[.spec.ingress[]|select(any(.ports[]?;.port==8083))|.from[]]|length")" 5
eq "extra caller is scoped by namespace AND pod labels" "$(q "$D2" "$CP|[.spec.ingress[].from[]?|select(.namespaceSelector.matchLabels[\"kubernetes.io/metadata.name\"]==\"agent-kourier\")|.podSelector.matchLabels[\"app.kubernetes.io/name\"]]|join(\",\")")" agent-kourier
eq "extra caller without podLabels admits its whole namespace" "$(q "$D2" "$CP|[.spec.ingress[].from[]?|select(.namespaceSelector.matchLabels[\"kubernetes.io/metadata.name\"]==\"other-ns\")|has(\"podSelector\")]|join(\",\")")" false
eq "extra callers do not open the other ports"         "$(q "$D2" "$CP|[.spec.ingress[]|select(has(\"from\")|not)|.ports[]|(.port|tostring)+\"-\"+(.endPort|tostring)]|sort|join(\",\")")" 1-8082,8084-65535
eq "default policy is byte-identical when the list is empty" "$(q "$D" "$CP|.spec.ingress|length")" 2

echo "# channels and triggers (Agent Kourier, opt-in)"
KAPI="--api-versions agentkourier.dev/v1alpha1"
CHF="$(dirname "$0")/.channels.yaml"
cat > "$CHF" <<'YAML'
channels:
  kourier:
    enabled: true
  connections:
    - name: orders-hook
      platform: webhook
      existingSecret: orders-hook-webhook
      webhook:
        identity: orders-system
        verify: {scheme: hmac-sha256, header: X-Sig, prefix: "sha256="}
        channel: {from: [/channel]}
        actor: {from: [/user/login]}
        thread: {field: conversation, prefix: conv-}
        delivery: {header: X-Delivery}
        fields:
          - {name: event, from: [/event], kind: id}
          - {name: conversation, from: [/conversation], kind: id}
    - name: team-slack
      platform: slack
      existingSecret: team-slack-tokens
      slack: {mode: socket}
  bindings:
    - name: orders-assistant
      connection: orders-hook
      channel: orders
      agent: assistant
      triggers:
        - type: webhookEvent
          match: {event: '^order\.failed$'}
          promptTemplate: "Check order {{ .Fields.conversation }}."
          limits: {rateLimit: {maxRuns: 5, per: 1h, maxConcurrent: 1}, maxRunsPerDay: 20}
    - name: eng-slack
      connection: team-slack
      channel: C0123456789
      agent: assistant
      userId: slack-eng
      output: final
      triggers:
        - {type: mention}
    - name: eng-default
      connection: team-slack
      channel: C0999999999
      agent: assistant
      triggers:
        - {type: personMessage}
YAML
D=$(docs)
eq "default render has no agentkourier.dev objects"    "$(count "$D" '(.apiVersion|startswith("agentkourier.dev"))')" 0
eq "default render has no channels namespace objects"  "$(count "$D" '.metadata.namespace=="openagentix-channels"')" 0
eq "channels.kourier.enabled defaults to false"        "$(yq -c .channels.kourier.enabled "$CHART/values.yaml")" false
eq "channels.connections and bindings default to empty" "$(yq -c '[.channels.connections,.channels.bindings]' "$CHART/values.yaml")" '[[],[]]'
H0=$(helm template openagentix "$CHART" -n openagentix-system 2>&1 | shasum -a 256)
H1=$(helm template openagentix "$CHART" -n openagentix-system -f <(sed 's/enabled: true/enabled: false/' "$CHF") $KAPI 2>&1 | shasum -a 256)
eq "enabled=false with connections/bindings defined renders byte-identical to the defaults" "$H1" "$H0"
D=$(docs -f "$CHF")
eq "enabled but CRDs absent (no api-versions): no Kourier custom resources" "$(count "$D" '(.apiVersion|startswith("agentkourier.dev"))')" 0
eq "enabled but CRDs absent: Role and marker ConfigMap still render" "$(count "$D" '.metadata.namespace=="openagentix-channels"')" 3
D=$(docs -f "$CHF" $KAPI)
eq "AgentBackend, 2 ChatConnections, 3 Bindings"       "$(q "$D" '[.[]|select(.apiVersion|startswith("agentkourier.dev"))|.kind]|sort|join(",")')" AgentBackend,Binding,Binding,Binding,ChatConnection,ChatConnection
eq "CRs live in channels.kourier.namespace"            "$(q "$D" '[.[]|select(.apiVersion|startswith("agentkourier.dev"))|.metadata.namespace]|unique|join(",")')" openagentix-channels
AB='.[]|select(.kind=="AgentBackend")'
eq "AgentBackend dialect kagent-v1"                    "$(q "$D" "$AB|.spec.dialect")" kagent-v1
eq "AgentBackend url derived from kagent values"       "$(q "$D" "$AB|.spec.url")" http://kagent-controller.kagent.svc.cluster.local:8083
CW='.[]|select(.kind=="ChatConnection" and .metadata.name=="orders-hook")'
CS='.[]|select(.kind=="ChatConnection" and .metadata.name=="team-slack")'
eq "webhook connection: platform and Secret reference" "$(q "$D" "$CW|.spec.platform+\" \"+.spec.credentialsSecretRef.name")" "webhook orders-hook-webhook"
eq "webhook settings pass through faithfully"          "$(q "$D" "$CW|.spec.webhook|[.identity,.verify.scheme,.verify.header,.verify.prefix,.channel.from[0],.actor.from[0],.thread.field,.thread.prefix,.delivery.header,(.fields|length)]|join(\" \")")" "orders-system hmac-sha256 X-Sig sha256= /channel /user/login conversation conv- X-Delivery 2"
eq "webhook connection has no slack block"             "$(q "$D" "$CW|.spec|has(\"slack\")")" false
eq "slack connection: platform, Secret, mode"          "$(q "$D" "$CS|.spec.platform+\" \"+.spec.credentialsSecretRef.name+\" \"+.spec.slack.mode")" "slack team-slack-tokens socket"
eq "slack connection has no webhook block"             "$(q "$D" "$CS|.spec|has(\"webhook\")")" false
BW='.[]|select(.kind=="Binding" and .metadata.name=="orders-assistant")'
BS='.[]|select(.kind=="Binding" and .metadata.name=="eng-slack")'
eq "binding: agent ref (name, namespace, backend)"     "$(q "$D" "$BW|.spec.agent|.name+\" \"+.namespace+\" \"+.backendRef.name")" "assistant kagent kagent"
eq "binding: connection and channel"                   "$(q "$D" "$BW|.spec.chat|.connectionRef.name+\" \"+.channel")" "orders-hook orders"
eq "binding: default userId is kourier:<binding name>" "$(q "$D" "$BW|.spec.identity.userId")" kourier:orders-assistant
eq "binding: explicit userId wins"                     "$(q "$D" "$BS|.spec.identity.userId")" slack-eng
eq "binding on a webhook connection defaults to output none" "$(q "$D" "$BW|.spec.chat.output")" none
eq "binding: explicit output honoured"                 "$(q "$D" "$BS|.spec.chat.output")" final
eq "binding on slack without output omits it (Kourier default live)" "$(q "$D" '.[]|select(.kind=="Binding" and .metadata.name=="eng-default")|.spec.chat|has("output")')" false
eq "webhook trigger passes through (type, match, template, limits)" "$(q "$D" "$BW|.spec.chat.triggers[0]|[.type,.match.event,.promptTemplate,.limits.rateLimit.maxRuns,.limits.maxRunsPerDay]|map(tostring)|join(\"|\")")" 'webhookEvent|^order\.failed$|Check order {{ .Fields.conversation }}.|5|20'
eq "mention trigger passes through"                    "$(q "$D" "$BS|.spec.chat.triggers[0].type")" mention
RL='.[]|select(.kind=="Role" and .metadata.namespace=="openagentix-channels")'
eq "Role grants list+watch on exactly the connection Secrets" "$(q "$D" "$RL|.rules|map((.resources|join(\",\"))+\":\"+(.resourceNames|sort|join(\",\"))+\":\"+(.verbs|sort|join(\",\")))|join(\";\")")" "secrets:orders-hook-webhook,team-slack-tokens:list,watch"
eq "RoleBinding grants it to the broker ServiceAccount"  "$(q "$D" '.[]|select(.kind=="RoleBinding" and .metadata.namespace=="openagentix-channels")|.subjects[0]|.kind+" "+.name+" "+.namespace')" "ServiceAccount agent-kourier agent-kourier"
eq "marker ConfigMap names the team and broker namespaces" "$(q "$D" '.[]|select(.kind=="ConfigMap" and .metadata.name=="openagentix-channels")|.data.namespace+" "+.data.brokerNamespace')" "openagentix-channels agent-kourier"
eq "marker ConfigMap carries the broker egress rule to the controller" "$(q "$D" '.[]|select(.kind=="ConfigMap" and .metadata.name=="openagentix-channels")|.data.brokerEgress|fromjson|.[0]|[.to[0].namespaceSelector.matchLabels["kubernetes.io/metadata.name"],.to[0].podSelector.matchLabels["app.kubernetes.io/component"],(.ports[0].port|tostring)]|join(" ")')" "kagent controller 8083"
eq "no Secret objects, no data/stringData"             "$(count "$D" '.kind=="Secret" or (has("data") and .kind!="ConfigMap") or has("stringData")')" 0
S1=$(docs -f "$CHF" $KAPI --set channels.connections[0].token=SENTINEL-CH-777)
eq "stray token value is never rendered"               "$(q "$S1" 'tostring|contains("SENTINEL-CH-777")')" false
echo "# channels: validation and network policy"
EB='--set channels.bindings[0].agent=ghost'
eq "binding to an unknown agent fails the render"      "$([ -n "$(render_err -f "$CHF" $KAPI $EB)" ] && echo failed || echo rendered)" failed
eq "the failure names the agent and lists the valid ones" "$(render_err -f "$CHF" $KAPI $EB | command grep -c 'binding "orders-assistant": agent "ghost" is not in agents\[\].*assistant')" 1
eq "binding to an unknown connection fails the render" "$([ -n "$(render_err -f "$CHF" $KAPI --set channels.bindings[0].connection=nope)" ] && echo failed || echo rendered)" failed
eq "unknown platform fails the render"                 "$([ -n "$(render_err -f "$CHF" $KAPI --set channels.connections[0].platform=irc)" ] && echo failed || echo rendered)" failed
eq "connection without existingSecret fails the render" "$([ -n "$(render_err -f "$CHF" $KAPI --set channels.connections[0].existingSecret=)" ] && echo failed || echo rendered)" failed
eq "channels enabled without kagent fails the render"  "$([ -n "$(render_err -f "$CHF" $KAPI --set kagent.enabled=false)" ] && echo failed || echo rendered)" failed
eq "an invalid binding is not checked while the block is disabled" "$([ -z "$(render_err -f "$CHF" $KAPI $EB --set channels.kourier.enabled=false)" ] && echo rendered || echo failed)" rendered
KP='.[]|select(.kind=="NetworkPolicy" and .metadata.name=="openagentix-kagent-controller")'
Dd=$(docs)
eq "controller policy has no Kourier peer by default"  "$(q "$Dd" "$KP|[.spec.ingress[].from[]?|select(.namespaceSelector.matchLabels[\"kubernetes.io/metadata.name\"]==\"agent-kourier\")]|length")" 0
eq "enabled: controller policy admits the Kourier pod (namespace AND pod labels)" "$(q "$D" "$KP|[.spec.ingress[]|select(any(.ports[]?;.port==8083))|.from[]|select(.namespaceSelector.matchLabels[\"kubernetes.io/metadata.name\"]==\"agent-kourier\")|.podSelector.matchLabels[\"app.kubernetes.io/name\"]]|join(\",\")")" agent-kourier
eq "enabled: 8083 has one more source than the default (4)" "$(q "$D" "$KP|[.spec.ingress[]|select(any(.ports[]?;.port==8083))|.from[]]|length")" 4
EXC="$(dirname "$0")/.extra2.yaml"
printf 'networkPolicy:\n  extraA2ACallers:\n    - namespace: other-ns\n' > "$EXC"
D2=$(docs -f "$CHF" -f "$EXC" $KAPI); rm -f "$EXC"
eq "extraA2ACallers still works next to the Kourier peer (5)" "$(q "$D2" "$KP|[.spec.ingress[]|select(any(.ports[]?;.port==8083))|.from[]]|length")" 5
D3=$(docs -f "$CHF" $KAPI --set networkPolicy.enabled=false)
eq "networkPolicy.enabled=false: no controller policy, so no peer" "$(count "$D3" '.kind=="NetworkPolicy"')" 0
D4=$(docs -f "$CHF" $KAPI --set channels.connections=null --set channels.bindings=null)
eq "no connections: no Role, no Kourier connection/binding" "$(count "$D4" '.kind=="Role" or .kind=="ChatConnection" or .kind=="Binding"')" 0
rm -f "$CHF"

echo "# schedules (CronJobs that call an agent over A2A)"
SCF="$(dirname "$0")/.schedules.yaml"
cat > "$SCF" <<'YAML'
schedules:
  - name: daily-brief
    agent: assistant
    schedule: "0 8 * * 1-5"
    timeZone: Europe/Berlin
    prompt: |
      Summarise the open vulnerabilities in lodash 4.17.20.
      Keep it short.
  - name: quarter-hour
    agent: assistant
    schedule: "*/15 * * * *"
    prompt: "Say ok."
    userId: custom-user
    timeout: 2m
    suspend: true
    concurrencyPolicy: Replace
    historyLimits: {successful: 1, failed: 5}
    backoffLimit: 1
    startingDeadlineSeconds: 120
    resources:
      requests: {cpu: 10m, memory: 20Mi}
      limits: {cpu: 50m, memory: 40Mi}
YAML
D=$(docs)
eq "schedules default to an empty list"                "$(yq -c .schedules "$CHART/values.yaml")" '[]'
eq "default render has no CronJob"                     "$(count "$D" '.kind=="CronJob"')" 0
eq "default render has no schedules namespace objects" "$(count "$D" '.metadata.namespace=="openagentix-schedules"')" 0
eq "default controller policy has no schedule peer"    "$(q "$D" '.[]|select(.kind=="NetworkPolicy" and .metadata.name=="openagentix-kagent-controller")|[.spec.ingress[].from[]?|select(.namespaceSelector.matchLabels["kubernetes.io/metadata.name"]=="openagentix-schedules")]|length')" 0
D=$(docs -f "$SCF")
CJ='.[]|select(.kind=="CronJob" and .metadata.name=="daily-brief")'
CJ2='.[]|select(.kind=="CronJob" and .metadata.name=="quarter-hour")'
CP2='.template.spec'
POD="$CJ|.spec.jobTemplate.spec.template.spec"
eq "one CronJob per schedule"                          "$(count "$D" '.kind=="CronJob"')" 2
eq "one prompt ConfigMap per schedule"                 "$(q "$D" '[.[]|select(.kind=="ConfigMap" and .metadata.namespace=="openagentix-schedules")|.metadata.name]|sort|join(",")')" schedule-daily-brief,schedule-quarter-hour
eq "everything lives in openagentix-schedules"         "$(q "$D" '[.[]|select(.kind=="CronJob" or (.kind=="ConfigMap" and (.metadata.name|startswith("schedule-")))) |.metadata.namespace]|unique|join(",")')" openagentix-schedules
eq "cron expression and time zone pass through"        "$(q "$D" "$CJ|.spec.schedule+\" \"+.spec.timeZone")" "0 8 * * 1-5 Europe/Berlin"
eq "no timeZone key when none is set"                  "$(q "$D" "$CJ2|.spec|has(\"timeZone\")")" false
eq "concurrencyPolicy defaults to Forbid"              "$(q "$D" "$CJ|.spec.concurrencyPolicy")" Forbid
eq "concurrencyPolicy can be set"                      "$(q "$D" "$CJ2|.spec.concurrencyPolicy")" Replace
eq "suspend defaults to false"                         "$(q "$D" "$CJ|.spec.suspend")" false
eq "suspend can be set"                                "$(q "$D" "$CJ2|.spec.suspend")" true
eq "history limits default to 3 and 3"                 "$(q "$D" "$CJ|[.spec.successfulJobsHistoryLimit,.spec.failedJobsHistoryLimit]|map(tostring)|join(\",\")")" 3,3
eq "history limits can be set"                         "$(q "$D" "$CJ2|[.spec.successfulJobsHistoryLimit,.spec.failedJobsHistoryLimit]|map(tostring)|join(\",\")")" 1,5
eq "startingDeadlineSeconds defaults to 300"           "$(q "$D" "$CJ|.spec.startingDeadlineSeconds")" 300
eq "startingDeadlineSeconds can be set"                "$(q "$D" "$CJ2|.spec.startingDeadlineSeconds")" 120
eq "backoffLimit defaults to 0 (a failed agent run is never retried blindly)" "$(q "$D" "$CJ|.spec.jobTemplate.spec.backoffLimit")" 0
eq "backoffLimit can be set"                           "$(q "$D" "$CJ2|.spec.jobTemplate.spec.backoffLimit")" 1
eq "finished Jobs are removed after a day"             "$(q "$D" "$CJ|.spec.jobTemplate.spec.ttlSecondsAfterFinished")" 86400
eq "activeDeadlineSeconds is the timeout plus 60 (default 300s)" "$(q "$D" "$CJ|.spec.jobTemplate.spec.activeDeadlineSeconds")" 360
eq "activeDeadlineSeconds follows a custom timeout (2m)" "$(q "$D" "$CJ2|.spec.jobTemplate.spec.activeDeadlineSeconds")" 180
eq "pod never restarts in place"                       "$(q "$D" "$POD|.restartPolicy")" Never
eq "image comes from scheduling.image"                 "$(q "$D" "$POD|.containers[0].image")" openagentix/a2a-call:dev
eq "image is not pulled when present (kind-loaded)"    "$(q "$D" "$POD|.containers[0].imagePullPolicy")" IfNotPresent
eq "A2A_URL targets the schedule's agent on the controller" "$(q "$D" "$POD|.containers[0].env[]|select(.name==\"A2A_URL\")|.value")" http://kagent-controller.kagent.svc.cluster.local:8083/agents/kagent/assistant
eq "user id defaults to schedule:<name>"               "$(q "$D" "$POD|.containers[0].env[]|select(.name==\"A2A_USER_ID\")|.value")" schedule:daily-brief
eq "user id can be set"                                "$(q "$D" "$CJ2|.spec.jobTemplate.spec.template.spec.containers[0].env[]|select(.name==\"A2A_USER_ID\")|.value")" custom-user
eq "A2A_TIMEOUT defaults to 300s"                      "$(q "$D" "$POD|.containers[0].env[]|select(.name==\"A2A_TIMEOUT\")|.value")" 300s
eq "A2A_TIMEOUT can be set"                            "$(q "$D" "$CJ2|.spec.jobTemplate.spec.template.spec.containers[0].env[]|select(.name==\"A2A_TIMEOUT\")|.value")" 2m
eq "the answer is not printed (PRINT_ANSWER unset)"    "$(q "$D" "$POD|[.containers[0].env[]|select(.name==\"PRINT_ANSWER\")]|length")" 0
eq "prompt is read from a file"                        "$(q "$D" "$POD|.containers[0].env[]|select(.name==\"PROMPT_FILE\")|.value")" /etc/schedule/prompt.txt
eq "the prompt file comes from the schedule's ConfigMap" "$(q "$D" "$POD|.volumes[0].configMap.name")" schedule-daily-brief
eq "the prompt volume is mounted read-only"            "$(q "$D" "$POD|.containers[0].volumeMounts[0]|[.mountPath,.readOnly]|map(tostring)|join(\" \")")" "/etc/schedule true"
eq "ConfigMap holds the prompt text verbatim"          "$(q "$D" '.[]|select(.kind=="ConfigMap" and .metadata.name=="schedule-daily-brief")|.data["prompt.txt"]')" "$(printf 'Summarise the open vulnerabilities in lodash 4.17.20.\nKeep it short.')"
eq "no service account token in the pod"               "$(q "$D" "$POD|.automountServiceAccountToken")" false
eq "pod runs as non-root"                              "$(q "$D" "$POD|.securityContext|[.runAsNonRoot,.runAsUser,.runAsGroup]|map(tostring)|join(\",\")")" true,65532,65532
eq "seccomp RuntimeDefault"                            "$(q "$D" "$POD|.securityContext.seccompProfile.type")" RuntimeDefault
eq "container: no privilege escalation, read-only root, all caps dropped" "$(q "$D" "$POD|.containers[0].securityContext|[.allowPrivilegeEscalation,.readOnlyRootFilesystem,(.capabilities.drop|join(\",\"))]|map(tostring)|join(\" \")")" "false true ALL"
eq "default resources are small"                       "$(q "$D" "$POD|.containers[0].resources|[.requests.memory,.limits.memory]|join(\",\")")" 16Mi,32Mi
eq "resources can be set"                              "$(q "$D" "$CJ2|.spec.jobTemplate.spec.template.spec.containers[0].resources.limits.memory")" 40Mi
eq "pods carry the label the network policies select"  "$(q "$D" "$CJ|.spec.jobTemplate.spec.template.metadata.labels[\"openagentix.io/component\"]")" schedule
eq "pod template carries no source hash by default"    "$(q "$D" "$CJ|.spec.jobTemplate.spec.template.metadata|has(\"annotations\")")" false
D1=$(docs -f "$SCF" --set-string scheduling.sourceHash=abc123)
eq "scheduling.sourceHash is stamped on the pod template" "$(q "$D1" "$CJ|.spec.jobTemplate.spec.template.metadata.annotations[\"openagentix.io/source-hash\"]")" abc123
eq "no Secret objects, no stringData"                  "$(count "$D" '.kind=="Secret" or has("stringData")')" 0
echo "# schedules: network policy"
KP='.[]|select(.kind=="NetworkPolicy" and .metadata.name=="openagentix-kagent-controller")'
eq "controller policy admits the schedule pods: namespace AND pod label" "$(q "$D" "$KP|[.spec.ingress[]|select(any(.ports[]?;.port==8083))|.from[]|select(.namespaceSelector.matchLabels[\"kubernetes.io/metadata.name\"]==\"openagentix-schedules\")|.podSelector.matchLabels[\"openagentix.io/component\"]]|join(\",\")")" schedule
eq "8083 has exactly one more source than the default (4)" "$(q "$D" "$KP|[.spec.ingress[]|select(any(.ports[]?;.port==8083))|.from[]]|length")" 4
eq "the other ports stay as before"                    "$(q "$D" "$KP|[.spec.ingress[]|select(has(\"from\")|not)|.ports[]|(.port|tostring)+\"-\"+(.endPort|tostring)]|sort|join(\",\")")" 1-8082,8084-65535
EP='.[]|select(.kind=="NetworkPolicy" and .metadata.name=="openagentix-schedules-egress")'
eq "egress policy selects exactly the schedule pods"   "$(q "$D" "$EP|.metadata.namespace+\" \"+(.spec.podSelector.matchLabels|tojson)")" 'openagentix-schedules {"openagentix.io/component":"schedule"}'
eq "egress policy restricts Egress only"               "$(q "$D" "$EP|.spec.policyTypes|join(\",\")")" Egress
eq "egress: DNS (udp+tcp 53) to CoreDNS in kube-system" "$(q "$D" "$EP|.spec.egress[0]|[(.to[0].namespaceSelector.matchLabels[\"kubernetes.io/metadata.name\"]),(.to[0].podSelector.matchLabels[\"k8s-app\"]),([.ports[]|.protocol+(.port|tostring)]|sort|join(\"+\"))]|join(\" \")")" "kube-system kube-dns TCP53+UDP53"
eq "egress: the kagent controller pod on 8083 only"    "$(q "$D" "$EP|.spec.egress[1]|[.to[0].namespaceSelector.matchLabels[\"kubernetes.io/metadata.name\"],.to[0].podSelector.matchLabels[\"app.kubernetes.io/component\"],(.ports|map(.protocol+(.port|tostring))|join(\"+\"))]|join(\" \")")" "kagent controller TCP8083"
eq "egress policy has exactly two rules"               "$(q "$D" "$EP|.spec.egress|length")" 2
D3=$(docs -f "$SCF" --set networkPolicy.enabled=false)
eq "networkPolicy.enabled=false: no policies for schedules either" "$(count "$D3" '.kind=="NetworkPolicy"')" 0
eq "networkPolicy.enabled=false: the CronJobs still render" "$(count "$D3" '.kind=="CronJob"')" 2
echo "# schedules: validation"
bad_sched() { # <jq-free yq edit applied to the first schedule>  -> prints helm's error
  local f="$(dirname "$0")/.sched-bad.yaml"
  yq -y "$1" "$SCF" > "$f"; render_err -f "$f"; rm -f "$f"
}
fails() { [ -n "$1" ] && echo failed || echo rendered; }
E=$(bad_sched '.schedules[0].agent="ghost"')
eq "unknown agent fails the render"                    "$(fails "$E")" failed
eq "the failure names the schedule, the agent and the valid agents" "$(printf '%s' "$E" | command grep -c 'schedule "daily-brief": agent "ghost" is not in agents\[\].*assistant')" 1
for cron in "0 8 * *" "0 8 * * * *" "61 8 * * *" "* 24 * * *" "* * 0 * *" "* * 32 * *" "* * * 13 *" "* * * * 8" "every day" "*/0 * * * *" "0 8 * * 1-" "0 8 * * 1;2" ""; do
  eq "invalid cron rejected: '$cron'"                  "$(fails "$(bad_sched ".schedules[0].schedule=\"$cron\"")")" failed
done
for cron in "0 8 * * 1-5" "*/15 * * * *" "0,30 9-17 * * mon-fri" "0 0 1 jan *" "5 4 * * 7" "0-59/10 * * * *" "0 0 29 2 *"; do
  eq "valid cron accepted: '$cron'"                    "$(fails "$(bad_sched ".schedules[0].schedule=\"$cron\"")")" rendered
done
eq "missing prompt fails the render"                   "$(fails "$(bad_sched '.schedules[0].prompt=""')")" failed
eq "missing name fails the render"                     "$(fails "$(bad_sched 'del(.schedules[0].name)')")" failed
eq "an invalid name (upper case) fails the render"     "$(fails "$(bad_sched '.schedules[0].name="Daily"')")" failed
eq "a name longer than 40 characters fails the render" "$(fails "$(bad_sched '.schedules[0].name="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"')")" failed
eq "duplicate names fail the render"                   "$(fails "$(bad_sched '.schedules[1].name="daily-brief"')")" failed
eq "an invalid timeout fails the render"               "$(fails "$(bad_sched '.schedules[0].timeout="soon"')")" failed
eq "a zero timeout fails the render"                   "$(fails "$(bad_sched '.schedules[0].timeout="0s"')")" failed
eq "an invalid concurrencyPolicy fails the render"     "$(fails "$(bad_sched '.schedules[0].concurrencyPolicy="Sometimes"')")" failed
eq "a bad timeZone shape fails the render"             "$(fails "$(bad_sched '.schedules[0].timeZone="Europe/Berlin; rm"')")" failed
eq "schedules without kagent fail the render"          "$(fails "$(render_err -f "$SCF" --set kagent.enabled=false)")" failed
D4=$(docs -f "$SCF" --set scheduling.namespace=jobs-ns)
eq "scheduling.namespace moves everything"             "$(q "$D4" '[.[]|select(.kind=="CronJob")|.metadata.namespace]|unique|join(",")')" jobs-ns
eq "scheduling.namespace moves the controller peer"    "$(q "$D4" "$KP|[.spec.ingress[].from[]?|select(.podSelector.matchLabels[\"openagentix.io/component\"]==\"schedule\")|.namespaceSelector.matchLabels[\"kubernetes.io/metadata.name\"]]|join(\",\")")" jobs-ns
D5=$(docs -f "$SCF" --set 'schedules=null')
eq "schedules=null renders like the default"           "$(count "$D5" '.kind=="CronJob"')" 0
H0=$(helm template openagentix "$CHART" -n openagentix-system 2>&1 | shasum -a 256)
H1=$(helm template openagentix "$CHART" -n openagentix-system --set schedules=null 2>&1 | shasum -a 256)
eq "schedules=null is byte-identical to the default"   "$H1" "$H0"
rm -f "$SCF"

echo "# notify service (agents deliver a result to a chat)"
NNS=openagentix-notify
NV="$(dirname "$0")/.notify-values.yaml"
# the enabled render: the first agent opts in; tokens: the default Telegram one plus a second, made-up name
yq -y '{agents: (.agents|.[0].notify={enabled: true})}' "$CHART/values.yaml" > "$NV"
cat >> "$NV" <<'YAML'
notify:
  enabled: true
  tokens:
    - {env: NOTIFY_TELEGRAM_TOKEN, secret: notify-credentials, key: NOTIFY_TELEGRAM_TOKEN}
    - {env: NOTIFY_SLACK_TOKEN, secret: slack-creds, key: tok}
YAML
D=$(docs)
eq "notify is OFF by default"                          "$(yq -c .notify.enabled "$CHART/values.yaml")" false
eq "default render has nothing in the notify namespace" "$(count "$D" '.metadata.namespace=="openagentix-notify"')" 0
eq "default render has no delivery MCPServerEntry"     "$(count "$D" '.kind=="MCPServerEntry" and .metadata.name=="delivery"')" 0
DA=$(docs -f "$NV")
NP='.[]|select(.kind=="Deployment" and .metadata.name=="notify")'
NC="$NP|.spec.template.spec.containers[0]"
eq "enabled: kinds in the notify namespace"            "$(q "$DA" '[.[]|select(.metadata.namespace=="openagentix-notify")|.kind]|sort|join(",")')" Deployment,NetworkPolicy,NetworkPolicy,Service
eq "enabled: one MCPServerEntry and one auth config"   "$(count "$DA" '(.kind=="MCPServerEntry" or .kind=="MCPExternalAuthConfig") and (.metadata.name|startswith("delivery"))')" 2
eq "entry lives in mcp-tools, in the vMCP group"       "$(q "$DA" '.[]|select(.kind=="MCPServerEntry" and .metadata.name=="delivery")|.metadata.namespace+" "+.spec.groupRef.name')" "mcp-tools tools"
eq "entry points at the notify /mcp endpoint"          "$(q "$DA" '.[]|select(.kind=="MCPServerEntry" and .metadata.name=="delivery")|.spec.remoteUrl')" http://notify.openagentix-notify.svc.cluster.local:8080/mcp
eq "entry is unauthenticated (explicitly)"             "$(q "$DA" '.[]|select(.kind=="MCPExternalAuthConfig" and .metadata.name=="delivery-noauth")|.spec.type')" unauthenticated
eq "entry references that auth config"                 "$(q "$DA" '.[]|select(.kind=="MCPServerEntry" and .metadata.name=="delivery")|.spec.externalAuthConfigRef.name')" delivery-noauth
eq "exactly one replica, Recreate (limits are per replica)" "$(q "$DA" "$NP|(.spec.replicas|tostring)+\" \"+.spec.strategy.type")" "1 Recreate"
eq "image from values"                                 "$(q "$DA" "$NC|.image")" openagentix/notify:dev
eq "config path env"                                   "$(q "$DA" "$NC|.env[]|select(.name==\"NOTIFY_CONFIG\")|.value")" /etc/notify/destinations.yaml
eq "destinations come from an existing Secret volume"  "$(q "$DA" "$NP|.spec.template.spec.volumes[0].secret|.secretName+\" \"+(.optional|tostring)+\" \"+(.items[0].key)")" "notify-destinations true destinations.yaml"
eq "the volume is mounted read-only at /etc/notify"    "$(q "$DA" "$NC|.volumeMounts[0]|.mountPath+\" \"+(.readOnly|tostring)")" "/etc/notify true"
eq "token env vars are wired from the values list (2)" "$(q "$DA" "$NC|[.env[]|select(.valueFrom.secretKeyRef!=null)|.name+\"=\"+.valueFrom.secretKeyRef.name+\"/\"+.valueFrom.secretKeyRef.key]|sort|join(\",\")")" "NOTIFY_SLACK_TOKEN=slack-creds/tok,NOTIFY_TELEGRAM_TOKEN=notify-credentials/NOTIFY_TELEGRAM_TOKEN"
eq "token env vars are optional (pod starts without them)" "$(q "$DA" "$NC|[.env[]|select(.valueFrom.secretKeyRef!=null)|.valueFrom.secretKeyRef.optional]|unique|join(\",\")")" true
eq "no env var carries a literal value that looks like a secret" "$(q "$DA" "$NC|[.env[]|select((.name|test(\"TOKEN|SECRET|CHAT\")) and has(\"value\"))]|length")" 0
eq "hardened pod: non-root, no token, read-only root" "$(q "$DA" "$NP|.spec.template.spec|[.automountServiceAccountToken,.securityContext.runAsNonRoot,.containers[0].securityContext.readOnlyRootFilesystem,.containers[0].securityContext.allowPrivilegeEscalation]|map(tostring)|join(\",\")")" false,true,true,false
eq "readiness is /readyz by default"                   "$(q "$DA" "$NC|.readinessProbe.httpGet.path")" /readyz
eq "pod template carries no source hash by default"    "$(q "$DA" "$NP|.spec.template.metadata|has(\"annotations\")")" false
D1=$(docs -f "$NV" --set-string notify.sourceHash=abc123)
eq "notify.sourceHash is stamped on the pod template"  "$(q "$D1" "$NP|.spec.template.metadata.annotations[\"openagentix.io/source-hash\"]")" abc123
D1=$(docs -f "$NV" --set notify.requireReady=false)
eq "requireReady=false: readiness is /healthz (empty config must not block helm --wait)" "$(q "$D1" "$NC|.readinessProbe.httpGet.path")" /healthz
eq "the Service selects the pod, port 8080"             "$(q "$DA" '.[]|select(.kind=="Service" and .metadata.name=="notify")|(.spec.selector|tojson)+" "+(.spec.ports[0].port|tostring)')" '{"app":"notify"} 8080'
echo "# notify: network policy"
NI='.[]|select(.kind=="NetworkPolicy" and .metadata.name=="notify-ingress")'
NE='.[]|select(.kind=="NetworkPolicy" and .metadata.name=="notify-egress")'
eq "ingress: only the vMCP pods of mcp-tools, MCP port only" "$(q "$DA" "$NI|.spec.ingress|map(.from|map(.namespaceSelector.matchLabels[\"kubernetes.io/metadata.name\"]+\" \"+(.podSelector.matchLabels|tojson))|join(\";\"))|join(\"|\")")" 'mcp-tools {"app":"virtualmcpserver","toolhive-name":"tools"}'
eq "ingress: one rule, TCP 8080"                       "$(q "$DA" "$NI|.spec.ingress|length|tostring")+$(q "$DA" "$NI|.spec.ingress[0].ports|map(.protocol+(.port|tostring))|join(\",\")")" "1+TCP8080"
eq "ingress policy selects the notify pods"            "$(q "$DA" "$NI|.spec.podSelector.matchLabels|tojson")" '{"app":"notify"}'
eq "egress: Egress only, two rules (DNS, public 443)"  "$(q "$DA" "$NE|(.spec.policyTypes|join(\",\"))+\" \"+(.spec.egress|length|tostring)")" "Egress 2"
eq "egress: DNS to kube-dns"                           "$(q "$DA" "$NE|.spec.egress[0]|[.to[0].namespaceSelector.matchLabels[\"kubernetes.io/metadata.name\"],.to[0].podSelector.matchLabels[\"k8s-app\"],(.ports|map(.protocol+(.port|tostring))|sort|join(\"+\"))]|join(\" \")")" "kube-system kube-dns TCP53+UDP53"
eq "egress: 443 to public addresses, private ranges excluded" "$(q "$DA" "$NE|.spec.egress[1]|[.ports[0].port,(.to[0].ipBlock.cidr),(.to[0].ipBlock.except|length)]|map(tostring)|join(\" \")")" "443 0.0.0.0/0 5"
D1=$(docs -f "$NV" --set networkPolicy.enabled=false)
eq "networkPolicy.enabled=false: no notify policies"   "$(count "$D1" '.kind=="NetworkPolicy"')" 0
D1=$(docs -f "$NV" --set mcp.virtual.name=gw)
eq "ingress follows mcp.virtual.name"                  "$(q "$D1" "$NI|.spec.ingress[0].from[0].podSelector.matchLabels[\"toolhive-name\"]")" gw
echo "# notify: agent opt-in"
AT='.[]|select(.kind=="AgentTemplate" and .metadata.name=="assistant-template")'
eq "tool names: entry name prefix + the service's tool names" "$(q "$DA" "$AT|[.spec.tools[0].mcp.tools[]|select(test(\"^delivery_\"))]|sort|join(\",\")")" delivery_notify_list_destinations,delivery_notify_send
eq "the opted-in agent keeps its other tools (5 + 3 kb + 2)" "$(q "$DA" "$AT|.spec.tools[0].mcp.tools|length")" 10
eq "prompt names both tools and the rules"             "$(q "$DA" "$AT|.spec.systemPrompt|test(\"delivery_notify_send\") and test(\"delivery_notify_list_destinations\") and test(\"explicitly asks\") and test(\"exactly as\") and test(\"secret\")")" true
eq "original prompt text is kept"                      "$(q "$DA" "$AT|.spec.systemPrompt|test(\"say when you used one\")")" true
DB=$(docs -f "$NV" --set 'agents[0].notify.enabled=false')
cat > "$(dirname "$0")/.nonotify-agent.yaml" <<'YAML'
notify: {enabled: true}
agents:
  - name: plain
    model: claude-haiku
    systemPrompt: "You are plain."
    tools: [{tools: [fetch_fetch]}]
YAML
D2=$(docs -f "$(dirname "$0")/.nonotify-agent.yaml"); rm -f "$(dirname "$0")/.nonotify-agent.yaml"
eq "agent without notify keeps its tools"              "$(q "$D2" '.[]|select(.kind=="AgentTemplate")|.spec.tools[0].mcp.tools|join(",")')" fetch_fetch
eq "agent without notify keeps its prompt"             "$(q "$D2" '.[]|select(.kind=="AgentTemplate")|.spec.systemPrompt')" "You are plain."
eq "enabled but no agent opted in: AgentTemplate is identical to the default render" "$(q "$D2" '.[]|select(.kind=="AgentTemplate")|tojson'|shasum -a 256)" "$(docs -f /dev/stdin <<<'agents: [{name: plain, model: claude-haiku, systemPrompt: "You are plain.", tools: [{tools: [fetch_fetch]}]}]' | jq -r '.[]|select(.kind=="AgentTemplate")|tojson'|shasum -a 256)"
D3=$(docs -f "$NV" --set notify.enabled=false)
eq "notify.enabled=false with an agent opt-in: no notify tools, no notify prompt" "$(q "$D3" "$AT|(.spec.tools[0].mcp.tools|length|tostring)+\" \"+(.spec.systemPrompt|test(\"delivery_\")|tostring)")" "8 false"
D3=$(docs -f "$NV" --set mcp.enabled=false)
eq "mcp.enabled=false: no entry, and the agent gets no notify tools" "$(count "$D3" '.kind=="MCPServerEntry"')+$(q "$D3" "$AT|.spec.systemPrompt|test(\"delivery_\")")" "0+false"
echo "# notify: hygiene"
eq "no Secret objects, no data/stringData anywhere"    "$(count "$DA" '.kind=="Secret" or has("data") or has("stringData")')" 0
eq "no chat id, URL or token looking value is rendered in the notify namespace" "$(q "$DA" '[.[]|select(.metadata.namespace=="openagentix-notify")|tojson|select(test("api\\.telegram|chatId|chat_id|[0-9]{8,}"))]|length')" 0
H0=$(helm template openagentix "$CHART" -n openagentix-system 2>&1 | shasum -a 256)
H1=$(helm template openagentix "$CHART" -n openagentix-system --set notify.enabled=false 2>&1 | shasum -a 256)
eq "notify.enabled=false is byte-identical to the default render" "$H1" "$H0"
if [ -n "${OA_BASELINE_RENDER:-}" ]; then   # set to the output of `helm template openagentix <chart at the previous commit> -n openagentix-system`
  eq "default render is byte-identical to the baseline render (OA_BASELINE_RENDER)" "$H0" "$(shasum -a 256 < "$OA_BASELINE_RENDER")"
fi
E=$(render_err -f "$NV" --set notify.tokens[0].env=lower-case)
eq "an invalid token env var name fails the render"    "$([ -n "$E" ] && echo failed || echo rendered)" failed
E=$(render_err -f "$NV" --set notify.destinations.existingSecret.name=)
eq "enabled without a destinations Secret name fails the render" "$([ -n "$E" ] && echo failed || echo rendered)" failed
rm -f "$NV"

echo "# secrets hygiene"
D=$(docs --set bridge.token=SENTINEL-TOKEN-123 --set gateway.apiKey=SENTINEL-TOKEN-123)
eq "chart renders no Secret objects"                   "$(count "$D" '.kind=="Secret"')" 0
eq "no stringData/data anywhere"                       "$(q "$D" '[.[]|select(has("data") or has("stringData"))]|length')" 0
eq "stray secret-looking values are never rendered"    "$(q "$D" 'tostring|contains("SENTINEL-TOKEN-123")')" false
eq "no Secret-valued env var has a literal value"      "$(q "$D" '[.[]|select(.kind=="Deployment")|.spec.template.spec.containers[].env[]|select((.name|test("TOKEN|KEY|CHAT_IDS")) and has("value"))]|length')" 0

echo
echo "passed=$PASS failed=$FAIL"
[ "$FAIL" -eq 0 ]
