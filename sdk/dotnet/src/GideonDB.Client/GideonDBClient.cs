using System.Net;
using System.Net.Http.Headers;
using System.Net.Http.Json;
using System.Text.Json;
using System.Text.Json.Nodes;
using System.Text.Json.Serialization;

namespace GideonDB.Client;

/// <summary>Asynchronous client for GideonDB's experimental REST API.</summary>
public sealed class GideonDBClient : IDisposable
{
    private const int MaxResponseBytes = 16 << 20;
    private static readonly JsonSerializerOptions JsonOptions = new(JsonSerializerDefaults.Web)
    {
        DefaultIgnoreCondition = JsonIgnoreCondition.WhenWritingNull
    };

    private readonly Uri origin;
    private readonly string apiKey;
    private readonly TimeSpan timeout;
    private readonly HttpClient httpClient;
    private readonly bool ownsClient;

    public GideonDBClient(string baseUrl, string? apiKey = null, TimeSpan? timeout = null)
        : this(baseUrl, apiKey, timeout, new HttpClient(), true) { }

    /// <summary>Creates a client using a caller-owned HTTP client.</summary>
    public GideonDBClient(string baseUrl, HttpClient httpClient, string? apiKey = null, TimeSpan? timeout = null)
        : this(baseUrl, apiKey, timeout, httpClient, false) { }

    private GideonDBClient(string baseUrl, string? apiKey, TimeSpan? timeout, HttpClient httpClient, bool ownsClient)
    {
        origin = ValidateOrigin(baseUrl);
        this.timeout = timeout ?? TimeSpan.FromSeconds(30);
        if (this.timeout <= TimeSpan.Zero || this.timeout == Timeout.InfiniteTimeSpan)
            throw new ArgumentOutOfRangeException(nameof(timeout), "Timeout must be positive.");
        this.apiKey = apiKey ?? string.Empty;
        this.httpClient = httpClient ?? throw new ArgumentNullException(nameof(httpClient));
        this.httpClient.Timeout = Timeout.InfiniteTimeSpan;
        this.ownsClient = ownsClient;
    }

    public async Task HealthAsync(CancellationToken cancellationToken = default) =>
        await SendAsync<JsonElement>(HttpMethod.Get, "/v1/health", null, cancellationToken).ConfigureAwait(false);

    public async Task ReadyAsync(CancellationToken cancellationToken = default) =>
        await SendAsync<JsonElement>(HttpMethod.Get, "/v1/ready", null, cancellationToken).ConfigureAwait(false);

    public async Task<IReadOnlyList<CollectionConfig>> ListCollectionsAsync(CancellationToken cancellationToken = default) =>
        (await SendAsync<CollectionList>(HttpMethod.Get, "/v1/collections", null, cancellationToken).ConfigureAwait(false)).Collections;

    public Task<CollectionConfig> CreateCollectionAsync(CollectionConfig config, CancellationToken cancellationToken = default) =>
        SendAsync<CollectionConfig>(HttpMethod.Post, "/v1/collections", config, cancellationToken);

    public Task<CollectionDescription> DescribeCollectionAsync(string name, CancellationToken cancellationToken = default) =>
        SendAsync<CollectionDescription>(HttpMethod.Get, CollectionPath(name), null, cancellationToken);

    public async Task DeleteCollectionAsync(string name, CancellationToken cancellationToken = default) =>
        await SendAsync<JsonElement>(HttpMethod.Delete, CollectionPath(name), null, cancellationToken).ConfigureAwait(false);

    public Task<VectorRecord> UpsertAsync(string collection, VectorRecord record, CancellationToken cancellationToken = default) =>
        SendAsync<VectorRecord>(HttpMethod.Post, $"{CollectionPath(collection)}/vectors", record, cancellationToken);

    public async Task<IReadOnlyList<VectorRecord>> BatchUpsertAsync(string collection, IReadOnlyList<VectorRecord> records, CancellationToken cancellationToken = default) =>
        (await SendAsync<RecordList>(HttpMethod.Post, $"{CollectionPath(collection)}/vectors/batch", new { records }, cancellationToken).ConfigureAwait(false)).Records;

