{{- define "oa.labels" -}}
app.kubernetes.io/part-of: openagentix
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/instance: {{ .Release.Name }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{/* Base URL of the model gateway as seen from kagent sandboxes (FQDN: sandboxes do not apply search domains). */}}
{{- define "oa.gatewayBaseUrl" -}}
{{- if .Values.kagent.gatewayBaseUrl -}}{{ .Values.kagent.gatewayBaseUrl }}
{{- else -}}http://{{ .Values.gateway.name }}.{{ .Values.gateway.namespace }}.svc.cluster.local{{ if ne (int .Values.gateway.port) 80 }}:{{ .Values.gateway.port }}{{ end }}/v1
{{- end -}}
{{- end -}}

{{- define "oa.modelConfigName" -}}
{{- default (printf "gateway-%s" .name) .modelConfigName -}}
{{- end -}}

{{- define "oa.virtualKeySecret" -}}
{{- default (printf "%s-virtual-key" .name) .virtualKeySecret -}}
{{- end -}}

{{/* Tool names of the knowledge base as the vMCP exposes them: <prefix of the entry><kb tool>. JSON list. */}}
{{- define "oa.knowledgeTools" -}}
{{- $pfx := replace "{workload}" .Values.knowledge.mcp.entryName .Values.mcp.virtual.prefixFormat -}}
{{- $l := list (printf "%skb_search" $pfx) (printf "%skb_get_document" $pfx) (printf "%skb_list_collections" $pfx) -}}
{{- if .Values.knowledge.kb.graph.enabled }}{{ $l = concat $l (list (printf "%skb_graph_search" $pfx) (printf "%skb_graph_entity" $pfx) (printf "%skb_graph_paths" $pfx)) }}{{ end -}}
{{- $l | toJson -}}
{{- end -}}

{{- define "oa.knowledgeAgentPrompt" -}}
{{- $t := include "oa.knowledgeTools" . | fromJsonArray -}}
{{- $p := .Values.knowledge.agentPrompt | replace "{search}" (index $t 0) | replace "{get}" (index $t 1) -}}
{{- if .Values.knowledge.kb.graph.enabled }}{{ $p = printf "%s\n%s" (trim $p) (.Values.knowledge.kb.graph.agentPrompt | replace "{graph_search}" (index $t 3) | replace "{graph_entity}" (index $t 4) | replace "{graph_paths}" (index $t 5) | replace "{get}" (index $t 1) | trim) }}{{ end -}}
{{- $p -}}
{{- end -}}

{{/* "true" when at least one agent has long-term memory enabled (and kagent is deployed). */}}
{{- define "oa.memoryEnabled" -}}
{{- $on := false -}}
{{- if .Values.kagent.enabled }}{{ range .Values.agents }}{{ if and .memory .memory.enabled }}{{ $on = true }}{{ end }}{{ end }}{{ end -}}
{{- if $on -}}true{{- end -}}
{{- end -}}

{{/* Gateway base URL for the embedding ModelConfig: same gateway, different (alias) Service name, so a different origin. */}}
{{- define "oa.memoryBaseUrl" -}}
http://{{ .Values.memory.gatewayAlias }}.{{ .Values.gateway.namespace }}.svc.cluster.local{{ if ne (int .Values.gateway.port) 80 }}:{{ .Values.gateway.port }}{{ end }}/v1
{{- end -}}

{{/* JSON list: the distinct Secrets named by channels.connections[], sorted. */}}
{{- define "oa.channelSecrets" -}}
{{- $s := list -}}{{- range .Values.channels.connections }}{{ $s = append $s .existingSecret }}{{ end -}}
{{- if include "oa.a2aGateOn" . }}{{ range .Values.channels.bindings }}{{ $s = append $s (printf "a2a-key-%s" .name) }}{{ end }}{{ end -}}
{{- $s | uniq | sortAlpha | toJson -}}
{{- end -}}

