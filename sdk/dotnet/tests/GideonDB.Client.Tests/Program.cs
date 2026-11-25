using System.Net;
using System.Text;
using GideonDB.Client;

await LifecycleAndEncoding();
await DistributedAndErrors();
await ValidationCancellationAndBounds();
await HybridAndQuantization();
await SearchFeatures();
await SuggestAndDocuments();
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

static async Task SearchFeatures()
{
    var handler = new QueueHandler();
    handler.Add(HttpStatusCode.Created, "{\"name\":\"docs\",\"dimension\":2}");
    handler.Add(HttpStatusCode.OK, "{\"results\":[{\"id\":\"one\",\"score\":1,\"highlights\":[\"<b>hello</b> world\"]}]}");
    handler.Add(HttpStatusCode.OK, "{\"results\":[],\"facets\":{\"lang\":[{\"value\":\"en\",\"count\":3},{\"value\":7,\"count\":1}]}}");
    handler.Add(HttpStatusCode.OK, "{\"results\":[],\"aggregations\":{\"p\":{\"count\":2,\"min\":1,\"max\":3,\"sum\":4,\"avg\":2},\"r\":{\"buckets\":[{\"key\":\"cheap\",\"to\":10,\"count\":5}]}}}");
    handler.Add(HttpStatusCode.OK, "{\"results\":[],\"partial\":false,\"failures\":[],\"metadata_epoch\":1,\"authoritative_placement\":true,\"facets\":{\"lang\":[{\"value\":\"en\",\"count\":2}]},\"aggregations\":{\"t\":{\"buckets\":[{\"key\":true,\"count\":1}]}}}");
    using var client = new GideonDBClient("https://db.example", new HttpClient(handler));
    await client.CreateCollectionAsync(new CollectionConfig
    {
        Name = "docs", Dimension = 2, Metric = "cosine", ShardCount = 1, TextAnalyzer = "english",
        Synonyms = new[] { new[] { "car", "auto" } }
    });
    Check(handler.Bodies[0] == "{\"name\":\"docs\",\"dimension\":2,\"metric\":\"cosine\",\"shard_count\":1,\"text_analyzer\":\"english\",\"synonyms\":[[\"car\",\"auto\"]]}", "analyzer body: " + handler.Bodies[0]);

    var results = await client.SearchAsync("docs", new SearchOptions
    {
        TopK = 2, Text = "\"hello world\" -spam", Syntax = "simple",
        Highlight = new HighlightOptions { Query = "hello", PreTag = "<b>", PostTag = "</b>", FragmentSize = 80, Fragments = 2, Raw = true },
        Sort = new[] { new SortField { Field = "price", Order = "asc" }, new SortField { Field = "_score" } },
        SortCandidates = 500
    });
    Check(handler.Bodies[1] == "{\"top_k\":2,\"text\":\"\\u0022hello world\\u0022 -spam\",\"syntax\":\"simple\",\"highlight\":{\"query\":\"hello\",\"pre_tag\":\"\\u003Cb\\u003E\",\"post_tag\":\"\\u003C/b\\u003E\",\"fragment_size\":80,\"fragments\":2,\"raw\":true},\"sort\":[{\"field\":\"price\",\"order\":\"asc\"},{\"field\":\"_score\"}],\"sort_candidates\":500}", "feature body: " + handler.Bodies[1]);
    Check(results[0].Highlights is [ "<b>hello</b> world" ], "highlights");

    var faceted = await client.SearchFacetsAsync("docs", new SearchOptions { TopK = 1, Text = "x" }, new[] { new FacetRequest { Field = "lang", Size = 5 }, new FacetRequest { Field = "tag" } });
    Check(handler.Bodies[2] == "{\"top_k\":1,\"text\":\"x\",\"facets\":[{\"field\":\"lang\",\"size\":5},{\"field\":\"tag\"}]}", "facet body: " + handler.Bodies[2]);
    var lang = faceted.Facets!["lang"];
    Check(lang[0].Value.GetString() == "en" && lang[0].Count == 3 && lang[1].Value.GetInt32() == 7, "facet parse");

    var aggregated = await client.SearchAggregateAsync("docs", new SearchOptions { TopK = 1, Text = "x" }, new Dictionary<string, Aggregation>
    {
        ["p"] = new Aggregation { Type = "stats", Field = "price" },
        ["h"] = new Aggregation { Type = "histogram", Field = "price", Interval = 10 },
        ["r"] = new Aggregation { Type = "range", Field = "price", Ranges = new[] { new AggregationRange { Key = "cheap", To = 10 }, new AggregationRange { From = 10 } } },
        ["t"] = new Aggregation { Type = "terms", Field = "tag", Size = 3 }
    });
    Check(handler.Bodies[3] == "{\"top_k\":1,\"text\":\"x\",\"aggregations\":{\"p\":{\"type\":\"stats\",\"field\":\"price\"},\"h\":{\"type\":\"histogram\",\"field\":\"price\",\"interval\":10},\"r\":{\"type\":\"range\",\"field\":\"price\",\"ranges\":[{\"key\":\"cheap\",\"to\":10},{\"from\":10}]},\"t\":{\"type\":\"terms\",\"field\":\"tag\",\"size\":3}}}", "aggregation body: " + handler.Bodies[3]);
    var stats = aggregated.Aggregations!["p"];
    Check(stats.Count == 2 && stats.Min == 1 && stats.Max == 3 && stats.Sum == 4 && stats.Avg == 2 && stats.Buckets is null, "stats parse");
    var bucket = aggregated.Aggregations["r"].Buckets![0];
    Check(bucket.Key.GetString() == "cheap" && bucket.From is null && bucket.To == 10 && bucket.Count == 5, "bucket parse");

    var distributed = await client.DistributedSearchAsync("docs", new SearchOptions
    {
        TopK = 1, Text = "x", AllowPartial = true, Facets = new[] { new FacetRequest { Field = "lang" } },
        Aggregations = new Dictionary<string, Aggregation> { ["t"] = new Aggregation { Type = "terms", Field = "tag" } }
    });
    Check(handler.Bodies[4] == "{\"top_k\":1,\"text\":\"x\",\"facets\":[{\"field\":\"lang\"}],\"aggregations\":{\"t\":{\"type\":\"terms\",\"field\":\"tag\"}},\"allow_partial\":true}", "distributed body: " + handler.Bodies[4]);
    Check(handler.Requests[4].RequestUri!.AbsolutePath == "/v1/cluster/collections/docs/search", "distributed path");
    Check(distributed.Facets!["lang"][0].Count == 2 && distributed.Aggregations!["t"].Buckets![0].Key.GetBoolean(), "distributed parse");
    try { await client.SearchAsync("docs", new SearchOptions { TopK = 1, Text = "x", SortCandidates = 0 }); throw new Exception("Expected sort candidates error"); }
    catch (ArgumentOutOfRangeException) { }
}