    public Task<VectorRecord> GetAsync(string collection, string id, string? @namespace = null, CancellationToken cancellationToken = default) =>
        SendAsync<VectorRecord>(HttpMethod.Get, RecordPath(collection, id, @namespace), null, cancellationToken);

    public async Task DeleteAsync(string collection, string id, string? @namespace = null, CancellationToken cancellationToken = default) =>
        await SendAsync<JsonElement>(HttpMethod.Delete, RecordPath(collection, id, @namespace), null, cancellationToken).ConfigureAwait(false);

    public Task<RecordPage> ScrollAsync(string collection, ScrollOptions? options = null, CancellationToken cancellationToken = default) =>
        ScrollPathAsync(CollectionPath(collection), options ?? new ScrollOptions(), cancellationToken);

    public Task<RecordPage> DistributedScrollAsync(string collection, ScrollOptions? options = null, CancellationToken cancellationToken = default) =>
        ScrollPathAsync(ClusterCollectionPath(collection), options ?? new ScrollOptions(), cancellationToken);

    public async Task<IReadOnlyList<SearchResult>> SearchAsync(string collection, SearchOptions options, CancellationToken cancellationToken = default) =>
        (await SendAsync<SearchResults>(HttpMethod.Post, $"{CollectionPath(collection)}/search", SearchBody(options, false), cancellationToken).ConfigureAwait(false)).Results;

    /// <summary>Runs a search and returns results plus any requested facets and aggregations.</summary>
    public Task<SearchResponse> SearchWithResponseAsync(string collection, SearchOptions options, CancellationToken cancellationToken = default) =>
        SendAsync<SearchResponse>(HttpMethod.Post, $"{CollectionPath(collection)}/search", SearchBody(options, false), cancellationToken);

    /// <summary>Runs a search and counts the most common values of metadata fields.</summary>
    public async Task<SearchResponse> SearchFacetsAsync(string collection, SearchOptions options, IReadOnlyList<FacetRequest> facets, CancellationToken cancellationToken = default)
    {
        ArgumentNullException.ThrowIfNull(options);
        return await SearchWithResponseAsync(collection, options with { Facets = facets ?? throw new ArgumentNullException(nameof(facets)) }, cancellationToken).ConfigureAwait(false);
    }

    /// <summary>Runs a search and computes named aggregations over every matching record.</summary>
    public async Task<SearchResponse> SearchAggregateAsync(string collection, SearchOptions options, IReadOnlyDictionary<string, Aggregation> aggregations, CancellationToken cancellationToken = default)
    {
        ArgumentNullException.ThrowIfNull(options);
        return await SearchWithResponseAsync(collection, options with { Aggregations = aggregations ?? throw new ArgumentNullException(nameof(aggregations)) }, cancellationToken).ConfigureAwait(false);
    }

    /// <summary>Completes the last word of <paramref name="text"/> and proposes a spelling fix.</summary>
    public Task<Suggestions> SuggestAsync(string collection, string text, int size = 0, CancellationToken cancellationToken = default)
    {
        var query = $"?text={Encode(text)}";
        if (size > 0) query += $"&size={size}";
        return SendAsync<Suggestions>(HttpMethod.Get, $"{CollectionPath(collection)}/suggest{query}", null, cancellationToken);
    }

    /// <summary>Uploads a text, Markdown, HTML, PDF or DOCX file for server-side extraction, chunking and embedding.</summary>
    public Task<DocumentResult> IngestDocumentAsync(string collection, string fileName, byte[] content, DocumentOptions? options = null, CancellationToken cancellationToken = default) =>
        IngestDocumentAsync(collection, fileName, new MemoryStream(content ?? throw new ArgumentNullException(nameof(content)), false), options, cancellationToken);

