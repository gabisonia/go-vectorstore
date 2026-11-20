using Microsoft.Extensions.VectorData;

internal static class ArticleFixture
{
    public static async Task Run(VectorStore store, string phase, string name)
    {
        var collection = store.GetCollection<long, Article>(name);
        switch (phase)
        {
            case "seed":
                await collection.EnsureCollectionExistsAsync();
                await collection.UpsertAsync(new Article { Id = 7, Body = "reference", Direction = new float[] { 0, 1 } });
                break;
            case "verify":
                var point = await collection.GetAsync(42, new() { IncludeVectors = true });
                if (point?.Body != "from Go" || !point.Direction.ToArray().SequenceEqual(new float[] { 1, 0 }))
                    throw new InvalidOperationException("Go point did not round-trip");
                var ids = new List<long>();
                await foreach (var hit in collection.SearchAsync(new ReadOnlyMemory<float>(new float[] { 1, 0 }), 2))
                    ids.Add(hit.Record.Id);
                if (!ids.SequenceEqual(new long[] { 42, 7 })) throw new InvalidOperationException("Native ranking differs");
                await collection.DeleteAsync(42);
                break;
            default: throw new ArgumentException("Unknown phase");
        }
    }
}

public sealed class Article
{
    [VectorStoreKey(StorageName = "id")] public long Id { get; set; }
    [VectorStoreData(StorageName = "body")] public string Body { get; set; } = "";
    [VectorStoreVector(2, StorageName = "direction", DistanceFunction = DistanceFunction.CosineDistance)]
    public ReadOnlyMemory<float> Direction { get; set; }
}
