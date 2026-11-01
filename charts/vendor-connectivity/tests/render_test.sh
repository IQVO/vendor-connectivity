#!/usr/bin/env bash
# Chart render assertions: the env cmd/api reads is the env the chart renders,
# the mandatory configuration has no silent default, no mode switch or log
# publisher is ever rendered, and the shutdown room covers the binary's bounds.
# Run: bash charts/vendor-connectivity/tests/render_test.sh   (needs helm)
set -euo pipefail

chart_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo_dir="$(cd "$chart_dir/../.." && pwd)"
release=vendor-connectivity
# Every non-test Go file of the cmd/api package: the composition root is split across files
# (main.go, config.go, wire_adapters.go, background.go), so scanning main.go alone misses env reads.
# telemetry.go is excluded here: its OTEL_* variables have their own section below (Deployment env only).
api_go=($(ls "$repo_dir"/cmd/api/*.go | grep -v -E '_test\.go$|/telemetry\.go$'))

# Dummy values, no password: the chart refuses to render without a database
# source, Kafka brokers, the five consumer groups and the four neighbour URLs.
# None is ever dialled.
groups=(nodeConsumerGroup:vendor-connectivity-node
  listingConsumerGroup:vendor-connectivity-listing
  sourcingConsumerGroup:vendor-connectivity-sourcing
  inboundConsumerGroup:vendor-connectivity-inbound
  packagingConsumerGroup:vendor-connectivity-packaging)
urls=(nodeTopologyUrl:http://node-topology.example.invalid
  offerAvailabilityUrl:http://offer-availability.example.invalid
  shippingAndTrackingUrl:http://shipping-and-tracking.example.invalid
  nodePerformanceUrl:http://node-performance.example.invalid)
base=(--set database.url=postgres://u@example.invalid:5432/db
  --set kafka.brokers=kafka.example.invalid:9092)
for g in "${groups[@]}" "${urls[@]}"; do base+=(--set "config.${g%%:*}=${g#*:}"); done
# Every optional value set, so every env var the binary can read is rendered.
everything=(--set config.outboxRelayInterval=2s --set config.shutdownDrainDelay=8s
  --set terminationGracePeriodSeconds=60
  --set config.txProcessingDelay=500ms --set config.txFailureRate=0.1
  --set config.poRedeliveryRate=0.2 --set config.rateLimitRps=50
  --set config.submissionPollInterval=2s --set config.expirySweepInterval=45s)

fail() { echo "FAIL: $*" >&2; exit 1; }

configmap="$(helm template "$release" "$chart_dir" "${base[@]}" --show-only templates/configmap.yaml)"
api_deployment="$(helm template "$release" "$chart_dir" "${base[@]}" --show-only templates/deployment.yaml)"
configmap_name="$(awk '/^  name:/ {print $2; exit}' <<<"$configmap")"
[[ -n "$configmap_name" ]] || fail "could not read the ConfigMap name"

# The api Deployment mounts the chart ConfigMap with envFrom, or a value that
# is only in the ConfigMap silently does nothing.
grep -A3 'envFrom:' <<<"$api_deployment" | grep -q 'configMapRef:' \
  || fail "api Deployment does not mount the ConfigMap via envFrom"
grep -A3 'envFrom:' <<<"$api_deployment" | grep -q "name: $configmap_name" \
  || fail "api Deployment envFrom does not reference ConfigMap $configmap_name"

# A tuned config.* value reaches the ConfigMap the api mounts and the container env.
tuned="$(helm template "$release" "$chart_dir" "${base[@]}" --set config.outboxRelayInterval=7s --set config.outboxMaxAttempts=5 --show-only templates/configmap.yaml)"
grep -q 'OUTBOX_RELAY_INTERVAL: "7s"' <<<"$tuned" || fail "config.outboxRelayInterval is not rendered into the ConfigMap"
grep -q 'OUTBOX_MAX_ATTEMPTS: "5"' <<<"$tuned" || fail "config.outboxMaxAttempts is not rendered into the ConfigMap"
# The relay attempt cap defaults to 20 and 0 (unlimited) is rendered as is.
grep -q 'OUTBOX_MAX_ATTEMPTS: "20"' <<<"$configmap" || fail "OUTBOX_MAX_ATTEMPTS must default to 20"
grep -A1 'name: OUTBOX_MAX_ATTEMPTS' <<<"$api_deployment" | grep -q 'value: "20"' \
  || fail "api Deployment does not set OUTBOX_MAX_ATTEMPTS"
unlimited="$(helm template "$release" "$chart_dir" "${base[@]}" --set config.outboxMaxAttempts=0 --show-only templates/configmap.yaml)"
grep -q 'OUTBOX_MAX_ATTEMPTS: "0"' <<<"$unlimited" || fail "config.outboxMaxAttempts=0 is not rendered"

# Chart vs binary: every env var cmd/api reads must be rendered into the
# Deployment AND the ConfigMap when every optional value is set. The names come
# from the binary's own getenv("...") / env("...") literals plus the
# consumerGroups and neighbourURLs slices, so a new variable in cmd/api/main.go
# fails this test until the chart renders it.
literal_env() {
  grep -ohE '(Getenv|getenv|env)\("[A-Z][A-Z0-9_]*"' "${api_go[@]}" | sed -E 's/.*\("//; s/"$//' | sort -u
}
slice_env() { # slice_env <go slice variable>
  grep -hE "^var $1 = " "${api_go[@]}" | grep -oE '"[A-Z][A-Z0-9_]*"' | tr -d '"' | sort -u
}
# Word splitting is safe: the names are [A-Z0-9_] only (bash 3.2 has no mapfile).
# shellcheck disable=SC2207
api_vars=($( { literal_env; slice_env consumerGroups; slice_env neighbourURLs; } | sort -u))
(( ${#api_vars[@]} >= 20 )) || fail "found only ${#api_vars[@]} env names in cmd/api; the scan is looking in the wrong place"
(( $(slice_env consumerGroups | wc -l) == 5 )) || fail "expected 5 consumer groups in cmd/api, found $(slice_env consumerGroups | wc -l)"
(( $(slice_env neighbourURLs | wc -l) == 4 )) || fail "expected 4 neighbour URLs in cmd/api, found $(slice_env neighbourURLs | wc -l)"
full_deployment="$(helm template "$release" "$chart_dir" "${base[@]}" "${everything[@]}" --show-only templates/deployment.yaml)"
full_configmap="$(helm template "$release" "$chart_dir" "${base[@]}" "${everything[@]}" --show-only templates/configmap.yaml)"
secret_vars=" DATABASE_URL MIGRATIONS_DATABASE_URL "
for var in "${api_vars[@]}"; do
  grep -q "name: $var\$" <<<"$full_deployment" || fail "the api Deployment does not render $var, which cmd/api reads"
  if [[ "$secret_vars" != *" $var "* ]]; then
    grep -q "^  $var: " <<<"$full_configmap" || fail "the ConfigMap does not carry $var, which cmd/api reads"
  fi
done
# Every key the ConfigMap renders is a variable cmd/api reads (no dead keys).
api_known=" ${api_vars[*]} "
while read -r key; do
  [[ "$api_known" == *" $key "* ]] || fail "ConfigMap key $key is read by no code in cmd/api (dead key)"
done < <(awk '/^data:/ {d=1; next} d && /^  [A-Z]/ {sub(/:.*/, ""); sub(/^  /, ""); print}' <<<"$full_configmap")
# Every mandatory variable cmd/api names in a boot error is rendered.
for var in $(grep -ho 'errors.New("[A-Z_]* is required' "${api_go[@]}" | sed -E 's/errors.New\("//; s/ is required//' | sort -u); do
  grep -q "name: $var\$" <<<"$api_deployment" || fail "the api Deployment does not render $var, which cmd/api requires"