    /// <summary>Uploads a document read from <paramref name="content"/>; the stream is not disposed.</summary>
    public async Task<DocumentResult> IngestDocumentAsync(string collection, string fileName, Stream content, DocumentOptions? options = null, CancellationToken cancellationToken = default)
    {
        ArgumentNullException.ThrowIfNull(content);
        if (string.IsNullOrWhiteSpace(fileName)) throw new ArgumentException("File name is required.", nameof(fileName));
        options ??= new DocumentOptions();
        var form = new MultipartFormDataContent();
        void Field(string name, string? value) { if (!string.IsNullOrEmpty(value)) form.Add(new StringContent(value), name); }
        Field("id", options.Id);
        Field("namespace", options.Namespace);
        if (options.Metadata is not null) Field("metadata", JsonSerializer.Serialize(options.Metadata, JsonOptions));
        if (options.ChunkSize is { } size) Field("chunk_size", size.ToString(System.Globalization.CultureInfo.InvariantCulture));
        if (options.ChunkOverlap is { } overlap) Field("chunk_overlap", overlap.ToString(System.Globalization.CultureInfo.InvariantCulture));
        var file = new StreamContent(new NonDisposingStream(content));
        if (!string.IsNullOrEmpty(options.ContentType)) file.Headers.ContentType = MediaTypeHeaderValue.Parse(options.ContentType);
        form.Add(file, "file", Path.GetFileName(fileName));
        return await SendAsync<DocumentResult>(HttpMethod.Post, $"{CollectionPath(collection)}/documents", form, cancellationToken).ConfigureAwait(false);
    }

    public Task<DistributedSearchResponse> DistributedSearchAsync(string collection, SearchOptions options, CancellationToken cancellationToken = default) =>
        SendAsync<DistributedSearchResponse>(HttpMethod.Post, $"{ClusterCollectionPath(collection)}/search", SearchBody(options, true), cancellationToken);

    public Task<DistributedWriteResponse> DistributedBatchUpsertAsync(
        string collection,
        IReadOnlyList<VectorRecord> records,
        string? acknowledgement = null,
        CancellationToken cancellationToken = default) =>
        SendAsync<DistributedWriteResponse>(HttpMethod.Post, $"{ClusterCollectionPath(collection)}/vectors/batch",
            string.IsNullOrWhiteSpace(acknowledgement) ? new { records } : new { records, acknowledgement }, cancellationToken);

    private Task<RecordPage> ScrollPathAsync(string path, ScrollOptions options, CancellationToken cancellationToken)
    {
        if (options.Limit is < 1 or > 200)
            throw new ArgumentOutOfRangeException(nameof(options), "Limit must be between 1 and 200.");
        var query = $"?limit={options.Limit}";
        if (!string.IsNullOrEmpty(options.Namespace)) query += $"&namespace={Encode(options.Namespace)}";
        if (!string.IsNullOrEmpty(options.Cursor)) query += $"&cursor={Encode(options.Cursor)}";
        if (options.IncludeVector) query += "&include_vector=true";
        return SendAsync<RecordPage>(HttpMethod.Get, $"{path}/vectors{query}", null, cancellationToken);
    }

    private async Task<T> SendAsync<T>(HttpMethod method, string path, object? body, CancellationToken cancellationToken)
    {
        using var timeoutSource = new CancellationTokenSource(timeout);
        using var linkedSource = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken, timeoutSource.Token);
        using var request = new HttpRequestMessage(method, new Uri(origin, path));
        request.Headers.Accept.Add(new MediaTypeWithQualityHeaderValue("application/json"));
        request.Headers.TryAddWithoutValidation("X-GideonDB-Client", "dotnet/dev");
        if (!string.IsNullOrEmpty(apiKey)) request.Headers.Authorization = new AuthenticationHeaderValue("Bearer", apiKey);
        if (body is HttpContent content) request.Content = content;
        else if (body is not null) request.Content = JsonContent.Create(body, options: JsonOptions);

