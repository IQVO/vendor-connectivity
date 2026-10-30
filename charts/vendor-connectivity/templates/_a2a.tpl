{{/*
Fully qualified name of the A2A agent Deployment/Service (cmd/a2a, ADR 0009).
*/}}
{{- define "vendor-connectivity.a2aFullname" -}}
{{- include "vendor-connectivity.fullname" . }}-a2a
{{- end }}
