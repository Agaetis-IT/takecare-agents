#!/usr/bin/env python3
"""Automatic scoring of one investigation, fixed before the first measured run.

Two columns, both computed, nobody reads a diagnosis by hand:

1. chain_correct (the official score): the chain rule of spec 04, applied to the
   diagnosis text. Case-insensitive; every part must be named.
2. evidence_read (objective, not a score): did the run call a tool that reads
   the decisive evidence of the scenario? Checked on the tool calls recorded
   for the run, with their arguments, no human judgement. It catches the right
   answer given without having looked (with the spec 04 rule, an answer that
   says "the proxy idle timeout" passes whether or not the DBProxy was read).

Chain rules (parts are ANDed; the alternatives inside a part are ORed):

  proxy-timeout       spec 04 as written: names the proxy ("proxy") AND the idle
                      timeout ("idle", "idleClientTimeout", "600 s").
  bad-config-rollout  names the ConfigMap ("configmap", "orders-config") AND the
                      wrong host ("orders-db-primary").
  missing-secret-key  names the missing key ("SMTP_PASSWORD") AND the Secret it
                      is read from ("notifications-smtp", "secret").

Decisive evidence (a call to a cluster-reading tool whose flattened arguments
match):

  proxy-timeout       the DBProxy resource      -> /dbproxy|dbproxies/
  bad-config-rollout  the ConfigMap or the logs -> /orders-config|orders-api/
  missing-secret-key  the pod logs or the spec  -> /notifications-worker/

A run with no recorded tool call at all is an instrumentation error, not a
score (an empty scan must fail, never pass).

Usage:  bench_score.py --selftest
Import: chain_correct(scenario, text), evidence_read(scenario, calls)
"""
import re
import sys

CHAIN = {
    "proxy-timeout": [
        ["proxy"],
        ["idle", "idleclienttimeout", "600 s"],
    ],
    "bad-config-rollout": [
        ["configmap", "orders-config"],
        ["orders-db-primary"],
    ],
    "missing-secret-key": [
        ["smtp_password"],
        ["notifications-smtp", "secret"],
    ],
}

EVIDENCE = {
    "proxy-timeout": re.compile(r"dbproxy|dbproxies", re.I),
    "bad-config-rollout": re.compile(r"orders-config|orders-api", re.I),
    "missing-secret-key": re.compile(r"notifications-worker", re.I),
}

# Tools that read the cluster (not the corpus): kubernetes-mcp-server's tools and
# HolmesGPT's native Kubernetes tools.
CLUSTER_READ = re.compile(
    r"^(pods_|resources_|events_list|fetch_pod_logs|kubernetes_|kubectl_)", re.I
)


def chain_correct(scenario, text):
    low = (text or "").lower()
    return all(any(alt.lower() in low for alt in part) for part in CHAIN[scenario])


def evidence_read(scenario, calls):
    """calls: [{"tool": str, "args": str}]. Raises if there are none."""
    if not calls:
        raise ValueError("no tool call recorded: instrumentation error, not a score")
    pat = EVIDENCE[scenario]
    return any(CLUSTER_READ.match(c["tool"]) and pat.search(c.get("args") or "") for c in calls)


def selftest():
    n = 0

    def check(label, got, want):
        nonlocal n
        n += 1
        if got != want:
            print(f"FAIL {label}: got {got!r}, want {want!r}")
            sys.exit(1)

    # chain rule: positives
    check("proxy ok", chain_correct("proxy-timeout", "The RDS Proxy idleClientTimeout (600 s) is shorter than the pool's 900 s."), True)
    check("proxy ok bare words", chain_correct("proxy-timeout", "the database PROXY has an IDLE limit"), True)
    check("config ok", chain_correct("bad-config-rollout", "ConfigMap orders-config sets DB_HOST to orders-db-primary, which does not exist."), True)
    check("secret ok", chain_correct("missing-secret-key", "SMTP_PASSWORD is not in secret notifications-smtp."), True)
    # chain rule: negatives (each part is required, an empty text fails)
    check("proxy memory lead", chain_correct("proxy-timeout", "Raise the memory limit of billing-api."), False)
    check("proxy only", chain_correct("proxy-timeout", "The proxy is unhealthy."), False)
    check("proxy empty", chain_correct("proxy-timeout", ""), False)
    check("proxy None", chain_correct("proxy-timeout", None), False)
    check("config no host", chain_correct("bad-config-rollout", "The ConfigMap was changed."), False)
    check("config host only", chain_correct("bad-config-rollout", "orders-db-primary does not resolve."), False)
    check("config oom", chain_correct("bad-config-rollout", "The pod was OOMKilled."), False)
    check("secret oom", chain_correct("missing-secret-key", "The container was OOMKilled (exit 137)."), False)
    check("secret key only", chain_correct("missing-secret-key", "SMTP_PASSWORD is missing."), False)
    # evidence column: positives
    check("ev proxy mcp", evidence_read("proxy-timeout", [{"tool": "resources_get", "args": '{"kind":"DBProxy","name":"billing-db-proxy"}'}]), True)
    check("ev proxy holmes", evidence_read("proxy-timeout", [{"tool": "kubernetes_jq_query", "args": '{"kind":"dbproxies.rds.services.k8s.aws"}'}]), True)
    check("ev config logs", evidence_read("bad-config-rollout", [{"tool": "pods_log", "args": '{"name":"orders-api-5cb8fd6866-sczx9"}'}]), True)
    check("ev secret logs", evidence_read("missing-secret-key", [{"tool": "fetch_pod_logs", "args": '{"pod_name":"notifications-worker-74855966d-k89l4"}'}]), True)
    # evidence column: negatives, including a corpus tool that merely mentions the name
    check("ev proxy corpus only", evidence_read("proxy-timeout", [{"tool": "deployments_search", "args": '{"query":"billing dbproxy idleClientTimeout"}'}]), False)
    check("ev proxy other kind", evidence_read("proxy-timeout", [{"tool": "resources_get", "args": '{"kind":"Deployment","name":"billing-api"}'}]), False)
    check("ev secret wrong pod", evidence_read("missing-secret-key", [{"tool": "pods_log", "args": '{"name":"billing-api-1"}'}]), False)
    # an empty record must raise, never score
    try:
        evidence_read("proxy-timeout", [])
        print("FAIL empty tool calls did not raise")
        sys.exit(1)
    except ValueError:
        n += 1
    if n < 20:
        print(f"FAIL only {n} checks ran")
        sys.exit(1)
    print(f"selftest: {n} checks passed")


if __name__ == "__main__":
    if sys.argv[1:] == ["--selftest"]:
        selftest()
    else:
        print(__doc__)
