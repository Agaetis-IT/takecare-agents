#!/usr/bin/env python3
"""
Demo application, standard library only (spike 001, throwaway code).

Ported from the challenge POC (content/app/exporter.py) and translated.

It plays the demo services (`billing-api`, `orders-api`, `notifications-worker`,
`orders-db`) AND exposes its own Prometheus metrics, like a real instrumented
application would. No dependency: the script is mounted from a ConfigMap into a
plain python image, so there is nothing to build or download at pod start.

MODE selects the behaviour; each mode serves one scenario:

  healthy   Everything is fine. Scenery around the service in trouble.

  leaky     Scenario `proxy-timeout`. RSS really grows and intermittent 504s
            appear. Growth is capped well below the memory limit: the pod must
            NOT be OOM-killed, otherwise the story changes ("no restart").
            Logs say `connection reset by peer`: the PROXY closes, the app does
            not time out. A timeout blames the app, a reset blames whatever is
            on the other side. The app states only what it observes; linking it
            to the proxy idle timeout is the agent's job.

  db-check  Scenario `bad-config-rollout`. Readiness passes only when DB_HOST
            accepts a TCP connection; liveness always passes. With a wrong
            DB_HOST the pod stays Running, never restarts, never becomes Ready.
            The cause (a ConfigMap change) is outside the pod.

REQUIRED_SETTINGS (any mode) lists environment variables that must be set and
non-empty; when one is missing the process logs it and exits 1, so Kubernetes
restarts it in a loop (scenario `missing-secret-key`).

Environment variables:
  MODE           healthy | leaky | db-check                 (default: healthy)
  SERVICE        service name in the metrics                (default: demo-api)
  RAMP_MINUTES   duration of the ramp (leaky)               (default: 10)
  LEAK_MINUTES   duration of the memory growth (leaky)      (default: RAMP_MINUTES)
  LEAK_CEILING_MB  cap of the memory growth (leaky)         (default: 180)
  MEMORY_LIMIT_MB  container memory limit, for the logs     (default: 512)
  DB_HOST        host:port probed by readiness (db-check)   (default: empty)
  DB_POOL_IDLE_TIMEOUT_SECONDS  idle connection recycling   (default: 900)
  REQUIRED_SETTINGS  comma-separated required variables     (default: empty)
  PORT           listening port                             (default: 8080)

Prometheus scrapes in real time and cannot invent history: the curve builds up
scrape after scrape.
"""
import os
import socket
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

MODE = os.environ.get("MODE", "healthy")
SERVICE = os.environ.get("SERVICE", "demo-api")
RAMP_SECONDS = float(os.environ.get("RAMP_MINUTES", "10")) * 60
DB_HOST = os.environ.get("DB_HOST", "")
LEAK_CEILING_MB = int(os.environ.get("LEAK_CEILING_MB", "180"))
MEMORY_LIMIT_MB = int(os.environ.get("MEMORY_LIMIT_MB", "512"))
# Leak duration, distinct from the error ramp: the alert fires around the middle
# of the ramp, and memory must already be high by then to weigh in the
# investigation. Defaults to the ramp duration.
LEAK_SECONDS = float(os.environ.get("LEAK_MINUTES", "0")) * 60 or RAMP_SECONDS
# Application side of the AWS rule: the pool must recycle its idle connections
# BEFORE the proxy closes them. Read here so that it shows in a plain
# `kubectl describe pod`, and repeated in the startup banner: the application
# half of the diagnosis must be observable, not told.
DB_POOL_IDLE_TIMEOUT = int(os.environ.get("DB_POOL_IDLE_TIMEOUT_SECONDS", "900"))
REQUIRED_SETTINGS = [s.strip() for s in os.environ.get("REQUIRED_SETTINGS", "").split(",") if s.strip()]
PORT = int(os.environ.get("PORT", "8080"))

STARTED = time.monotonic()

# Levels: (healthy, degraded)
LATENCY_P99 = (0.35, 4.20)   # seconds
LATENCY_P50 = (0.08, 0.55)
DB_WAIT_P99 = (0.01, 2.80)   # time to acquire a pooled connection: the real cause
REQUEST_RATE = 42.0          # req/s, steady: traffic does not change

_ballast: list[bytearray] = []
_ballast_lock = threading.Lock()


def progress() -> float:
    """0.0 -> 1.0: progress of the degradation, bounded and monotonic."""
    if MODE != "leaky":
        return 0.0
    if RAMP_SECONDS <= 0:
        return 1.0
    return min(1.0, (time.monotonic() - STARTED) / RAMP_SECONDS)


def lerp(pair, p):
    lo, hi = pair
    return lo + (hi - lo) * p


def leak_worker() -> None:
    """Real, capped memory growth. The cap is mandatory: an OOMKill would
    contradict the scenario ("no restart")."""
    chunk_mb = 4
    while True:
        with _ballast_lock:
            if len(_ballast) * chunk_mb < LEAK_CEILING_MB:
                # Filled, not only reserved: a page never written is not
                # resident, and `kubectl top` would see nothing grow.
                _ballast.append(bytearray(b"x") * (chunk_mb * 1024 * 1024))
        time.sleep(max(0.5, LEAK_SECONDS / (LEAK_CEILING_MB / chunk_mb)))


def db_probe() -> str | None:
    """TCP probe of DB_HOST. Returns None when reachable, else the error."""
    if not DB_HOST:
        return None
    host, _, port = DB_HOST.partition(":")
    try:
        with socket.create_connection((host, int(port or 5432)), timeout=2):
            return None
    except OSError as exc:
        return str(exc) or exc.__class__.__name__