{{/* Fails the render when .cron is not a 5-field cron expression (minute hour day-of-month month day-of-week) with in-range
numbers, month/weekday names, ranges, steps and lists. The API server validates again when the CronJob is applied. */}}
{{- define "oa.validateCron" -}}
{{- $fields := regexSplit "\\s+" (trim .cron) -1 -}}
{{- if ne (len $fields) 5 -}}{{ fail (printf "schedule %q: schedule %q must have 5 fields (minute hour day-of-month month day-of-week)" .name .cron) }}{{- end -}}
{{- $min := list 0 0 1 1 0 -}}
{{- $max := list 59 23 31 12 7 -}}
{{- $names := list "" "" "" "jan feb mar apr may jun jul aug sep oct nov dec" "sun mon tue wed thu fri sat" -}}
{{- range $i, $f := $fields -}}
{{- range $e := splitList "," $f -}}
{{- if not (regexMatch "^(\\*|([0-9]+|[A-Za-z]{3})(-([0-9]+|[A-Za-z]{3}))?)(/[1-9][0-9]*)?$" $e) -}}{{ fail (printf "schedule %q: field %d (%q) of schedule %q is not valid cron syntax" $.name (add $i 1) $e $.cron) }}{{- end -}}
{{- $base := regexReplaceAll "/.*$" $e "" -}}
{{- range $n := regexFindAll "[0-9]+" $base -1 -}}
{{- if or (lt (int $n) (index $min $i)) (gt (int $n) (index $max $i)) -}}{{ fail (printf "schedule %q: %s in schedule %q is out of range %d-%d for field %d" $.name $n $.cron (index $min $i) (index $max $i) (add $i 1)) }}{{- end -}}
{{- end -}}
{{- range $w := regexFindAll "[A-Za-z]+" $base -1 -}}
{{- if not (has (lower $w) (splitList " " (index $names $i))) -}}{{ fail (printf "schedule %q: %q is not valid in field %d of schedule %q" $.name $w (add $i 1) $.cron) }}{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* Tool names of the notify service as the vMCP exposes them: <prefix of the entry><service tool>. JSON list. */}}
{{- define "oa.notifyTools" -}}
{{- $pfx := replace "{workload}" .Values.notify.mcp.entryName .Values.mcp.virtual.prefixFormat -}}
{{- list (printf "%snotify_send" $pfx) (printf "%snotify_list_destinations" $pfx) | toJson -}}
{{- end -}}

{{- define "oa.notifyAgentPrompt" -}}
{{- $t := include "oa.notifyTools" . | fromJsonArray -}}
{{- .Values.notify.agentPrompt | replace "{send}" (index $t 0) | replace "{list}" (index $t 1) -}}
{{- end -}}

{{/* "true" when observability is switched on (the block may be absent: `observability: null`). */}}
{{- define "oa.obsOn" -}}
{{- $o := .Values.observability | default dict -}}
{{- if $o.enabled -}}true{{- end -}}
{{- end -}}

{{/* OTLP endpoint every component exports to: observability.otlp.endpoint, else the bundled Jaeger; fails when neither exists. */}}
{{- define "oa.obsEndpoint" -}}
{{- $o := .Values.observability -}}
{{- if $o.otlp.endpoint -}}{{ $o.otlp.endpoint }}
{{- else if $o.bundled.enabled -}}http://jaeger.{{ $o.namespace }}.svc.cluster.local:{{ if eq $o.otlp.protocol "grpc" }}4317{{ else }}4318{{ end }}
{{- else -}}{{ fail "observability.enabled needs observability.otlp.endpoint or observability.bundled.enabled" }}
{{- end -}}
{{- end -}}

{{/* Checked once per render when observability is on. */}}
{{- define "oa.obsValidate" -}}
{{- $o := .Values.observability -}}
{{- if not (has $o.otlp.protocol (list "http/protobuf" "grpc")) }}{{ fail (printf "observability.otlp.protocol must be http/protobuf or grpc, got %q" $o.otlp.protocol) }}{{ end -}}
{{- $r := float64 $o.tracing.samplingRate -}}
{{- if or (lt $r 0.0) (gt $r 1.0) }}{{ fail (printf "observability.tracing.samplingRate must be between 0 and 1, got %v" $o.tracing.samplingRate) }}{{ end -}}
{{- $_ := include "oa.obsEndpoint" . -}}
{{- end -}}

