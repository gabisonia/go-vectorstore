using CommunityToolkit.VectorData.AzureDocumentDB;
using Microsoft.Extensions.VectorData;
using MongoDB.Bson.Serialization.Attributes;
using MongoDB.Driver;

using var client = new MongoClient(Environment.GetEnvironmentVariable("DOCUMENTDB_TEST_URI"));
using var store = new DocumentDBVectorStore(client.GetDatabase("parity"));
var collection = store.GetCollection<string, Article>(args[1]);
switch (args[0])
{
    case "create":
        await collection.EnsureCollectionExistsAsync();
        break;
    case "seed":
        await collection.UpsertAsync(new Article { Id = "omit", Body = "omit", Direction = [1, 0] });
        await collection.UpsertAsync(new Article { Id = "second", Body = "from Go", Direction = [-1, 0] });
        await collection.UpsertAsync(new Article { Id = "shared", Body = "reference", Direction = [0, 1] });
        break;
    case "verify":
        var doc = await collection.GetAsync("shared", new() { IncludeVectors = true });
        if (doc?.Body != "from Go" || !doc.Direction.SequenceEqual(new float[] { 1, 0 }))
            throw new InvalidOperationException("Go update did not round-trip");
        var hits = new List<(string, double?)>();
        await foreach (var hit in collection.SearchAsync(new ReadOnlyMemory<float>(new float[] { 1, 0 }), 3, new() { Filter = a => a.Body == "from Go" }))
            hits.Add((hit.Record.Id, hit.Score));
        if (!hits.SequenceEqual(new (string, double?)[] { ("shared", 1), ("second", -1) }))
            throw new InvalidOperationException("Unexpected cosine ranking: " + string.Join(", ", hits));
        await collection.DeleteAsync("shared");
        break;
    default: throw new ArgumentException("Unknown phase");
}

public sealed class Article
{
    [BsonId, VectorStoreKey] public string Id { get; set; } = "";
    [BsonElement("body"), VectorStoreData(StorageName = "body", IsIndexed = true)] public string Body { get; set; } = "";
    [BsonElement("direction"), VectorStoreVector(2, StorageName = "direction", IndexKind = IndexKind.Hnsw, DistanceFunction = DistanceFunction.CosineSimilarity)]
    public float[] Direction { get; set; } = [];
}
