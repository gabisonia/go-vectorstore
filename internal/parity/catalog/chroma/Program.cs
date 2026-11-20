using ChromaDB.Client;
using ChromaDB.Client.Models;
using CommunityToolkit.VectorData.Chroma;
using Microsoft.Extensions.VectorData;

var options = new ChromaConfigurationOptions("http://localhost:8000");
using var store = new ChromaVectorStore(new ChromaClient(options), ownsClient: true);
var collection = store.GetCollection<string, Article>(args[1]);
switch (args[0])
{
    case "seed":
        await collection.EnsureCollectionExistsAsync();
        await collection.UpsertAsync(new Article { Id = "seed", Body = "reference", Category = "guide", Direction = new float[] { 0, 1 } });
        break;
    case "verify":
        var p = await collection.GetAsync("go", new() { IncludeVectors = true });
        if (p?.Body != "Go update" || p.Category != "sample" || !p.Direction.ToArray().SequenceEqual(new float[] { 1, 0 }))
            throw new InvalidOperationException("Go document, metadata or embedding did not round-trip");
        var ids = new List<string>();
        await foreach (var hit in collection.SearchAsync(new ReadOnlyMemory<float>(new float[] { 1, 0 }), 2))
            ids.Add(hit.Record.Id);
        if (!ids.SequenceEqual(new[] { "go", "seed" })) throw new InvalidOperationException("Unexpected cosine ranking");
        await collection.DeleteAsync("go");
        break;
    case "verify-delete":
        if (await collection.GetAsync("seed") != null) throw new InvalidOperationException("Go delete was not visible");
        break;
    default: throw new ArgumentException("Unknown phase");
}

public sealed class Article
{
    [VectorStoreKey] public string Id { get; set; } = "";
    [VectorStoreData(IsFullTextIndexed = true)] public string Body { get; set; } = "";
    [VectorStoreData(StorageName = "category")] public string Category { get; set; } = "";
    [VectorStoreVector(2, DistanceFunction = DistanceFunction.CosineDistance)]
    public ReadOnlyMemory<float> Direction { get; set; }
}
