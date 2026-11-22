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
