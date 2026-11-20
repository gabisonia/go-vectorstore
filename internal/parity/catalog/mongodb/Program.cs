using Microsoft.Extensions.VectorData;
using Microsoft.SemanticKernel.Connectors.MongoDB;
using MongoDB.Bson.Serialization.Attributes;
using MongoDB.Driver;

using var client = new MongoClient("mongodb://localhost:27017/?directConnection=true");
using var store = new MongoVectorStore(client.GetDatabase("parity"));
var collection = store.GetCollection<string, Article>(args[1]);
try
{
    await foreach (var hit in collection.SearchAsync(new ReadOnlyMemory<float>(new float[] { 1, 0 }), 1,
        new() { Filter = a => a.Body == "keep" })) { }
}
catch (TypeLoadException ex) when (ex.Message.Contains("Microsoft.Extensions.VectorData.VectorSearchFilter"))
{
    Console.WriteLine("Confirmed MongoDB connector search incompatibility on VectorData 11.0.0: " + ex.Message);
    return;
}
throw new InvalidOperationException("Search compatibility changed. Re-run the live fixture on VectorData 11.0.0.");
public sealed class Article
{
    [BsonId, VectorStoreKey] public string Id { get; set; } = "";
    [BsonElement("body"), VectorStoreData(StorageName = "body", IsIndexed = true)] public string Body { get; set; } = "";
    [BsonElement("direction"), VectorStoreVector(2, StorageName = "direction", DistanceFunction = DistanceFunction.CosineSimilarity)]
    public float[] Direction { get; set; } = [];
}
