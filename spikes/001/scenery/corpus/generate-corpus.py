#!/usr/bin/env python3
"""
Compose the corpora served by the demo corpus MCP servers (spike 001).

Ported from the challenge POC (content/mcp-corpus/generate-corpus.py) and
translated.

Each corpus = CURATED records (seed/, hand-written, they carry the scenario
signals) + generated NOISE around them. Without noise, finding the signal
proves only that the agent can call a tool; the noise makes the haystack.

Dates are relative to the incident time (default: now, UTC). Seeds express
them with @TS<minutes>@ and @DAY<days>@ markers, resolved here. UTC, never
local time: the agent compares these dates with what it reads in the cluster
(pod logs, object ages, alert time), all in UTC.

Curated records carrying a `scenario` field are the "live" changes of one
scenario (the change that breaks the cluster now). They are kept only when
that scenario is generated (or with --scenario all), so that the deployment
log never claims a change the cluster does not show. The field is stripped
from the output.

Determinism: the random generator uses a fixed seed. For a given scenario the
corpus always has the same shape and the same identifiers; only dates move.

Usage:
  generate-corpus.py <out_dir> [--scenario NAME|all|none] [--incident-time ISO8601]
"""
import argparse
import json
import os
import random
import re
import sys
from datetime import datetime, timedelta, timezone

HERE = os.path.dirname(os.path.abspath(__file__))
SEED_DIR = os.path.join(HERE, "seed")
SCENARIOS = ("proxy-timeout", "bad-config-rollout", "missing-secret-key")

NOW = datetime.now(timezone.utc)  # replaced by --incident-time in main()
RNG = random.Random(20260909)

# Each corpus has its own rhythm: a CI chain ships several deployments a day,
# an SRE team writes a few post-mortems a year.
VOLUME = {
    "deployments": 160,   # ~5/day over 30 days
    "support": 50,        # ~2.5/day over 21 days
    "vendor": 15,         # cloud vendor bulletins over 90 days
    "incidents": 18,      # post-mortems over 2 years
    "runbooks": 28,       # a team's runbook library
}

# The deployment log is numbered chronologically from this base.
CI_BASE = 2300


def ts(minutes_ago: float) -> str:
    return (NOW - timedelta(minutes=minutes_ago)).strftime("%Y-%m-%d %H:%M")


def day(days_ago: float) -> str:
    return (NOW - timedelta(days=days_ago)).strftime("%Y-%m-%d")


def load_seed(name: str, scenario: str) -> list:
    """Load a seed, resolve its relative date markers, filter live changes."""
    with open(os.path.join(SEED_DIR, f"{name}.json"), encoding="utf-8") as fh:
        raw = fh.read()
    raw = re.sub(r'"@TS([0-9]+)@"', lambda m: json.dumps(ts(int(m.group(1)))), raw)
    raw = re.sub(r'"@DAY([0-9]+)@"', lambda m: json.dumps(day(int(m.group(1)))), raw)
    out = []
    for rec in json.loads(raw):
        owner = rec.pop("scenario", None)
        if owner is None or scenario == "all" or owner == scenario:
            out.append(rec)
    return out


def minutes_since(stamp: str) -> float:
    for fmt in ("%Y-%m-%d %H:%M", "%Y-%m-%d"):
        try:
            d = datetime.strptime(stamp, fmt).replace(tzinfo=timezone.utc)
            return (NOW - d).total_seconds() / 60
        except ValueError:
            continue
    raise ValueError(f"unreadable date: {stamp}")


# =============================================================================
# Pools
# =============================================================================

SERVICES = {
    "payments-api":         "payments",
    "checkout-api":         "checkout",
    "billing-api":          "billing",
    "orders-api":           "orders",
    "web-front":            "front",
    "notifications-worker": "notifications",
    "catalog-api":          "catalog",
    "auth-service":         "identity",
    "search-api":           "search",
    "batch-export":         "data",
}

