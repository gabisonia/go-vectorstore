#!/usr/bin/env python3
"""Wait for one explicitly selected Aspire resource to exit successfully."""
import argparse
import json
import subprocess
import sys
import time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--apphost", required=True)
    parser.add_argument("--resource", required=True)
    parser.add_argument("--timeout", type=int, default=1200)
    args = parser.parse_args()
    deadline = time.monotonic() + args.timeout
    previous = None
    while time.monotonic() < deadline:
        result = subprocess.run(
            ["aspire", "describe", "--apphost", args.apphost, "--format", "Json", "--non-interactive"],
            capture_output=True, text=True, timeout=30,
        )
        if result.returncode:
            raise RuntimeError("Cannot inspect the selected AppHost: " + result.stderr.strip())
        if not result.stdout.strip():
            raise RuntimeError("No resource status returned; the selected AppHost may have stopped")
        rows = json.loads(result.stdout)["resources"]
        resource = next((r for r in rows if r.get("displayName") == args.resource or r.get("name") == args.resource), None)
        state = resource.get("state") if resource else "Not discovered"
        if state != previous:
            print(f"{args.resource}: {state}", file=sys.stderr, flush=True)
            previous = state
        if state == "Exited":
            code = resource.get("exitCode")
            if code not in (0, "0"):
                raise RuntimeError(f"{args.resource} did not exit successfully (code={code!r})")
            return
        if state in ("FailedToStart", "Failed", "RuntimeUnhealthy"):
            raise RuntimeError(f"{args.resource}: {state}; inspect aspire logs for this resource")
        time.sleep(1)
    raise TimeoutError(f"{args.resource} did not finish within {args.timeout}s")


if __name__ == "__main__":
    main()
