# Metadata filtering

**Maturity:** Experimental

Search accepts a `filter` object. Multiple keys in one object are implicitly
ANDed:

```json
{
  "vector": [0.1, 0.2, 0.3],
  "top_k": 10,
  "filter": {
    "language": "go",
    "year": {"$gte": 2024}
  }
}
```

Supported field operators are `$eq`, `$ne`, `$gt`, `$gte`, `$lt`, `$lte`,
`$in`, `$nin`, `$exists`, and `$text`. `$geo_radius` filters by location.
Logical operators are `$and`, `$or`, and `$not`:

```json
{
  "$or": [
    {"language": {"$in": ["go", "rust"]}},
    {"featured": true}
  ]
}
```

`$ne` and `$nin` match only records where the field exists. Use `$not` when
missing fields should also match. Range comparison supports finite numeric
values and strings of the same type. Equality normalizes JSON numeric types so
`2024` and `2024.0` address the same posting.

## Word matching

`$text` matches string fields that contain every word of the query, ignoring
case and punctuation. It matches whole words: `"data"` does not match
`"database"`.

```json
{"title": {"$text": "vector databases"}}
```

`$text` scans the values of one field rather than using a posting list, so it
costs time proportional to the records that have that field. Combine it with
selective filters on large collections. For ranked keyword search, use
[sparse and hybrid retrieval](sparse-hybrid.md).

## Geo radius

`$geo_radius` matches records within a great-circle distance of a point.
Store latitude and longitude in degrees as two numeric metadata fields,
`lat` and `lon` by default:

```json
{
  "$geo_radius": {"lat": 48.8566, "lon": 2.3522, "meters": 25000}
}
```

Use `lat_field` and `lon_field` to name other fields. The numeric index
narrows candidates to a bounding box, which handles the antimeridian and the
poles, and each candidate is then checked exactly. Records missing either
field never match.

## Execution

Each mutable segment maintains:

- a dense bitmap live-ordinal universe;
- typed field values by ordinal;
- adaptive equality postings keyed by canonical scalar value: inline singleton,
  sparse small set, then dense bitmap after promotion;
- numeric immutable sorted bases plus bounded append-only mutable deltas.

Logical expressions use word-wise bitmap intersection, union and difference.
Equality, membership and existence avoid scanning records. Numeric range
queries use binary search to locate bounds in the immutable base, scan at most
4,095 pending delta entries, and materialize matching ordinals. An
invalidated-base bitmap suppresses replaced/deleted base entries without a hash
lookup per match. String ranges currently scan only the selected field's typed
value column. The resulting bitmap is supplied to the vector index.
Flat search skips disallowed ordinals. HNSW may traverse disallowed nodes to
maintain graph connectivity but admits only matching ordinals to its result
heap, preventing post-filter underfill caused by an unrelated top-k cutoff.
When the metadata bitmap contains at most `max(ef_search, 4 * top_k)` entries,
HNSW scores the allowed set exactly; larger sets retain graph traversal.

Only top-level scalar metadata fields are indexed. Nested objects and arrays
remain stored in the record but cannot be filtered yet. Namespace selection is
an additional exact predicate and omission means only the default empty
namespace—not all namespaces.

## Update and recovery behavior

Upsert removes old postings before adding new ones, and delete removes the
ordinal from the universe and every indexed field. New checkpoints persist a
versioned filter index containing the live universe, typed field values,
adaptive equality postings, presence sets, and sorted numeric entries. Recovery
validates its checksum, manifest count/LSN, ordinal coverage, posting/value
consistency, and numeric entries before installing it directly. Older
checkpoints without the file rebuild indexes from records for compatibility.
Filter semantics are tested against explicit expected sets for every supported
operator.

Dense bitmaps are effective because segment ordinals are compact. Sparse or
very large ordinal spaces will require a compressed/Roaring representation.
Numeric inserts append to the mutable delta in `O(1)`. At 4,096 pending entries
or invalidations, current values merge into a replacement sorted base. This
amortizes sorting but a merge still pauses mutation under the segment write
lock. Background merge scheduling remains planned with multi-segment storage.

See the [metadata benchmark](../benchmarks/metadata-filtering.md) for the first
before/after allocation evidence.

## Pagination and grouping

Search also accepts:

| Field | Meaning |
|---|---|
| `offset` | Skip this many results (or groups); `offset + top_k` is at most 10,000 |
| `group_by` | Group hits by this metadata field; the response has `groups` instead of `results` |
| `group_size` | Hits per group, 1–100 (default 1) |

```json
{"vector": [0.1, 0.2, 0.3], "top_k": 5, "group_by": "author", "group_size": 2}
```

```json
{"groups": [{"value": "ana", "hits": [{"id": "p7", "score": 0.93}, {"id": "p2", "score": 0.88}]}]}
```

Groups are ordered by their best hit and formed from the best
`(offset + top_k) × group_size × 4` candidates, so rare groups far down the
ranking may be missing. Records without the field are left out. Both options
work on single-node and `/v1/cluster/collections/{name}/search` requests.