DEPLOY_IMAGE = [
    ("Application release, no configuration change.", "release version"),
    ("Logging and tracing fix.", "fix logs traces"),
    ("Read query optimisation.", "performance queries"),
    ("Security dependency updates.", "dependencies security"),
    ("Detailed health endpoint added.", "probe health endpoint"),
    ("Pagination regression fixed.", "fix pagination"),
    ("HTTP client moved to a new major version.", "http client dependency"),
    ("Lower memory footprint at startup.", "memory startup"),
    ("Business usage metrics added.", "metrics observability"),
    ("Encoding fix on exports.", "fix encoding export"),
]
DEPLOY_CONFIG = [
    ("Rate limiting thresholds adjusted.", "configmap rate-limit thresholds"),
    ("Connection pool size increased.", "configmap pool connections"),
    ("Second-level cache enabled.", "configmap cache"),
    ("Outgoing call timeouts revised.", "configmap timeout outgoing"),
    ("Log level switched to INFO.", "configmap logs level"),
]
DEPLOY_SECRET = [
    ("Scheduled rotation of service credentials.", "secret rotation credentials"),
    ("Client certificate renewed.", "secret certificate tls"),
]
DEPLOY_HELM = [
    ("Resource requests and limits revised.", "helm resources limits"),
    ("Two replicas during business hours.", "helm replicas autoscaling"),
]
DEPLOY_FAIL = [
    ("Automatic rollback after startup probe failures.", "rollback failed probe"),
    ("Deployment interrupted: registry image quota exceeded.", "failed registry quota"),
    ("Cancelled by the author before the end of the rollout.", "cancelled manual"),
]

SUPPORT_CUSTOMERS = [
    "Vasseur Group", "Maison Leonard", "Ferrand Accounting", "Bruneau Ltd",
    "Sodexia", "Delmas Trading", "Rouviere Workshop", "Pelletier Group",
    "Chastel Books", "Anthelme Glassworks", "Nadaud Transport", "Grand Pre Farm",
]
SUPPORT_TOPICS = [
    ("Request to add a user", "The customer wants an extra access for a new colleague.", "access user request", "low"),
    ("Question about the monthly invoice", "The customer asks for the detail of a line of the last invoice.", "invoice question detail", "low"),
    ("Incomplete CSV export", "Some columns are missing from the export downloaded from the portal.", "export csv columns", "medium"),
    ("Notification not received", "The customer did not receive the email confirmation of the last operation.", "notification email missing", "medium"),
    ("Slow catalog display", "Loading the product list takes several seconds at peak time.", "catalog slow display", "medium"),
    ("Error at login", "Generic error message when signing in, solved by retrying.", "authentication error login", "medium"),
    ("Feature request: filter by date", "Functional wish, forwarded to product.", "feature filter request", "low"),
    ("Duplicate order", "An order shows twice in the customer history.", "order duplicate history", "medium"),
    ("Question about pricing terms", "Commercial request forwarded to the account manager.", "commercial pricing question", "low"),
    ("Attachment rejected", "The portal rejects a document above a certain size.", "portal attachment size", "low"),
]
CHANNELS = ["phone", "portal", "email", "chat"]

AWS_BULLETINS = [
    ("Amazon EC2", "eu-west-1", "Elevated instance launch errors", "An increased failure rate when launching new instances has been resolved.", "ec2 launch instances"),
    ("Amazon S3", "eu-west-3", "Elevated latency on GET requests", "Increased latency on read operations has been resolved.", "s3 latency read"),
    ("Amazon RDS", "eu-central-1", "Delayed automatic failovers", "Some Multi-AZ failovers took longer than expected.", "rds failover multi-az"),
    ("Elastic Load Balancing", "eu-west-3", "Unstable health checks", "Intermittent health checks have been fixed.", "elb health intermittent"),
    ("Amazon EKS", "eu-west-1", "Delayed control plane updates", "Control plane updates were delayed.", "eks control plane"),
    ("Amazon CloudWatch", "eu-west-3", "Metric ingestion delay", "A delay in custom metric ingestion has been resolved.", "cloudwatch metrics delay"),
    ("AWS Lambda", "eu-west-1", "Longer cold starts", "Cold start durations above normal were observed.", "lambda cold start"),
    ("Amazon EBS", "eu-central-1", "Slow volume creation", "Creation of new volumes was temporarily slowed down.", "ebs volume creation"),
    ("Amazon Route 53", "global", "Slow DNS propagation", "Some changes propagated more slowly than usual.", "route53 dns propagation"),
    ("Amazon SQS", "eu-west-3", "Message delivery latency", "Increased latency on message delivery has been resolved.", "sqs message latency"),
    ("AWS IAM", "global", "Delayed policy propagation", "Policy changes took longer to propagate.", "iam policy propagation"),
    ("Amazon ECR", "eu-west-3", "Slow image pulls", "Slow image pulls have been fixed.", "ecr image pull"),
]

