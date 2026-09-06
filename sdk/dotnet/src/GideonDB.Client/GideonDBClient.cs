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
        if (body is not null) request.Content = JsonContent.Create(body, options: JsonOptions);

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
        var body = new JsonObject
        {
            ["vector"] = JsonSerializer.SerializeToNode(options.Vector, JsonOptions),
            ["top_k"] = options.TopK
        };
        if (!string.IsNullOrEmpty(options.Namespace)) body["namespace"] = options.Namespace;
        if (options.Filter is not null) body["filter"] = options.Filter.DeepClone();
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
}

public sealed record CollectionConfig
{
    [JsonPropertyName("name")] public string Name { get; init; } = string.Empty;
    [JsonPropertyName("dimension")] public int Dimension { get; init; }
    [JsonPropertyName("metric")] public string Metric { get; init; } = string.Empty;
    [JsonPropertyName("shard_count")] public int ShardCount { get; init; }
    [JsonPropertyName("index")] public IndexConfig? Index { get; init; }
}

public sealed record CollectionDescription(
    [property: JsonPropertyName("config")] CollectionConfig Config,
    [property: JsonPropertyName("vector_count")] long VectorCount);

public sealed record VectorRecord
{
    [JsonPropertyName("id")] public string Id { get; init; } = string.Empty;
    [JsonPropertyName("vector")] public IReadOnlyList<float>? Vector { get; init; }
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
}

public sealed record SearchResult
{
    [JsonPropertyName("id")] public string Id { get; init; } = string.Empty;
    [JsonPropertyName("score")] public float Score { get; init; }
    [JsonPropertyName("metadata")] public IReadOnlyDictionary<string, JsonElement>? Metadata { get; init; }
    [JsonPropertyName("payload")] public IReadOnlyDictionary<string, JsonElement>? Payload { get; init; }
    [JsonPropertyName("namespace")] public string? Namespace { get; init; }
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
    [property: JsonPropertyName("authoritative_placement")] bool AuthoritativePlacement);

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
