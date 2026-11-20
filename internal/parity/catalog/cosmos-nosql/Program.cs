using VectorData = Microsoft.Extensions.VectorData;
using CommunityToolkit.VectorData.CosmosNoSql;
using Microsoft.Azure.Cosmos;
using Microsoft.Extensions.VectorData;
using System.Text.Json;
using System.Text.Json.Serialization;

using var client = new CosmosClient("https://localhost:8081", Environment.GetEnvironmentVariable("COSMOS_EMULATOR_KEY"),
    new CosmosClientOptions {
        ConnectionMode = ConnectionMode.Gateway,
        UseSystemTextJsonSerializerWithOptions = new JsonSerializerOptions(),
        ServerCertificateCustomValidationCallback = (_, _, _) => true
    });
using var store = new CosmosNoSqlVectorStore(client.GetDatabase("parity"));
var collection = store.GetCollection<string, Article>(args[1]);
switch (args[0])
{
    case "create":
        await collection.EnsureCollectionExistsAsync();
        break;
    case "seed":
        await collection.UpsertAsync(new Article { Id = "second", Body = "keep", Direction = [0, 1] });
        await collection.UpsertAsync(new Article { Id = "shared", Body = "initial", Direction = [-1, 0] });
        await collection.UpsertAsync(new Article { Id = "excluded", Body = "omit", Direction = [1, 0] });
        break;
    case "verify":
        var doc = await collection.GetAsync("shared", new() { IncludeVectors = true });
        if (doc?.Body != "keep" || !doc.Direction.SequenceEqual(new float[] { 4, 3 }))
            throw new InvalidOperationException("Go update did not round-trip");
        // Capture the short page observed when the excluded item is the closest match.
        await CheckRanking(2, ["shared"]);
        await CheckRanking(3, ["shared", "second"]);
        await collection.DeleteAsync("shared");
        break;
    default: throw new ArgumentException("Unknown phase");
}

async Task CheckRanking(int top, string[] expected)
{
    var ids = new List<string>();
    await foreach (var hit in collection.SearchAsync(new ReadOnlyMemory<float>(new float[] { 1, 0 }), top,
        new() { Filter = a => a.Body == "keep" }))
        ids.Add(hit.Record.Id);
    if (!ids.SequenceEqual(expected))
        throw new InvalidOperationException($"Filtered top {top}: expected [{string.Join(", ", expected)}], got [{string.Join(", ", ids)}]");
}

public sealed class Article
{
    [JsonPropertyName("id"), VectorStoreKey] public string Id { get; set; } = "";
    [JsonPropertyName("body"), VectorStoreData] public string Body { get; set; } = "";
    [JsonPropertyName("direction"), VectorStoreVector(2, IndexKind = VectorData.IndexKind.DiskAnn, DistanceFunction = VectorData.DistanceFunction.CosineSimilarity)]
    public float[] Direction { get; set; } = [];
}
