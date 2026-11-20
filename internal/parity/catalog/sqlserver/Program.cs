using CommunityToolkit.VectorData.SqlServer;

using var store = new SqlServerVectorStore(Environment.GetEnvironmentVariable("REFERENCE_SQL_DSN")!);
await ArticleFixture.Run(store, args[0], args[1]);
