# Contributing to go-vectorstore

A good first contribution can be a quickstart usability report, a correction
to an example, or a regression test. Browse the
[open good first issues](https://github.com/gabisonia/go-vectorstore/labels/good%20first%20issue)
for tasks with a concrete finish line. For security problems, use the
[private reporting channel](SECURITY.md).

## Run one check

From a checkout with Go 1.26 or later, run:

```sh
go test ./samples/typed-search
```

The test runs the same filtered search in memory and in a temporary SQLite
database. A passing run prints `ok` followed by the package path. The first
run downloads Go dependencies. No API keys or database services are needed.

## Check your change

| If you change | Start with |
| --- | --- |
| An example under `samples/typed-search` | `go test ./samples/typed-search` |
| Public types, filters or codecs | `go test ./vectordata ./stores/memory` |
| SQLite storage or ranking | `go test -race ./stores/sqlite` |
| A guide or documentation link | `python3 scripts/check-docs.py` (requires Python 3 and Go) |
| The handbook walkthrough | `bash samples/handbook/replay.sh sqlite` |

Format changed Go files with `gofmt`. For provider integrations, release
checks and isolated Go modules, see the [repository verification guide](AGENTS.md).
For edits limited to existing, allowlisted documentation such as this guide or
the README, CI builds the site and runs the executable quickstarts on both
supported Go versions. Code changes, mixed changes, and added or renamed files
run the full matrix. Every push to master and every release runs the full
matrix. Both profiles report their result through **Required CI**.

## Submit a pull request

Fork the repo, create a branch from current master in your fork, and open a
PR against master. Describe the behavior someone can observe, link the issue
if there is one, and give the exact command you tested. For example, the
[handbook walkthrough PR](https://github.com/gabisonia/go-vectorstore/pull/121)
explains what appears before and after reindexing and names both tested backends.
If a check needs a service you couldn't run, say which one.