        try
        {
            using var response = await httpClient.SendAsync(request, HttpCompletionOption.ResponseHeadersRead, linkedSource.Token).ConfigureAwait(false);
            var bytes = await ReadBoundedAsync(response.Content, linkedSource.Token).ConfigureAwait(false);
            if (!response.IsSuccessStatusCode)
            {
                ApiErrorBody? detail = null;
                try { detail = JsonSerializer.Deserialize<ApiErrorBody>(bytes, JsonOptions); }
                catch (JsonException) { }
                throw new GideonDBApiException((int)response.StatusCode, detail?.Code ?? string.Empty, detail?.Message ?? string.Empty);
            }
            if (bytes.Length == 0)
            {
                if (typeof(T) == typeof(JsonElement)) return (T)(object)default(JsonElement);
                throw new GideonDBResponseException("Successful response body is empty.");
            }
            return JsonSerializer.Deserialize<T>(bytes, JsonOptions)
                ?? throw new GideonDBResponseException("Successful response contains JSON null.");
        }
        catch (GideonDBException) { throw; }
        catch (OperationCanceledException exception) when (timeoutSource.IsCancellationRequested && !cancellationToken.IsCancellationRequested)
        {
            throw new GideonDBTransportException("Request timed out.", exception);
        }
        catch (OperationCanceledException) { throw; }
        catch (HttpRequestException exception) { throw new GideonDBTransportException("Request failed.", exception); }
        catch (JsonException exception) { throw new GideonDBResponseException("Response is not valid JSON.", exception); }
    }

    private static async Task<byte[]> ReadBoundedAsync(HttpContent content, CancellationToken cancellationToken)
    {
        if (content.Headers.ContentLength > MaxResponseBytes) throw new GideonDBResponseTooLargeException();
        await using var stream = await content.ReadAsStreamAsync(cancellationToken).ConfigureAwait(false);
        using var output = new MemoryStream();
        var buffer = new byte[81920];
        while (true)
        {
            var read = await stream.ReadAsync(buffer, cancellationToken).ConfigureAwait(false);
            if (read == 0) break;
            if (output.Length + read > MaxResponseBytes) throw new GideonDBResponseTooLargeException();
            output.Write(buffer, 0, read);
        }
        return output.ToArray();
    }

    private static object SearchBody(SearchOptions options, bool distributed)
    {
        ArgumentNullException.ThrowIfNull(options);
        if (options.TopK <= 0) throw new ArgumentOutOfRangeException(nameof(options), "TopK must be positive.");
        var hasText = !string.IsNullOrWhiteSpace(options.Text);
        var hasSparse = options.SparseVector is { Count: > 0 };
        var hasVector = options.Vector is { Count: > 0 };
        if (options.Alpha is { } alpha && !(alpha >= 0 && alpha <= 1))
            throw new ArgumentOutOfRangeException(nameof(options), "Alpha must be between 0 and 1.");
        if (!string.IsNullOrEmpty(options.Fusion) && options.Fusion is not ("weighted" or "rrf"))
            throw new ArgumentOutOfRangeException(nameof(options), "Fusion must be \"weighted\" or \"rrf\".");
        var body = new JsonObject();
        // Text-only and sparse-only searches omit the vector; otherwise keep sending it as before.
        if (hasVector || (!hasText && !hasSparse))
            body["vector"] = JsonSerializer.SerializeToNode(options.Vector ?? Array.Empty<float>(), JsonOptions);
        body["top_k"] = options.TopK;
        if (!string.IsNullOrEmpty(options.Namespace)) body["namespace"] = options.Namespace;
        if (options.Filter is not null) body["filter"] = options.Filter.DeepClone();
        if (hasText) body["text"] = options.Text;
        if (hasSparse) body["sparse_vector"] = JsonSerializer.SerializeToNode(options.SparseVector, JsonOptions);
        if (options.Alpha is { } weight) body["alpha"] = weight;
        if (!string.IsNullOrEmpty(options.Fusion)) body["fusion"] = options.Fusion;
        if (!string.IsNullOrEmpty(options.Syntax)) body["syntax"] = options.Syntax;
        if (options.Highlight is not null) body["highlight"] = JsonSerializer.SerializeToNode(options.Highlight, JsonOptions);
        if (options.Facets is { Count: > 0 }) body["facets"] = JsonSerializer.SerializeToNode(options.Facets, JsonOptions);
        if (options.Aggregations is { Count: > 0 }) body["aggregations"] = JsonSerializer.SerializeToNode(options.Aggregations, JsonOptions);
        if (options.Sort is { Count: > 0 }) body["sort"] = JsonSerializer.SerializeToNode(options.Sort, JsonOptions);
        if (options.SortCandidates is { } candidates)
        {
            if (candidates <= 0) throw new ArgumentOutOfRangeException(nameof(options), "SortCandidates must be positive.");
            body["sort_candidates"] = candidates;
        }
        if (distributed && options.AllowPartial) body["allow_partial"] = true;
        return body;
    }

    private static Uri ValidateOrigin(string baseUrl)
    {
        if (!Uri.TryCreate(baseUrl?.Trim(), UriKind.Absolute, out var uri)
            || (uri.Scheme != Uri.UriSchemeHttp && uri.Scheme != Uri.UriSchemeHttps)
            || !string.IsNullOrEmpty(uri.UserInfo)
            || uri.AbsolutePath != "/"
            || !string.IsNullOrEmpty(uri.Query)
            || !string.IsNullOrEmpty(uri.Fragment))
            throw new ArgumentException("Base URL must be an HTTP(S) origin.", nameof(baseUrl));
        return new Uri(uri.GetLeftPart(UriPartial.Authority) + "/");
    }

    private static string Encode(string value) => Uri.EscapeDataString(value ?? throw new ArgumentNullException(nameof(value)));
    private static string CollectionPath(string name) => $"/v1/collections/{Encode(name)}";
    private static string ClusterCollectionPath(string name) => $"/v1/cluster/collections/{Encode(name)}";
    private static string RecordPath(string collection, string id, string? @namespace) =>
        $"{CollectionPath(collection)}/vectors/{Encode(id)}" + (string.IsNullOrEmpty(@namespace) ? string.Empty : $"?namespace={Encode(@namespace)}");

    public void Dispose() { if (ownsClient) httpClient.Dispose(); }

    private sealed record CollectionList([property: JsonPropertyName("collections")] IReadOnlyList<CollectionConfig> Collections);
    private sealed record RecordList([property: JsonPropertyName("records")] IReadOnlyList<VectorRecord> Records);
    private sealed record SearchResults([property: JsonPropertyName("results")] IReadOnlyList<SearchResult> Results);
    /// <summary>Lets a caller-owned stream be wrapped in HttpContent without being disposed.</summary>
    private sealed class NonDisposingStream : Stream
    {
        private readonly Stream inner;
        public NonDisposingStream(Stream inner) => this.inner = inner;
        public override bool CanRead => inner.CanRead;
        public override bool CanSeek => inner.CanSeek;
        public override bool CanWrite => false;
        public override long Length => inner.Length;
        public override long Position { get => inner.Position; set => inner.Position = value; }
        public override void Flush() { }
        public override int Read(byte[] buffer, int offset, int count) => inner.Read(buffer, offset, count);
        public override ValueTask<int> ReadAsync(Memory<byte> buffer, CancellationToken cancellationToken = default) => inner.ReadAsync(buffer, cancellationToken);
        public override Task<int> ReadAsync(byte[] buffer, int offset, int count, CancellationToken cancellationToken) => inner.ReadAsync(buffer, offset, count, cancellationToken);
        public override long Seek(long offset, SeekOrigin origin) => inner.Seek(offset, origin);
        public override void SetLength(long value) => throw new NotSupportedException();
        public override void Write(byte[] buffer, int offset, int count) => throw new NotSupportedException();
    }

    private sealed record ApiErrorBody([property: JsonPropertyName("code")] string? Code, [property: JsonPropertyName("message")] string? Message);
}