done

# --- the mandatory configuration is rendered from values ------------------------------
env_value() { # env_value <rendered> <NAME>
  grep -A1 "name: $2\$" <<<"$1" | grep -m1 'value:' | sed -E 's/.*value: "?//; s/"$//'
}
for kv in 'KAFKA_BROKERS=kafka.example.invalid:9092' 'EVENT_PUBLISHER=kafka' \
  'NODE_CONSUMER_GROUP=vendor-connectivity-node' 'LISTING_CONSUMER_GROUP=vendor-connectivity-listing' \
  'SOURCING_CONSUMER_GROUP=vendor-connectivity-sourcing' 'INBOUND_CONSUMER_GROUP=vendor-connectivity-inbound' \
  'PACKAGING_CONSUMER_GROUP=vendor-connectivity-packaging' \
  'NODE_TOPOLOGY_URL=http://node-topology.example.invalid' 'OFFER_AVAILABILITY_URL=http://offer-availability.example.invalid' \
  'SHIPPING_AND_TRACKING_URL=http://shipping-and-tracking.example.invalid' 'NODE_PERFORMANCE_URL=http://node-performance.example.invalid'; do
  [[ "$(env_value "$api_deployment" "${kv%%=*}")" == "${kv#*=}" ]] || fail "the api Deployment does not render $kv"
  grep -q "^  ${kv%%=*}: \"${kv#*=}\"" <<<"$configmap" || fail "the ConfigMap does not carry $kv"
done
# Optional values are absent by default (the binary's own defaults apply) and rendered when set.
for var in TX_PROCESSING_DELAY TX_FAILURE_RATE PO_REDELIVERY_RATE RATE_LIMIT_RPS SUBMISSION_POLL_INTERVAL \
  EXPIRY_SWEEP_INTERVAL OUTBOX_RELAY_INTERVAL SHUTDOWN_DRAIN_DELAY; do
  if grep -q "name: $var\$" <<<"$api_deployment"; then fail "$var must not be rendered by default"; fi