INCIDENT_NOISE = [
    ("Disk saturation on compute nodes", "batch-export", "major", "2h10",
     "Pods of the data namespace go Evicted in waves.",
     "Application logs were not rotated on the batch node group nodes.",
     "Rotation enabled and disk alert threshold lowered to 80 %.", "disk saturation eviction rotation logs"),
    ("Expired certificate on the ingress gateway", "web-front", "critical", "48min",
     "All HTTPS requests fail with a verification error.",
     "Automatic renewal had silently failed three weeks earlier.",
     "Alert on certificate expiry at 21 days.", "certificate tls expiry renewal"),
    ("Restart loop after a version upgrade", "catalog-api", "major", "1h05",
     "The container restarts in a loop right after the deployment.",
     "A mandatory environment variable had been renamed without updating the chart.",
     "Schema validation of the Helm values in continuous integration.", "crashloop environment variable helm"),
    ("Lost messages on the notifications queue", "notifications-worker", "major", "3h45",
     "Some notifications are never delivered, with no error on the producer side.",
     "The queue retention was shorter than the consumer recovery delay.",
     "Retention raised to 7 days and alert on the oldest message age.", "queue message loss retention"),
    ("Subnet IP address exhaustion", "checkout-api", "major", "2h30",
     "New pods stay in ContainerCreating.",
     "The subnet CIDR range no longer fitted the pod count after scaling up.",
     "CIDR extended and occupancy monitored.", "network cidr ip subnet pods"),
    ("Slow queries after a cache purge", "search-api", "minor", "55min",
     "P95 doubles for about ten minutes after each redeployment.",
     "The cache was emptied at startup without warm-up.", "Warm-up added at startup.",
     "cache warm-up latency redeployment"),
    ("Double billing on replayed orders", "billing-api", "critical", "5h20",
     "Some customers are charged twice for the same order.",
     "The automatic replay did not check the idempotency key.",
     "Idempotency key made mandatory in the API.", "idempotency replay double billing"),
    ("Identity provider outage", "auth-service", "critical", "1h40",
     "No login possible, service accounts included.",
     "Outage of the external identity provider, no fallback configured.",
     "Short-lived token cache added.", "identity sso outage external"),
    ("Clock drift on a node", "payments-api", "minor", "40min",
     "Request signatures rejected by a partner.",
     "The time synchronisation service was stopped on one node.",
     "NTP drift monitoring added.", "clock ntp signature drift"),
    ("Blocked on a schema migration", "checkout-api", "major", "2h55",
     "The deployment stays blocked, the database rejects writes.",
     "A migration took an exclusive lock on a large table.",
     "Migrations split and run off-peak.", "migration lock database schema"),
    ("File descriptor leak", "batch-export", "major", "4h15",
     "The service stops accepting connections after several hours.",
     "Temporary files were not closed on exceptions.",
     "Systematic close and alert on descriptor count.", "descriptors files leak connections"),
    ("Cascading rejections behind rate limiting", "web-front", "major", "1h20",
     "A legitimate traffic peak triggers mass rejections.",
     "The limit was computed per pod, not globally.",
     "Rate limiting moved to the gateway.", "rate-limit rejections peak traffic"),
    ("Unreadable backup at restore time", "billing-api", "critical", "6h30",
     "The test restore fails on a corrupted archive.",
     "The backup was taken during a write, without quiescing.",
     "Consistent snapshot and monthly restore test.", "backup restore corruption"),
    ("CPU contention after adding a sidecar", "notifications-worker", "minor", "1h10",
     "P99 rises by 40 % without application change.",
     "The sidecar had no CPU limit and starved the main container.",
     "Explicit limits on every sidecar.", "cpu contention sidecar limits"),
]

