using Azure.Core;
using Azure.Search.Documents.Indexes;
using CommunityToolkit.VectorData.AzureAISearch;
using Microsoft.Extensions.VectorData;

var credential = new FixtureCredential((await Console.In.ReadToEndAsync()).Trim());
var client = new SearchIndexClient(new Uri(args[1]), credential);
using var store = new AzureAISearchVectorStore(client);
var collection = store.GetCollection<string, Article>(args[2]);
switch (args[0])
{
    case "create":
        await collection.EnsureCollectionExistsAsync();
        break;
    case "seed":
        await collection.UpsertAsync(new Article { Id = "seed", Body = "reference", Direction = new float[] { 0, 1 } });
        break;
    case "verify":
        var point = await collection.GetAsync("go");
        if (point?.Body != "Go document") throw new InvalidOperationException("Go record was not readable");
        var hits = new List<string>();
        await foreach (var hit in collection.SearchAsync(new ReadOnlyMemory<float>(new float[] { 1, 0 }), 2))
            hits.Add(hit.Record.Id);
        if (!hits.SequenceEqual(new[] { "go", "seed" })) throw new InvalidOperationException("C# ranking differs");
        break;
    case "verify-delete":
        if (await collection.GetAsync("go") != null) throw new InvalidOperationException("Go deletion was not visible");
        break;
    case "cleanup":
        await collection.EnsureCollectionDeletedAsync();
        break;
    default: throw new ArgumentException("Unknown phase");
}

public sealed class Article
{
    [VectorStoreKey] public string Id { get; set; } = "";
    [VectorStoreData] public string Body { get; set; } = "";
    [VectorStoreVector(2, DistanceFunction = DistanceFunction.CosineSimilarity)]
    public ReadOnlyMemory<float> Direction { get; set; }
}

sealed class FixtureCredential(string token) : TokenCredential
{
    public override AccessToken GetToken(TokenRequestContext context, CancellationToken cancellationToken)
        => new(token, DateTimeOffset.UtcNow.AddMinutes(5));
    public override ValueTask<AccessToken> GetTokenAsync(TokenRequestContext context, CancellationToken cancellationToken)
        => ValueTask.FromResult(GetToken(context, cancellationToken));
}
