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
