#!/usr/bin/env python3
"""
Ranking tests for the corpus: replay after ANY corpus change (spike 001).

Ported from the challenge POC (content/mcp-corpus/check-ranking.py).

The server's scoring is naive (occurrence count), hence sensitive to volume.
Adding noise or enriching `tags` can push a curated signal behind a
competitor without any generator invariant noticing: the invariants check what
the corpus CONTAINS, these tests check what the search RETURNS.

They go through a real MCP client on the real `server.py` (in memory), not a
copy of the scoring. Rank-based identifiers (the deployment log) are resolved
from the content, not hard-coded.

The bar is "findable", not "always first": generic queries only require the
signal in the top 3, so that the agent still has to read and pick.

Usage: ./check-ranking.py [corpus_dir]   (default: generate one, --scenario all)
Requires: pip install 'fastmcp==3.4.9'
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


def load(corpus, source):
    with open(os.path.join(corpus, f"{source}.json"), encoding="utf-8") as fh:
        return json.load(fh)


def id_of(corpus, source, **crit):
    for e in load(corpus, source):
        if all(str(e.get(k, "")) == v for k, v in crit.items()):
            return e["id"]
    sys.exit(f"record not found in {source}: {crit}")


def cases(corpus):
    infra = id_of(corpus, "deployments", title="infra-aws: idleClientTimeout set on the billing DBProxy")
    orders = id_of(corpus, "deployments", title="orders-config v3.9.0 rolled out")
    notif = id_of(corpus, "deployments", title="notifications-worker v2.0.0 rolled out")
    rotation = id_of(corpus, "deployments", title="Secret rotation on notifications-worker",
                     type="secret", author="CI pipeline (platform team)")
    return [
        # proxy-timeout
        ("runbooks", "memory growth 504 no restart", "RB-014", 1),
        ("runbooks", "pool idle timeout proxy", "RB-014", 1),
        ("runbooks", "connection reset by peer", "RB-014", 1),
        # RB-007 is the false lead: findable on the naive query, not
        # necessarily first. Its warning, not its rank, protects the agent.
        ("runbooks", "increase memory limit", "RB-007", 3),
        ("incidents", "memory 504 billing", "INC-2024-0312", 1),
        ("incidents", "pool rds proxy idle timeout", "INC-2024-0312", 1),
        ("deployments", "billing idleClientTimeout proxy", infra, 1),
        ("deployments", "billing-api infrastructure change", infra, 3),
        # bad-config-rollout
        ("deployments", "orders-api configmap", orders, 3),
        ("deployments", "orders database host", orders, 3),
        ("runbooks", "readiness probe dependency", "RB-021", 1),
        ("runbooks", "pod not ready rollout stuck", "RB-021", 3),
        # missing-secret-key
        ("deployments", "notifications-worker release", notif, 3),
        ("deployments", "notifications smtp secret", rotation, 3),
        ("runbooks", "crashloopbackoff restart after release", "RB-026", 1),
        ("runbooks", "container restarting exit code configuration", "RB-026", 3),
    ]


async def main():
    if len(sys.argv) > 1:
        corpus = sys.argv[1]
    else:
        corpus = tempfile.mkdtemp(prefix="corpus-")
        subprocess.run([sys.executable, os.path.join(HERE, "generate-corpus.py"), corpus,
                        "--scenario", "all"], check=True, stdout=subprocess.DEVNULL)
    sys.path.insert(0, HERE)
    failures = 0
    all_cases = cases(corpus)
    for source, query, expected, max_rank in all_cases:
        os.environ["CORPUS_PATH"] = os.path.join(corpus, f"{source}.json")
        os.environ["TOOL_PREFIX"] = source
        os.environ["DOMAIN"] = source
        sys.modules.pop("server", None)
        import server  # reloaded each time: the environment selects the corpus

        async with Client(server.mcp) as client:
            res = await client.call_tool(f"{source}_search", {"query": query, "limit": 5})
            ids = [e["id"] for e in json.loads(res.content[0].text)] if res.content else []
        rank = ids.index(expected) + 1 if expected in ids else 0
        ok = 0 < rank <= max_rank
        failures += not ok
        need = "1st" if max_rank == 1 else f"top {max_rank}"
        print(f"{'ok' if ok else 'KO'}  {source:12s} {query:46s} {expected:14s} {need:6s} -> "
              f"{rank or 'absent'}  {ids[:3]}")
    print()
    if failures:
        sys.exit(f"{failures} ranking test(s) failed: the corpus no longer discriminates.")
    print(f"{len(all_cases)} ranking tests passed.")


if __name__ == "__main__":
    asyncio.run(main())
