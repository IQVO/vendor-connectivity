#!/usr/bin/env python3
"""Assert this chart's Services select exactly one Deployment each.

Why this exists (fleet-wide): `app.kubernetes.io/name` + `instance` are identical
on every pod a release creates. A Service that selects on those two alone selects
ALL of them (a Kong request for an api /healthz was once answered by the wrong
pod). The Deployment carries `app.kubernetes.io/component` in selector.matchLabels
and its pod labels, and the Service selects on it, so each Service selects
EXACTLY ONE Deployment. This test fails if that ever stops being true.

This chart has three components: api (on by default), mcp and frontend (off by
default, enabled together here). The optional HPA, Ingress and HTTPRoutes are
turned on too and must all point at this chart's Services. Run:
python3 charts/vendor-connectivity/tests/test_service_selectors.py
Needs: helm, PyYAML (a missing PyYAML prints SKIP, which CI treats as a failure).
"""

from __future__ import annotations

import subprocess
import sys
from pathlib import Path

CHART_DIR = Path(__file__).resolve().parents[1]
RELEASE = "vendor-connectivity"

GROUPS = {
    "nodeConsumerGroup": "vendor-connectivity-node",
    "listingConsumerGroup": "vendor-connectivity-listing",
    "sourcingConsumerGroup": "vendor-connectivity-sourcing",
}
URLS = {
    "nodeTopologyUrl": "http://node-topology.example.invalid",
    "offerAvailabilityUrl": "http://offer-availability.example.invalid",
    "shippingAndTrackingUrl": "http://shipping-and-tracking.example.invalid",
    "nodePerformanceUrl": "http://node-performance.example.invalid",
}


def runtime(skip: str | None = None) -> list[str]:
    """The mandatory values, minus the one named by skip (database, kafka or a config key)."""
    args: list[str] = []
    if skip != "database":
        args += ["--set", "database.url=postgres://u@example.invalid:5432/db"]
    if skip != "kafka":
        args += ["--set", "kafka.brokers=kafka.example.invalid:9092"]
    for key, value in {**GROUPS, **URLS}.items():
        if skip != key:
            args += ["--set", f"config.{key}={value}"]
    return args


BASE = runtime()
ENABLE_EVERYTHING = BASE + [
    "--set", "autoscaling.api.enabled=true",
    "--set", "gatewayApi.enabled=true",
    "--set", "ingress.enabled=true",
    "--set", "config.txProcessingDelay=500ms",
    "--set", "mcp.enabled=true",
    "--set", "mcp.route.enabled=true",
    "--set", "frontend.enabled=true",
]


def helm_template(extra_args: list[str]) -> subprocess.CompletedProcess:
    return subprocess.run(
        ["helm", "template", RELEASE, str(CHART_DIR), *extra_args],
        capture_output=True, text=True,
    )


def render(extra_args: list[str]) -> list[dict]:
    result = helm_template(extra_args)
    if result.returncode != 0:
        raise SystemExit(f"FAIL: helm template {' '.join(extra_args)}:\n{result.stderr}")
    try:
        import yaml  # type: ignore
    except ModuleNotFoundError:  # pragma: no cover - environment guard
        print("SKIP: PyYAML not available; cannot assert selectors")
        raise SystemExit(0)
    return [d for d in yaml.safe_load_all(result.stdout) if d]


def selector_of(doc: dict) -> dict:
    return doc.get("spec", {}).get("selector") or {}


def pod_labels_of(doc: dict) -> dict:
    return doc.get("spec", {}).get("template", {}).get("metadata", {}).get("labels") or {}


def matches(selector: dict, labels: dict) -> bool:
    return bool(selector) and all(labels.get(k) == v for k, v in selector.items())


