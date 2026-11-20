using CommunityToolkit.VectorData.Weaviate;
using Microsoft.Extensions.VectorData;

using var client = new HttpClient { BaseAddress = new Uri("http://localhost:8080/v1/") };
using var store = new WeaviateVectorStore(client, new() { HasNamedVectors = true });
var collection = store.GetCollection<Guid, Article>(args[1]);
var seed = Guid.Parse("313939d8-0101-4a0a-8000-000000000001");
var go = Guid.Parse("313939d8-0101-4a0a-8000-000000000002");
switch (args[0])
{
    case "seed":
        await collection.EnsureCollectionExistsAsync();
        await collection.UpsertAsync(new Article { Id = seed, Text = "reference", Direction = new float[] { 0, 1 } });
        break;
    case "verify":
        var p = await collection.GetAsync(go, new() { IncludeVectors = true });
        if (p?.Text != "Go update" || !p.Direction.ToArray().SequenceEqual(new float[] { 1, 0 }))
            throw new InvalidOperationException("Go update was not visible");
        var hits = new List<Guid>();
        await foreach (var hit in collection.SearchAsync(new ReadOnlyMemory<float>(new float[] { 1, 0 }), 2))
            hits.Add(hit.Record.Id);
        if (!hits.SequenceEqual(new[] { go, seed })) throw new InvalidOperationException("Cosine ranking differs");
        p.Text = "reference update";
        await collection.UpsertAsync(p);
        await collection.DeleteAsync(seed);
        break;
    case "verify-delete":
        if (await collection.GetAsync(go) != null) throw new InvalidOperationException("Go delete was not visible");
        break;
    default: throw new ArgumentException("Unknown phase");
}

public sealed class Article
{
    [VectorStoreKey] public Guid Id { get; set; }
    [System.Text.Json.Serialization.JsonPropertyName("body"), VectorStoreData(StorageName = "body")] public string Text { get; set; } = "";
    [VectorStoreVector(2, StorageName = "direction", IndexKind = IndexKind.Hnsw, DistanceFunction = DistanceFunction.CosineDistance)]
    public ReadOnlyMemory<float> Direction { get; set; }
}
