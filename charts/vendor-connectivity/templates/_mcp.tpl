{{/*
Fully qualified name of the MCP server Deployment/Service (cmd/mcp, ADR 0005).
*/}}
{{- define "vendor-connectivity.mcpFullname" -}}
{{- include "vendor-connectivity.fullname" . }}-mcp
{{- end }}