def check_components(docs: list[dict], failures: list[str]) -> None:
    services = {d["metadata"]["name"]: d for d in docs if d.get("kind") == "Service"}
    deployments = {d["metadata"]["name"]: d for d in docs if d.get("kind") == "Deployment"}
    api, mcp, frontend = RELEASE, f"{RELEASE}-mcp", f"{RELEASE}-frontend"

    if set(deployments) != {api, mcp, frontend}:
        failures.append(f"expected exactly the api/mcp/frontend Deployments, got {sorted(deployments)}")
    if set(services) != {api, mcp, frontend}:
        failures.append(f"expected exactly the api/mcp/frontend Services, got {sorted(services)}")
    if api in services and selector_of(services[api]).get("app.kubernetes.io/component") != "api":
        failures.append("the api Service selector must pin component=api")
    if mcp in services and selector_of(services[mcp]).get("app.kubernetes.io/component") != "mcp":
        failures.append("the mcp Service selector must pin component=mcp")
    if frontend in services and selector_of(services[frontend]).get("app.kubernetes.io/component") != "frontend":
        failures.append("the frontend Service selector must pin component=frontend")

    # Every Deployment's own selector must pin a component too, and be satisfied by its pod labels.
    for name, dep in deployments.items():
        match_labels = dep["spec"]["selector"].get("matchLabels") or {}
        if "app.kubernetes.io/component" not in match_labels:
            failures.append(f"Deployment {name} selector.matchLabels lacks app.kubernetes.io/component")
        if not matches(match_labels, pod_labels_of(dep)):
            failures.append(f"Deployment {name} pod labels do not satisfy its own selector")

    # The real invariant: each Service selects exactly one Deployment.
    for svc_name, svc in services.items():
        sel = selector_of(svc)
        hit = [d for d, dep in deployments.items() if matches(sel, pod_labels_of(dep))]
        if len(hit) != 1:
            failures.append(f"Service {svc_name} selects {len(hit)} Deployments {sorted(hit)}; expected exactly 1")

    # The HPA owns replicas when enabled and scales the api Deployment.
    hpa = next((d for d in docs if d.get("kind") == "HorizontalPodAutoscaler"), None)
    if hpa is None or hpa["spec"]["scaleTargetRef"]["name"] != RELEASE:
        failures.append("the api HPA was not rendered (or does not scale the api Deployment)")
    elif "replicas" in deployments.get(RELEASE, {}).get("spec", {}):
        failures.append("the api Deployment must omit replicas when its HPA owns them")

    # The Ingress and the HTTPRoutes route to this chart's Services, never to a
    # pod directly; the mcp route backs the mcp Service.
    ingress = next((d for d in docs if d.get("kind") == "Ingress"), None)
    if ingress is None:
        failures.append("the Ingress was not rendered with ingress.enabled=true")
    else:
        for rule in ingress["spec"]["rules"]:
            for path in rule["http"]["paths"]:
                if path["backend"]["service"]["name"] not in services:
                    failures.append("the Ingress backend is not a Service of this chart")
    for route in (d for d in docs if d.get("kind") == "HTTPRoute"):
        for rule in route["spec"]["rules"]:
            for ref in rule["backendRefs"]:
                if ref["name"] not in services:
                    failures.append(f"the HTTPRoute {route['metadata']['name']} backend {ref['name']} is not a Service of this chart")
    mcp_route = next((d for d in docs if d.get("kind") == "HTTPRoute" and d["metadata"]["name"] == mcp), None)
    if mcp_route is None:
        failures.append("the mcp HTTPRoute was not rendered with mcp.route.enabled=true")
    elif mcp_route["spec"]["rules"][0]["backendRefs"][0]["name"] != mcp:
        failures.append("the mcp HTTPRoute must back the mcp Service")


def check_refusals(failures: list[str]) -> None:
    cases: list[tuple[str, list[str], str]] = [
        ("without a database source", runtime("database"), "requires database.url or database.existingSecret"),
        ("without kafka.brokers", runtime("kafka"), "kafka.brokers"),
        ("the log publisher", BASE + ["--set", "config.eventPublisher=log"], "the only publisher is"),
        ("an unknown publisher", BASE + ["--set", "config.eventPublisher=nats"], "the only publisher is"),
        ("EVENT_PUBLISHER=log through extraEnv",
         BASE + ["--set", "extraEnv[0].name=EVENT_PUBLISHER", "--set", "extraEnv[0].value=log"], "extraEnv sets EVENT_PUBLISHER=log"),
        ("a *_MODE through extraEnv",
         BASE + ["--set", "extraEnv[0].name=NODE_MODE", "--set", "extraEnv[0].value=permissive"], "extraEnv sets NODE_MODE"),
        ("a grace period under 40s", BASE + ["--set", "terminationGracePeriodSeconds=35"], "terminationGracePeriodSeconds"),
    ]
    for key in {**GROUPS, **URLS}:
        cases.append((f"without config.{key}", runtime(key), f"config.{key} is required"))
    for label, args, needle in cases:
        result = helm_template(args)
        if result.returncode == 0 or needle not in result.stderr:
            failures.append(f"chart rendered (or failed for another reason) {label}")


def main() -> int:
    failures: list[str] = []

    check_components(render(ENABLE_EVERYTHING), failures)

    # Default values must not deploy an HPA, a route, an Ingress or the
    # optional mcp/frontend workloads.
    defaults = render(BASE)
    stray = [d["kind"] for d in defaults if d.get("kind") in
             {"HorizontalPodAutoscaler", "Ingress", "HTTPRoute", "Deployment"}]
    if len(stray) != 1:
        failures.append(f"default values must render exactly the api Deployment, got {stray}")
    stray_components = {pod_labels_of(d).get("app.kubernetes.io/component") for d in defaults if d.get("kind") == "Deployment"}
    if stray_components != {"api"}:
        failures.append(f"default values must render only component=api, got {stray_components}")

    check_refusals(failures)

    if failures:
        for f in failures:
            print(f"FAIL: {f}")
        return 1

    print("PASS: every Service (api, mcp, frontend) selects exactly one Deployment via its component label; "
          "the HPA, Ingress and HTTPRoutes are off by default and target this chart's Services; the mcp route "
          "backs the mcp Service; the chart refuses to render without a database source, brokers, any consumer "
          "group or neighbour URL, with a log/unknown publisher or a *_MODE in extraEnv, or with a grace period "
          "under 40s")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
