using System.Net;
using System.Text;
using GideonDB.Client;

await LifecycleAndEncoding();
await DistributedAndErrors();
await ValidationCancellationAndBounds();
await HybridAndQuantization();
Console.WriteLine(".NET SDK tests passed");

static async Task LifecycleAndEncoding()
{
    var handler = new QueueHandler();
    handler.Add(HttpStatusCode.OK, "{\"status\":\"ok\"}");
    handler.Add(HttpStatusCode.OK, "{\"records\":[{\"id\":\"one\"}],\"next_cursor\":\"next/value\",\"vectors_included\":false}");
    handler.Add(HttpStatusCode.OK, "{\"records\":[],\"next_cursor\":\"\",\"vectors_included\":false,\"metadata_epoch\":7,\"authoritative_placement\":true}");
    handler.Add(HttpStatusCode.OK, "{\"id\":\"a/b\",\"vector\":[1,0]}");
    using var http = new HttpClient(handler);
    using var client = new GideonDBClient("https://db.example/", http, "secret", TimeSpan.FromSeconds(2));
    await client.HealthAsync();
    var page = await client.ScrollAsync("docs", new ScrollOptions { Namespace = "tenant one", Limit = 25, Cursor = "prior/value", IncludeVector = true });
    Check(page.NextCursor == "next/value", "scroll cursor");
    Check((await client.DistributedScrollAsync("docs")).MetadataEpoch == 7, "distributed epoch");
    Check((await client.GetAsync("docs", "a/b", "tenant one")).Id == "a/b", "record ID");
    Check(handler.Requests[1].RequestUri!.ToString().Contains("cursor=prior%2Fvalue", StringComparison.Ordinal), "encoded cursor");
    Check(handler.Requests[2].RequestUri!.AbsolutePath.Contains("/v1/cluster/collections/docs/vectors", StringComparison.Ordinal), "cluster path");
    Check(handler.Requests[3].RequestUri!.OriginalString.EndsWith("a%2Fb?namespace=tenant%20one", StringComparison.Ordinal), "encoded path");
    Check(handler.Requests[3].Headers.Authorization?.Parameter == "secret", "API key");
}

static async Task DistributedAndErrors()
{
    var handler = new QueueHandler();
    handler.Add((HttpStatusCode)207, "{\"outcomes\":[{\"shard_id\":0,\"status\":\"unknown\"}],\"partial\":true,\"metadata_epoch\":2,\"authoritative_placement\":true}");
    handler.Add(HttpStatusCode.NotFound, "{\"code\":\"not_found\",\"message\":\"missing\"}");
    using var client = new GideonDBClient("http://db.example", new HttpClient(handler));
    var response = await client.DistributedBatchUpsertAsync("docs", new[] { new VectorRecord { Id = "one", Vector = new[] { 1f, 0f } } }, "all");
    Check(response.Partial, "partial response");
    try { await client.DescribeCollectionAsync("missing"); throw new Exception("Expected API exception"); }
    catch (GideonDBApiException exception) { Check(exception.StatusCode == 404 && exception.Code == "not_found", "typed API error"); }
}

static async Task ValidationCancellationAndBounds()
{
    try { _ = new GideonDBClient("ftp://db.example"); throw new Exception("Expected origin error"); }
    catch (ArgumentException) { }
    try { _ = new GideonDBClient("https://db.example/path"); throw new Exception("Expected path error"); }
    catch (ArgumentException) { }

    var handler = new QueueHandler();
    handler.Add(HttpStatusCode.OK, new byte[(16 << 20) + 1]);
    using var client = new GideonDBClient("https://db.example", new HttpClient(handler));
    try { await client.HealthAsync(); throw new Exception("Expected response bound error"); }
    catch (GideonDBResponseTooLargeException) { }

    using var cancelled = new CancellationTokenSource();
    cancelled.Cancel();
    try { await client.ReadyAsync(cancelled.Token); throw new Exception("Expected cancellation"); }
    catch (OperationCanceledException) { }
}

