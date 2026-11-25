# Query syntax and synonyms

By default, search `text` is a list of words. Any matching word scores a
record with BM25. Add `"syntax": "simple"` to use operators. The syntax is a
subset of OpenSearch's `simple_query_string`.

```json
{"text": "+\"vector database\" -deprecated index* hnsw~ recall^2", "syntax": "simple", "top_k": 10}
```

| Syntax | Meaning |
|---|---|
| `word` | Optional (should) clause. Matching documents score higher |
| `+word` | Required (must). Any other clauses become optional |
| `-word` | Excluded (must not) |
| `"two words"` | Phrase: the terms in order, with the same gaps (removed stopwords leave a gap, as in Lucene). `+"…"` and `-"…"` work too |
| `word~`, `word~2` | Fuzzy: up to 1 edit for words of 3–5 letters and 2 edits for 6+ letters, or the given 0–2. A transposition counts as one edit |
| `pre*` | Prefix: up to 64 indexed terms that start with `pre`, the most common first |
| `word^2.5`, `"phrase"^3` | Boost a clause (0 < boost ≤ 1000) |

- Words go through the collection's [analyzer](full-text.md#analyzers), so with
  `english`, `+indexes` matches "indexing". Fuzzy and prefix terms are matched
  against the indexed (stemmed) vocabulary of each segment. A fuzzy match
  scores at 0.8× an exact match.
- **Scoring:** a record's score is the sum of its matching clauses' BM25,
  times their boosts. When several fuzzy, prefix or synonym variants match
  one position, only the best one counts. Plain text without operators scores
  exactly as before.
- **Hybrid search:** in hybrid search (`vector` plus `text`), required and
  excluded clauses also filter the dense candidates, so `-word` removes
  records from vector results too.
- **Facets and highlighting:** [facets and aggregations](aggregations-and-sorting.md)
  count only records that satisfy the query. Highlighting marks the query's
  words (operators removed, excluded words skipped).
- **Limits:** at most 64 clauses. An unterminated quote, a bad fuzzy distance or
  a bad boost is a 400 error.

## Synonyms

Give a collection synonym groups when you create it. A query word in a group
also matches every other word in that group, both inside phrases and in plain
queries:

```json
{"name": "shop", "dimension": 384, "metric": "cosine", "shard_count": 2, "text_analyzer": "english",
 "synonyms": [["tv", "television"], ["laptop", "notebook"], ["sneaker", "trainer", "shoe"]]}
```

- Synonyms apply at query time, so stored text is unchanged. Each group needs
  2–64 single words, and there can be at most 10,000 groups.
- Words are analyzed like queries, so `["sneakers", "trainers"]` also matches
  "sneaker". Multi-word synonyms are ignored.
- Synonyms are fixed when the collection is created. To change them,
  create a new collection.
