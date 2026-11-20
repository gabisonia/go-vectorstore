using CommunityToolkit.VectorData.Redis;
using Microsoft.Extensions.VectorData;
using StackExchange.Redis;

using var redis = await ConnectionMultiplexer.ConnectAsync("localhost:6379");
using var store = new RedisVectorStore(redis.GetDatabase(), new() {
    StorageType = args[2] == "HASH" ? RedisStorageType.HashSet : RedisStorageType.Json
});
var collection = store.GetCollection<string, Document>(args[1]);
switch (args[0])
{
    case "seed":
        await collection.EnsureCollectionExistsAsync();
        await collection.UpsertAsync(new Document { Id = "seed", Body = "reference", Direction = new float[] { 0, 1 } });
        break;
    case "verify":
        var point = await collection.GetAsync("go", new() { IncludeVectors = true });
        if (point?.Body != "from Go" || !point.Direction.ToArray().SequenceEqual(new float[] { 1, 0 }))
            throw new InvalidOperationException("Go record did not round-trip");
        var ids = new List<string>();
        await foreach (var hit in collection.SearchAsync(new ReadOnlyMemory<float>(new float[] { 1, 0 }), 2))
            ids.Add(hit.Record.Id);
        if (!ids.SequenceEqual(new[] { "go", "seed" })) throw new InvalidOperationException("Native ranking differs");
        await collection.DeleteAsync("go");
        break;
    default: throw new ArgumentException("Unknown phase");
}

public sealed class Document
{
    [VectorStoreKey] public string Id { get; set; } = "";
    [VectorStoreData] public string Body { get; set; } = "";
    [VectorStoreVector(2, IndexKind = IndexKind.Flat, DistanceFunction = DistanceFunction.CosineSimilarity)]
    public ReadOnlyMemory<float> Direction { get; set; }
}