public abstract class GideonDBException : Exception
{
    protected GideonDBException(string message, Exception? innerException = null) : base($"gideondb: {message}", innerException) { }
}

public sealed class GideonDBApiException : GideonDBException
{
    public int StatusCode { get; }
    public string Code { get; }
    public GideonDBApiException(int statusCode, string code, string message)
        : base(string.IsNullOrEmpty(code) ? $"HTTP {statusCode}" : $"{code} (HTTP {statusCode}): {message}")
    { StatusCode = statusCode; Code = code; }
}

public sealed class GideonDBTransportException : GideonDBException
{
    public GideonDBTransportException(string message, Exception innerException) : base(message, innerException) { }
}

public class GideonDBResponseException : GideonDBException
{
    public GideonDBResponseException(string message, Exception? innerException = null) : base(message, innerException) { }
}

public sealed class GideonDBResponseTooLargeException : GideonDBResponseException
{
    public GideonDBResponseTooLargeException() : base("Response exceeds 16 MiB.") { }
}

public sealed record IndexConfig
{
    [JsonPropertyName("type")] public string Type { get; init; } = "flat";
    [JsonPropertyName("m")] public int? M { get; init; }
    [JsonPropertyName("ef_construction")] public int? EfConstruction { get; init; }
    [JsonPropertyName("ef_search")] public int? EfSearch { get; init; }
    /// <summary>"binary" enables binary quantization (flat index only).</summary>
    [JsonPropertyName("quantization")] public string? Quantization { get; init; }
    /// <summary>Rescoring candidate multiplier, 1-100 (server default 4).</summary>
    [JsonPropertyName("oversampling")] public int? Oversampling { get; init; }
}

