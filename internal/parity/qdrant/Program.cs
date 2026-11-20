using CommunityToolkit.VectorData.Qdrant;
using Microsoft.Extensions.VectorData;
using Qdrant.Client;

if (args.Length != 3) throw new ArgumentException("Expected phase, gRPC port and collection name");
using var client = new QdrantClient("localhost", int.Parse(args[1]));
using var store = new QdrantVectorStore(client, ownsClient: false, new() { HasNamedVectors = true });
if (args[0].StartsWith("named-"))
{
    var named = store.GetCollection<Guid, NamedArticle>(args[2]);
    var seed = Guid.Parse("12345678-0000-4000-8000-000000000001");
    var id = Guid.Parse("12345678-0000-4000-8000-000000000002");
    switch (args[0])
    {
        case "named-seed":
            await named.EnsureCollectionExistsAsync();
            await named.UpsertAsync(new NamedArticle { Id = seed, Body = "retrieval guide", Title = new float[] { 0, 1 }, Content = new float[] { 1, 0, 0 } });
            break;
        case "named-verify":
            var item = await named.GetAsync(id, new() { IncludeVectors = true });
            if (item?.Body != "storage guide" || !item.Title.ToArray().SequenceEqual(new float[] { 1, 0 }) ||
                !item.Content.ToArray().SequenceEqual(new float[] { 0, 1, 0 }))
                throw new InvalidOperationException("UUID or named vector mapping differs");
            var hits = new List<object>();
            await foreach (var hit in named.HybridSearchAsync(new ReadOnlyMemory<float>(new float[] { 1, 0 }),
                new[] { "retrieval" }, 2, new() { VectorProperty = x => x.Title, AdditionalProperty = x => x.Body }))
                hits.Add(new { ID = hit.Record.Id, Score = hit.Score });
            Console.WriteLine(System.Text.Json.JsonSerializer.Serialize(hits));
            break;
        case "named-delete":
            await named.DeleteAsync(id);
            break;
        default: throw new ArgumentException("Unknown named phase");
    }
    return;
}
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
    case "verify-deletion":
        if (await collection.GetAsync(ulong.MaxValue) is not null || await collection.GetAsync(7) is null)
            throw new InvalidOperationException("Delete affected the wrong records");
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

public sealed class NamedArticle
{
    [VectorStoreKey] public Guid Id { get; set; }
    [VectorStoreData(StorageName = "body", IsFullTextIndexed = true)] public string Body { get; set; } = "";
    [VectorStoreVector(2, StorageName = "title", DistanceFunction = DistanceFunction.CosineSimilarity)]
    public ReadOnlyMemory<float> Title { get; set; }
    [VectorStoreVector(3, StorageName = "content", DistanceFunction = DistanceFunction.CosineSimilarity)]
    public ReadOnlyMemory<float> Content { get; set; }
}
