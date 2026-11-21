# Search an engineering handbook

Who handles a request that exceeds its deadline? Search three short handbook
pages, get the passage and its filename, then change the answer and reindex.
From a checkout with Go 1.26+, run the terminal replay:

```sh
bash samples/handbook/replay.sh sqlite
```

The replay creates a temporary database and copies the pages before editing one.
Its first search prints:

```text
timeouts.md
When a request exceeds its deadline, cancel the context and page the API on-call.
```

Editing the source leaves the indexed answer intact. After reindexing, the same
query returns `timeouts.md` with the updated instruction to contact the platform
rotation. The replay checks both answers and removes its temporary files.

## Try your own edit

From the repository root, run:

```sh
work=$(mktemp -d)
cp -R samples/handbook/pages "$work/pages"
go build -o "$work/handbook" ./samples/handbook
cd "$work"
./handbook -pages pages index
./handbook query 'How do I cancel a request at its deadline?'
```

Edit `pages/timeouts.md` to name a different on-call team. Repeat the query,
run `./handbook -pages pages index`, then query again. Try `'reduce cloud costs'`
and `'search vector embeddings'` to find the two other pages. Each Markdown
file is one passage. Its filename is its stable key, so reindexing updates
existing passages. Renaming or removing a file leaves its old record in the
collection. The binary and `handbook.db` live in your temporary directory;
delete that directory when finished.

The sample reuses the deterministic three-topic embedder from
[typed-search](../typed-search/README.md). It counts a small vocabulary for
costs, deadlines and vector search. Queries outside that vocabulary can tie.
For model-based retrieval and quality measurements, see the
[docs retrieval pilot](../docs-retrieval-pilot/README.md).

## Run the same walkthrough on PostgreSQL

From the repository root with Docker running:

```sh
bash samples/handbook/replay.sh postgres
```

This starts a disposable PostgreSQL 16 server with pgvector on a loopback port
and removes the container and its volume on exit. For an existing disposable
database, set `HANDBOOK_POSTGRES_DSN` and run the same index and query commands.
Use a database where the sample may create the vector extension and the
`handbook_passages` collection. SQLite uses a local file by default. PostgreSQL
requires a reachable server and credentials and keeps the collection after
the CLI exits. Both use the same Go model, cosine metric, source IDs and
retrieval code. The replay checks the returned text on both systems.

The three original [handbook pages](pages/timeouts.md) use the repository's
[MIT license](../../LICENSE).
