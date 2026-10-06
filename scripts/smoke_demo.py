"""Exercise the Compose demo path and time synthetic metric-detection to rollback."""
from datetime import datetime
import json
import statistics
import time
from urllib.error import URLError
from urllib.request import Request, urlopen


API = "http://127.0.0.1:8080"
RELEASES = 25
TIMEOUT_SECONDS = 35


def request_json(path: str, method: str = "GET", payload: dict | None = None) -> object:
    body = json.dumps(payload).encode() if payload is not None else None
    request = Request(API + path, data=body, method=method, headers={"Content-Type": "application/json"})
    with urlopen(request, timeout=5) as response:
        return json.loads(response.read())


def parse_time(value: str) -> datetime:
    return datetime.fromisoformat(value.replace("Z", "+00:00"))


def main() -> None:
    config = request_json("/api/config")
    if not config.get("demoMode"):
        raise RuntimeError("controller did not start in DEMO_MODE")

    rollout_ids = []
    for _ in range(RELEASES):
        rollout = request_json("/api/demo/start", "POST", {"scenario": "bad"})
        rollout_ids.append(rollout["id"])

    deadline = time.monotonic() + TIMEOUT_SECONDS
    while time.monotonic() < deadline:
        rollouts = request_json("/api/rollouts")
        phases = {rollout["id"]: rollout["phase"] for rollout in rollouts}
        if all(phases.get(rollout_id) == "RolledBack" for rollout_id in rollout_ids):
            break
        time.sleep(0.2)
    else:
        pending = [rid for rid in rollout_ids if phases.get(rid) != "RolledBack"]
        raise AssertionError(f"{len(pending)} of {RELEASES} bad releases did not roll back before timeout")

    events = request_json("/api/events")
    latencies_ms = []
    for rollout_id in rollout_ids:
        matching = [event for event in events if event["rolloutId"] == rollout_id]
        samples = [event for event in matching if event["kind"] == "sample" and event["errorRate"] > 0.01]
        rollbacks = [event for event in matching if event["kind"] == "rollback"]
        if not samples or not rollbacks:
            raise AssertionError(f"missing threshold sample or rollback event for {rollout_id}")
        delay = (parse_time(rollbacks[-1]["at"]) - parse_time(samples[-1]["at"])).total_seconds() * 1000
        if delay < 0:
            raise AssertionError(f"rollback timestamp preceded detection sample for {rollout_id}")
        latencies_ms.append(delay)

    median = statistics.median(latencies_ms)
    maximum = max(latencies_ms)
    print(f"PASS: {RELEASES}/{RELEASES} bad demo releases rolled back")
    print(f"Detection-sample-to-rollback event delay: median={median:.1f} ms, max={maximum:.1f} ms")
    if median >= 9000:
        raise AssertionError(f"simulated decision median {median:.1f} ms exceeds 9000 ms")


if __name__ == "__main__":
    try:
        main()
    except URLError as exc:
        raise SystemExit(f"demo API was not reachable: {exc}") from exc
