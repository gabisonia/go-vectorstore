using CommunityToolkit.VectorData.PgVector;

using var store = new PostgresVectorStore(Environment.GetEnvironmentVariable("REFERENCE_PG_DSN")!);
await ArticleFixture.Run(store, args[0], args[1]);