RUNBOOK_NOISE = [
    ("Node in NotReady", "kubernetes", "A node turns NotReady and its pods are evicted.",
     "1. Check the node disk and memory pressure.\n2. Check the kubelet and the container runtime.\n3. Cordon then drain before any intervention.", "node notready kubelet eviction"),
    ("TLS certificate close to expiry", "security", "A certificate expiry is reported within 21 days.",
     "1. Identify the issuer and the renewal mode.\n2. Trigger the renewal.\n3. Check the chain presented by the gateway.", "certificate expiry renewal tls"),
    ("Message queue lagging", "messaging", "The age of the oldest message exceeds the threshold.",
     "1. Compare production and consumption rates.\n2. Check consumer errors.\n3. Increase parallelism if processing is the bottleneck.", "queue message lag consumer"),
    ("Critical disk space on a node", "resources", "Available space drops below 10 %.",
     "1. Identify the largest consumers.\n2. Purge unused images.\n3. Check log rotation.", "disk space purge images rotation"),
    ("Database restore", "databases", "Proven data loss or corruption.",
     "1. Freeze writes.\n2. Identify the last valid restore point.\n3. Restore to a control instance before switching.", "restore backup database pitr"),
    ("Manual failover of a replicated database", "databases", "The primary instance is unreachable.",
     "1. Confirm the primary is really down.\n2. Check the replication lag.\n3. Promote the replica and repoint the services.", "failover replication promotion primary"),
    ("Abnormal traffic from an address range", "security", "Request peak concentrated on few addresses.",
     "1. Characterise the pattern.\n2. Apply a targeted limit.\n3. Escalate if the pattern persists.", "abnormal traffic limit addresses"),
    ("Rollout blocked mid-way", "kubernetes", "The rollout no longer progresses.",
     "1. Read the ReplicaSet state and the events.\n2. Check available resources.\n3. Roll back to the previous revision if the service is degraded.", "rollout blocked replicaset revision"),
    ("Sustained CPU saturation alert", "performance", "CPU usage stays at the ceiling for more than 15 minutes.",
     "1. Tell a legitimate load from a loop.\n2. Check the container limits.\n3. Adjust replicas before limits.", "cpu saturation limits replicas"),
    ("Lost application logs", "observability", "A service's logs no longer reach the collector.",
     "1. Check the collection agent on the node.\n2. Check the application output format.\n3. Check the collector quotas.", "logs collection agent quota"),
    ("Missing metrics for a service", "observability", "A service's series disappear from the dashboards.",
     "1. Check the scrape annotations on the pods.\n2. Check that /metrics is reachable.\n3. Check the relabel rules.", "metrics scrape annotations prometheus"),
    ("Service credential rotation", "security", "Scheduled rotation or suspected compromise.",
     "1. Generate the new credential.\n2. Deploy with double validity.\n3. Revoke the old one after verification.", "rotation credential secret revocation"),
    ("Scaling up before a sales event", "capacity", "Planned traffic peak.",
     "1. Read the load of the previous equivalent peak.\n2. Pre-size the replicas.\n3. Check the vendor quotas.", "capacity peak replicas quota"),
    ("502 errors behind the gateway", "network", "502 rate rising without application error.",
     "1. Check the Service endpoints.\n2. Compare gateway and application timeouts.\n3. Check recent restarts.", "502 gateway endpoints timeout"),
    ("Drift between declared and actual state", "gitops", "A gap persists between the repository and the cluster.",
     "1. Read the drift report.\n2. Identify a possible manual change.\n3. Resync after validation.", "gitops drift synchronisation"),
    ("Namespace stuck in Terminating", "kubernetes", "Deletion does not complete.",
     "1. List the remaining resources.\n2. Identify the blocking finalizers.\n3. Remove the finalizer after verification.", "namespace terminating finalizer deletion"),
    ("Startup probe too short", "kubernetes", "The pod is killed before it finishes starting.",
     "1. Measure the real startup time.\n2. Use a startupProbe rather than lengthening liveness.\n3. Validate again after deployment.", "startupprobe startup liveness delay"),
    ("Namespace quota reached", "resources", "Pod creations are rejected.",
     "1. Compare the summed requests with the quota.\n2. Identify oversized workloads.\n3. Adjust requests before the quota.", "quota namespace resources requests"),
    ("Clock out of sync on a node", "system", "Signature rejections or inconsistent timestamps.",
     "1. Check the synchronisation service.\n2. Measure the drift.\n3. Restart the service and watch.", "ntp clock drift synchronisation"),
    ("Container image not found", "kubernetes", "The pod stays in ImagePullBackOff.",
     "1. Check the image name and tag.\n2. Check the registry credentials.\n3. Check the pull quotas.", "imagepullbackoff registry tag"),
    ("Persistent volume not attached", "storage", "The pod waits for its volume.",
     "1. Check the availability zone of the volume and of the node.\n2. Check the claim state.\n3. Check the storage controller.", "persistent volume zone attach pvc"),
    ("Replication lagging", "databases", "Replication lag exceeds the threshold.",
     "1. Measure the lag and its trend.\n2. Identify a long transaction on the primary.\n3. Avoid any failover while the lag grows.", "replication lag transaction primary"),
    ("Connection leak towards a third-party service", "network", "The number of established connections grows and never goes down.",
     "1. Count connections by state.\n2. Check HTTP client reuse.\n3. Set a cap and an idle timeout.", "connections leak http client cap"),
]

