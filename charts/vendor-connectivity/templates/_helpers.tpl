{{/*
Expand the name of the chart.
*/}}
{{- define "vendor-connectivity.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "vendor-connectivity.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Chart name and version as used by the chart label.
*/}}
{{- define "vendor-connectivity.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "vendor-connectivity.labels" -}}
helm.sh/chart: {{ include "vendor-connectivity.chart" . }}
{{ include "vendor-connectivity.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels. The Deployment and the Service ALSO pin
app.kubernetes.io/component (api), so a component added later cannot be
selected by accident.
*/}}
{{- define "vendor-connectivity.selectorLabels" -}}
app.kubernetes.io/name: {{ include "vendor-connectivity.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "vendor-connectivity.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "vendor-connectivity.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Name of the Secret holding DATABASE_URL: the operator's own
(database.existingSecret) or the one this chart creates from database.url.
*/}}
{{- define "vendor-connectivity.databaseSecretName" -}}
{{- if .Values.database.existingSecret }}
{{- .Values.database.existingSecret }}
{{- else }}
{{- include "vendor-connectivity.fullname" . }}-database
{{- end }}
{{- end }}

{{/*
Image reference.
*/}}
{{- define "vendor-connectivity.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end }}

{{/*
The non-secret environment of cmd/api, as a YAML map: the single source for the
ConfigMap (envFrom) and for the container env, so the two cannot drift. Every
key is a variable cmd/api reads (tests/render_test.sh checks both directions).
DATABASE_URL and MIGRATIONS_DATABASE_URL never appear here: they come from a Secret.
*/}}
{{- define "vendor-connectivity.configData" -}}
HTTP_ADDR: {{ .Values.config.httpAddr | quote }}
LOG_LEVEL: {{ .Values.config.logLevel | quote }}
EVENT_PUBLISHER: {{ .Values.config.eventPublisher | quote }}
KAFKA_BROKERS: {{ .Values.kafka.brokers | quote }}
NODE_CONSUMER_GROUP: {{ .Values.config.nodeConsumerGroup | quote }}
LISTING_CONSUMER_GROUP: {{ .Values.config.listingConsumerGroup | quote }}
SOURCING_CONSUMER_GROUP: {{ .Values.config.sourcingConsumerGroup | quote }}
INBOUND_CONSUMER_GROUP: {{ .Values.config.inboundConsumerGroup | quote }}
PACKAGING_CONSUMER_GROUP: {{ .Values.config.packagingConsumerGroup | quote }}
NODE_TOPOLOGY_URL: {{ .Values.config.nodeTopologyUrl | quote }}
OFFER_AVAILABILITY_URL: {{ .Values.config.offerAvailabilityUrl | quote }}
SHIPPING_AND_TRACKING_URL: {{ .Values.config.shippingAndTrackingUrl | quote }}
NODE_PERFORMANCE_URL: {{ .Values.config.nodePerformanceUrl | quote }}
OUTBOX_MAX_ATTEMPTS: {{ .Values.config.outboxMaxAttempts | quote }}
{{- with .Values.config.outboxRelayInterval }}
OUTBOX_RELAY_INTERVAL: {{ . | quote }}
{{- end }}
{{- with .Values.config.shutdownDrainDelay }}
SHUTDOWN_DRAIN_DELAY: {{ . | quote }}
{{- end }}
{{- with .Values.config.txProcessingDelay }}
TX_PROCESSING_DELAY: {{ . | quote }}
{{- end }}
{{- with .Values.config.txFailureRate }}
TX_FAILURE_RATE: {{ . | quote }}
{{- end }}
{{- with .Values.config.poRedeliveryRate }}
PO_REDELIVERY_RATE: {{ . | quote }}
{{- end }}
{{- with .Values.config.rateLimitRps }}
RATE_LIMIT_RPS: {{ . | quote }}
{{- end }}
{{- with .Values.config.submissionPollInterval }}
SUBMISSION_POLL_INTERVAL: {{ . | quote }}
{{- end }}
{{- with .Values.config.expirySweepInterval }}
EXPIRY_SWEEP_INTERVAL: {{ . | quote }}
{{- end }}
{{- end -}}

{{/*
MANDATORY: cmd/api exits at boot without DATABASE_URL; there is no in-memory
mode. Fails the render naming the values instead of crash-looping.
*/}}
{{- define "vendor-connectivity.requireDatabase" -}}
{{- if not (or .Values.database.url .Values.database.existingSecret) -}}
{{- fail "vendor-connectivity requires database.url or database.existingSecret to be set (DATABASE_URL): the service runs on real Postgres only and exits at boot without it." -}}
{{- end -}}
{{- end -}}

{{/*
MANDATORY: cmd/api exits at boot without KAFKA_BROKERS and rejects
EVENT_PUBLISHER=log. There is no default broker and no log sink.
*/}}
{{- define "vendor-connectivity.requireKafka" -}}
{{- $_ := required "vendor-connectivity requires kafka.brokers to be set (KAFKA_BROKERS, comma-separated): the service runs on real Kafka only and exits at boot without it." .Values.kafka.brokers -}}
{{- if ne (toString .Values.config.eventPublisher) "kafka" -}}
{{- fail (printf "config.eventPublisher is %q: the only publisher is \"kafka\"; the binary rejects any other EVENT_PUBLISHER at boot." (toString .Values.config.eventPublisher)) -}}
{{- end -}}
{{- end -}}

{{/*
The five consumers always run, each under a STABLE consumer group id that
comes from values (cmd/api exits at boot without NODE_CONSUMER_GROUP,
LISTING_CONSUMER_GROUP, SOURCING_CONSUMER_GROUP, INBOUND_CONSUMER_GROUP and PACKAGING_CONSUMER_GROUP). A stable id is what lets a
rescheduled pod resume from its committed offsets; never derive it from the pod name.
*/}}
{{- define "vendor-connectivity.requireConsumerGroups" -}}
{{- range $c := list (dict "key" "nodeConsumerGroup" "env" "NODE_CONSUMER_GROUP") (dict "key" "listingConsumerGroup" "env" "LISTING_CONSUMER_GROUP") (dict "key" "sourcingConsumerGroup" "env" "SOURCING_CONSUMER_GROUP") (dict "key" "inboundConsumerGroup" "env" "INBOUND_CONSUMER_GROUP") (dict "key" "packagingConsumerGroup" "env" "PACKAGING_CONSUMER_GROUP") -}}
{{- if not (index $.Values.config $c.key) -}}
{{- fail (printf "config.%s is required (%s): the consumer always runs on real Kafka and cmd/api exits at boot without a group. Use a STABLE, service-prefixed id, never the pod name." $c.key $c.env) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
The four neighbour base URLs have no default: cmd/api exits at boot without them.
*/}}
{{- define "vendor-connectivity.requireNeighbourUrls" -}}
{{- range $c := list (dict "key" "nodeTopologyUrl" "env" "NODE_TOPOLOGY_URL") (dict "key" "offerAvailabilityUrl" "env" "OFFER_AVAILABILITY_URL") (dict "key" "shippingAndTrackingUrl" "env" "SHIPPING_AND_TRACKING_URL") (dict "key" "nodePerformanceUrl" "env" "NODE_PERFORMANCE_URL") -}}
{{- if not (index $.Values.config $c.key) -}}
{{- fail (printf "config.%s is required (%s): the in-organization call it names has no default and cmd/api exits at boot without it." $c.key $c.env) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
extraEnv may not bring back a log publisher or a mode switch: no EVENT_PUBLISHER
other than kafka and no *_MODE variable at all (this service has no modes), and
no variable the chart renders itself for observability (the same variable is
never set twice), in the api's extraEnv or the mcp component's.
*/}}
{{- define "vendor-connectivity.requireNoInMemoryEnv" -}}
{{- $owned := list "ENVIRONMENT" "SERVICE_VERSION" "OTEL_SERVICE_NAME" "OTEL_EXPORTER_OTLP_ENDPOINT" -}}
{{- range (concat .Values.extraEnv .Values.mcp.extraEnv) -}}
{{- $name := toString .name -}}
{{- if has $name $owned -}}
{{- fail (printf "extraEnv sets %s: the chart renders it itself (otel.*, environment, image.tag); set the value, not the variable." $name) -}}
{{- end -}}
{{- $value := toString (default "" .value) -}}
{{- if and (eq $name "EVENT_PUBLISHER") (ne $value "kafka") -}}
{{- fail (printf "extraEnv sets EVENT_PUBLISHER=%s: the only publisher is \"kafka\"." $value) -}}
{{- end -}}
{{- if hasSuffix "_MODE" $name -}}
{{- fail (printf "extraEnv sets %s: *_MODE variables are not supported; vendor-connectivity runs on real Postgres and real Kafka only and has no mode switch." $name) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
The graceful shutdown is sequential (cmd/api/main.go): SHUTDOWN_DRAIN_DELAY
(default 5s) + HTTP drain (10s) + consumers and loops (10s) + outbox relay
(10s) = 35s. The grace period must be at least 40 and above drain delay + 30s,
or the kubelet SIGKILLs the pod between "consumers stopped" and "relay stopped",
stranding an uncommitted offset or an in-flight outbox batch.
config.shutdownDrainDelay must be whole seconds ("5s", "0") so this can be checked.
*/}}
{{- define "vendor-connectivity.requireGracePeriod" -}}
{{- $drain := 5 -}}
{{- with (toString .Values.config.shutdownDrainDelay) -}}
{{- if regexMatch "^[0-9]+s?$" . -}}
{{- $drain = int (trimSuffix "s" .) -}}
{{- else -}}
{{- fail (printf "config.shutdownDrainDelay is %q: use whole seconds such as \"5s\" so the chart can check terminationGracePeriodSeconds against it." .) -}}
{{- end -}}
{{- end -}}
{{- $need := add $drain 30 -}}
{{- if lt $need 40 -}}{{- $need = 40 -}}{{- end -}}
{{- if lt (int .Values.terminationGracePeriodSeconds) (int $need) -}}
{{- fail (printf "terminationGracePeriodSeconds is %v: want at least %v (SHUTDOWN_DRAIN_DELAY %vs + HTTP drain 10s + consumers and loops 10s + relay 10s, plus margin); a shorter grace period SIGKILLs the consumers or the relay mid-drain." .Values.terminationGracePeriodSeconds $need $drain) -}}
{{- end -}}
{{- end -}}

{{/*
Every render-time requirement of the real runtime, in one include. The api
Deployment includes it, so a mis-set release fails `helm template` / `helm install`.
*/}}
{{- define "vendor-connectivity.requireRealRuntime" -}}
{{- include "vendor-connectivity.requireDatabase" . -}}
{{- include "vendor-connectivity.requireKafka" . -}}
{{- include "vendor-connectivity.requireConsumerGroups" . -}}
{{- include "vendor-connectivity.requireNeighbourUrls" . -}}
{{- include "vendor-connectivity.requireNoInMemoryEnv" . -}}
{{- include "vendor-connectivity.requireGracePeriod" . -}}
{{- end -}}
