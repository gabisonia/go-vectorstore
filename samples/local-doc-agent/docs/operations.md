# Operating a vector application

## Backups

Use SQLite-aware tooling for a live backup. Copying only the main database file
while WAL writes are active can omit committed data. Stop all users and verify a
clean checkpoint for an offline copy. Never remove WAL or SHM files from an open
database. Restore to an isolated path and verify known records and counts before
replacing active data.

## Embedding models

Use the same embedding model and configuration for ingestion and queries. Equal
dimensions do not prove that two embedding models are compatible. Re-embed data
when changing models. This demo records the embedding model name and dimensions
in its SQLite index and rejects a configuration change until a new index is used.

## Cancellation

Pass context deadlines through model, embedding and database requests. Check tool
and iterator errors. Cancel or finish in-flight work before closing the database.
A failed network response after a write does not always prove that commit failed;
reconcile uncertain outcomes before retrying writes with generated keys.