TEAM_OF = {s: f"CI pipeline ({t} team)" for s, t in SERVICES.items()}

# Quiet windows (minutes) per service in the deployment log: no noise change
# there, so that each scenario's change is the only recent one to examine.
QUIET = {
    "billing-api": 24 * 60,           # proxy-timeout: only the DBProxy change
    "orders-api": 6 * 60,             # bad-config-rollout
    "notifications-worker": 6 * 60,   # missing-secret-key
    "checkout-api": 24 * 60,          # silence behind the checkout tickets
}


# =============================================================================
# Noise generators
# =============================================================================

def noise_deployments(n, curated):
    taken = {minutes_since(e["date"]) for e in curated}
    # Curated titles plus every version a seed mentions, so that noise never
    # duplicates a curated change.
    reserved = {e["title"] for e in curated} | {
        v for e in load_seed("deployments", "all") for v in re.findall(r"v\d+\.\d+\.\d+", e["title"])}
    out = []
    span = 30 * 24 * 60
    while len(out) < n:
        m = RNG.uniform(20, span)
        svc = RNG.choice(list(SERVICES))
        if m < QUIET.get(svc, 0):
            continue
        if any(abs(m - t) < 3 for t in taken):
            continue
        taken.add(m)
        version = f"v{RNG.randint(1, 6)}.{RNG.randint(0, 9)}.{RNG.randint(0, 9)}"
        roll = RNG.random()
        if roll < 0.06:
            summary, tags = RNG.choice(DEPLOY_FAIL)
            kind, status = "image", ("cancelled" if "Cancelled" in summary else "failed")
            title = f"Deployment failed: {svc} {version}"
        elif roll < 0.20:
            summary, tags = RNG.choice(DEPLOY_CONFIG)
            kind, status = "configmap", "success"
            title = f"{svc.split('-')[0]}-config {version} rolled out"
        elif roll < 0.25:
            summary, tags = RNG.choice(DEPLOY_SECRET)
            kind, status = "secret", "success"
            title = f"Secret rotation on {svc}"
        elif roll < 0.30:
            summary, tags = RNG.choice(DEPLOY_HELM)
            kind, status = "helm", "success"
            title = f"Helm revision of {svc}"
        else:
            summary, tags = RNG.choice(DEPLOY_IMAGE)
            kind, status = "image", "success"
            title = f"{svc} {version} rolled out"
        if title in reserved or version in reserved:
            continue
        out.append({
            "_m": m, "date": ts(m), "title": title, "service": svc, "type": kind,
            "author": TEAM_OF[svc], "summary": summary, "status": status,
            "tags": f"{SERVICES[svc]} {tags}",
        })
    return out


