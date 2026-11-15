# Changelog

## v0.4.0 — 2026-11-15

[Release notes](https://github.com/gabisonia/go-vectorstore/releases/tag/v0.4.0).

### Model-driven collections

- Add ModelStore/ModelCollection, dynamic records, native string/int64/UUID keys,
  generated int64 keys, typed collections, struct tags and explicit schemas.
- Support multiple named dense vectors, float16 precision, packed binary/Hamming
  vectors, and sparse cosine/L2/inner-product/L1 vectors, with compatible indexes.
- Support optional vectors of every kind, nullable typed fields, and explicit
  missing-vector exclusion in vector/hybrid searches.
- Add native UUID/bytes, numeric/bool/UUID/timestamp arrays, date-only/time-only
  values and arrays, and exact .NET-range decimal properties and arrays.
- Add portable model filtering, JSON paths, array membership and efficient
  ContainsAny/ContainsAll predicates; deterministic retrieval/paging, thresholds,
  optional projections, iterators, and context cancellation.
- Add automatic text embeddings with store/collection/property overrides and
  bounded batches, and PostgreSQL keyword/vector hybrid search with RRF.
- Add local capability discovery and shared provider conformance tests.

### Providers and schema evolution

- Add durable embedded SQLite model storage with transactional batches, generated
  sequences, consistent snapshot queries and exact dense/binary/sparse search.
- Add an in-memory provider for development and contract testing.
- Add PostgreSQL native model schema registration/validation, index compatibility
  metadata, read-only migration plans, drift detection and explicit atomic
  application of reviewed optional data/vector and index additions.
- Reduce PostgreSQL bulk vector serialization allocations with scoped binary
  parameters; add measured performance comparisons and index-plan verification.

### Compatibility and verification

- Keep the original Record API and custom codecs available alongside model APIs.
- Preserve JSON numeric precision with json.Number; see the
  [upgrade guide](docs/upgrading.md) for application adjustments and migration rules.
- Require Go 1.25+, update dependencies, and validate Go minimum/current stable,
  PostgreSQL 16/18, races, vet, vulnerabilities and release tooling in CI.
- Add model/SQLite examples, provider parity checklist and release/upgrade guides.

SQLite queries scan an entire collection snapshot and provide no physical ANN,
full-text or hybrid indexes. Automatic migration application remains an additive
subset; non-additive conversion/backfill/re-embedding requires separate work.

## v0.3.1

The previous published release is available in the
[GitHub release history](https://github.com/gabisonia/go-vectorstore/releases/tag/v0.3.1).
