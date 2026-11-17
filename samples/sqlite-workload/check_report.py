#!/usr/bin/env python3
"""Exercise report integrity checks using a genuine small harness run."""
import copy
import argparse
import hashlib
import json
from pathlib import Path
import tempfile

from summarize import load


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source")
    parser.add_argument("--manifest")
    args = parser.parse_args()
    source = Path(args.source)
    load(source)
    events = [json.loads(line) for line in source.read_text(encoding="utf-8").splitlines()]
    with tempfile.TemporaryDirectory(prefix="workload-report-check-") as directory:
        def reject(name, changed, message):
            path = Path(directory) / name
            path.write_text("".join(json.dumps(event) + "\n" for event in changed), encoding="utf-8")
            try:
                load(path)
            except ValueError as error:
                if message not in str(error):
                    raise AssertionError(f"unexpected rejection: {error}") from error
            else:
                raise AssertionError(f"accepted {name}")

        reject("incomplete.jsonl", events[:-1], "completion")
        changed = copy.deepcopy(events)
        digest = changed[1]["queries"][0]["page_sha256"]
        changed[1]["queries"][0]["page_sha256"] = ("0" if digest[0] != "0" else "1") + digest[1:]
        reject("different-page.jsonl", changed, "digests differ")
        changed = copy.deepcopy(events)
        changed[1]["queries"].pop()
        reject("missing-sample.jsonl", changed, "query matrix is incomplete")
    print("Verified report completion, sample coverage and cross-path digest checks.")
    if args.manifest:
        manifest_path = Path(args.manifest)
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        for relative, expected in manifest["sha256"].items():
            actual = hashlib.sha256((manifest_path.parent / relative).read_bytes()).hexdigest()
            if actual != expected:
                raise AssertionError(f"artifact checksum differs: {relative}")
        print("Verified published artifact and generator checksums.")


if __name__ == "__main__":
    main()
