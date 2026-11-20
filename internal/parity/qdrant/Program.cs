using CommunityToolkit.VectorData.Qdrant;
using Microsoft.Extensions.VectorData;
using Qdrant.Client;

if (args.Length != 3) throw new ArgumentException("Expected phase, gRPC port and collection name");
using var client = new QdrantClient("localhost", int.Parse(args[1]));
using var store = new QdrantVectorStore(client, ownsClient: false, new() { HasNamedVectors = true });
var collection = store.GetCollection<ulong, Article>(args[2]);
switch (args[0])
{
    case "seed":
        await collection.EnsureCollectionExistsAsync();
        await collection.UpsertAsync(new Article { Id = 7, Category = "reference", Direction = new float[] { 0, 1 } });
        break;
    case "verify":
        var record = await collection.GetAsync(ulong.MaxValue, new() { IncludeVectors = true });
        if (record?.Category != "go" || !record.Direction.ToArray().SequenceEqual(new float[] { 1, 0 }))
            throw new InvalidOperationException("Go record did not round-trip");
        var hits = new List<ulong>();
        await foreach (var hit in collection.SearchAsync(new ReadOnlyMemory<float>(new float[] { 1, 0 }), 2,
            new() { Filter = a => a.Category == "go" }))
            hits.Add(hit.Record.Id);
        if (!hits.SequenceEqual(new[] { ulong.MaxValue })) throw new InvalidOperationException("Filtered ranking mismatch");
        record.Category = "updated";
        await collection.UpsertAsync(record);
        break;
    default: throw new ArgumentException("Unknown phase");
}

public sealed class Article
{
    [VectorStoreKey] public ulong Id { get; set; }
    [VectorStoreData(StorageName = "category", IsIndexed = true)] public string Category { get; set; } = "";
    [VectorStoreVector(2, StorageName = "direction", DistanceFunction = DistanceFunction.CosineSimilarity)]
    public ReadOnlyMemory<float> Direction { get; set; }
}