public sealed record CollectionConfig
{
    [JsonPropertyName("name")] public string Name { get; init; } = string.Empty;
    [JsonPropertyName("dimension")] public int Dimension { get; init; }
    [JsonPropertyName("metric")] public string Metric { get; init; } = string.Empty;
    [JsonPropertyName("shard_count")] public int ShardCount { get; init; }
    [JsonPropertyName("vectorizer")] public string? Vectorizer { get; init; }
    [JsonPropertyName("index")] public IndexConfig? Index { get; init; }
    /// <summary>BM25 analyzer such as "english" or "cjk"; null uses the server's "standard".</summary>
    [JsonPropertyName("text_analyzer")] public string? TextAnalyzer { get; init; }
    /// <summary>Groups of interchangeable words for text queries.</summary>
    [JsonPropertyName("synonyms")] public IReadOnlyList<IReadOnlyList<string>>? Synonyms { get; init; }
}

public sealed record CollectionDescription(
    [property: JsonPropertyName("config")] CollectionConfig Config,
    [property: JsonPropertyName("vector_count")] long VectorCount);

public sealed record VectorRecord
{
    [JsonPropertyName("id")] public string Id { get; init; } = string.Empty;
    [JsonPropertyName("vector")] public IReadOnlyList<float>? Vector { get; init; }
    [JsonPropertyName("text")] public string? Text { get; init; }
    [JsonPropertyName("sparse_vector")] public IReadOnlyDictionary<string, float>? SparseVector { get; init; }
    [JsonPropertyName("metadata")] public IReadOnlyDictionary<string, JsonElement>? Metadata { get; init; }
    [JsonPropertyName("payload")] public IReadOnlyDictionary<string, JsonElement>? Payload { get; init; }
    [JsonPropertyName("timestamp")] public long? Timestamp { get; init; }
    [JsonPropertyName("version")] public ulong? Version { get; init; }
    [JsonPropertyName("namespace")] public string? Namespace { get; init; }
}

public sealed record ScrollOptions
{
    public string? Namespace { get; init; }
    public int Limit { get; init; } = 50;
    public string? Cursor { get; init; }
    public bool IncludeVector { get; init; }
}

public sealed record RecordPage(
    [property: JsonPropertyName("records")] IReadOnlyList<VectorRecord> Records,
    [property: JsonPropertyName("next_cursor")] string NextCursor,
    [property: JsonPropertyName("vectors_included")] bool VectorsIncluded,
    [property: JsonPropertyName("metadata_epoch")] ulong MetadataEpoch = 0,
    [property: JsonPropertyName("authoritative_placement")] bool AuthoritativePlacement = false);

public sealed record SearchOptions
{
    public IReadOnlyList<float> Vector { get; init; } = Array.Empty<float>();
    public int TopK { get; init; }
    public string? Namespace { get; init; }
    public JsonNode? Filter { get; init; }
    public bool AllowPartial { get; init; }
    /// <summary>Query text for BM25 (and server-side embedding when no vector is given).</summary>
    public string? Text { get; init; }
    public IReadOnlyDictionary<string, float>? SparseVector { get; init; }
    /// <summary>Dense weight from 0 to 1 for weighted fusion (server default 0.5).</summary>
    public double? Alpha { get; init; }
    /// <summary>"weighted" (server default) or "rrf".</summary>
    public string? Fusion { get; init; }
    /// <summary>"simple" enables "phrases", +must, -must_not, fuzzy~, prefix* and ^boosts in Text.</summary>
    public string? Syntax { get; init; }
    /// <summary>Requests marked snippets of each result's record text.</summary>
    public HighlightOptions? Highlight { get; init; }
    /// <summary>Metadata fields whose most common values are counted (see SearchResponse.Facets).</summary>
    public IReadOnlyList<FacetRequest>? Facets { get; init; }
    /// <summary>Named aggregations over every matching record (see SearchResponse.Aggregations).</summary>
    public IReadOnlyDictionary<string, Aggregation>? Aggregations { get; init; }
    /// <summary>Orders relevance candidates by metadata fields or "_score".</summary>
    public IReadOnlyList<SortField>? Sort { get; init; }
    /// <summary>Size of the candidate pool that Sort reorders (server default max(100, 10x page)).</summary>
    public int? SortCandidates { get; init; }
}