def noise_support(n, curated):
    """Nothing on checkout-api more recent than SUP-4471."""
    floor = min(minutes_since(e["date"]) for e in curated if e["service"] == "checkout-api")
    out = []
    span = 21 * 24 * 60
    while len(out) < n:
        m = RNG.uniform(30, span)
        svc = RNG.choice(list(SERVICES))
        if svc == "checkout-api" and m < floor + 15:
            continue
        title, summary, tags, prio = RNG.choice(SUPPORT_TOPICS)
        out.append({
            "_m": m, "date": ts(m), "title": title, "service": svc,
            "channel": RNG.choice(CHANNELS), "priority": prio,
            "customer": RNG.choice(SUPPORT_CUSTOMERS), "summary": summary,
            "status": "closed" if m > 2 * 24 * 60 else RNG.choice(["open", "in progress"]),
            "tags": f"{SERVICES[svc]} {tags}",
        })
    return out


def noise_vendor(n, curated):
    """Resolved bulletins over 90 days, drawn without replacement."""
    if n > len(AWS_BULLETINS):
        raise SystemExit(f"vendor: {n} bulletins requested for {len(AWS_BULLETINS)} available")
    out = []
    for svc, region, title, summary, tags in RNG.sample(AWS_BULLETINS, n):
        d = RNG.uniform(3, 90)
        out.append({
            "_m": d * 24 * 60, "id": "", "date": day(d),
            "title": f"Resolved - {title.lower()}, {region}", "service": svc,
            "region": region, "status": "resolved", "summary": summary,
            "tags": f"{tags} {region} resolved",
        })
    return out


def noise_incidents(n, curated):
    out = []
    used = {e["id"] for e in curated}
    for i in range(n):
        title, svc, sev, duration, sympt, cause, fix, tags = INCIDENT_NOISE[i % len(INCIDENT_NOISE)]
        d = RNG.uniform(40, 730)
        dt = NOW - timedelta(days=d)
        iid = f"INC-{dt.strftime('%Y-%m%d')}"
        while iid in used:
            iid += "b"
        used.add(iid)
        out.append({
            "_m": d * 24 * 60, "id": iid, "date": dt.strftime("%Y-%m-%d"),
            "title": title, "service": svc, "severity": sev, "duration": duration,
            "symptoms": sympt, "root_cause": cause, "fix": fix, "tags": tags,
        })
    return out


def noise_runbooks(n, curated):
    used = {e["id"] for e in curated}
    out, k = [], 1
    for i in range(n):
        title, dom, sympt, proc, tags = RUNBOOK_NOISE[i % len(RUNBOOK_NOISE)]
        while f"RB-{k:03d}" in used:
            k += 1
        rid = f"RB-{k:03d}"
        used.add(rid)
        out.append({
            "_m": 0, "id": rid, "title": title, "domain": dom, "services": "all",
            "revision": (NOW - timedelta(days=RNG.randint(60, 900))).strftime("%Y-%m-%d"),
            "symptoms": sympt, "procedure": proc,
            "verification": "The symptom disappears and does not come back within 30 minutes.",
            "tags": tags,
        })
    return out


# =============================================================================
# Composition
# =============================================================================

def compose(name, scenario, noise_fn, sort_key, renumber=None):
    curated = load_seed(name, scenario)
    extra = noise_fn(VOLUME[name] - len(curated), curated)
    for e in curated:
        e["_m"] = minutes_since(e["date"]) if "date" in e else 0
    merged = sorted(curated + extra, key=sort_key)
    if renumber:
        renumber(merged)
    for e in merged:
        e.pop("_m", None)
    return merged


def renumber_from(prefix, base):
    """Number a chronological series from a fixed base (CI counters grow)."""
    def renumber(entries):
        for i, e in enumerate(entries):
            e.pop("id", None)
            entries[i] = {"id": f"{prefix}{base + i}", **e}
    return renumber


def renumber_anchored(prefix, anchor_id):
    """Number a chronological series so that a curated anchor keeps its ID."""
    def renumber(entries):
        anchor_pos = next(i for i, e in enumerate(entries) if e.get("id") == anchor_id)
        anchor_num = int(anchor_id.rsplit("-", 1)[1])
        for i, e in enumerate(entries):
            e.pop("id", None)
            entries[i] = {"id": f"{prefix}{anchor_num - anchor_pos + i}", **e}
    return renumber


