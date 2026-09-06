using System.Net;
using System.Text;
using GideonDB.Client;

await LifecycleAndEncoding();
await DistributedAndErrors();
await ValidationCancellationAndBounds();
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

static void Check(bool condition, string message) { if (!condition) throw new Exception(message); }

sealed class QueueHandler : HttpMessageHandler
{
    private readonly Queue<HttpResponseMessage> responses = new();
    public List<HttpRequestMessage> Requests { get; } = new();
    public void Add(HttpStatusCode status, string body) => Add(status, Encoding.UTF8.GetBytes(body));
    public void Add(HttpStatusCode status, byte[] body) => responses.Enqueue(new HttpResponseMessage(status) { Content = new ByteArrayContent(body) });
    protected override Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken cancellationToken)
    {
        cancellationToken.ThrowIfCancellationRequested();
        Requests.Add(Clone(request));
        return Task.FromResult(responses.Dequeue());
    }
    private static HttpRequestMessage Clone(HttpRequestMessage request)
    {
        var clone = new HttpRequestMessage(request.Method, request.RequestUri);
        foreach (var header in request.Headers) clone.Headers.TryAddWithoutValidation(header.Key, header.Value);
        return clone;
    }
}
