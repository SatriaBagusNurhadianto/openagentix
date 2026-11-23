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
{{- list (printf "%skb_search" $pfx) (printf "%skb_get_document" $pfx) (printf "%skb_list_collections" $pfx) | toJson -}}
{{- end -}}

{{- define "oa.knowledgeAgentPrompt" -}}
{{- $t := include "oa.knowledgeTools" . | fromJsonArray -}}
{{- .Values.knowledge.agentPrompt | replace "{search}" (index $t 0) | replace "{get}" (index $t 1) -}}
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