def renumber_aws(entries):
    taken = {e["id"] for e in entries if e.get("id")}
    for e in entries:
        if e.get("id"):
            continue
        y, m, d = e["date"].split("-")
        rank = RNG.randint(1, 4)
        while f"AWS-{y}-{m}{d}-{rank:02d}" in taken:
            rank += 1
        e["id"] = f"AWS-{y}-{m}{d}-{rank:02d}"
        taken.add(e["id"])
    for i, e in enumerate(entries):
        entries[i] = {"id": e.pop("id"), **e}


# =============================================================================
# Signals and invariants: what must hold for the scenarios to work. Checked at
# generation time: a corpus that drowns its own signal never reaches the
# cluster.
# =============================================================================

def recent_changes(dep, service, minutes):
    return [e for e in dep if e["service"] == service and minutes_since(e["date"]) < minutes]


def signals(corpora, scenario):
    """Signal record IDs per scenario (deployment IDs are rank-based)."""
    dep = corpora["deployments"]

    def find(service, title):
        found = [e["id"] for e in dep if e["service"] == service and e["title"] == title]
        if len(found) != 1:
            raise SystemExit(f"signal record '{title}' found {len(found)} times")
        return found[0]

    out = {}
    if scenario in ("proxy-timeout", "all"):
        out["proxy-timeout"] = [find("billing-api", "infra-aws: idleClientTimeout set on the billing DBProxy"), "RB-014", "RB-007", "INC-2024-0312"]
    if scenario in ("bad-config-rollout", "all"):
        out["bad-config-rollout"] = [find("orders-api", "orders-config v3.9.0 rolled out"),
                                     find("orders-api", "orders-config v3.8.0 rolled out"), "RB-021"]
    if scenario in ("missing-secret-key", "all"):
        out["missing-secret-key"] = [find("notifications-worker", "notifications-worker v2.0.0 rolled out"),
                                     find("notifications-worker", "Secret rotation on notifications-worker"), "RB-026"]
    return out