{{/* The metrics port of our own services ("knowledge" for the kb, "notify"), checked. Prints nothing while observability is off. Call: include "oa.obsMetricsPort" (dict "root" . "svc" "knowledge") */}}
{{- define "oa.obsMetricsPort" -}}
{{- if include "oa.obsOn" .root -}}
{{- $m := dict -}}
{{- if eq .svc "knowledge" }}{{ $m = .root.Values.knowledge.kb.metrics }}{{ else }}{{ $m = .root.Values.notify.metrics }}{{ end -}}
{{- $key := ternary "knowledge.kb.metrics.port" "notify.metrics.port" (eq .svc "knowledge") -}}
{{- if not (regexMatch "^[0-9]+$" (toString $m.port)) }}{{ fail (printf "%s must be a port number, got %v" $key $m.port) }}{{ end -}}
{{- $p := int $m.port -}}
{{- if or (lt $p 1024) (gt $p 65535) (eq $p 8080) }}{{ fail (printf "%s must be 1024..65535 and different from the API port 8080, got %d" $key $p) }}{{ end -}}
{{- $p -}}
{{- end -}}
{{- end -}}

{{/* Propagators: tracecontext only (no baggage) unless observability.baggage. */}}
{{- define "oa.obsPropagators" -}}
{{- if .Values.observability.baggage -}}tracecontext,baggage{{- else -}}tracecontext{{- end -}}
{{- end -}}

{{/* The single content switch. Prints "true" or "false". */}}
{{- define "oa.obsCapture" -}}
{{- if .Values.observability.capture.content -}}true{{- else -}}false{{- end -}}
{{- end -}}

{{/* {a: b, c: d} -> "a=b,c=d" (label selector string, keys sorted). */}}
{{- define "oa.selectorString" -}}
{{- $l := list -}}{{- range $k := keys . | sortAlpha }}{{ $l = append $l (printf "%s=%s" $k (get $ $k)) }}{{ end -}}
{{- join "," $l -}}
{{- end -}}

{{/* Jaeger v2 configuration of the bundled profile (used for the ConfigMap AND for the pod template hash, so a changed drop list restarts Jaeger). */}}
{{- define "oa.jaegerConfig" -}}
{{- $o := .Values.observability -}}
{{- $b := $o.bundled -}}
# Jaeger v2: OTLP in, in-memory store (capped), query UI. Nothing is persisted; a restart loses all traces.
service:
  extensions: [jaeger_storage, jaeger_query, healthcheckv2]
  pipelines:
    traces:
      receivers: [otlp]
      processors: [memory_limiter, {{ if not $o.capture.content }}attributes/drop-content, {{ end }}batch]
      exporters: [jaeger_storage_exporter]
  telemetry:
    resource: {service.name: jaeger}
    metrics: {level: none}
    logs: {level: info}
extensions:
  healthcheckv2:
    use_v2: true
    http: {endpoint: "0.0.0.0:13133"}
  jaeger_query:
    storage: {traces: memstore}
  jaeger_storage:
    backends:
      memstore:
        memory: {max_traces: {{ $b.traces.maxTraces }}}
receivers:
  otlp:
    protocols:
      grpc: {endpoint: "0.0.0.0:4317"}
      http: {endpoint: "0.0.0.0:4318"}