/// <summary>Snippet options; null fields use the server defaults (&lt;em&gt; tags, 150-byte fragments, at most 3).</summary>
public sealed record HighlightOptions
{
    [JsonPropertyName("query")] public string? Query { get; init; }
    [JsonPropertyName("pre_tag")] public string? PreTag { get; init; }
    [JsonPropertyName("post_tag")] public string? PostTag { get; init; }
    [JsonPropertyName("fragment_size")] public int? FragmentSize { get; init; }
    [JsonPropertyName("fragments")] public int? Fragments { get; init; }
    [JsonPropertyName("raw")] public bool? Raw { get; init; }
}

public sealed record FacetRequest
{
    [JsonPropertyName("field")] public string Field { get; init; } = string.Empty;
    [JsonPropertyName("size")] public int? Size { get; init; }
}

public sealed record FacetBucket
{
    /// <summary>The metadata value (string, number or boolean).</summary>
    [JsonPropertyName("value")] public JsonElement Value { get; init; }
    [JsonPropertyName("count")] public long Count { get; init; }
}

/// <summary>One "stats", "histogram", "range" or "terms" aggregation.</summary>
public sealed record Aggregation
{
    [JsonPropertyName("type")] public string Type { get; init; } = string.Empty;
    [JsonPropertyName("field")] public string Field { get; init; } = string.Empty;
    [JsonPropertyName("interval")] public double? Interval { get; init; }
    [JsonPropertyName("ranges")] public IReadOnlyList<AggregationRange>? Ranges { get; init; }
    [JsonPropertyName("size")] public int? Size { get; init; }
}

/// <summary>A half-open [From, To) range; null bounds are open.</summary>
public sealed record AggregationRange
{
    [JsonPropertyName("key")] public string? Key { get; init; }
    [JsonPropertyName("from")] public double? From { get; init; }
    [JsonPropertyName("to")] public double? To { get; init; }
}

public sealed record AggregationResult
{
    [JsonPropertyName("count")] public long? Count { get; init; }
    [JsonPropertyName("min")] public double? Min { get; init; }
    [JsonPropertyName("max")] public double? Max { get; init; }
    [JsonPropertyName("sum")] public double? Sum { get; init; }
    [JsonPropertyName("avg")] public double? Avg { get; init; }
    [JsonPropertyName("buckets")] public IReadOnlyList<AggregationBucket>? Buckets { get; init; }
}

public sealed record AggregationBucket
{
    [JsonPropertyName("key")] public JsonElement Key { get; init; }
    [JsonPropertyName("from")] public double? From { get; init; }
    [JsonPropertyName("to")] public double? To { get; init; }
    [JsonPropertyName("count")] public long Count { get; init; }
}

/// <summary>Orders results by one field; Order is "asc" or "desc".</summary>
public sealed record SortField
{
    [JsonPropertyName("field")] public string Field { get; init; } = string.Empty;
    [JsonPropertyName("order")] public string? Order { get; init; }
}

public sealed record SearchResponse
{
    [JsonPropertyName("results")] public IReadOnlyList<SearchResult> Results { get; init; } = Array.Empty<SearchResult>();
    [JsonPropertyName("facets")] public IReadOnlyDictionary<string, IReadOnlyList<FacetBucket>>? Facets { get; init; }
    [JsonPropertyName("aggregations")] public IReadOnlyDictionary<string, AggregationResult>? Aggregations { get; init; }
}

public sealed record SuggestionCompletion
{
    [JsonPropertyName("text")] public string Text { get; init; } = string.Empty;
    [JsonPropertyName("word")] public string Word { get; init; } = string.Empty;
    [JsonPropertyName("count")] public long Count { get; init; }
}

