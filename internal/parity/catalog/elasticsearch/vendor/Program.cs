using Elastic.Clients.Elasticsearch;
using Elastic.SemanticKernel.Connectors.Elasticsearch;
using Microsoft.Extensions.VectorData;

var client = new ElasticsearchClient(new Uri("http://localhost:9200"));
using var store = new ElasticsearchVectorStore(client, ownsClient: true);
var collection = store.GetCollection<string, Article>(args[1]);
switch (args[0])
{
    case "seed":
        await collection.EnsureCollectionExistsAsync();
        await collection.UpsertAsync(new Article { Id = "seed", Body = "reference", Direction = new float[] { 0, 1 } });
        break;
    case "verify":
        var p = await collection.GetAsync("go", new() { IncludeVectors = true });
        if (p?.Body != "Go update" || !p.Direction.ToArray().SequenceEqual(new float[] { 1, 0 }))
            throw new InvalidOperationException("Go document did not round-trip");
        var ids = new List<string>();
        await foreach (var hit in collection.SearchAsync(new ReadOnlyMemory<float>(new float[] { 1, 0 }), 2))
            ids.Add(hit.Record.Id);
        if (!ids.SequenceEqual(new[] { "go", "seed" })) throw new InvalidOperationException("Native ranking differs");
        p.Body = "C# update";
        await collection.UpsertAsync(p);
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
    [VectorStoreData(IsFullTextIndexed = true)] public string Body { get; set; } = "";
    [VectorStoreVector(2, IndexKind = IndexKind.Hnsw, DistanceFunction = DistanceFunction.CosineSimilarity)]
    public ReadOnlyMemory<float> Direction { get; set; }
}