static async Task HybridAndQuantization()
{
    var handler = new QueueHandler();
    handler.Add(HttpStatusCode.Created, "{\"name\":\"docs\",\"dimension\":2}");
    handler.Add(HttpStatusCode.OK, "{\"id\":\"one\"}");
    handler.Add(HttpStatusCode.OK, "{\"results\":[{\"id\":\"one\",\"score\":0.5}]}");
    handler.Add(HttpStatusCode.OK, "{\"results\":[]}");
    using var client = new GideonDBClient("https://db.example", new HttpClient(handler));
    await client.CreateCollectionAsync(new CollectionConfig
    {
        Name = "docs", Dimension = 2, Metric = "cosine", ShardCount = 1, Vectorizer = "mini",
        Index = new IndexConfig { Type = "flat", Quantization = "binary", Oversampling = 4 }
    });
    Check(handler.Bodies[0] == "{\"name\":\"docs\",\"dimension\":2,\"metric\":\"cosine\",\"shard_count\":1,\"vectorizer\":\"mini\",\"index\":{\"type\":\"flat\",\"quantization\":\"binary\",\"oversampling\":4}}", "collection body: " + handler.Bodies[0]);
    await client.UpsertAsync("docs", new VectorRecord { Id = "one", Text = "hello world", SparseVector = new Dictionary<string, float> { ["hello"] = 1.5f } });
    Check(handler.Bodies[1] == "{\"id\":\"one\",\"text\":\"hello world\",\"sparse_vector\":{\"hello\":1.5}}", "record body: " + handler.Bodies[1]);
    await client.SearchAsync("docs", new SearchOptions { TopK = 3, Text = "hello", SparseVector = new Dictionary<string, float> { ["hello"] = 2f }, Alpha = 0.25, Fusion = "rrf" });
    Check(handler.Bodies[2] == "{\"top_k\":3,\"text\":\"hello\",\"sparse_vector\":{\"hello\":2},\"alpha\":0.25,\"fusion\":\"rrf\"}", "hybrid search body: " + handler.Bodies[2]);
    await client.SearchAsync("docs", new SearchOptions { TopK = 1, Vector = new[] { 1f, 0f }, Text = "hello" });
    Check(handler.Bodies[3].StartsWith("{\"vector\":[1,0],\"top_k\":1,\"text\":\"hello\"", StringComparison.Ordinal), "dense plus text body");
    try { await client.SearchAsync("docs", new SearchOptions { TopK = 1, Text = "x", Alpha = 1.5 }); throw new Exception("Expected alpha error"); }
    catch (ArgumentOutOfRangeException) { }
    try { await client.SearchAsync("docs", new SearchOptions { TopK = 1, Text = "x", Fusion = "max" }); throw new Exception("Expected fusion error"); }
    catch (ArgumentOutOfRangeException) { }
}

static void Check(bool condition, string message) { if (!condition) throw new Exception(message); }

sealed class QueueHandler : HttpMessageHandler
{
    private readonly Queue<HttpResponseMessage> responses = new();
    public List<HttpRequestMessage> Requests { get; } = new();
    public List<string> Bodies { get; } = new();
    public void Add(HttpStatusCode status, string body) => Add(status, Encoding.UTF8.GetBytes(body));
    public void Add(HttpStatusCode status, byte[] body) => responses.Enqueue(new HttpResponseMessage(status) { Content = new ByteArrayContent(body) });
    protected override Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken cancellationToken)
    {
        cancellationToken.ThrowIfCancellationRequested();
        Requests.Add(Clone(request));
        Bodies.Add(request.Content is null ? string.Empty : request.Content.ReadAsStringAsync(cancellationToken).GetAwaiter().GetResult());
        return Task.FromResult(responses.Dequeue());
    }
    private static HttpRequestMessage Clone(HttpRequestMessage request)
    {
        var clone = new HttpRequestMessage(request.Method, request.RequestUri);
        foreach (var header in request.Headers) clone.Headers.TryAddWithoutValidation(header.Key, header.Value);
        return clone;
    }
}