done
for kv in TX_PROCESSING_DELAY=500ms TX_FAILURE_RATE=0.1 PO_REDELIVERY_RATE=0.2 RATE_LIMIT_RPS=50 \
  SUBMISSION_POLL_INTERVAL=2s EXPIRY_SWEEP_INTERVAL=45s OUTBOX_RELAY_INTERVAL=2s SHUTDOWN_DRAIN_DELAY=8s; do
  [[ "$(env_value "$full_deployment" "${kv%%=*}")" == "${kv#*=}" ]] || fail "the api Deployment does not render $kv when set"
done
# DATABASE_URL comes from a Secret, never from the ConfigMap; MIGRATIONS_DATABASE_URL is optional.
grep -A4 'name: DATABASE_URL$' <<<"$api_deployment" | grep -q 'secretKeyRef:' || fail "DATABASE_URL must come from a Secret (secretKeyRef)"
grep -A6 'name: MIGRATIONS_DATABASE_URL$' <<<"$api_deployment" | grep -q 'optional: true' || fail "MIGRATIONS_DATABASE_URL must be an optional secretKeyRef"
if grep -qE '^  (MIGRATIONS_)?DATABASE_URL:' <<<"$configmap"; then fail "a DSN must never land in the ConfigMap"; fi
existing="$(helm template "$release" "$chart_dir" --set database.existingSecret=my-db "${base[@]:2}" --show-only templates/deployment.yaml)"
grep -A4 'name: DATABASE_URL$' <<<"$existing" | grep -q 'name: my-db' || fail "database.existingSecret is not used for DATABASE_URL"
existing_all="$(helm template "$release" "$chart_dir" --set database.existingSecret=my-db "${base[@]:2}")"
if grep -q 'kind: Secret' <<<"$existing_all"; then fail "no Secret may be rendered when database.existingSecret is set"; fi
# Pod hardening and probes.
for needle in 'runAsNonRoot: true' 'readOnlyRootFilesystem: true' 'allowPrivilegeEscalation: false' 'type: RuntimeDefault' '- ALL' \
  'path: /healthz' 'path: /readyz'; do
  grep -qF -- "$needle" <<<"$api_deployment" || fail "the api Deployment lacks: $needle"
done
grep -A2 'livenessProbe:' <<<"$api_deployment" | grep -q 'path: /healthz' || fail "liveness must probe /healthz"
grep -A2 'readinessProbe:' <<<"$api_deployment" | grep -q 'path: /readyz' || fail "readiness must probe /readyz"
grep -q 'terminationGracePeriodSeconds: 40' <<<"$api_deployment" || fail "terminationGracePeriodSeconds must default to 40"

# No mode, log publisher or in-memory wording is ever rendered.
render_all="$(helm template "$release" "$chart_dir" "${base[@]}" "${everything[@]}" --set autoscaling.api.enabled=true --set ingress.enabled=true --set gatewayApi.enabled=true)"
if grep -qE '[A-Z]_MODE' <<<"$render_all"; then fail "the render carries a *_MODE variable"; fi
if grep -qiE 'value: "?(permissive|log|off)"?$' <<<"$render_all"; then fail "the render carries permissive/log/off"; fi
if grep -qiE 'in-memory|silently runs|permissive' <<<"$render_all"$'\n'"$(cat "$chart_dir/templates/NOTES.txt")"; then
  fail "the render or NOTES.txt mentions an in-memory / permissive mode"
fi
# Only the api component exists.
if grep -E 'app.kubernetes.io/component: ' <<<"$render_all" | grep -qvE 'component: api$'; then
  fail "a component other than api rendered"
fi

