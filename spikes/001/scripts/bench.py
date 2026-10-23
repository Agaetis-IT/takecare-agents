#!/usr/bin/env python3
"""Stage 6 runner: executes scripts/plan.sh top to bottom, one run at a time.

Rules, fixed in plan.sh before the first measured run:
  - one incident per scenario, no reset inside a scenario, one investigation at
    a time, a quiet interval before reading usage;
  - every engine is measured as it is; nothing is hardened during the measures;
  - a run that fails for an INFRASTRUCTURE reason is replayed (any engine) and
    recorded as replayed with its cause; a failure of the engine's own
    behaviour is a failed run, never replayed;
  - a run is valid only if its task is COMPLETED with a non-empty answer;
  - scoring is automatic (bench_score.py): chain rule + "decisive evidence
    read". A run with no recorded tool call stops the bench: it is an
    instrumentation error, and going on would corrupt the data.

Results: results/bench/run-<NN>-<engine>-<scenario>.json, one per run. A run
whose file already exists is skipped, so an interrupted bench resumes.

Usage: scripts/bench.py [--only N,M,...] [--dry-run] [--no-incident]
  --no-incident  use the incident already open instead of starting the scenario
  BENCH_RESULTS=dir  write results elsewhere (smoke tests)
Needs KUBECONFIG (out/kubeconfig) and the cluster of `make up` + holmes.sh.
"""
import atexit
import datetime as dt
import json
import os
import re
import subprocess
import sys
import time
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
SPIKE = os.path.dirname(HERE)
OUT = os.path.join(SPIKE, "out")
RESULTS = os.environ.get("BENCH_RESULTS") or os.path.join(SPIKE, "results", "bench")
GW = "http://localhost:18080"
PROM = "http://localhost:19092"
JAEGER = "http://localhost:16686"
KUBECONFIG = os.path.join(OUT, "kubeconfig")
TRIGGER = os.path.join(OUT, "bin", "trigger")
INSTRUCTIONS = os.path.join(SPIKE, "manifests", "doc", "instructions.md")
MAX_REPLAYS = 2
INCIDENT_WAIT_S = 20 * 60

sys.path.insert(0, HERE)
import bench_score  # noqa: E402

DESCRIPTION = {
    "proxy-timeout": "More than 2 % of billing-api requests have answered 504 for 30 seconds.",
    "bad-config-rollout": "A pod of orders-api has not reached the Ready state for 5 minutes.",
    "missing-secret-key": "Pod notifications-worker has restarted more than 3 times in 15 minutes.",
}

ENGINES = {
    "kagent": {"protocol": "a2a", "url": GW + "/api/a2a/takecare-system/doc-service-py/"},
    "doc-engine": {"protocol": "a2a", "url": GW + "/engine/doc-engine"},
    "holmes-native": {"protocol": "holmes", "url": GW + "/holmes/holmes-native"},
    "holmes-mcp": {"protocol": "holmes", "url": GW + "/holmes/holmes-mcp"},
}

# Failures caused by the infrastructure, not by the engine's own behaviour.
INFRA = re.compile(
    r"connection refused|no route to host|dial tcp|i/o timeout|HTTP 5\d\d|status[ =:]*5\d\d|"
    r"\b50[0-4]\b|\b429\b|rate.?limit|overloaded|Empty reply|init MCP session|initialize|"
    r"unavailable|EOF",
    re.I,
)

_pfs = []


def log(msg):
    print(f"{dt.datetime.now(dt.timezone.utc):%H:%M:%S} {msg}", flush=True)


def env():
    e = dict(os.environ)
    e["KUBECONFIG"] = KUBECONFIG
    e["OTEL_EXPORTER_OTLP_ENDPOINT"] = "http://localhost:14317"
    e["OTEL_EXPORTER_OTLP_INSECURE"] = "true"
    return e


def http_json(url, timeout=15):
    with urllib.request.urlopen(url, timeout=timeout) as r:
        return json.loads(r.read())


