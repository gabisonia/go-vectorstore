# Bounded Qdrant model collections

Run from the repository root with Go 1.26+. The [shared Aspire services](../aspire-services/README.md)
require Aspire 17, Go 1.26+ and Docker:

```sh
aspire start --apphost samples/aspire-services/apphost.go
aspire wait qdrant --apphost samples/aspire-services/apphost.go
go run ./samples/qdrant-store
go run ./samples/qdrant-store
```

Both runs print:

```text
Guides: 2
Nearest: System architecture (distance 0.000)
Records after reopen: 3
```

The sample uses supplied vectors, typed records, portable category filtering and
exact Go ranking over one durable canonical snapshot. It needs no model or API
key. Three stable fixture IDs remain in `qdrant_demo` / `documents`; reruns reuse
them. `-url`, `-namespace` and `QDRANT_API_KEY` configure another dedicated server
or namespace. Use verified HTTPS for remote servers.

This foundation requires standalone Qdrant **1.20.2**, one auto shard and one
replica. It does not use native vector indexes. Reads/writes transfer the entire
bounded snapshot. The default limits are 4,096 records, 4 MiB canonical record/key
bytes and 16,384 retained receipts across all logical incarnations. Exhaustion
fails closed; there is no online receipt eviction. See [provider limits and
unknown-write recovery](../../docs/qdrant.md) before using it in an application.

`DeleteCollection` logically deletes only this sample collection, retaining
ownership, epochs and receipts. Ordinary Aspire stops retain its container/data;
`aspire stop --apphost samples/aspire-services/apphost.go --force` removes the
development containers while preserving their volumes. Physical reset requires
external quiescence and is outside the provider's operation contract.
