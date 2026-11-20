// Exercise the locked 11.0.0 graph without making a network request.
using Elastic.Clients.Elasticsearch;
using Elastic.SemanticKernel.Connectors.Elasticsearch;
using Microsoft.Extensions.VectorData;

var client = new ElasticsearchClient(new Uri("http://localhost:9200"));
using var store = new ElasticsearchVectorStore(client, ownsClient: true);
try
{
    _ = store.GetCollection<string, Article>("reference");
}
catch (MissingMethodException ex) when (ex.Message.Contains("CollectionModelBuilder.Build("))
{
    Console.WriteLine("Confirmed upstream incompatibility with VectorData 11.0.0: " + ex.Message);
    return;
}
throw new InvalidOperationException("Collection construction succeeded. Re-run live interoperability against 11.0.0 and update the compatibility report.");

public sealed class Article
{
    [VectorStoreKey] public string Id { get; set; } = "";
    [VectorStoreVector(2)] public ReadOnlyMemory<float> Direction { get; set; }
}
