using CommunityToolkit.VectorData.SqliteVec;

using var store = new SqliteVectorStore($"Data Source={args[1]}");
await ArticleFixture.Run(store, args[0], "articles");