def leaked_mb() -> int:
    with _ballast_lock:
        return len(_ballast) * 4


def render_metrics() -> str:
    p = progress()
    elapsed = time.monotonic() - STARTED
    total = REQUEST_RATE * elapsed
    # In `leaky` the error share must cross the HighErrorRate threshold (2 %):
    # 0.1 % + 6 % * p^2 crosses 2 % at p ~ 0.56, i.e. a bit past the middle of
    # the ramp. With the [1m] rate window and `for: 30s`, the alert fires about
    # 3 to 4 minutes after start with RAMP_MINUTES=4.
    err_ratio = 0.001 + (0.06 if MODE == "leaky" else 0.0) * (p ** 2)
    ok = total * (1 - err_ratio)
    err = total * err_ratio

    out = [
        "# HELP http_request_duration_seconds HTTP request latency (summary).",
        "# TYPE http_request_duration_seconds summary",
        f'http_request_duration_seconds{{service="{SERVICE}",quantile="0.5"}} {lerp(LATENCY_P50, p):.4f}',
        f'http_request_duration_seconds{{service="{SERVICE}",quantile="0.99"}} {lerp(LATENCY_P99, p):.4f}',
        "# HELP http_requests_total HTTP requests handled.",
        "# TYPE http_requests_total counter",
        f'http_requests_total{{service="{SERVICE}",status="200"}} {ok:.0f}',
        f'http_requests_total{{service="{SERVICE}",status="504"}} {err:.0f}',
        "# HELP db_connection_wait_seconds Time to acquire a pooled database connection.",
        "# TYPE db_connection_wait_seconds summary",
        f'db_connection_wait_seconds{{service="{SERVICE}",quantile="0.99"}} {lerp(DB_WAIT_P99, p):.4f}',
        "# HELP app_heap_bytes Memory retained by the application.",
        "# TYPE app_heap_bytes gauge",
        f'app_heap_bytes{{service="{SERVICE}"}} {leaked_mb() * 1024 * 1024}',
        "# HELP demo_degradation_progress Progress of the scripted degradation (0 to 1).",
        "# TYPE demo_degradation_progress gauge",
        f'demo_degradation_progress{{service="{SERVICE}"}} {p:.4f}',
    ]
    return "\n".join(out) + "\n"


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def _send(self, code: int, body: bytes, ctype: str = "text/plain; charset=utf-8"):
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path.startswith("/metrics"):
            self._send(200, render_metrics().encode(),
                       "text/plain; version=0.0.4; charset=utf-8")
        elif self.path.startswith("/ready"):
            err = db_probe() if MODE == "db-check" else None
            if err is None:
                self._send(200, b"ready\n")
            else:
                # What a real app would log: the host it tried and the error.
                # Whether the host is right is not something the pod knows.
                print(f"WARN  database {DB_HOST} not reachable ({err}), retrying", flush=True)
                self._send(503, b"not ready: database not reachable\n")
        elif self.path.startswith("/healthz"):
            # Always 200: liveness passes, so the pod does not restart.
            self._send(200, b"alive\n")
        elif self.path in ("/", "/api"):
            self._send(200, b"ok\n")
        else:
            self.send_error(404)

    def log_message(self, fmt, *args):  # no access log noise in the pod logs
        pass


def chatter() -> None:
    """A few application log lines so that `kubectl logs` has material,
    without ever naming the root cause."""
    while True:
        p = progress()
        if MODE == "leaky" and p > 0.3:
            # `reset by peer`, not "timeout": the app did not give up waiting,
            # the other side hung up. The acquire time is true and measured;
            # it is the trace of the pool rebuilding itself, not a conclusion.
            print(f"WARN  db connection reset by peer, retrying "
                  f"(pool acquire {lerp(DB_WAIT_P99, p):.2f}s)", flush=True)
            # The false lead, where everybody looks: the logs. True (it is the
            # retained memory), and it concludes nothing.
            heap = leaked_mb()
            if heap >= 0.4 * MEMORY_LIMIT_MB:
                print(f"WARN  memory pressure: heap {heap} MB / {MEMORY_LIMIT_MB} MB limit "
                      f"({100 * heap // MEMORY_LIMIT_MB}%), gc pauses rising", flush=True)
        else:
            print(f"INFO  {SERVICE} serving, p99={lerp(LATENCY_P99, p):.2f}s", flush=True)
        time.sleep(20)


def check_required_settings() -> None:
    missing = [name for name in REQUIRED_SETTINGS if not os.environ.get(name)]
    if not missing:
        return
    for name in missing:
        print(f"ERROR {SERVICE}: missing required setting {name} "
              f"(environment variable is unset or empty)", flush=True)
    print(f"FATAL {SERVICE}: configuration invalid, exiting", flush=True)
    time.sleep(1)  # let the log line reach the container runtime
    sys.exit(1)


if __name__ == "__main__":
    print(f"{SERVICE}: mode={MODE} ramp={RAMP_SECONDS / 60:.0f}min "
          f"db_host={DB_HOST or '-'} port={PORT}", flush=True)
    check_required_settings()
    if MODE == "leaky":
        print(f"{SERVICE}: db pool min_size=5 max_size=20 "
              f"idle_timeout={DB_POOL_IDLE_TIMEOUT}s", flush=True)
        threading.Thread(target=leak_worker, daemon=True).start()
    threading.Thread(target=chatter, daemon=True).start()
    ThreadingHTTPServer(("0.0.0.0", PORT), Handler).serve_forever()