static async Task SuggestAndDocuments()
{
    var handler = new QueueHandler();
    handler.Add(HttpStatusCode.OK, "{\"completions\":[{\"text\":\"vector datab\",\"word\":\"database\",\"count\":4}],\"did_you_mean\":\"vector database\"}");
    handler.Add(HttpStatusCode.OK, "{\"completions\":[]}");
    handler.Add(HttpStatusCode.Created, "{\"document_id\":\"guide\",\"title\":\"Guide\",\"format\":\"markdown\",\"chunks\":3,\"characters\":2100,\"replaced_chunks_removed\":1}");
    handler.Add(HttpStatusCode.Created, "{\"document_id\":\"notes\",\"title\":\"\",\"format\":\"text\",\"chunks\":1,\"characters\":5,\"replaced_chunks_removed\":0}");
    using var client = new GideonDBClient("https://db.example", new HttpClient(handler));

    var suggestions = await client.SuggestAsync("my docs", "vectr datab", 5);
    Check(handler.Requests[0].Method == HttpMethod.Get && handler.Requests[0].RequestUri!.OriginalString == "https://db.example/v1/collections/my%20docs/suggest?text=vectr%20datab&size=5", "suggest URL: " + handler.Requests[0].RequestUri);
    Check(suggestions.Completions[0].Word == "database" && suggestions.Completions[0].Count == 4 && suggestions.Completions[0].Text == "vector datab" && suggestions.DidYouMean == "vector database", "suggest parse");
    var none = await client.SuggestAsync("docs", "a&b");
    Check(handler.Requests[1].RequestUri!.OriginalString.EndsWith("/suggest?text=a%26b", StringComparison.Ordinal) && none.DidYouMean is null, "suggest default size");

    var document = await client.IngestDocumentAsync("docs", "/tmp/guide.md", Encoding.UTF8.GetBytes("# Guide"), new DocumentOptions
    {
        Id = "guide", Namespace = "team", Metadata = new Dictionary<string, object?> { ["lang"] = "en", ["v"] = 2 },
        ChunkSize = 500, ChunkOverlap = 50, ContentType = "text/markdown"
    });
    var body = handler.Bodies[2];
    Check(handler.Requests[2].Method == HttpMethod.Post && handler.Requests[2].RequestUri!.AbsolutePath == "/v1/collections/docs/documents", "document path");
    Check(handler.ContentTypes[2]!.StartsWith("multipart/form-data; boundary=", StringComparison.Ordinal), "multipart type: " + handler.ContentTypes[2]);
    foreach (var expected in new[]
    {
        "Content-Disposition: form-data; name=id\r\n\r\nguide\r\n",
        "Content-Disposition: form-data; name=namespace\r\n\r\nteam\r\n",
        "Content-Disposition: form-data; name=metadata\r\n\r\n{\"lang\":\"en\",\"v\":2}\r\n",
        "Content-Disposition: form-data; name=chunk_size\r\n\r\n500\r\n",
        "Content-Disposition: form-data; name=chunk_overlap\r\n\r\n50\r\n",
        "Content-Type: text/markdown\r\nContent-Disposition: form-data; name=file; filename=guide.md; filename*=utf-8''guide.md\r\n\r\n# Guide\r\n"
    })
        Check(body.Contains(expected, StringComparison.Ordinal), "multipart missing " + expected + " in " + body);
    Check(document.DocumentId == "guide" && document.Title == "Guide" && document.Format == "markdown" && document.Chunks == 3 && document.Characters == 2100 && document.ReplacedChunksRemoved == 1, "document parse");

    using var stream = new MemoryStream(Encoding.UTF8.GetBytes("hello"));
    var notes = await client.IngestDocumentAsync("docs", "notes.txt", stream);
    Check(!handler.Bodies[3].Contains("name=id", StringComparison.Ordinal) && !handler.Bodies[3].Contains("name=metadata", StringComparison.Ordinal) && handler.Bodies[3].Contains("filename=notes.txt", StringComparison.Ordinal), "minimal multipart: " + handler.Bodies[3]);
    Check(stream.CanRead && notes.Chunks == 1, "caller stream left open");
}

static void Check(bool condition, string message) { if (!condition) throw new Exception(message); }

sealed class QueueHandler : HttpMessageHandler
{
    private readonly Queue<HttpResponseMessage> responses = new();
    public List<HttpRequestMessage> Requests { get; } = new();
    public List<string> Bodies { get; } = new();
    public List<string?> ContentTypes { get; } = new();
    public void Add(HttpStatusCode status, string body) => Add(status, Encoding.UTF8.GetBytes(body));
    public void Add(HttpStatusCode status, byte[] body) => responses.Enqueue(new HttpResponseMessage(status) { Content = new ByteArrayContent(body) });
    protected override Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken cancellationToken)
    {
        cancellationToken.ThrowIfCancellationRequested();
        Requests.Add(Clone(request));
        ContentTypes.Add(request.Content?.Headers.ContentType?.ToString());
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
