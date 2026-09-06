# Go SDK

**Maturity:** Experimental

The dependency-free client in `pkg/client` covers health/readiness, collection
lifecycle, vector CRUD and batches, filtered search, and placement-aware
distributed search and writes.

```go
sdk, err := client.New("https://gideondb.example.com", client.Options{
    APIKey: os.Getenv("GIDEONDB_API_KEY"),
})
if err != nil {
    log.Fatal(err)
}

ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()

_, err = sdk.CreateCollection(ctx, client.CollectionConfig{
    Name: "docs", Dimension: 768, Metric: "cosine", ShardCount: 16,
})
```

Every operation requires a caller-provided context. The default HTTP client has
a 30-second timeout and responses are bounded to 16 MiB. Supply
`Options.HTTPClient` to configure private roots, client certificates, proxies,
connection pooling, or a different total timeout. The base URL must be one
HTTP(S) origin without credentials, query parameters, or a path prefix.

Non-2xx responses return `*client.APIError` with the HTTP status, server code,
and message:

```go
var apiErr *client.APIError
if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
    // Handle a missing collection or record.
}
```

The SDK deliberately performs no automatic retries. Cross-shard batches and
timeouts can have ambiguous durable outcomes; applications should use stable
record IDs, inspect distributed shard outcomes, and apply their own bounded
retry policy. A `207 Multi-Status` distributed write is decoded as a successful
transport response whose `Partial` flag and `Outcomes` require inspection.

`Search` and `BatchUpsert` use single-node routes. Clusters with static routing
must use `DistributedSearch` and `DistributedBatchUpsert`. Partial distributed
search is opt-in through `SearchOptions.AllowPartial`; the default fails closed
when any shard is unavailable.

`Scroll` browses records physically present on one node. Pass its opaque cursor
unchanged for the next page; vectors require explicit inclusion:

```go
page, err := sdk.Scroll(ctx, "documents", client.ScrollOptions{Limit: 50})
next, err := sdk.Scroll(ctx, "documents", client.ScrollOptions{
    Limit: 50, Cursor: page.NextCursor, IncludeVector: true,
})
```

Use `DistributedScroll` with the same options for a placement-aware page across
all shards. Its cursor is fenced to the cluster metadata epoch:

```go
page, err := sdk.DistributedScroll(ctx, "documents", client.ScrollOptions{Limit: 50})
```
