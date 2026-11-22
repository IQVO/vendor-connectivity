{{/*
Fully qualified name of the A2A agent Deployment/Service (cmd/a2a, ADR 0009).
*/}}
{{- define "vendor-connectivity.a2aFullname" -}}
{{- include "vendor-connectivity.fullname" . }}-a2a
{{- end }}

{{/* agents.colocate (warehouse-infra ADR 0012): "true" iff colocate, mcp and a2a are all on; else empty. */}}
{{- define "vendor-connectivity.agentsColocated" -}}
{{- if and (.Values.agents | default dict).colocate .Values.mcp.enabled .Values.a2a.enabled }}true{{ end -}}
{{- end }}

{{- define "vendor-connectivity.agentsFullname" -}}
{{- printf "%s-agents" (include "vendor-connectivity.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}
