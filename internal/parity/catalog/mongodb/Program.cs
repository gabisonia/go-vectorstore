using Microsoft.Extensions.VectorData;
using Microsoft.SemanticKernel.Connectors.MongoDB;
using MongoDB.Bson.Serialization.Attributes;
using MongoDB.Driver;

using var client = new MongoClient("mongodb://localhost:27017/?directConnection=true");
using var store = new MongoVectorStore(client.GetDatabase("parity"));
var collection = store.GetCollection<string, Article>(args[1]);
switch (args[0])
{
    case "probe":
        break;
    case "seed":
        await collection.EnsureCollectionExistsAsync();
        await collection.UpsertAsync(new Article { Id = "seed", Body = "keep", Direction = [0, 1] });
        await collection.UpsertAsync(new Article { Id = "excluded", Body = "omit", Direction = [1, 0] });
        break;
    case "verify":
        var doc = await collection.GetAsync("go", new() { IncludeVectors = true });
        if (doc?.Body != "keep" || !doc.Direction.SequenceEqual(new float[] { 1, 0 }))
            throw new InvalidOperationException("Go update did not round-trip");
        var ids = new List<string>();
        await foreach (var hit in collection.SearchAsync(new ReadOnlyMemory<float>(new float[] { 1, 0 }), 3,
            new() { Filter = a => a.Body == "keep" }))
            ids.Add(hit.Record.Id);
        if (!ids.SequenceEqual(new[] { "go", "seed" })) throw new InvalidOperationException("Unexpected filtered ranking");
        doc.Body = "edited";
        await collection.UpsertAsync(doc);
        await collection.DeleteAsync("seed");
        break;
    case "verify-delete":
        if (await collection.GetAsync("go") != null) throw new InvalidOperationException("Go delete was not visible");
        break;
    default: throw new ArgumentException("Unknown phase");
}

public sealed class Article
{
    [BsonId, VectorStoreKey] public string Id { get; set; } = "";
    [BsonElement("body"), VectorStoreData(IsIndexed = true)] public string Body { get; set; } = "";
    [BsonElement("direction"), VectorStoreVector(2, DistanceFunction = DistanceFunction.CosineSimilarity)]
    public float[] Direction { get; set; } = [];
}
