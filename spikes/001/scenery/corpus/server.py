#!/usr/bin/env python3
"""
Corpus MCP server: one implementation, several instances (spike 001).

Ported from the challenge POC (content/mcp-corpus/server.py) and translated.

Each demo source (incident history, deployment log, runbooks, vendor status,
support tickets) is one instance of this server with its own JSON corpus and
its own tool prefix. Tool names carry the domain (`incidents_search`,
`deployments_recent`...): far more telling in a trace than a generic
`search_files`.

Environment variables:
  TOOL_PREFIX   tool name prefix               (default: corpus)
  DOMAIN        domain description, injected in the tool descriptions
  CORPUS_PATH   path of the JSON corpus        (default: /corpus/data.json)
  PORT          listening port                 (default: 3000)

Corpus format: a list of objects with at least `id`, `title`, and usually
`date`. Extra fields are kept and returned as is.

The corpus is hot-reloaded when the file changes (mtime + size compared on
every tool call): a scenario refreshes the relative dates by updating the
mounted ConfigMap only, without restarting the pod.

Every tool is read-only and declares it through MCP tool annotations.
"""
import json
import os
import threading
from typing import Any

from fastmcp import FastMCP
from mcp.types import ToolAnnotations

TOOL_PREFIX = os.environ.get("TOOL_PREFIX", "corpus")
DOMAIN = os.environ.get("DOMAIN", "Records")
CORPUS_PATH = os.environ.get("CORPUS_PATH", "/corpus/data.json")
PORT = int(os.environ.get("PORT", "3000"))

READ_ONLY = ToolAnnotations(readOnlyHint=True, destructiveHint=False,
                            idempotentHint=True, openWorldHint=False)

# Corpus state, rebuilt only when the file changed. INDEX is the flat
# concatenation of the textual values of each record.
_lock = threading.Lock()
_stamp: tuple[int, int] | None = None
RECORDS: list[dict[str, Any]] = []
INDEX: list[tuple[dict[str, Any], str]] = []


def _load() -> None:
    """(Re)read the corpus if the file changed since the last load.

    A mounted ConfigMap is updated by swapping a symlink, so the read is
    atomic; comparing (mtime_ns, size) is enough.
    """
    global _stamp, RECORDS, INDEX
    try:
        st = os.stat(CORPUS_PATH)
    except FileNotFoundError:
        return
    stamp = (st.st_mtime_ns, st.st_size)
    if stamp == _stamp:
        return
    with _lock:
        if stamp == _stamp:  # another thread may have reloaded meanwhile
            return
        with open(CORPUS_PATH, encoding="utf-8") as fh:
            records = json.load(fh)
        RECORDS = records
        INDEX = [(rec, " ".join(str(v).lower() for v in rec.values())) for rec in records]
        _stamp = stamp
        print(f"mcp-corpus[{TOOL_PREFIX}]: corpus loaded ({len(records)} records)", flush=True)


_load()

mcp = FastMCP(name=f"{TOOL_PREFIX}-corpus")


def _summary(rec: dict[str, Any]) -> dict[str, Any]:
    return {k: rec.get(k) for k in ("id", "date", "title") if rec.get(k) is not None}


# The description is passed to the decorator rather than left to the
# docstring: it tells the model *what* it searches, while the code is shared by
# every instance.
@mcp.tool(
    name=f"{TOOL_PREFIX}_search",
    description=(
        f"{DOMAIN}. Keyword search in this source. Returns the most relevant "
        f"records as summaries (id, date, title); use {TOOL_PREFIX}_get for the "
        f"full record."
    ),
    annotations=READ_ONLY,
)
def search(query: str, limit: int = 5) -> list[dict[str, Any]]:
    """Keyword search, scored by term occurrences."""
    _load()
    terms = [t for t in query.lower().split() if t]
    scored = []
    for rec, blob in INDEX:
        score = sum(blob.count(t) for t in terms)
        if score:
            scored.append((score, rec))
    scored.sort(key=lambda pair: pair[0], reverse=True)
    return [_summary(rec) for _, rec in scored[: max(1, min(limit, 25))]]


@mcp.tool(
    name=f"{TOOL_PREFIX}_get",
    description=f"{DOMAIN}. Full record of this source, by identifier.",
    annotations=READ_ONLY,
)
def get(id: str) -> dict[str, Any]:  # noqa: A002 - tool argument name seen by the model
    """Full record by identifier."""
    _load()
    for rec in RECORDS:
        if str(rec.get("id")) == str(id):
            return rec
    return {"error": f"no record with identifier {id}"}


@mcp.tool(
    name=f"{TOOL_PREFIX}_recent",
    description=(
        f"{DOMAIN}. Most recent records of this source, newest first "
        f"(summaries only)."
    ),
    annotations=READ_ONLY,
)
def recent(limit: int = 10) -> list[dict[str, Any]]:
    """Most recent records, newest first."""
    _load()
    ordered = sorted(RECORDS, key=lambda r: str(r.get("date", "")), reverse=True)
    return [_summary(rec) for rec in ordered[: max(1, min(limit, 50))]]


# SPIKE-ONLY. A stub `act` tool, registered only where ACT_STUB is set (the
# support source). It changes nothing and answers a refusal: it exists so that
# the spike can prove an `act` tool is hidden from, and refused to, a read-only
# role at the gateway (spec 02, acceptance criterion 4).
# It must NOT follow the corpus servers into the product (spec 03): an `act`
# tool on a `knowledge` connector is exactly what the product's read-only
# guard has to reject. Remove this block when porting the server.
if os.environ.get("ACT_STUB") == "true":
    @mcp.tool(
        name=f"{TOOL_PREFIX}_create_ticket",
        description=f"{DOMAIN}. Open a new ticket in this source.",
        annotations=ToolAnnotations(readOnlyHint=False, destructiveHint=False,
                                    idempotentHint=False, openWorldHint=False),
    )
    def create_ticket(title: str, body: str) -> dict[str, Any]:
        """Stub: never writes anything."""
        print(f"mcp-corpus[{TOOL_PREFIX}]: create_ticket called (stub, nothing written)", flush=True)
        return {"error": "stub tool: nothing was created"}


if __name__ == "__main__":
    print(f"mcp-corpus: prefix={TOOL_PREFIX} domain={DOMAIN!r} "
          f"records={len(RECORDS)} port={PORT}", flush=True)
    mcp.run(transport="http", host="0.0.0.0", port=PORT, path="/mcp", show_banner=False)