processors:
  memory_limiter: {check_interval: 1s, limit_mib: 150, spike_limit_mib: 30}
  batch: {}
  {{- if not $o.capture.content }}
  # The Go ADK writes tool arguments and results into spans whatever the capture setting (google/adk-go#1634), the vMCP writes
  # mcp.tool.arguments, and
  # the GenAI content keys exist only when capture is on, and user.id carries the caller's id. They are deleted here, before storage. Only
  # observability.capture.content=true removes this processor.
  attributes/drop-content:
    actions:
      - {key: gcp.vertex.agent.tool_call_args, action: delete}
      - {key: gcp.vertex.agent.tool_response, action: delete}
      - {key: gcp.vertex.agent.llm_request, action: delete}
      - {key: gcp.vertex.agent.llm_response, action: delete}
      - {key: gen_ai.input.messages, action: delete}
      - {key: gen_ai.output.messages, action: delete}
      - {key: gen_ai.system_instructions, action: delete}
      - {key: gen_ai.tool.call.arguments, action: delete}
      - {key: gen_ai.tool.call.result, action: delete}
      - {key: mcp.tool.arguments, action: delete}
      # user.id is the caller id the platform forwards (for the Telegram bridge: telegram:<chat id>), personal data that is
      # not needed to follow a request: the conversation id is the key. Kept only with capture.content=true.
      - {key: user.id, action: delete}
      - {key: enduser.id, action: delete}
      {{- range $o.bundled.traces.extraDropAttributes }}
      - {key: {{ . | quote }}, action: delete}
      {{- end }}
  {{- end }}
exporters:
  jaeger_storage_exporter: {trace_storage: memstore}
{{- end -}}

{{- define "oa.a2aGateOn" -}}
{{- $g := .Values.governance | default dict -}}
{{- $a := $g.a2a | default dict -}}
{{- if $a.enabled -}}true{{- end -}}
{{- end -}}

{{- define "oa.a2aGateUrl" -}}
http://a2a-gate.{{ .Values.governance.a2a.namespace }}.svc.cluster.local:8080
{{- end -}}

