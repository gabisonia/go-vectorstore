"""Select required verification for a PR and check its completed job results."""

import json
import os
import re
import subprocess
import sys


# Only regular-file edits to known documentation qualify. Additions, deletions,
# renames, mode changes and new paths receive full verification.
PROSE_FILES = {
    b"CONTRIBUTING.md", b"README.md", b"docs/README.md", b"docs/ai-guide.md",
    b"docs/architecture.md", b"docs/connector-development.md",
    b"docs/embedding-outputs.md", b"docs/migrations.md", b"docs/model-api.md",
    b"docs/native-support.md", b"docs/parity.md", b"docs/performance.md",
    b"docs/production.md", b"docs/qdrant.md", b"docs/redis.md",
    b"docs/sqlite.md", b"docs/sqlitevec.md", b"docs/typed-fields.md",
    b"docs/upgrading.md",
}
PROSE_REQUIRED = {"profile", "test", "documentation-site"}
FULL_ONLY = {
    "fuzz", "sqlite-vec-probe", "qdrant-probe", "reference-catalog",
    "standalone-dotnet-parity", "sqlserver-dotnet-parity", "qdrant-dotnet-parity",
    "integration-postgres", "integration-redis", "local-doc-agent",
    "docs-retrieval-pilot", "aspire", "vulnerabilities", "api-compatibility",
    "storage-upgrade",
}


def classify(diff):
    fields = diff.split(b"\0")
    if len(fields) < 3 or len(fields) % 2 != 1 or fields[-1]:
        return "full"
    for header, path in zip(fields[0:-1:2], fields[1:-1:2]):
        if not re.fullmatch(rb":100644 100644 [0-9a-f]+ [0-9a-f]+ M", header):
            return "full"
        if path not in PROSE_FILES:
            return "full"
    return "prose"


def profile(event, base, head):
    if event != "pull_request" or not all(re.fullmatch("[0-9a-f]{40}", sha) for sha in (base, head)):
        return "full"
    diff = subprocess.run(
        ["git", "diff", "--raw", "-z", "--no-renames", f"{base}...{head}", "--"],
        stdout=subprocess.PIPE, check=False,
    )
    return classify(diff.stdout) if diff.returncode == 0 else "full"


def verify(jobs):
    selected = jobs.get("profile", {}).get("outputs", {}).get("profile")
    if selected not in {"full", "prose"}:
        raise ValueError(f"Missing or invalid CI profile: {selected!r}")
    missing = (PROSE_REQUIRED | FULL_ONLY) - jobs.keys()
    if missing:
        raise ValueError(f"Required CI dependencies missing: {sorted(missing)}")
    for name, job in jobs.items():
        expected = "skipped" if selected == "prose" and name in FULL_ONLY else "success"
        if job.get("result") != expected:
            raise ValueError(f"{name}: expected {expected}, got {job.get('result')}")
    return selected


if __name__ == "__main__":
    if sys.argv[1:] == ["classify"]:
        print(profile(os.environ["GITHUB_EVENT_NAME"], os.environ["BASE_SHA"], os.environ["HEAD_SHA"]))
    elif sys.argv[1:] == ["verify"]:
        print(f"Required verification passed for {verify(json.loads(os.environ['JOB_RESULTS']))} changes.")
    else:
        sys.exit("Usage: ci_profile.py {classify|verify}")
