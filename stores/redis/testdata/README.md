# Redis format-1 compatibility fixture

`snapshot-v1.json` was generated using the Redis implementation in merged commit
`950401c3d8a8314a9b724091708034c1f1aace54` before the hash-storage changes. The
generator used its normalized definition and canonical record codec, with a fixed
revision for reproducibility. No Redis server state or credentials are included.

The fixture contains an int64 generated-key definition with a MaxInt64 next
sequence, a MinInt64 record key, an exact JSON MaxUint64 number, JSON null, native
NULL and an optional dense vector. Unit and real-server tests preserve reads,
explicit upgrade, client reopen and sequence exhaustion.

SHA-256: `27d7293f1364ce9164993b625d5906b7d63bfc09c92150a393c8d01ce15b0e69`.
Keep this fixture immutable; do not regenerate it during ordinary codec changes.