def up(url):
    try:
        urllib.request.urlopen(url, timeout=5).read()
        return True
    except Exception:
        return False


def ensure_forward(name, ns, svc, lport, rport, probe):
    if up(probe):
        return
    p = subprocess.Popen(
        ["kubectl", "-n", ns, "port-forward", f"svc/{svc}", f"{lport}:{rport}"],
        env=env(), stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    _pfs.append(p)
    for _ in range(40):
        time.sleep(1)
        if probe is None or up(probe):
            return
    raise SystemExit(f"port-forward {name} did not come up")


@atexit.register
def _cleanup():
    for p in _pfs:
        p.terminate()


def sh(cmd, **kw):
    return subprocess.run(cmd, env=env(), cwd=SPIKE, text=True, capture_output=True, **kw)


def plan():
    r = sh(["bash", "scripts/plan.sh"])
    if r.returncode != 0 or not r.stdout.strip():
        raise SystemExit(f"plan.sh failed or empty: {r.stderr[:300]}")
    return [json.loads(line) for line in r.stdout.splitlines() if line.strip()]


def preflight():
    log("preflight")
    ensure_forward("prometheus", "monitoring", "prometheus-server", 19092, 80, PROM + "/-/ready")
    ensure_forward("jaeger", "observability", "jaeger", 16686, 16686, JAEGER + "/api/v3/services")
    ensure_forward("collector", "observability", "otel-collector-opentelemetry-collector", 14317, 4317, None)
    r = sh(["python3", "-I", os.path.join(HERE, "bench_score.py"), "--selftest"])
    if r.returncode != 0:
        raise SystemExit("scorer self-test failed:\n" + r.stdout + r.stderr)
    log("  " + r.stdout.strip())
    if not os.path.exists(TRIGGER):
        raise SystemExit("trigger binary missing: build engine/cmd/trigger into out/bin/trigger")
    for name, e in ENGINES.items():
        probe = e["url"].rstrip("/") + ("/.well-known/agent-card.json" if e["protocol"] == "a2a" else "/api/model")
        if not up(probe):
            raise SystemExit(f"engine {name} not reachable at {probe}")
    # The homemade engine drops message content from its spans (ADR 0006), and
    # with it the tool arguments the evidence column needs: switch capture on
    # for the measures (a local Jaeger, no customer data; behaviour unchanged).
    r = sh(["kubectl", "-n", "takecare-system", "set", "env", "deploy/doc-engine", "CAPTURE_CONTENT=true"])
    r2 = sh(["kubectl", "-n", "takecare-system", "rollout", "status", "deploy/doc-engine", "--timeout=120s"])
    if r.returncode or r2.returncode:
        raise SystemExit("could not enable content capture on doc-engine: " + r.stderr + r2.stderr)
    for _ in range(30):
        if up(ENGINES["doc-engine"]["url"].rstrip("/") + "/.well-known/agent-card.json"):
            break
        time.sleep(2)
    log("  doc-engine CAPTURE_CONTENT=true for the measures (tool arguments in spans)")


def wait_alert(alertname, since):
    deadline = time.time() + INCIDENT_WAIT_S
    while time.time() < deadline:
        try:
            alerts = http_json(PROM + "/api/v1/alerts")["data"]["alerts"]
        except Exception:
            alerts = []
        for a in alerts:
            if (a["labels"].get("alertname") == alertname and a["state"] == "firing"
                    and a["activeAt"][:19] > since[:19]):
                return a["activeAt"]
        time.sleep(5)
    raise SystemExit(f"alert {alertname} did not fire within {INCIDENT_WAIT_S}s")


def start_incident(run):
    s = run["scenario"]
    since = dt.datetime.now(dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    log(f"incident: {s} (reset, then start)")
    r = sh(["bash", "scripts/scenario.sh", "start", s])
    if r.returncode != 0:
        raise SystemExit(f"scenario start failed: {r.stdout[-300:]} {r.stderr[-300:]}")
    active = wait_alert(run["alert"]["alertname"], since)
    log(f"  {run['alert']['alertname']} firing since {active}")
    return since


def trigger(run, label, instructions):
    e = ENGINES[run["engine"]]
    cmd = [TRIGGER, "-protocol", e["protocol"], "-url", e["url"], "-agent", run["engine"],
           "-label", label, "-out", RESULTS, "-timeout", "300s",
           "-alert", run["alert"]["alertname"], "-service", run["alert"]["service"],
           "-namespace", run["alert"]["namespace"], "-description", DESCRIPTION[run["scenario"]]]
    if e["protocol"] == "holmes" and instructions:
        cmd += ["-instructions", INSTRUCTIONS]
    subprocess.run(cmd, env=env(), cwd=SPIKE, text=True, capture_output=True)
    path = os.path.join(RESULTS, label + ".json")
    if not os.path.exists(path):
        return {"valid": False, "error": "trigger produced no result file", "artifact_text": ""}
    return json.load(open(path))


def usage(label):
    e = env()
    e["RESULTS_DIR"] = RESULTS
    r = subprocess.run(["bash", "scripts/run-usage.sh", label], env=e, cwd=SPIKE, text=True, capture_output=True)
    try:
        return json.loads(r.stdout)
    except Exception:
        return None


def tool_calls(run, res, label):
    """[{tool, args}] from the run's own record: Holmes' response, or the spans."""
    if ENGINES[run["engine"]]["protocol"] == "holmes":
        detail = (res.get("metadata") or {}).get("holmes_tool_calls_detail") or []
        return [{"tool": d["tool"], "args": json.dumps(d.get("params") or {})} for d in detail]
    path = os.path.join(OUT, f"trace-{label}.json")
    try:
        trace = json.load(open(path))["data"][0]
    except Exception:
        return []
    calls = []
    for sp in trace["spans"]:
        op = sp["operationName"]
        if not op.startswith("execute_tool ") or op == "execute_tool (merged)":
            continue
        args = next((t["value"] for t in sp["tags"] if t["key"] == "gcp.vertex.agent.tool_call_args"), "")
        calls.append({"tool": op[len("execute_tool "):], "args": str(args)})
    return calls


def one_attempt(run, label, instructions):
    t0 = time.time()
    res = trigger(run, label, instructions)
    time.sleep(run.get("quiet_after_seconds", 15) + 5)
    use = usage(label) if res.get("trace_id") else None
    return res, use, time.time() - t0


def execute(run, n_label):
    instructions = run.get("instructions", True)
    attempts = []
    for attempt in range(1, MAX_REPLAYS + 2):
        label = n_label if attempt == 1 else f"{n_label}-a{attempt}"
        res, use, secs = one_attempt(run, label, instructions)
        text = res.get("artifact_text") or ""
        if res.get("valid"):
            return res, use, label, attempts, secs
        blob = f"{res.get('error') or ''} {text}"
        infra = bool(INFRA.search(blob))
        attempts.append({"attempt": attempt, "label": label, "cause": "infrastructure" if infra else "engine",
                         "error": (res.get("error") or "")[:300], "state": res.get("state")})
        if not infra:
            return res, use, label, attempts, secs  # the engine's own failure: counted, not replayed
        log(f"  replay ({attempt}/{MAX_REPLAYS + 1}): infrastructure failure: {blob[:140]}")
        time.sleep(30)
    return res, use, label, attempts, secs


def record(run, res, use, label, attempts, secs):
    text = res.get("artifact_text") or ""
    valid = bool(res.get("valid"))
    calls = tool_calls(run, res, label)
    out = {
        "run": run["run"], "scenario": run["scenario"], "engine": run["engine"],
        "round": run.get("round"), "control": bool(run.get("control")),
        "instructions": run.get("instructions", True),
        "bench_run_label": (run.get("alert", {}).get("labels") or {}).get("bench_run"),
        "label": label, "valid": valid, "attempts": attempts, "replayed": any(a["cause"] == "infrastructure" for a in attempts),
        "state": res.get("state"), "error": res.get("error"),
        "latency_seconds": res.get("latency_seconds"), "trace_id": res.get("trace_id"),
        "answer_chars": len(text), "diagnosis": text,
        "memory_mentioned": bool(re.search(r"memory|heap|oom", text, re.I)),
        "tool_call_count": len(calls), "tools_called": [c["tool"] for c in calls],
        "usage": use,
        "recorded_at": dt.datetime.now(dt.timezone.utc).isoformat(),
    }
    if valid:
        out["chain_correct"] = bench_score.chain_correct(run["scenario"], text)
        try:
            out["evidence_read"] = bench_score.evidence_read(run["scenario"], calls)
        except ValueError as e:
            out["instrumentation_error"] = str(e)
    return out


def main():
    only = None
    if "--only" in sys.argv:
        only = {int(x) for x in sys.argv[sys.argv.index("--only") + 1].split(",")}
    p = plan()
    main_runs = [r for r in p if not r.get("control")]
    controls = [r for r in p if r.get("control")]
    log(f"plan: {len(main_runs)} runs + {len(controls)} controls")
    if "--dry-run" in sys.argv:
        for r in p:
            print(r["run"], r["scenario"], r["engine"], "control" if r.get("control") else "")
        return
    os.makedirs(RESULTS, exist_ok=True)
    preflight()
    json.dump({"started": dt.datetime.now(dt.timezone.utc).isoformat(), "runs": len(main_runs),
               "controls": len(controls), "max_replays": MAX_REPLAYS,
               "kagent": "0.10.3 runtime python (not the default go runtime)",
               "note": "doc-engine measured with CAPTURE_CONTENT=true (tool arguments in spans)"},
              open(os.path.join(RESULTS, "config.json"), "w"), indent=2)

    current = None
    pending_controls = list(controls)

    def run_one(run, label_prefix):
        path = os.path.join(RESULTS, f"{label_prefix}.record.json")
        if os.path.exists(path):
            log(f"skip {label_prefix} (record exists)")
            return json.load(open(path))
        res, use, label, attempts, secs = execute(run, label_prefix)
        rec = record(run, res, use, label, attempts, secs)
        json.dump(rec, open(path, "w"), indent=2)
        flag = ("OK" if rec["valid"] else "INVALID")
        log(f"run {run['run']:>2} {run['engine']:<13} {run['scenario']:<19} {flag} "
            f"chain={rec.get('chain_correct')} evidence={rec.get('evidence_read')} "
            f"{rec.get('latency_seconds')}s calls={rec['tool_call_count']}"
            + (" REPLAYED" if rec["replayed"] else ""))
        if rec.get("instrumentation_error"):
            raise SystemExit(f"STOP: {rec['instrumentation_error']} (run {run['run']}, {rec['engine']})")
        return rec

    for run in main_runs:
        if run["scenario"] != current:
            if current == "proxy-timeout" and pending_controls:
                log("controls: question only, on the open proxy-timeout incident")
                for c in pending_controls:
                    if only is None or c["run"] in only:
                        run_one(c, c["label"])
                pending_controls = []
            current = run["scenario"]
            todo = [r for r in main_runs if r["scenario"] == current
                    and (only is None or r["run"] in only)
                    and not os.path.exists(os.path.join(RESULTS, f"bench-{r['run']:02d}-{r['engine']}-{r['scenario']}.record.json"))]
            if todo and "--no-incident" not in sys.argv:
                start_incident(run)
        if only is not None and run["run"] not in only:
            continue
        run_one(run, f"bench-{run['run']:02d}-{run['engine']}-{run['scenario']}")
    log("bench finished")


if __name__ == "__main__":
    main()
