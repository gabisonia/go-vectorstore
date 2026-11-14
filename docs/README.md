# Documentation

This directory contains internal design and implementation notes for `go-vectorstore`.

- [`architecture.md`](architecture.md): architecture, request flow, schema behavior, filter system, and index model
- [`stores-implementation.md`](stores-implementation.md): detailed store-backend design, component responsibilities, flows, invariants, and backend differences
- [`connector-development.md`](connector-development.md): step-by-step checklist for implementing a new backend connector
- [`performance.md`](performance.md): reproducible benchmarks, measured allocation changes, and indexing/recall checks

- [`model-api.md`](model-api.md): model definitions, typed/native keys, named vectors, retrieval, embeddings, and hybrid search

- [`migrations.md`](migrations.md): read-only planning, explicit additive application, SQL/risk classifications, drift and registry finalization
