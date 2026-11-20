using Couchbase;
using Couchbase.SemanticKernel;
using Microsoft.Extensions.VectorData;

using var cluster = await Cluster.ConnectAsync("couchbase://localhost", "Administrator", "parity-fixture");
var bucket = await cluster.BucketAsync("parity");
using var store = new CouchbaseVectorStore(await bucket.ScopeAsync("docs"));
try
{
    var collection = store.GetCollection<string, Article>("articles", new CouchbaseSearchCollectionOptions { IndexName = "vectors" });
    await foreach (var hit in collection.SearchAsync(new float[] { 1, 0 }, 1, new() { Filter = a => a.Body == "keep" })) { }
}
catch (MissingMethodException ex) when (ex.Message.Contains("CollectionModelBuilder.Build("))
{
    Console.WriteLine("Confirmed VectorData 11.0.0 incompatibility: " + ex.Message);
    return;
}
throw new InvalidOperationException("Re-evaluate the live reference against VectorData 11.0.0.");

public sealed class Article
{
    [VectorStoreKey] public string Id { get; set; } = "";
    [VectorStoreData(IsIndexed = true)] public string Body { get; set; } = "";
    [VectorStoreVector(2)] public float[] Direction { get; set; } = [];
}
