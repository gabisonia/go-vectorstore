# Changelog

## Unreleased

- Investigate SQLite vector execution using the pinned driver's pure-Go
  sqlite-vec package. Record the opt-in design, precision and mixed-writer gates,
  and add an isolated compatibility probe with CGo-free Linux/macOS/Windows CI.
  Production provider behavior and capabilities remain unchanged.

- Add immutable JSON-backed provider annotations on key/data/vector definitions,
  typed accessors, exact numeric canonicalization and backward-compatible empty
  registrations. Implement Redis TEXT NoStem with typed options, local validation
  and guarded HASH/JSON schema checks. Cover cross-provider conformance, ownership,
  numeric round trips, old fixtures, hybrid matching, rebuild/drift and rerunnable
  samples. Force fresh CI backend integration runs instead of cached results.
  Record the ordered Microsoft.Extensions.VectorData parity plan.

- Use count-bounded CI fuzz campaigns to avoid the Go 1.25 duration-expiry
  shutdown race. Keep every fuzz target, fatal failures and per-target timeouts;
  retain explicit duration budgets for longer local campaigns.

- Add typed and dynamic model InsertOne/UpsertOne/DeleteOne/TryGet helpers for
  Microsoft-style single-record workflows. Preserve batch/provider compatibility,
  generated keys, vector projection, codec failures and found/error semantics;
  make one underlying call without write retries. Fix typed batch replies silently
  accepting nil keys or incorrect key counts. Classify duplicate PostgreSQL model
  writes as ErrInvalidRecord while preserving native SQLSTATE diagnostics.
  Add shared provider contracts,
  error/cancellation regressions, executable examples and rerunnable recipes.

- Add opt-in SQLite scalar indexes for indexed string/int64/float64/bool fields.
  Build new collections atomically and explicitly backfill existing format-1
  collections without changing payloads or registrations. Triggers maintain all
  writers; eligible Query/Count predicates use indexed candidate keys with exact
  SQL residuals and one WAL snapshot. Cover conformance, real index plans,
  precision, legacy writers, failure rollback, samples and capability discovery.

- Add native Redis hybrid KeywordTerms lists for HASH/JSON with OR between
  plain-text AND groups, literal bound tokens, validation before embedding and
  caller-owned list snapshots. Enable capability discovery, shared typed/input/
  iterator contracts, rerunnable samples and CI coverage.

- Add native dense Redis hybrid search for HASH/JSON TEXT generations, combining
  plain keyword BM25 ranks and canonical vector ranks with RRF. Read both lists
  and payloads under one manifest/readiness guard; preserve filters, optional
  vectors, exact key ties, thresholds, paging and guarded exact fallbacks. Add
  text/custom-input forms, lazy iterators, local validation before embedding,
  compatibility/readiness errors and native hybrid capability discovery. Cover
  contracts, RRF/reference cases, recall and generation consistency; add rerunnable
  samples and CI. Standalone keyword queries remain absent.

- Honor string full-text hints in native RedisJSON mode with separate raw TEXT
  projections, capability discovery and index metadata version 5. Preserve legacy
  vector/scalar schemas until explicit rebuild/native writes and keep canonical
  payloads/registrations unchanged. Cover tokenization, NULL/Unicode fidelity,
  schema drift, field limits, storage conversion and atomic lifecycle on both
  layouts; add a rerunnable JSON full-text sample and CI coverage.

- Add native RedisJSON scalar filtering for indexed strings, int64, float64 and
  bool values plus keys, accelerating filtered Query/Count and HNSW/FLAT search.
  Preserve full integer precision and SQL NULL truth with separate literal TAGs,
  presence markers and split integer projections. Index metadata version 4 retains
  vector-only version-3 reads until explicit rebuild/native writes. Cover native
  routing, precision, drift, compatibility and atomic lifecycle behavior; update
  samples, capability discovery and measured benchmarks.

- Add opt-in RedisJSON native HNSW/FLAT vector indexes with canonical precision,
  prepublication readiness, atomic KNN/wrapper reads, explicit rebuild and safe
  index pruning. Cover both native layouts with contracts, recall/exact-reference
  and failure tests; add rerunnable JSON native samples and CI.

