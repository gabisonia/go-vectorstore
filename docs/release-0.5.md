# v0.5.0 compatibility review

Published [v0.5.0](https://github.com/gabisonia/go-vectorstore/releases/tag/v0.5.0)
follows v0.4.1. It is a minor release because
it adds model APIs, provider options and connectors and includes the deliberate
source incompatibility below. It is not a v1 long-term API stability commitment.
Its release workflow completed before publication on November 20, 2026.

## Public Go API

The v0.5.0 release compared all exported module packages with immutable v0.4.1
using a pinned golang.org/x/exp apidiff tool outside the root module. The archived
[reviewed report](../scripts/api-v0.4.1-incompatible.txt) contains one incompatible
change: `HybridSearchOptions` gained a slice and ceased to be comparable.

After publication, `bash scripts/check-api.sh` used v0.5.0 as its baseline and
required zero incompatible API changes, including APIs introduced in v0.5.0.
See the [release policy](releases.md#compatibility-policy) for the current baseline.
The historical report is not used to allow breaks in the current minor line.
The tool requires Go 1.26+; the library keeps Go 1.25. Review API and upgrade
consequences before advancing a published baseline.

Existing required ModelStore/ModelCollection interfaces, original Record API
and existing public method signatures remain supported. New workflows use
additive functions, concrete methods and optional interfaces. Exported schema
and option structs gained fields; applications should use keyed literals. The
API comparison does not prove behavioral, persisted-data or unkeyed-literal
compatibility. Correctness fixes can reject invalid data previously accepted.

Review [upgrade notes](upgrading.md) for UUID key bindings, JSON scalar encoding,
custom JSON decoders, native arrays and cancellation around user codecs. In
particular, validate/re-upsert affected old JSON from authoritative inputs;
neither reading nor Ensure rewrites it automatically.

## Stored data and providers

- SQLite canonical format 1 remains unchanged. The published v0.4.0 fixture is
  still tested. Scalar/vector projections are optional and rebuilding existing
  collections is explicit; unaware writers invalidate vector readiness. The
  sqlite-vec adapter uses the same engine/files and retains documented fallback
  and scalar L1 precision gates. Older readers are not guaranteed to understand
  newly registered schema features. See [SQLite](sqlite.md) and
  [sqlite-vec](sqlitevec.md).
- PostgreSQL retains original and model storage contracts. Apply only reviewed
  additive schema/index migrations with a backup and application cutover plan;
  Ensure does not convert Record tables. See [migrations](migrations.md).
- Redis and Qdrant are newly included providers. Redis snapshot/HASH/JSON formats
  have explicit conversion, immutable compatibility fixtures, bounded collection
  sizes and whole-generation write costs. Qdrant is a bounded standalone
  canonical-snapshot provider with finite receipts and explicit unknown-write
  handling. Neither connector implies distributed failover or unbounded scale.
  See [Redis deployment limits](redis.md#costs-and-deployment-settings) and
  [Qdrant scope](qdrant.md).

## Release evidence and remaining application work

The release workflow runs all CI matrices, fuzzing, docs/samples, dependency
scans and API comparison before publication. SQLite and Qdrant have process-crash
checks. Redis adds AOF restart and isolated RDB restore checks plus concurrent
batch/query/search validation and generation cleanup. The weekly Redis job
retains JSONL observations; see [recovery and workload limits](redis.md#recovery-and-mixed-workload-verification).

These checks cover specific fixtures and process failures. Each production
deployment still needs workload/recall measurements, actual backup/restore drills,
capacity/deadline budgets, credentials/TLS and monitoring. No cloud API, tenant
authorization, telemetry backend or hosting SLA is supplied by the library.
See [production operation](production.md) and [security support](../SECURITY.md).

Before v1, review consumer feedback on exported contracts and provider limits,
decide the long-term compatibility policy, and publish migration guidance for
any further breaking changes. The v1 compatibility review remains future work.
