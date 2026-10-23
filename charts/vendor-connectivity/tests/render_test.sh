#!/usr/bin/env bash
# Chart render assertions: the env cmd/api reads is the env the chart renders,
# the mandatory configuration has no silent default, no mode switch or log
# publisher is ever rendered, and the shutdown room covers the binary's bounds.
# Run: bash charts/vendor-connectivity/tests/render_test.sh   (needs helm)
set -euo pipefail

chart_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo_dir="$(cd "$chart_dir/../.." && pwd)"
release=vendor-connectivity
main_go="$repo_dir/cmd/api/main.go"

# Dummy values, no password: the chart refuses to render without a database
# source, Kafka brokers, the three consumer groups and the four neighbour URLs.
# None is ever dialled.
groups=(nodeConsumerGroup:vendor-connectivity-node
  listingConsumerGroup:vendor-connectivity-listing
  sourcingConsumerGroup:vendor-connectivity-sourcing)
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
  grep -ohE '(Getenv|getenv|env)\("[A-Z][A-Z0-9_]*"' "$main_go" | sed -E 's/.*\("//; s/"$//' | sort -u
}
slice_env() { # slice_env <go slice variable>
  grep -E "^var $1 = " "$main_go" | grep -oE '"[A-Z][A-Z0-9_]*"' | tr -d '"' | sort -u
}
# Word splitting is safe: the names are [A-Z0-9_] only (bash 3.2 has no mapfile).
# shellcheck disable=SC2207
api_vars=($( { literal_env; slice_env consumerGroups; slice_env neighbourURLs; } | sort -u))
(( ${#api_vars[@]} >= 20 )) || fail "found only ${#api_vars[@]} env names in cmd/api/main.go; the scan is looking in the wrong place"
(( $(slice_env consumerGroups | wc -l) == 3 )) || fail "expected 3 consumer groups in cmd/api/main.go, found $(slice_env consumerGroups | wc -l)"
(( $(slice_env neighbourURLs | wc -l) == 4 )) || fail "expected 4 neighbour URLs in cmd/api/main.go, found $(slice_env neighbourURLs | wc -l)"
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
for var in $(grep -ho 'errors.New("[A-Z_]* is required' "$main_go" | sed -E 's/errors.New\("//; s/ is required//' | sort -u); do
  grep -q "name: $var\$" <<<"$api_deployment" || fail "the api Deployment does not render $var, which cmd/api requires"
done

# --- the mandatory configuration is rendered from values ------------------------------
env_value() { # env_value <rendered> <NAME>
  grep -A1 "name: $2\$" <<<"$1" | grep -m1 'value:' | sed -E 's/.*value: "?//; s/"$//'
}
for kv in 'KAFKA_BROKERS=kafka.example.invalid:9092' 'EVENT_PUBLISHER=kafka' \
  'NODE_CONSUMER_GROUP=vendor-connectivity-node' 'LISTING_CONSUMER_GROUP=vendor-connectivity-listing' \
  'SOURCING_CONSUMER_GROUP=vendor-connectivity-sourcing' \
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

echo "PASS: vendor-connectivity chart render assertions"
