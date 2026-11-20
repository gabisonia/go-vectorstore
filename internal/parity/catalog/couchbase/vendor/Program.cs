using Couchbase;
using Couchbase.SemanticKernel;
using Microsoft.Extensions.VectorData;

using var cluster = await Cluster.ConnectAsync("couchbase://localhost", "Administrator", "parity-fixture");
var bucket = await cluster.BucketAsync("parity");
using var store = new CouchbaseVectorStore(await bucket.ScopeAsync("docs"));
var collection = store.GetCollection<string, Article>("articles", new CouchbaseSearchCollectionOptions { IndexName = "vectors", NumCandidates = 3 });
switch (args[0])
{
    case "seed":
        try { await collection.EnsureCollectionExistsAsync(); }
        catch (NotImplementedException) { Console.WriteLine("Collection creation requires external provisioning."); }
        await collection.UpsertAsync(new Article { Id = "seed", Body = "keep", Direction = [0, 1] });
        await collection.UpsertAsync(new Article { Id = "excluded", Body = "omit", Direction = [1, 0] });
        break;
    case "verify":
        var doc = await collection.GetAsync("go", new() { IncludeVectors = true });
        if (doc?.Body != "keep" || !doc.Direction.SequenceEqual(new float[] { 0.8f, 0.6f }))
            throw new InvalidOperationException("Go update did not round-trip");
        var bodies = new List<string>();
        await foreach (var hit in collection.SearchAsync(new float[] { 1, 0 }, 3, new() { Filter = a => a.Body == "keep" }))
            bodies.Add(hit.Record.Body);
        if (!bodies.SequenceEqual(new[] { "omit", "keep", "keep" }))
            throw new InvalidOperationException("Re-evaluate upstream filter behavior: " + string.Join(",", bodies));
        Console.WriteLine("C# filtered search returned the excluded highest-scoring document.");
        // The connector reads the payload without restoring the external document key.
        doc.Id = "go";
        doc.Body = "edited";
        await collection.UpsertAsync(doc);
        await collection.DeleteAsync("seed");
        break;
    case "verify-delete":
        if (await collection.GetAsync("go") != null) throw new InvalidOperationException("Go deletion was not visible");
        break;
    default: throw new ArgumentException("Unknown phase");
}

public sealed class Article
{
    [VectorStoreKey] public string Id { get; set; } = "";
    [VectorStoreData(IsIndexed = true)] public string Body { get; set; } = "";
    [VectorStoreVector(2)] public float[] Direction { get; set; } = [];
}
