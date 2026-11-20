using CommunityToolkit.VectorData.SqlServer;

using var store = new SqlServerVectorStore(Environment.GetEnvironmentVariable("REFERENCE_SQL_DSN")!);
var collection = store.GetCollection<long, Article>(args[1]);
switch (args[0])
{
    case "update":
        var record = await collection.GetAsync(7, new() { IncludeVectors = true });
        if (record?.Body != "Go update") throw new InvalidOperationException("Go update was not visible");
        record.Body = "C# update";
        await collection.UpsertAsync(record);
        break;
    case "verify-empty":
        if (await collection.GetAsync(7) != null) throw new InvalidOperationException("Go delete was not visible");
        break;
    default:
        await ArticleFixture.Run(store, args[0], args[1]);
        break;
}
