using CommunityToolkit.VectorData.InMemory;
using Microsoft.Extensions.VectorData;
using System.Text.Json;

var input = JsonSerializer.Deserialize<Article[]>(Console.In.ReadToEnd())!;
using var store = new InMemoryVectorStore();
var collection = store.GetCollection<string, Article>("articles");
await collection.EnsureCollectionExistsAsync();
await collection.UpsertAsync(input.AsEnumerable());
var ranked = new List<string>();
await foreach (var hit in collection.SearchAsync(new ReadOnlyMemory<float>(new float[] { 1, 0 }), 2,
    new() { Filter = a => a.Category == "included" }))
    ranked.Add(hit.Record.Id);
var top = await collection.GetAsync(ranked[0], new() { IncludeVectors = true });
await collection.DeleteAsync(ranked[0]);
Console.WriteLine(JsonSerializer.Serialize(new { Ranked = ranked, Vector = top.Vector.ToArray(),
    Deleted = await collection.GetAsync(ranked[0]) is null }));

public sealed class Article
{
    [VectorStoreKey] public string Id { get; set; } = "";
    [VectorStoreData(IsIndexed = true)] public string Category { get; set; } = "";
    [VectorStoreVector(2, DistanceFunction = DistanceFunction.CosineSimilarity)]
    public ReadOnlyMemory<float> Vector { get; set; }
}