# --- refusals: the mandatory values name themselves -----------------------------------
expect_refusal() { # expect_refusal <why> <needle> <helm args...>
  local why="$1" needle="$2" err; shift 2
  if err="$(helm template "$release" "$chart_dir" "$@" 2>&1)"; then
    fail "the chart must refuse to render: $why"
  fi
  grep -qF -- "$needle" <<<"$err" || fail "the refusal for '$why' does not name '$needle':
$err"
}
mandatory_except() { # mandatory_except <database|kafka|<config key>>
  local skip="$1" out=()
  [[ "$skip" == database ]] || out+=(--set database.url=postgres://u@h/db)
  [[ "$skip" == kafka ]] || out+=(--set kafka.brokers=k:9092)
  for g in "${groups[@]}" "${urls[@]}"; do
    [[ "${g%%:*}" == "$skip" ]] || out+=(--set "config.${g%%:*}=${g#*:}")
  done
  printf '%s\n' "${out[@]}"
}
refusal_without() { # refusal_without <value to leave out> <needle>
  local args=()
  while IFS= read -r line; do args+=("$line"); done < <(mandatory_except "$1")
  expect_refusal "no $1" "$2" "${args[@]}"
}
refusal_without database "database.url or database.existingSecret"
refusal_without kafka "kafka.brokers"
for g in "${groups[@]}" "${urls[@]}"; do refusal_without "${g%%:*}" "config.${g%%:*}"; done
# An explicitly blank value refuses too.
for g in "${groups[@]}" "${urls[@]}"; do
  expect_refusal "blank ${g%%:*}" "config.${g%%:*}" "${base[@]}" --set "config.${g%%:*}="
done
expect_refusal "the log publisher" "config.eventPublisher" "${base[@]}" --set config.eventPublisher=log
expect_refusal "an unknown publisher" "config.eventPublisher" "${base[@]}" --set config.eventPublisher=nats
expect_refusal "the log publisher through extraEnv" "EVENT_PUBLISHER" "${base[@]}" --set 'extraEnv[0].name=EVENT_PUBLISHER' --set 'extraEnv[0].value=log'
expect_refusal "a *_MODE through extraEnv" "_MODE" "${base[@]}" --set 'extraEnv[0].name=NODE_MODE' --set 'extraEnv[0].value=permissive'
# Shutdown room: 5 + 10 + 10 + 10 = 35s, so under 40 is refused.
expect_refusal "a grace period under 40s" "terminationGracePeriodSeconds" "${base[@]}" --set terminationGracePeriodSeconds=35
expect_refusal "a grace period of 39s" "terminationGracePeriodSeconds" "${base[@]}" --set terminationGracePeriodSeconds=39
expect_refusal "no grace period" "terminationGracePeriodSeconds" "${base[@]}" --set terminationGracePeriodSeconds=0
expect_refusal "a drain delay that eats the grace period" "terminationGracePeriodSeconds" "${base[@]}" --set config.shutdownDrainDelay=30s --set terminationGracePeriodSeconds=40
expect_refusal "a drain delay the chart cannot add up" "config.shutdownDrainDelay" "${base[@]}" --set config.shutdownDrainDelay=1m
helm template "$release" "$chart_dir" "${base[@]}" --set terminationGracePeriodSeconds=40 >/dev/null \
  || fail "a 40s grace period must still render"
helm template "$release" "$chart_dir" "${base[@]}" --set config.shutdownDrainDelay=30s --set terminationGracePeriodSeconds=61 >/dev/null \
  || fail "a 30s drain delay with a 61s grace period must render"

# --- observability (ADR 0008, fleet standard item 7) ---------------------------------
# The api renders the fleet pipeline's env: the Collector endpoint, the service
# name, the version (image tag, appVersion when empty) and the environment.
otel_endpoint="otel-collector.observability.svc.cluster.local:4317"
# Chart vs binary: every env var cmd/api/telemetry.go reads is rendered.
# shellcheck disable=SC2207
tel_vars=($(grep -ohE '(Getenv|getenv|env)\("[A-Z][A-Z0-9_]*"' "$repo_dir/cmd/api/telemetry.go" | sed -E 's/.*\("//; s/"$//' | sort -u))
(( ${#tel_vars[@]} >= 3 )) || fail "found only ${#tel_vars[@]} env names in cmd/api/telemetry.go; the scan is looking in the wrong place"
for var in "${tel_vars[@]}"; do
  grep -q "name: $var\$" <<<"$api_deployment" || fail "the api Deployment does not render $var, which cmd/api/telemetry.go reads"
done
[[ "$(env_value "$api_deployment" OTEL_EXPORTER_OTLP_ENDPOINT)" == "$otel_endpoint" ]] \
  || fail "the api Deployment does not render OTEL_EXPORTER_OTLP_ENDPOINT from otel.endpoint"
[[ "$(env_value "$api_deployment" OTEL_SERVICE_NAME)" == "vendor-connectivity" ]] \
  || fail "the api Deployment does not render OTEL_SERVICE_NAME=vendor-connectivity from otel.serviceName"
[[ "$(env_value "$api_deployment" SERVICE_VERSION)" == "0.1.0" ]] \
  || fail "SERVICE_VERSION must default to the chart appVersion"
[[ "$(env_value "$api_deployment" ENVIRONMENT)" == "production" ]] || fail "ENVIRONMENT must come from environment"
tagged="$(helm template "$release" "$chart_dir" "${base[@]}" --set image.tag=v9.9.9 --set otel.serviceName=vendors --set otel.endpoint=http://collector:4317 \
  --set environment=staging --show-only templates/deployment.yaml)"
[[ "$(env_value "$tagged" SERVICE_VERSION)" == "v9.9.9" ]] || fail "SERVICE_VERSION must follow image.tag"
[[ "$(env_value "$tagged" OTEL_SERVICE_NAME)" == "vendors" ]] || fail "OTEL_SERVICE_NAME must follow otel.serviceName"
[[ "$(env_value "$tagged" OTEL_EXPORTER_OTLP_ENDPOINT)" == "http://collector:4317" ]] || fail "OTEL_EXPORTER_OTLP_ENDPOINT must follow otel.endpoint"
[[ "$(env_value "$tagged" ENVIRONMENT)" == "staging" ]] || fail "ENVIRONMENT must follow environment"
# otel.enabled=false renders no OTEL_* env (SERVICE_VERSION stays: it also labels the logs' build).
otel_off="$(helm template "$release" "$chart_dir" "${base[@]}" --set otel.enabled=false --set mcp.enabled=true --set a2a.enabled=true)"
if grep -q 'name: OTEL_' <<<"$otel_off"; then fail "otel.enabled=false must render no OTEL_* env in any component"; fi
grep -q 'name: SERVICE_VERSION$' <<<"$otel_off" || fail "SERVICE_VERSION must still render with otel.enabled=false"
# The values are chart-owned: neither extraEnv may set them a second time.
for owned in OTEL_SERVICE_NAME OTEL_EXPORTER_OTLP_ENDPOINT SERVICE_VERSION ENVIRONMENT; do
  for key in extraEnv mcp.extraEnv a2a.extraEnv; do
    err="$(helm template "$release" "$chart_dir" "${base[@]}" --set "$key[0].name=$owned" --set "$key[0].value=x" 2>&1)" \
      && fail "$key must not be able to set $owned"
    grep -qF "extraEnv sets $owned" <<<"$err" || fail "the refusal for $key $owned does not name it: $err"
  done
done

# --- cmd/mcp: the mcp component -------------------------------------------------------
mcp_go="$repo_dir/cmd/mcp/main.go"
mcp_on=(--set mcp.enabled=true)
mcp_deployment="$(helm template "$release" "$chart_dir" "${base[@]}" "${mcp_on[@]}" --show-only templates/mcp-deployment.yaml)"
mcp_service="$(helm template "$release" "$chart_dir" "${base[@]}" "${mcp_on[@]}" --show-only templates/mcp-service.yaml)"
[[ "$(awk '/^  name:/ {print $2; exit}' <<<"$mcp_service")" == "$release-mcp" ]] || fail "the mcp Service is not named $release-mcp"
grep -q 'app.kubernetes.io/component: mcp' <<<"$mcp_deployment" || fail "the mcp Deployment lacks component=mcp"
grep -q 'command: \["/app/mcp"\]' <<<"$mcp_deployment" || fail "the mcp Deployment must run /app/mcp"
grep -A2 'livenessProbe:' <<<"$mcp_deployment" | grep -q 'path: /healthz' || fail "mcp liveness must probe /healthz"
grep -q 'terminationGracePeriodSeconds: 40' <<<"$mcp_deployment" || fail "the mcp Deployment must carry the grace period"

# Chart vs binary: every env var cmd/mcp reads must be rendered into the mcp
# Deployment; and nothing the binary never reads (Kafka, publisher, consumer
# groups, api tuning) may appear on the mcp pod.
# shellcheck disable=SC2207
mcp_vars=($(grep -ohE '(Getenv|getenv|env)\("[A-Z][A-Z0-9_]*"' "$mcp_go" "$repo_dir/cmd/mcp/telemetry.go" | sed -E 's/.*\("//; s/"$//' | sort -u))
# The neighbour URL names live in cmd/mcp/main.go's own neighbourURLs slice.
# shellcheck disable=SC2207
mcp_slice=($(grep -E '^var neighbourURLs = ' "$mcp_go" | grep -oE '"[A-Z][A-Z0-9_]*"' | tr -d '"' | sort -u))
mcp_vars=($(printf '%s\n' "${mcp_vars[@]}" "${mcp_slice[@]}" | sort -u))
(( ${#mcp_vars[@]} >= 6 )) || fail "found only ${#mcp_vars[@]} env names in cmd/mcp/main.go; the scan is looking in the wrong place"
for var in "${mcp_vars[@]}"; do
  grep -q "name: $var\$" <<<"$mcp_deployment" || fail "the mcp Deployment does not render $var, which cmd/mcp reads"
done
for forbidden in KAFKA_BROKERS EVENT_PUBLISHER NODE_CONSUMER_GROUP LISTING_CONSUMER_GROUP SOURCING_CONSUMER_GROUP \
  OUTBOX_RELAY_INTERVAL SUBMISSION_POLL_INTERVAL EXPIRY_SWEEP_INTERVAL RATE_LIMIT_RPS HTTP_ADDR; do
  if grep -q "name: $forbidden\$" <<<"$mcp_deployment"; then fail "the mcp Deployment renders $forbidden, which cmd/mcp never reads"; fi
done
grep -A4 'name: DATABASE_URL$' <<<"$mcp_deployment" | grep -q 'secretKeyRef:' || fail "mcp DATABASE_URL must come from a Secret"
grep -A6 'name: MIGRATIONS_DATABASE_URL$' <<<"$mcp_deployment" | grep -q 'optional: true' || fail "mcp MIGRATIONS_DATABASE_URL must be an optional secretKeyRef"
[[ "$(env_value "$mcp_deployment" MCP_ADDR)" == ":8090" ]] || fail "the mcp Deployment must default MCP_ADDR to :8090"
[[ "$(env_value "$mcp_deployment" NODE_PERFORMANCE_URL)" == "http://node-performance.example.invalid" ]] \
  || fail "the mcp Deployment must render the neighbour URLs from config.*Url"

# The mcp component exports as <otel.serviceName>-mcp to the same Collector, with the api's version.
[[ "$(env_value "$mcp_deployment" OTEL_SERVICE_NAME)" == "vendor-connectivity-mcp" ]] || fail "the mcp component must export as <otel.serviceName>-mcp"
[[ "$(env_value "$mcp_deployment" OTEL_EXPORTER_OTLP_ENDPOINT)" == "$otel_endpoint" ]] || fail "the mcp component must export to otel.endpoint"
[[ "$(env_value "$mcp_deployment" SERVICE_VERSION)" == "0.1.0" ]] || fail "the mcp SERVICE_VERSION must default to the image tag / appVersion, like the api"
[[ "$(env_value "$mcp_deployment" ENVIRONMENT)" == "production" ]] || fail "ENVIRONMENT must come from mcp.environment"
mcp_named="$(helm template "$release" "$chart_dir" "${base[@]}" --set mcp.enabled=true --set otel.serviceName=vendors --set mcp.serviceVersion=dev --show-only templates/mcp-deployment.yaml)"
[[ "$(env_value "$mcp_named" OTEL_SERVICE_NAME)" == "vendors-mcp" ]] || fail "the mcp service name must follow otel.serviceName"
[[ "$(env_value "$mcp_named" SERVICE_VERSION)" == "dev" ]] || fail "SERVICE_VERSION must come from mcp.serviceVersion when set"

# The mcp route is off by default and rewrites /api/mcp/vendor-connectivity to /mcp.
mcp_all_on=(--set mcp.enabled=true --set mcp.route.enabled=true --set 'mcp.route.parentRefs[0].name=warehouse-gateway' --set 'mcp.route.parentRefs[0].namespace=kong')
mcp_route="$(helm template "$release" "$chart_dir" "${base[@]}" "${mcp_all_on[@]}" --show-only templates/mcp-route.yaml)"
grep -q "name: $release-mcp" <<<"$mcp_route" || fail "the mcp HTTPRoute is not named $release-mcp"
grep -q 'value: /api/mcp/vendor-connectivity' <<<"$mcp_route" || fail "the mcp route path is not /api/mcp/vendor-connectivity"
grep -q 'replacePrefixMatch: /mcp' <<<"$mcp_route" || fail "the mcp route must rewrite the prefix to /mcp"
grep -q "name: $release-mcp" <<<"$mcp_route" || fail "the mcp route must back the mcp Service"
mcp_no_route="$(helm template "$release" "$chart_dir" "${base[@]}" --set mcp.enabled=true 2>/dev/null | grep -c 'kind: HTTPRoute' || true)"
[[ "$mcp_no_route" == "0" ]] || fail "the mcp HTTPRoute must be off unless mcp.route.enabled"

# --- cmd/a2a: the a2a component -------------------------------------------------------
a2a_go="$repo_dir/cmd/a2a/main.go"
a2a_on=(--set a2a.enabled=true)
a2a_deployment="$(helm template "$release" "$chart_dir" "${base[@]}" "${a2a_on[@]}" --show-only templates/a2a-deployment.yaml)"
a2a_service="$(helm template "$release" "$chart_dir" "${base[@]}" "${a2a_on[@]}" --show-only templates/a2a-service.yaml)"
[[ "$(awk '/^  name:/ {print $2; exit}' <<<"$a2a_service")" == "$release-a2a" ]] || fail "the a2a Service is not named $release-a2a"
grep -q 'app.kubernetes.io/component: a2a' <<<"$a2a_deployment" || fail "the a2a Deployment lacks component=a2a"
grep -q 'command: \["/app/a2a"\]' <<<"$a2a_deployment" || fail "the a2a Deployment must run /app/a2a"
grep -A2 'livenessProbe:' <<<"$a2a_deployment" | grep -q 'path: /healthz' || fail "a2a liveness must probe /healthz"
grep -q 'terminationGracePeriodSeconds: 40' <<<"$a2a_deployment" || fail "the a2a Deployment must carry the grace period"

# Chart vs binary: every env var cmd/a2a reads must be rendered into the a2a
# Deployment; and nothing the binary never reads (Kafka, publisher, consumer
# groups, api tuning) may appear on the a2a pod.
# shellcheck disable=SC2207
a2a_vars=($(grep -ohE '(Getenv|getenv|env)\("[A-Z][A-Z0-9_]*"' "$a2a_go" "$repo_dir/cmd/a2a/telemetry.go" | sed -E 's/.*\("//; s/"$//' | sort -u))
# The neighbour URL names live in cmd/a2a/main.go's own neighbourURLs slice.
# shellcheck disable=SC2207
a2a_slice=($(grep -E '^var neighbourURLs = ' "$a2a_go" | grep -oE '"[A-Z][A-Z0-9_]*"' | tr -d '"' | sort -u))
# Every "<VAR> is required" boot error names a mandatory variable (the regex
# allows digits: A2A_AGENT_URL must not be read as A_AGENT_URL).
# shellcheck disable=SC2207
a2a_required=($(grep -ohE '[A-Z][A-Z0-9_]+ is required' $(ls "$repo_dir"/cmd/a2a/*.go | grep -v '_test\.go$') | sed -E 's/ is required//' | sort -u || true))
(( ${#a2a_required[@]} >= 2 )) || fail "found only ${#a2a_required[@]} 'is required' boot errors in cmd/a2a; the scan is looking in the wrong place"
printf '%s\n' "${a2a_required[@]}" | grep -qx 'A2A_AGENT_URL' || fail "the 'is required' scan did not find A2A_AGENT_URL in cmd/a2a (regex must allow digits)"
a2a_vars=($(printf '%s\n' "${a2a_vars[@]}" "${a2a_slice[@]}" "${a2a_required[@]}" | sort -u))
(( ${#a2a_vars[@]} >= 7 )) || fail "found only ${#a2a_vars[@]} env names in cmd/a2a/main.go; the scan is looking in the wrong place"
for var in "${a2a_vars[@]}"; do
  grep -q "name: $var\$" <<<"$a2a_deployment" || fail "the a2a Deployment does not render $var, which cmd/a2a reads"
done
for forbidden in KAFKA_BROKERS EVENT_PUBLISHER NODE_CONSUMER_GROUP LISTING_CONSUMER_GROUP SOURCING_CONSUMER_GROUP \
  OUTBOX_RELAY_INTERVAL SUBMISSION_POLL_INTERVAL EXPIRY_SWEEP_INTERVAL RATE_LIMIT_RPS HTTP_ADDR MCP_ADDR; do
  if grep -q "name: $forbidden\$" <<<"$a2a_deployment"; then fail "the a2a Deployment renders $forbidden, which cmd/a2a never reads"; fi
done
grep -A4 'name: DATABASE_URL$' <<<"$a2a_deployment" | grep -q 'secretKeyRef:' || fail "a2a DATABASE_URL must come from a Secret"
grep -A6 'name: MIGRATIONS_DATABASE_URL$' <<<"$a2a_deployment" | grep -q 'optional: true' || fail "a2a MIGRATIONS_DATABASE_URL must be an optional secretKeyRef"
[[ "$(env_value "$a2a_deployment" A2A_ADDR || true)" == ":8093" ]] || fail "the a2a Deployment must default A2A_ADDR to :8093"
[[ "$(env_value "$a2a_deployment" NODE_PERFORMANCE_URL || true)" == "http://node-performance.example.invalid" ]] \
  || fail "the a2a Deployment must render the neighbour URLs from config.*Url"

# The Agent Card URL defaults to this release's in-cluster Service (the namespace
# comes from the release, so the pattern is matched, not compared) and a2a.agentUrl overrides it.
a2a_url="$(env_value "$a2a_deployment" A2A_AGENT_URL || true)"
[[ "$a2a_url" =~ ^http://$release-a2a\.[a-z0-9-]+\.svc\.cluster\.local:8093/$ ]] \
  || fail "A2A_AGENT_URL must default to http://$release-a2a.<ns>.svc.cluster.local:8093/, got '$a2a_url'"
a2a_url_set="$(helm template "$release" "$chart_dir" "${base[@]}" "${a2a_on[@]}" --set a2a.agentUrl=https://agents.example.invalid/api/a2a/vendor-connectivity/ --show-only templates/a2a-deployment.yaml)"
[[ "$(env_value "$a2a_url_set" A2A_AGENT_URL || true)" == "https://agents.example.invalid/api/a2a/vendor-connectivity/" ]] \
  || fail "a2a.agentUrl must override A2A_AGENT_URL"

# The a2a component exports as <otel.serviceName>-a2a to the same Collector, with the api's version.
[[ "$(env_value "$a2a_deployment" OTEL_SERVICE_NAME || true)" == "vendor-connectivity-a2a" ]] || fail "the a2a component must export as <otel.serviceName>-a2a"
[[ "$(env_value "$a2a_deployment" OTEL_EXPORTER_OTLP_ENDPOINT || true)" == "$otel_endpoint" ]] || fail "the a2a component must export to otel.endpoint"
[[ "$(env_value "$a2a_deployment" SERVICE_VERSION || true)" == "0.1.0" ]] || fail "the a2a SERVICE_VERSION must default to the image tag / appVersion, like the api"
[[ "$(env_value "$a2a_deployment" ENVIRONMENT || true)" == "production" ]] || fail "ENVIRONMENT must come from a2a.environment"
a2a_named="$(helm template "$release" "$chart_dir" "${base[@]}" --set a2a.enabled=true --set otel.serviceName=vendors --set a2a.serviceVersion=dev --show-only templates/a2a-deployment.yaml)"
[[ "$(env_value "$a2a_named" OTEL_SERVICE_NAME || true)" == "vendors-a2a" ]] || fail "the a2a service name must follow otel.serviceName"
[[ "$(env_value "$a2a_named" SERVICE_VERSION || true)" == "dev" ]] || fail "SERVICE_VERSION must come from a2a.serviceVersion when set"

# a2a.extraEnv may not bring back a log publisher or a mode switch.
err="$(helm template "$release" "$chart_dir" "${base[@]}" "${a2a_on[@]}" --set 'a2a.extraEnv[0].name=EVENT_PUBLISHER' --set 'a2a.extraEnv[0].value=log' 2>&1)" \
  && fail "a2a.extraEnv must not be able to set EVENT_PUBLISHER=log"
grep -qF 'extraEnv sets EVENT_PUBLISHER=log' <<<"$err" || fail "the refusal for a2a.extraEnv EVENT_PUBLISHER does not name it: $(head -n 3 <<<"$err")"
err="$(helm template "$release" "$chart_dir" "${base[@]}" "${a2a_on[@]}" --set 'a2a.extraEnv[0].name=NODE_MODE' --set 'a2a.extraEnv[0].value=permissive' 2>&1)" \
  && fail "a2a.extraEnv must not be able to set a *_MODE"
grep -qF 'extraEnv sets NODE_MODE' <<<"$err" || fail "the refusal for a2a.extraEnv NODE_MODE does not name it: $(head -n 3 <<<"$err")"

# The a2a route is off by default and rewrites /api/a2a/vendor-connectivity to /
# (so the Agent Card stays at <prefix>/.well-known/agent-card.json).
a2a_all_on=(--set a2a.enabled=true --set a2a.route.enabled=true --set 'a2a.route.parentRefs[0].name=warehouse-gateway' --set 'a2a.route.parentRefs[0].namespace=kong')
a2a_route="$(helm template "$release" "$chart_dir" "${base[@]}" "${a2a_all_on[@]}" --show-only templates/a2a-route.yaml)"
grep -q "name: $release-a2a" <<<"$a2a_route" || fail "the a2a HTTPRoute is not named $release-a2a"
grep -q 'value: /api/a2a/vendor-connectivity' <<<"$a2a_route" || fail "the a2a route path is not /api/a2a/vendor-connectivity"
grep -q 'replacePrefixMatch: /$' <<<"$a2a_route" || fail "the a2a route must rewrite the prefix to /"
grep -q 'port: 8093' <<<"$a2a_route" || fail "the a2a route must back the a2a Service port 8093"
a2a_no_route="$(helm template "$release" "$chart_dir" "${base[@]}" --set a2a.enabled=true 2>/dev/null | grep -c 'kind: HTTPRoute' || true)"
[[ "$a2a_no_route" == "0" ]] || fail "the a2a HTTPRoute must be off unless a2a.route.enabled"

# --- web/: the frontend component ----------------------------------------------------
front_on=(--set frontend.enabled=true)
front_deployment="$(helm template "$release" "$chart_dir" "${base[@]}" "${front_on[@]}" --show-only templates/frontend-deployment.yaml)"
front_service="$(helm template "$release" "$chart_dir" "${base[@]}" "${front_on[@]}" --show-only templates/frontend-service.yaml)"
[[ "$(awk '/^  name:/ {print $2; exit}' <<<"$front_service")" == "$release-frontend" ]] || fail "the frontend Service is not named $release-frontend"
grep -q 'app.kubernetes.io/component: frontend' <<<"$front_deployment" || fail "the frontend Deployment lacks component=frontend"
grep -q 'type: ClusterIP' <<<"$front_service" || fail "the frontend Service must stay ClusterIP"
grep -A2 'readinessProbe:' <<<"$front_deployment" | grep -q 'path: /healthz' || fail "frontend readiness must probe /healthz"
grep -q 'readOnlyRootFilesystem: true' <<<"$front_deployment" || fail "the frontend container must run read-only"
if grep -qE '^\s+env:' <<<"$front_deployment"; then fail "the frontend container must render no env (static bytes only)"; fi

# With every component on, exactly api, mcp, a2a and frontend render, and each
# Service selects exactly its own component (see test_service_selectors.py
# for the selector proof).
all_components="$(helm template "$release" "$chart_dir" "${base[@]}" "${mcp_all_on[@]}" "${a2a_all_on[@]}" "${front_on[@]}")"
components="$(grep -E 'app.kubernetes.io/component: ' <<<"$all_components" | sed -E 's/^[[:space:]]+//; s/.*component: //' | sort -u | tr '\n' ' ')"
[[ "$components" == "a2a api frontend mcp " ]] || fail "components with everything on = '$components', want 'a2a api frontend mcp'"

# Off by default: neither the mcp, the a2a nor the frontend workload renders.
if grep -qE 'component: (mcp|a2a|frontend)$' <<<"$render_all"; then
  fail "the mcp/a2a/frontend components must be off by default"
fi

echo "PASS: vendor-connectivity chart render assertions"
