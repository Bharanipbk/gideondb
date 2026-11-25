# Aggregations and sorting

## Aggregations

`aggregations` computes statistics over **every** record that matches the
search's namespace, filter and text, not just the top-k hits. It sits
alongside [facets](full-text.md#facets). Name each aggregation and pick a type:

```json
{"text": "running shoes", "top_k": 10, "filter": {"in_stock": true},
 "aggregations": {
   "price":      {"type": "stats", "field": "price"},
   "price_hist": {"type": "histogram", "field": "price", "interval": 50},
   "bands":      {"type": "range", "field": "price", "ranges": [{"to": 100}, {"from": 100, "to": 200}, {"from": 200, "key": "premium"}]},
   "brands":     {"type": "terms", "field": "brand", "size": 5}
 }}
```

```json
"aggregations": {
  "price":      {"count": 412, "min": 19, "max": 349, "sum": 41736, "avg": 101.3},
  "price_hist": {"buckets": [{"key": 0, "count": 120}, {"key": 50, "count": 98}, ...]},
  "bands":      {"buckets": [{"key": "*-100", "to": 100, "count": 230}, {"key": "100-200", "from": 100, "to": 200, "count": 150}, {"key": "premium", "from": 200, "count": 32}]},
  "brands":     {"buckets": [{"key": "acme", "count": 140}, ...]}
}
```

| Type | Parameters | Result |
|---|---|---|
| `stats` | `field` | `count`, `min`, `max`, `sum`, `avg` |
| `histogram` | `field`, `interval` | Buckets keyed by lower bound (`floor(value/interval)*interval`), ascending, at most 1,000 |
| `range` | `field`, `ranges` (`from` inclusive, `to` exclusive, optional `key`) | One bucket per range, in request order |
| `terms` | `field`, `size` (default 10) | Most common values, like a facet |

- **Values:** numeric aggregations use numbers only. They skip strings, and
  count each element of an array. A request can have up to 16 aggregations.
- **Cluster:** `/v1/cluster/collections/{name}/search` merges exact partial
  results from every shard owner (an average is computed from the summed
  count and sum).
- **Cost:** counting visits every matching record, like facets do. Narrow
  large collections with a filter.

## Sorting

`sort` orders results by metadata fields instead of relevance:

```json
{"text": "running shoes", "top_k": 20, "sort": [{"field": "price", "order": "asc"}, {"field": "_score"}]}
```

- **How it works:** search first retrieves the `sort_candidates` most relevant records
  (default `max(100, 10 × (offset + top_k))`, at most 10,000), then orders them
  by the sort fields. This is OpenSearch's behaviour for sorting kNN results:
  you get the cheapest relevant items, not the cheapest items in the
  collection. Raise `sort_candidates` for a deeper pool. For a full listing,
  use [scroll](../api/rest.md) with a filter.
- **Fields:** up to 4 sort fields. `_score` sorts by relevance (default
  `desc`), and fields default to `asc`.
- **Order rules:** numbers sort before strings, which sort before booleans.
  Records without the field come last in either order, and remaining ties keep
  relevance order.
- **Paging and groups:** `offset` and `group_by` apply after sorting.

This is also available over [gRPC](../api/grpc.md#search-features).
