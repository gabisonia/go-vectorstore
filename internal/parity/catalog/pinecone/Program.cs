using CommunityToolkit.VectorData.Pinecone;
using Microsoft.Extensions.VectorData;
using Pinecone;

var client = new PineconeClient("local", new ClientOptions { BaseUrl = "http://localhost:5080", IsTlsEnabled = false });
var collection = new PineconeCollection<string, Article>(client, args[1], new PineconeCollectionOptions { IndexNamespace = "articles" });
switch (args[0])
{
    case "create":
        await collection.EnsureCollectionExistsAsync();
        break;
    case "seed":
        await collection.UpsertAsync(new Article { Id = "seed", Body = "keep", Direction = new float[] { 0, 1 } });
        await collection.UpsertAsync(new Article { Id = "excluded", Body = "omit", Direction = new float[] { 1, 0 } });
        break;
    case "verify":
        var p = await collection.GetAsync("go", new() { IncludeVectors = true });
        if (p?.Body != "keep" || !p.Direction.ToArray().SequenceEqual(new float[] { 0.8f, 0.6f }))
            throw new InvalidOperationException("Go update did not round-trip");
        var ids = new List<string>();
        await foreach (var hit in collection.SearchAsync(new float[] { 1, 0 }, 3,
            new() { Filter = a => a.Body == "keep" }))
            ids.Add(hit.Record.Id);
        if (!ids.SequenceEqual(new[] { "go", "seed" })) throw new InvalidOperationException("Unexpected filtered ranking: " + string.Join(",",ids));
        p.Body = "edited";
        await collection.UpsertAsync(p);
        await collection.DeleteAsync("seed");
        break;
    case "verify-delete":
        if (await collection.GetAsync("go") != null) throw new InvalidOperationException("Go delete was not visible");
        break;
    default: throw new ArgumentException("Unknown phase");
}

public sealed class Article
{
    [VectorStoreKey] public string Id { get; set; } = "";
    [VectorStoreData(StorageName = "body", IsIndexed = true)] public string Body { get; set; } = "";
    [VectorStoreVector(2, DistanceFunction = DistanceFunction.CosineSimilarity)] public ReadOnlyMemory<float> Direction { get; set; }
}