public sealed record Suggestions
{
    [JsonPropertyName("completions")] public IReadOnlyList<SuggestionCompletion> Completions { get; init; } = Array.Empty<SuggestionCompletion>();
    [JsonPropertyName("did_you_mean")] public string? DidYouMean { get; init; }
}

public sealed record DocumentOptions
{
    /// <summary>Document ID; the server defaults to the file name without extension.</summary>
    public string? Id { get; init; }
    public string? Namespace { get; init; }
    public IReadOnlyDictionary<string, object?>? Metadata { get; init; }
    /// <summary>Chunk size in characters (server default 1000).</summary>
    public int? ChunkSize { get; init; }
    /// <summary>Chunk overlap in characters (server default 150).</summary>
    public int? ChunkOverlap { get; init; }
    /// <summary>Optional Content-Type of the file part.</summary>
    public string? ContentType { get; init; }
}

public sealed record DocumentResult
{
    [JsonPropertyName("document_id")] public string DocumentId { get; init; } = string.Empty;
    [JsonPropertyName("title")] public string Title { get; init; } = string.Empty;
    [JsonPropertyName("format")] public string Format { get; init; } = string.Empty;
    [JsonPropertyName("chunks")] public int Chunks { get; init; }
    [JsonPropertyName("characters")] public int Characters { get; init; }
    [JsonPropertyName("replaced_chunks_removed")] public int ReplacedChunksRemoved { get; init; }
}

public sealed record SearchResult
{
    [JsonPropertyName("id")] public string Id { get; init; } = string.Empty;
    [JsonPropertyName("score")] public float Score { get; init; }
    [JsonPropertyName("metadata")] public IReadOnlyDictionary<string, JsonElement>? Metadata { get; init; }
    [JsonPropertyName("payload")] public IReadOnlyDictionary<string, JsonElement>? Payload { get; init; }
    [JsonPropertyName("namespace")] public string? Namespace { get; init; }
    /// <summary>Marked snippets, set when SearchOptions.Highlight was requested.</summary>
    [JsonPropertyName("highlights")] public IReadOnlyList<string>? Highlights { get; init; }
}

public sealed record ShardFailure(
    [property: JsonPropertyName("shard_id")] uint ShardId,
    [property: JsonPropertyName("node_id")] string? NodeId,
    [property: JsonPropertyName("error")] string Error);

public sealed record DistributedSearchResponse(
    [property: JsonPropertyName("results")] IReadOnlyList<SearchResult> Results,
    [property: JsonPropertyName("partial")] bool Partial,
    [property: JsonPropertyName("failures")] IReadOnlyList<ShardFailure> Failures,
    [property: JsonPropertyName("metadata_epoch")] ulong MetadataEpoch,
    [property: JsonPropertyName("authoritative_placement")] bool AuthoritativePlacement)
{
    /// <summary>Facet counts merged from every answering shard, when requested.</summary>
    [JsonPropertyName("facets")] public IReadOnlyDictionary<string, IReadOnlyList<FacetBucket>>? Facets { get; init; }
    [JsonPropertyName("aggregations")] public IReadOnlyDictionary<string, AggregationResult>? Aggregations { get; init; }
}

public sealed record ShardWriteOutcome
{
    [JsonPropertyName("shard_id")] public uint ShardId { get; init; }
    [JsonPropertyName("node_id")] public string? NodeId { get; init; }
    [JsonPropertyName("status")] public string Status { get; init; } = string.Empty;
    [JsonPropertyName("error")] public string? Error { get; init; }
    [JsonPropertyName("replicas_acknowledged")] public int ReplicasAcknowledged { get; init; }
    [JsonPropertyName("replication_factor")] public int ReplicationFactor { get; init; }
}

public sealed record DistributedWriteResponse(
    [property: JsonPropertyName("outcomes")] IReadOnlyList<ShardWriteOutcome> Outcomes,
    [property: JsonPropertyName("partial")] bool Partial,
    [property: JsonPropertyName("metadata_epoch")] ulong MetadataEpoch,
    [property: JsonPropertyName("authoritative_placement")] bool AuthoritativePlacement);