def check(corpora, scenario):
    errors = []
    fail = errors.append
    dep = corpora["deployments"]
    want = (lambda s: scenario in (s, "all"))

    billing = recent_changes(dep, "billing-api", 24 * 60)
    if want("proxy-timeout"):
        if len(billing) != 1 or billing[0].get("type") != "infrastructure" \
                or "idleClientTimeout" not in billing[0].get("diff", ""):
            fail(f"proxy-timeout: exactly one billing-api change < 24 h, infrastructure, naming "
                 f"idleClientTimeout, expected; found {[(e['id'], e.get('type')) for e in billing]}")
    elif billing:
        fail(f"billing-api must have no change < 24 h outside proxy-timeout, found {[e['id'] for e in billing]}")

    orders = recent_changes(dep, "orders-api", 6 * 60)
    if want("bad-config-rollout"):
        if len(orders) != 1 or orders[0].get("type") != "configmap" or "DB_HOST" not in orders[0].get("diff", ""):
            fail(f"bad-config-rollout: exactly one orders-api configmap change < 6 h naming DB_HOST "
                 f"expected; found {[e['id'] for e in orders]}")
    elif orders:
        fail(f"orders-api must have no change < 6 h outside bad-config-rollout, found {[e['id'] for e in orders]}")

    notif = recent_changes(dep, "notifications-worker", 6 * 60)
    if want("missing-secret-key"):
        if len(notif) != 1 or "SMTP_PASSWORD" not in notif[0].get("summary", ""):
            fail(f"missing-secret-key: exactly one notifications-worker change < 6 h naming "
                 f"SMTP_PASSWORD expected; found {[e['id'] for e in notif]}")
    elif notif:
        fail(f"notifications-worker must have no change < 6 h outside missing-secret-key, "
             f"found {[e['id'] for e in notif]}")
    if not any(e["service"] == "notifications-worker" and "smtp-password" in e.get("summary", "") for e in dep):
        fail("missing-secret-key: the Secret rotation record naming key smtp-password is missing")

    # The scenario rests on a relation, not on recognised numbers: 300/600 in
    # 2024, 600/900 today.
    hist = next((e for e in corpora["incidents"] if e["id"] == "INC-2024-0312"), {})
    if "300 s" not in hist.get("root_cause", "") or "600 s" not in hist.get("root_cause", ""):
        fail("proxy-timeout: INC-2024-0312 must carry 300 s / 600 s")
    if "900" in hist.get("root_cause", ""):
        fail("proxy-timeout: INC-2024-0312 must not carry the current pool value (900 s)")
    rbs = {e["id"]: e for e in corpora["runbooks"]}
    rb14 = rbs.get("RB-014", {})
    for need in ("idle", "pool"):
        if need not in rb14.get("tags", ""):
            fail(f"proxy-timeout: RB-014 lost the signal '{need}'")
    warning = rb14.get("warning", "").lower()
    for need in ("under load", "restart"):
        if need not in warning:
            fail(f"proxy-timeout: RB-014 warning must cover '{need}'")
    if "RB-014" not in rbs.get("RB-007", {}).get("warning", ""):
        fail("proxy-timeout: RB-007 must point to RB-014 in its warning")
    for rid, need in (("RB-021", "readiness"), ("RB-026", "crashloopbackoff")):
        if need not in rbs.get(rid, {}).get("tags", ""):
            fail(f"{rid} lost its signal '{need}'")

    ongoing = [e["id"] for e in corpora["vendor"] if e["status"] == "ongoing"]
    if ongoing != ["AWS-2026-0903-01"]:
        fail(f"vendor: exactly one ongoing bulletin expected, found {ongoing}")

    for name, prefix in (("deployments", "CI-"), ("support", "SUP-")):
        seq = sorted((e["date"], int(e["id"].removeprefix(prefix))) for e in corpora[name])
        nums = [n for _, n in seq]
        if nums != sorted(nums):
            fail(f"{name}: identifiers do not grow with time")

    utc_now = datetime.now(timezone.utc)
    for name, data in corpora.items():
        for e in data:
            if "date" not in e:
                continue
            fmt = "%Y-%m-%d %H:%M" if " " in e["date"] else "%Y-%m-%d"
            if (datetime.strptime(e["date"], fmt).replace(tzinfo=timezone.utc) - utc_now).total_seconds() > 60:
                fail(f"{name}: {e['id']} dated in the future ({e['date']})")
        ids = [e["id"] for e in data]
        if len(ids) != len(set(ids)):
            fail(f"{name}: duplicate identifiers {sorted({i for i in ids if ids.count(i) > 1})}")

    if errors:
        print("\nINVARIANTS VIOLATED - corpus not written:", file=sys.stderr)
        for e in errors:
            print(f"  x {e}", file=sys.stderr)
        sys.exit(1)


def main():
    global NOW
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("out")
    ap.add_argument("--scenario", default="all", choices=(*SCENARIOS, "all", "none"))
    ap.add_argument("--incident-time", help="ISO 8601 time the dates are relative to (default: now, UTC)")
    args = ap.parse_args()
    if args.incident_time:
        t = datetime.fromisoformat(args.incident_time.replace("Z", "+00:00"))
        NOW = (t if t.tzinfo else t.replace(tzinfo=timezone.utc)).astimezone(timezone.utc)

    s = args.scenario
    oldest_first = lambda e: -e["_m"]  # noqa: E731
    corpora = {
        "deployments": compose("deployments", s, noise_deployments, oldest_first,
                               renumber_from("CI-", CI_BASE)),
        "support": compose("support", s, noise_support, oldest_first,
                           renumber_anchored("SUP-", "SUP-4471")),
        "vendor": compose("vendor", s, noise_vendor, oldest_first, renumber_aws),
        "incidents": compose("incidents", s, noise_incidents, oldest_first),
        "runbooks": compose("runbooks", s, noise_runbooks, lambda e: e["id"]),
    }
    check(corpora, s)

    os.makedirs(args.out, exist_ok=True)
    for name, data in corpora.items():
        path = os.path.join(args.out, f"{name}.json")
        with open(path, "w", encoding="utf-8") as fh:
            json.dump(data, fh, ensure_ascii=False, indent=2)
        print(f"{path}: {len(data)} records")
    print(f"dates relative to {NOW.strftime('%Y-%m-%d %H:%M')} UTC, scenario={s}")
    for name, ids in signals(corpora, s).items():
        print(f"signals {name}: {' '.join(ids)}")


if __name__ == "__main__":
    main()