{{- /* The caller rules of the A2A gate, as JSON. Derived from the bridge, the schedules and the Kourier bindings, plus
       governance.a2a.callers. Each caller lists the JSON-RPC methods it may use: what the chart's own callers really send
       (the bridge and the schedules: SendMessage; Kourier: send, stream, read, subscribe, cancel). It holds no secret.
       Used for the rules ConfigMap and its checksum. */ -}}
{{- define "oa.a2aRules" -}}
{{- $a := .Values.governance.a2a -}}
{{- $kns := .Values.kagent.namespace -}}
{{- $agents := list -}}{{- range .Values.agents }}{{ $agents = append $agents .name }}{{ end -}}
{{- $callers := list -}}
{{- if .Values.bridge.enabled -}}
{{- $b := .Values.bridge -}}
{{- $callers = append $callers (dict "name" "bridge" "serviceAccount" (dict "namespace" $b.namespace "name" "telegram-bridge") "agents" (list (printf "%s/%s" $b.agentNamespace $b.agent)) "userId" (dict "prefix" (default "telegram:" $b.userIdPrefix)) "methods" (list "SendMessage")) -}}
{{- end -}}
{{- range .Values.schedules -}}
{{- $callers = append $callers (dict "name" (printf "schedule-%s" .name) "serviceAccount" (dict "namespace" $.Values.scheduling.namespace "name" (printf "schedule-%s" .name)) "agents" (list (printf "%s/%s" $kns .agent)) "userId" (dict "exact" (default (printf "schedule:%s" .name) .userId)) "methods" (list "SendMessage")) -}}
{{- end -}}
{{- if .Values.channels.kourier.enabled -}}
{{- range .Values.channels.bindings -}}
{{- $callers = append $callers (dict "name" (printf "kourier-%s" .name) "key" true "agents" (list (printf "%s/%s" $kns .agent)) "userId" (dict "fixed" (default (printf "kourier:%s" .name) .userId)) "methods" (list "SendMessage" "SendStreamingMessage" "GetTask" "SubscribeToTask" "CancelTask")) -}}
{{- end -}}
{{- end -}}
{{- $seen := list -}}
{{- range $i, $c := $a.callers -}}
{{- if not $c.name }}{{ fail (printf "governance.a2a.callers[%d]: name is required" $i) }}{{ end -}}
{{- if not (regexMatch "^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$" (toString $c.name)) }}{{ fail (printf "governance.a2a.callers[%d]: name %q must be letters, digits, '.', '_' or '-' (at most 63)" $i (toString $c.name)) }}{{ end -}}
{{- if or (eq $c.name "bridge") (hasPrefix "schedule-" $c.name) (hasPrefix "kourier-" $c.name) }}{{ fail (printf "governance.a2a.callers[%d]: name %q is reserved for the callers the chart derives (bridge, schedule-*, kourier-*)" $i $c.name) }}{{ end -}}
{{- if has $c.name $seen }}{{ fail (printf "governance.a2a.callers: name %q is used twice" $c.name) }}{{ end -}}
{{- $seen = append $seen $c.name -}}
{{- if not (and $c.serviceAccount $c.serviceAccount.namespace $c.serviceAccount.name) }}{{ fail (printf "governance.a2a.callers[%s]: serviceAccount needs namespace and name" $c.name) }}{{ end -}}
{{- if not $c.agents }}{{ fail (printf "governance.a2a.callers[%s]: agents must list at least one agent" $c.name) }}{{ end -}}
{{- $full := list -}}
{{- range $c.agents -}}
{{- if contains "/" (toString .) -}}{{- $full = append $full (toString .) -}}
{{- else -}}
{{- if not (has . $agents) }}{{ fail (printf "governance.a2a.callers[%s]: agent %q is not in agents[] (have: %s)" $c.name (toString .) (join ", " $agents)) }}{{ end -}}
{{- $full = append $full (printf "%s/%s" $kns .) -}}
{{- end -}}
{{- end -}}
{{- $u := $c.userId | default dict -}}
{{- $set := 0 -}}{{- range $k := list "exact" "prefix" "fixed" }}{{ if get $u $k }}{{ $set = add $set 1 }}{{ end }}{{ end -}}
{{- if ne (int $set) 1 }}{{ fail (printf "governance.a2a.callers[%s]: userId needs exactly one of exact, prefix and fixed" $c.name) }}{{ end -}}
{{- $methods := list -}}
{{- if hasKey $c "methods" -}}
{{- if or (not (kindIs "slice" $c.methods)) (not $c.methods) }}{{ fail (printf "governance.a2a.callers[%s]: methods must be a non-empty list (leave it out for the gate's default: SendMessage, SendStreamingMessage)" $c.name) }}{{ end -}}
{{- range $c.methods -}}
{{- if not (regexMatch "^[A-Za-z][A-Za-z0-9_./:-]{0,63}$" (toString .)) }}{{ fail (printf "governance.a2a.callers[%s]: method %q is not a valid method name" $c.name (toString .)) }}{{ end -}}
{{- if has (toString .) $methods }}{{ fail (printf "governance.a2a.callers[%s]: method %q is listed twice" $c.name (toString .)) }}{{ end -}}
{{- $methods = append $methods (toString .) -}}
{{- end -}}
{{- end -}}
{{- $entry := dict "name" $c.name "serviceAccount" (dict "namespace" $c.serviceAccount.namespace "name" $c.serviceAccount.name) "agents" $full "userId" (pick $u "exact" "prefix" "fixed") -}}
{{- if $methods }}{{ $_ := set $entry "methods" $methods }}{{ end -}}
{{- $callers = append $callers $entry -}}
{{- end -}}
{{- /* a caller of the controller's port that was admitted by networkPolicy.extraA2ACallers must have a rule, or it would be cut off silently */ -}}
{{- range $x := .Values.networkPolicy.extraA2ACallers -}}
{{- $has := false -}}
{{- range $a.callers }}{{ if eq .serviceAccount.namespace $x.namespace }}{{ $has = true }}{{ end }}{{ end -}}
{{- if not $has }}{{ fail (printf "networkPolicy.extraA2ACallers: namespace %q has no matching entry in governance.a2a.callers (serviceAccount.namespace). With the A2A gate on every caller needs a rule." $x.namespace) }}{{ end -}}
{{- end -}}
{{- toJson (dict "callers" $callers) -}}
{{- end -}}
