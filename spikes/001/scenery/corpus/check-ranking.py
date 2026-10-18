#!/usr/bin/env python3
"""
Ranking tests for the corpus: replay after ANY corpus change (spike 001).

Ported from the challenge POC (content/mcp-corpus/check-ranking.py), then
inverted. The spike compares engines and tool access paths: if the first
query an agent would write lands on the answer, the search skill is not
measured. So each signal is tested twice:

  obvious  the query written from the alert alone (service name, symptom).
           The signal must NOT come first: competitors (other changes on the
           same service, homonyms on other services, close but off-topic
           post-mortems and runbooks) outrank it.
  precise  the query written after reading the cluster evidence (the exact
           value 600, the exact host name, the exact key). The signal must
           be in the top 3.

False leads are tested separately: they must stay findable on the naive query.

The tests go through a real MCP client on the real `server.py` (in memory),
not a copy of the scoring. Rank-based identifiers (the deployment log) are
resolved from the content, not hard-coded.

Usage: ./check-ranking.py [corpus_dir]   (default: generate one, --scenario all)
Requires: fastmcp==3.4.9 (run it inside the corpus image)
"""
import asyncio
import json
import os
import subprocess
import sys
import tempfile

try:
    from fastmcp import Client
except ImportError:
    sys.exit("fastmcp missing: pip install 'fastmcp==3.4.9'")

HERE = os.path.dirname(os.path.abspath(__file__))
DEPTH = 10  # ranks recorded up to this depth; beyond it, "absent"


def load(corpus, source):
    with open(os.path.join(corpus, f"{source}.json"), encoding="utf-8") as fh:
        return json.load(fh)


def id_of(corpus, source, **crit):
    found = [e["id"] for e in load(corpus, source)
             if all(str(e.get(k, "")) == v for k, v in crit.items())]
    if len(found) != 1:
        sys.exit(f"{len(found)} records match {crit} in {source}")
    return found[0]


def signals(corpus):
    """(scenario, source, expected id, obvious query, precise query)."""
    infra = id_of(corpus, "deployments", title="infra-aws: idleClientTimeout set on the billing DBProxy")
    orders = id_of(corpus, "deployments", title="orders-config v3.9.0 rolled out")
    notif = id_of(corpus, "deployments", title="notifications-worker v2.0.0 rolled out")
    rotation = id_of(corpus, "deployments", title="Secret rotation on notifications-worker",
                     author="CI pipeline (platform team)")
    return [
        ("proxy-timeout", "deployments", infra,
         "billing-api idle timeout", "billing-db-proxy idleClientTimeout 600"),
        ("proxy-timeout", "runbooks", "RB-014",
         "504 connection reset by peer", "idleClientTimeout pool idle_timeout"),
        ("proxy-timeout", "incidents", "INC-2024-0312",
         "billing-api 504 memory", "rds proxy idleClientTimeout pool"),
        ("bad-config-rollout", "deployments", orders,
         "orders configmap DB_HOST", "orders-db-primary"),
        ("bad-config-rollout", "runbooks", "RB-021",
         "pod not ready readiness probe failing", "readiness dependency host configmap"),
        ("missing-secret-key", "deployments", notif,
         "notifications release", "notifications-worker SMTP_PASSWORD"),
        ("missing-secret-key", "deployments", rotation,
         "notifications smtp secret", "notifications-smtp smtp-password"),
        ("missing-secret-key", "runbooks", "RB-026",
         "crashloopbackoff restart", "optional secretKeyRef missing key"),
    ]


FALSE_LEADS = [
    # RB-007 must stay findable on the naive query; its warning, not its
    # rank, protects the agent.
    ("proxy-timeout", "runbooks", "RB-007", "increase memory limit", 3),
]


async def rank(corpus, source, query, expected):
    os.environ["CORPUS_PATH"] = os.path.join(corpus, f"{source}.json")
    os.environ["TOOL_PREFIX"] = source
    os.environ["DOMAIN"] = source
    sys.modules.pop("server", None)
    import server  # reloaded each time: the environment selects the corpus

    async with Client(server.mcp) as client:
        res = await client.call_tool(f"{source}_search", {"query": query, "limit": DEPTH})
        ids = [e["id"] for e in json.loads(res.content[0].text)] if res.content else []
    return (ids.index(expected) + 1 if expected in ids else 0), ids


def fmt(r):
    return str(r) if r else f">{DEPTH}"


async def main():
    if len(sys.argv) > 1:
        corpus = sys.argv[1]
    else:
        corpus = tempfile.mkdtemp(prefix="corpus-")
        subprocess.run([sys.executable, os.path.join(HERE, "generate-corpus.py"), corpus,
                        "--scenario", "all"], check=True, stdout=subprocess.DEVNULL)
    sys.path.insert(0, HERE)
    sys.dont_write_bytecode = True
    failures = 0

    print(f"{'scenario':19s} {'source':11s} {'signal':14s} {'obvious':>7s} {'precise':>7s}  queries")
    for scenario, source, expected, obvious, precise in signals(corpus):
        r_obv, ids_obv = await rank(corpus, source, obvious, expected)
        r_pre, _ = await rank(corpus, source, precise, expected)
        ok = r_obv != 1 and 0 < r_pre <= 3
        failures += not ok
        print(f"{scenario:19s} {source:11s} {expected:14s} {fmt(r_obv):>7s} {fmt(r_pre):>7s}  "
              f"{'ok' if ok else 'KO'}  obvious={obvious!r} (top: {ids_obv[:3]}) precise={precise!r}")

    print()
    for scenario, source, expected, query, max_rank in FALSE_LEADS:
        r, ids = await rank(corpus, source, query, expected)
        ok = 0 < r <= max_rank
        failures += not ok
        print(f"false lead {expected} ({scenario}) on {query!r}: rank {fmt(r)} "
              f"(need top {max_rank}) {'ok' if ok else 'KO'}  {ids[:3]}")

    print()
    if failures:
        sys.exit(f"{failures} ranking test(s) failed: obvious must not be 1st, precise must be top 3.")
    print("ranking tests passed: no obvious query lands first, every precise query finds its signal.")


if __name__ == "__main__":
    asyncio.run(main())