- Add opt-in RedisJSON storage with immutable format-3 document generations,
  atomic manifest publication/reads and explicit snapshot/hash conversion.
  Preserve canonical key/data/JSON precision through opaque payload strings,
  generated sequences, cancellation and uncertain-reply guarantees. Add guarded
  JSON pruning, cross-layout cleanup, real RedisJSON contracts/failure tests,
  a persistent sample and Redis 8.2 CI coverage. Default snapshot and existing hash
  formats stay compatible.

- Honor string `IsFullTextIndexed` hints with Redis TEXT indexes in native mode,
  independent of scalar TAG filtering. Add observable schema validation and
  full-text capability discovery while keeping keyword/hybrid APIs unsupported.
  Index metadata version 2 preserves legacy scalar/vector-only reads and explicit
  rebuilds without changing storage/registrations. Cover real tokenization,
  Unicode/NULL values, upgrades, drift and atomic lifecycle behavior; add a
  rerunnable full-text sample and CI coverage.

- Add native Redis FLAT indexes with `IndexMethodFlat`, eight additional dense
  metric/precision tuples and algorithm drift detection. Exhaustively remeasure
  matching vectors in Go to preserve exact ordering, ties, thresholds and paging;
  reject HNSW tuning before I/O/embedding. Keep atomic generation publication,
  fallback and cleanup guarantees. Add exact-reference/lifecycle tests, filtered
  and unfiltered benchmarks, and a rerunnable FLAT sample with CI coverage.

- Add opt-in Redis Search HNSW and scalar-filter acceleration on immutable hash
  generations. Preserve atomic publication/reads, exact integer and NULL filter
  behavior, Go distance/score units and fallback for unsupported cases/ties.
  Add search tuning, explicit rebuild/index pruning, real Search conformance and
  recall checks, read benchmarks and a native sample. Requires database zero;
  every write rebuilds the generation's index. Full-text/hybrid remain unsupported.

- Preserve Redis context deadline causes when a socket timeout arrives before
  the context timer updates; retain native errors and shorter client timeouts.
- Add opt-in Redis hash generations with atomic manifest publication and explicit
  format-1 upgrades. Preserve schema, key sequences and consistent reads; expose
  scalar/vector hash fields without creating indexes. Add safe orphan pruning,
  immutable compatibility fixtures, failure/concurrency tests and a real Redis
  Search projection proof. Default snapshot storage remains compatible.
- Add a standalone Redis model provider with atomic collection snapshots, native
  key/data precision, generated keys, exact dense float32/float16 search, portable
  filters, embedding defaults, iterators and metadata. Bound snapshot size and
  retry only explicit CAS conflicts; add Docker conformance/CI and a persistent
  sample. Default storage creates no property/vector/full-text indexes.
- Add an isolated local documentation-agent sample using Microsoft Agent Framework
  Go, go-vectorstore v0.4.1, SQLite and Ollama. Compose initializes local models;
  typed search/read tools and source-ID checks demonstrate agent-driven retrieval
  without cloud credentials. Add dedicated module CI and live smoke commands.
- Push supported SQLite scalar filtering, ordering and paging into SQL, decoding
  only the returned page and counting without vector decoding. Preserve native
  keys, float64 precision, NULL semantics, WAL snapshots and codec reentry; retain
  whole-snapshot fallback for unsupported types. Add benchmarks and a portable
  `scalar-query` recipe without changing storage format or adding property indexes.
- Accelerate SQLite top-level key equality/`In` queries and filtered counts with
  the existing primary index. Preserve native-key ordering, projection, paging,
  metadata validation and one-snapshot batching; counts avoid payload decoding.
- Add key-filter parity, concurrent-write/cancellation and index-plan coverage,
  measured snapshot-reference benchmarks and a portable `key-filters` recipe.
- Add configured store identity to PostgreSQL/SQLite model operation errors and
  PostgreSQL migration errors. Preserve existing error text, native/sentinel
  causes and cancellation; include closed-connection and diagnostic examples.
- Add optional hybrid `KeywordTerms` lists with PostgreSQL plain-text OR groups,
  validation before embedding, caller-owned snapshots and capability discovery.
  Support all hybrid slice/iterator variants while preserving string queries.
- Accept narrower signed/unsigned integer keys and named types in typed model
  collections. Check custom-codec key overflow before provider writes, preserving
  whole-batch rejection; generated integer keys still require 64-bit bindings.
