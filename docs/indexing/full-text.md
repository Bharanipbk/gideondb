# Full-text: analyzers, highlighting and facets

GideonDB scores record `text` with BM25 (see
[sparse and hybrid search](sparse-hybrid.md)). This page covers how that
text is turned into terms, how to get marked snippets back, and how to count
metadata values over the matching records.

## Analyzers

Pick an analyzer when you create a collection with `text_analyzer`. It is
applied to record text at index time and to query text at search time.

```bash
curl -fsS -X POST http://127.0.0.1:6333/v1/collections -H 'content-type: application/json' -d '{
  "name": "articles", "dimension": 384, "metric": "cosine", "shard_count": 2,
  "text_analyzer": "english"
}'
```

| Analyzer | Lowercase | Accent folding | Stopwords | Stemming | Use for |
|---|---|---|---|---|---|
| `standard` (default) | yes | no | 22 English words | no | Mixed or unknown languages; the original behaviour |
| `simple` | yes | no | none | no | Codes, names, short labels |
| `whitespace` | no | no | none | no | Pre-tokenized or case-sensitive text |
| `folding` | yes | yes | none | no | Any Latin-script language, no stemming |
| `cjk` | yes | yes | none | bigrams | Chinese, Japanese, Korean mixed with Latin text |
| `english` | yes | yes | Lucene English | Porter | English |
| `french`, `german`, `spanish`, `italian`, `portuguese` | yes | yes | Lucene lists | light (Savoy) | Those languages |

- Tokens split on any rune that is not a letter, digit or combining mark.
- `cjk` turns runs of Han, Kana and Hangul into overlapping bigrams
  (东京大学 → 东京 京大 大学), so these languages can be searched without a
  word segmenter.
- Light stemmers strip inflection (plurals, gender), not derivation, so
  `chevaux` and `cheval` match but `national` and `nation` stay apart.
- Lexical indexes are rebuilt from stored text when segments load. The
  analyzer is fixed when the collection is created.

## Highlighting

Add `highlight` to a search. Each result then carries `highlights`, which are
snippets of its text with the matching terms wrapped in tags. Matching uses
the collection analyzer, so with `english` a query for `indexes` marks
`index` and `Indexing`.

```json
{"text": "vector indexes", "top_k": 5,
 "highlight": {"pre_tag": "<mark>", "post_tag": "</mark>", "fragment_size": 120, "fragments": 2}}
```

| Field | Default | Notes |
|---|---|---|
| `query` | the search `text` | Required for vector-only searches |
| `pre_tag`, `post_tag` | `<em>`, `</em>` | At most 64 bytes each |
| `fragment_size` | 150 | Target snippet length in bytes, extended to word boundaries |
| `fragments` | 3 | Snippets with the most matches win, then they are returned in text order |
| `raw` | false | By default, text between tags is HTML-escaped |

Record text is never returned in search results. Only the snippets are.

## Facets

Add `facets` to count metadata values over **every** record that matches the
namespace, the filter and, when `text` is set, at least one query term. The
count is not limited to the top-k hits.

```json
{"text": "running shoes", "top_k": 10,
 "filter": {"in_stock": true},
 "facets": [{"field": "brand"}, {"field": "size", "size": 5}]}
```

```json
{"results": [...],
 "facets": {"brand": [{"value": "acme", "count": 128}, {"value": "zeta", "count": 41}],
            "size":  [{"value": 42, "count": 60}, ...]}}
```

- Up to 16 fields per request, and `size` (default 10, at most 1000) buckets each,
  largest count first. Ties are ordered by value.
- Array values count each distinct element once per record. Objects and
  nulls are skipped. `1` and `"1"` are different values.
- Overwritten and deleted records are counted once or not at all, even
  before a checkpoint merges segments.
- `/v1/cluster/collections/{name}/search` merges exact counts from every
  shard owner. With `allow_partial`, failed shards are left out of the counts
  as well as the results. Every node must run a version with facet support.
- gRPC: `SearchRequest.facets`, `SearchRequest.highlight`,
  `SearchResult.highlights` and `SearchResponse.facets`.

Facet counting visits every matching record in each segment, so its cost
grows with the match count. Narrow it with a filter on large collections.