- Add optional store/collection identity metadata across all providers and both
  typed wrappers, with system/database/namespace names, local snapshots and
  compatibility fallbacks. Include lifecycle conformance and a metadata recipe.
- Add explicit squared Euclidean distance (`DistanceL2Squared` / `l2_squared`)
  across all providers for dense float32/float16 and sparse vectors, with squared
  distance thresholds and reciprocal similarity scores. Preserve existing L2 units.
- Reuse PostgreSQL L2 HNSW/IVFFlat indexes while retaining native operator ordering;
  verify dense/sparse plans, reviewed index additions, SQLite reopen and recipes.
- Complete optional model read iterators for listing, batch retrieval, text/custom
  inputs and dense/text/input/binary/sparse hybrid search, including typed wrappers
  and buffered compatibility fallbacks for existing third-party providers.
- Stream PostgreSQL iterator results from cursors, stop before later batch chunks,
  preserve tuning rollback, and check cancellation through exhaustion. Memory and
  SQLite retain snapshots/pages and release resources before caller codecs.
- Add shared iterator conformance, fallback/laziness and single-connection cleanup
  coverage, including early exit before a later malformed database row.
- Add full-range `KeyUint64` with exact provider storage, typed mapping and filters,
  plus application-generated UUID keys across memory, SQLite and PostgreSQL.
- Preserve memory integer sequence state on failed batches and reject exhaustion
  instead of wrapping; add native-key conformance, SQLite reopen and sample coverage.
- Add optional `InputEmbedder` and typed `InputEmbeddingFunc[T]` for text, media
  bytes and custom application inputs, with store/collection/property overrides.
- Support transient `ModelRecord.EmbeddingInputs`, direct string vector fields,
  and the `vector,input` tag; keep inputs out of stored records and migration plans.
- Add `SearchInput` across memory/SQLite/PostgreSQL and PostgreSQL hybrid input
  search, preserving text APIs, bounded batching, cancellation and atomic writes.
- Add shared provider conformance coverage and a runnable custom-input sample.

## v0.4.1

### Production hardening

- Stop PostgreSQL model/query/search and original Record query iteration on
  cancellation between buffered rows and at exhaustion, preserving wrapped causes.
- Add SQLite process-kill/WAL recovery tests and PostgreSQL terminated-connection
  tests covering rollback of multi-batch writes and replacement-connection reuse.
- Verify SQLite format 1 against an immutable database generated by published
  v0.4.0, covering native types, vector kinds, nulls, registrations and sequences.
- Add bounded fuzz campaigns for decimals, float16, sparse distances, struct
  codecs, filter evaluation/SQL operands and SQLite payload round trips. Repeat
  extended fuzzing and vulnerability scanning weekly.
- Document production operation, security reporting, compatibility/support policy
  and release validation. Prepare v0.4.1; publication follows reviewed merge and
  the separately dispatched, CI-gated release workflow. Reject release inputs
  that disagree with VERSION or lack a matching changelog entry.


### Documentation and samples

- Add typed semantic-search and RAG samples with memory/SQLite/PostgreSQL setup,
  credential-free fixtures, opt-in OpenAI adapters and cited retrieval context.
- Add focused named-vector, iterator/cancellation, capability, PostgreSQL hybrid
  and migration-preview recipes, executable Go examples and CI sample/doc checks.
- Add a version-aware AI usage guide, contributor AGENTS.md and llms.txt discovery
  index; label the original Record API demos and document sample cleanup.

### Performance

- Bound memory/SQLite exact-search ranking to top + Skip candidates, preserving
  distance/key ordering across dense, float16, binary and sparse vectors. Memory
  copies only returned payloads; SQLite streams one read snapshot instead of
  rebuilding a memory collection.
- Use indexed, bounded SQLite GetMany lookups in a single read snapshot and SQL
  COUNT for unfiltered counts, avoiding full-collection payload reconstruction.

### Fixed

- Reject invalid UTF-8 lookup/delete keys in SQLite so they cannot alias valid
  replacement-character keys through JSON encoding.

- Honor SQLite context cancellation and deadlines during lock contention. Lock
  acquisition and side-effect-free reads use cancelable waits capped at five
  seconds; completed writes and commits are never replayed.
- Reject incompatible memory collection handles with ErrSchemaMismatch on record
  operations, including when a collection is recreated during embedding.

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
