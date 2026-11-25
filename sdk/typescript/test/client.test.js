import assert from "node:assert/strict";
import test from "node:test";
import { GideonDBAPIError, GideonDBClient, GideonDBTransportError } from "../src/index.js";

function queuedFetch(responses, requests = []) {
  return async (url, options) => {
    requests.push({ url, options });
    const response = responses.shift();
    if (response instanceof Error) throw response;
    return response;
  };
}

function jsonResponse(value, status = 200) {
  return new Response(status === 204 ? null : JSON.stringify(value), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

test("authenticated lifecycle requests encode path and namespace", async () => {
  const requests = [];
  const client = new GideonDBClient("https://db.example/", {
    apiKey: "secret",
    fetch: queuedFetch([
      jsonResponse({ status: "ok" }),
      jsonResponse({ name: "docs", dimension: 2, metric: "dot", shard_count: 2 }, 201),
      jsonResponse({ id: "a/b", vector: [1, 0], version: 1 }),
      jsonResponse({ id: "a/b", vector: [1, 0], version: 1 }),
      jsonResponse({ results: [{ id: "a/b", score: 1 }] }),
      jsonResponse(null, 204),
    ], requests),
  });
  await client.health();
  await client.createCollection({ name: "docs", dimension: 2, metric: "dot", shard_count: 2 });
  await client.upsert("docs", { id: "a/b", vector: [1, 0] });
  assert.equal((await client.get("docs", "a/b", { namespace: "tenant one" })).version, 1);
  assert.equal((await client.search("docs", { vector: [1, 0], topK: 1 }))[0].id, "a/b");
  await client.delete("docs", "a/b");
  assert.match(requests[3].url, /a%2Fb\?namespace=tenant\+one$/);
  assert.equal(requests[3].options.headers.Authorization, "Bearer secret");
});

test("distributed multi-status and API errors remain explicit", async () => {
  const client = new GideonDBClient("http://db.example", {
    fetch: queuedFetch([
      jsonResponse({ results: [], partial: true, failures: [{ shard_id: 1 }], metadata_epoch: 2, authoritative_placement: true }),
      jsonResponse({ outcomes: [{ shard_id: 0, status: "unknown" }], partial: true, metadata_epoch: 2, authoritative_placement: true }, 207),
      jsonResponse({ code: "not_found", message: "missing" }, 404),
    ]),
  });
  assert.equal((await client.distributedSearch("docs", { vector: [1, 0], topK: 3, allowPartial: true })).partial, true);
  assert.equal((await client.distributedBatchUpsert("docs", [{ id: "one", vector: [1, 0] }], { acknowledgement: "all" })).outcomes[0].status, "unknown");
  await assert.rejects(client.describeCollection("missing"), error => error instanceof GideonDBAPIError && error.statusCode === 404 && error.code === "not_found");
});

test("origin, timeout, cancellation, and response bounds fail safely", async () => {
  assert.throws(() => new GideonDBClient("ftp://db.example"), TypeError);
  assert.throws(() => new GideonDBClient("https://db.example/path"), TypeError);
  assert.throws(() => new GideonDBClient("https://db.example", { timeoutMs: 0 }), TypeError);
  const controller = new AbortController();
  controller.abort();
  const cancelled = new GideonDBClient("https://db.example", { fetch: async (_url, options) => { options.signal.throwIfAborted(); } });
  await assert.rejects(cancelled.health({ signal: controller.signal }), GideonDBTransportError);
  const oversized = new GideonDBClient("https://db.example", { fetch: queuedFetch([new Response(new Uint8Array((16 << 20) + 1))]) });
  await assert.rejects(oversized.health(), GideonDBTransportError);
});

test("scroll preserves opaque cursors and explicit vector inclusion", async () => {
  const requests = [];
  const client = new GideonDBClient("https://db.example", { fetch: queuedFetch([
    jsonResponse({ records: [{ id: "one" }], next_cursor: "next/value", vectors_included: false }),
    jsonResponse({ records: [], next_cursor: "", vectors_included: false, metadata_epoch: 7, authoritative_placement: true }),
  ], requests) });
  const page = await client.scroll("docs", { namespace: "tenant one", limit: 25, cursor: "prior/value", includeVector: true });
  assert.equal(page.next_cursor, "next/value");
  assert.match(requests[0].url, /namespace=tenant\+one/);
  assert.match(requests[0].url, /cursor=prior%2Fvalue/);
  assert.match(requests[0].url, /include_vector=true/);
  const clusterPage = await client.distributedScroll("docs", { limit: 25 });
  assert.equal(clusterPage.metadata_epoch, 7);
  assert.match(requests[1].url, /\/v1\/cluster\/collections\/docs\/vectors\?/);
  await assert.rejects(client.scroll("docs", { limit: 201 }), TypeError);
  await assert.rejects(client.distributedScroll("docs", { limit: 201 }), TypeError);
});

test("hybrid, text, sparse search and quantized vectorizer collections send exact bodies", async () => {
  const requests = [];
  const client = new GideonDBClient("https://db.example", { fetch: queuedFetch([
    jsonResponse({ name: "docs" }, 201),
    jsonResponse({ records: [] }),
    jsonResponse({ results: [] }),
    jsonResponse({ results: [] }),
    jsonResponse({ results: [], partial: false, failures: [], metadata_epoch: 1, authoritative_placement: true }),
  ], requests) });
  const config = { name: "docs", dimension: 384, metric: "cosine", shard_count: 1, vectorizer: "minilm", index: { type: "flat", quantization: "binary", oversampling: 4 } };
  await client.createCollection(config);
  await client.batchUpsert("docs", [{ id: "a", text: "hello world", sparse_vector: { hello: 1.5 } }]);
  await client.search("docs", { text: "hello", topK: 3, alpha: 0.3, fusion: "rrf" });
  await client.search("docs", { sparseVector: { hello: 2 }, topK: 1 });
  await client.distributedSearch("docs", { vector: [1, 0], text: "hi", sparseVector: { hi: 1 }, topK: 2, fusion: "weighted" });
  const bodies = requests.map(r => JSON.parse(r.options.body));
  assert.deepEqual(bodies[0], config);
  assert.deepEqual(bodies[1], { records: [{ id: "a", text: "hello world", sparse_vector: { hello: 1.5 } }] });
  assert.deepEqual(bodies[2], { top_k: 3, text: "hello", alpha: 0.3, fusion: "rrf" });
  assert.deepEqual(bodies[3], { top_k: 1, sparse_vector: { hello: 2 } });
  assert.deepEqual(bodies[4], { top_k: 2, vector: [1, 0], text: "hi", sparse_vector: { hi: 1 }, fusion: "weighted" });
  await assert.rejects(client.search("docs", { topK: 1 }), TypeError);
  await assert.rejects(client.search("docs", { text: "x", topK: 1, alpha: 1.5 }), TypeError);
  await assert.rejects(client.search("docs", { text: "x", topK: 1, fusion: "max" }), TypeError);
  assert.equal(requests.length, 5);
});

test("full-text collection options, query syntax, highlight, facets, aggregations and sort send exact bodies", async () => {
  const requests = [];
  const client = new GideonDBClient("https://db.example", { fetch: queuedFetch([
    jsonResponse({ name: "docs" }, 201),
    jsonResponse({ results: [{ id: "a", score: 1, highlights: ["<b>vector</b> db"] }] }),
    jsonResponse({
      results: [{ id: "a", score: 1 }],
      facets: { category: [{ value: "guide", count: 2 }] },
      aggregations: { price: { count: 2, min: 1, max: 3, sum: 4, avg: 2 }, bands: { buckets: [{ key: "cheap", to: 10, count: 2 }] } },
    }),
    jsonResponse({ results: [], partial: false, failures: [], metadata_epoch: 1, authoritative_placement: true, facets: { category: [] }, aggregations: {} }),
  ], requests) });
  const config = { name: "docs", dimension: 2, metric: "cosine", shard_count: 1, text_analyzer: "english", synonyms: [["car", "automobile"]] };
  await client.createCollection(config);
  const highlight = { query: "vector", pre_tag: "<b>", post_tag: "</b>", fragment_size: 80, fragments: 2, raw: true };
  const results = await client.search("docs", { text: '"vector db" +fast', topK: 5, syntax: "simple", highlight });
  assert.deepEqual(results[0].highlights, ["<b>vector</b> db"]);
  const facets = [{ field: "category", size: 5 }];
  const aggregations = {
    price: { type: "stats", field: "price" },
    hist: { type: "histogram", field: "price", interval: 10 },
    bands: { type: "range", field: "price", ranges: [{ key: "cheap", to: 10 }, { from: 10 }] },
    tags: { type: "terms", field: "tag", size: 3 },
  };
  const sort = [{ field: "price", order: "asc" }, { field: "_score" }];
  const detailed = await client.searchDetailed("docs", { text: "x", topK: 3, facets, aggregations, sort, sortCandidates: 200 });
  assert.equal(detailed.results[0].id, "a");
  assert.deepEqual(detailed.facets.category, [{ value: "guide", count: 2 }]);
  assert.equal(detailed.aggregations.price.avg, 2);
  assert.equal(detailed.aggregations.bands.buckets[0].key, "cheap");
  const cluster = await client.distributedSearch("docs", { text: "x", topK: 1, facets, syntax: "simple", sort });
  assert.deepEqual(cluster.facets, { category: [] });
  const bodies = requests.map(r => JSON.parse(r.options.body));
  assert.deepEqual(bodies[0], config);
  assert.deepEqual(bodies[1], { top_k: 5, text: '"vector db" +fast', syntax: "simple", highlight });
  assert.deepEqual(bodies[2], { top_k: 3, text: "x", facets, aggregations, sort, sort_candidates: 200 });
  assert.deepEqual(bodies[3], { top_k: 1, text: "x", syntax: "simple", facets, sort });
  assert.match(requests[3].url, /\/v1\/cluster\/collections\/docs\/search$/);
});

test("suggest encodes text and size and parses completions", async () => {
  const requests = [];
  const client = new GideonDBClient("https://db.example", { fetch: queuedFetch([
    jsonResponse({ completions: [{ text: "vector data", word: "data", count: 4 }], did_you_mean: "vector database" }),
    jsonResponse({ completions: [] }),
  ], requests) });
  const suggestions = await client.suggest("my docs", "vector da", 5);
  assert.equal(requests[0].url, "https://db.example/v1/collections/my%20docs/suggest?text=vector+da&size=5");
  assert.equal(requests[0].options.method, "GET");
  assert.deepEqual(suggestions.completions, [{ text: "vector data", word: "data", count: 4 }]);
  assert.equal(suggestions.did_you_mean, "vector database");
  await client.suggest("docs", "ve");
  assert.equal(requests[1].url, "https://db.example/v1/collections/docs/suggest?text=ve");
  await assert.rejects(client.suggest("docs", ""), TypeError);
  await assert.rejects(client.suggest("docs", "x", 0), TypeError);
});

test("ingestDocument uploads multipart form data and parses the result", async () => {
  const requests = [];
  const result = { document_id: "guide", title: "Guide", format: "markdown", chunks: 3, characters: 2400, replaced_chunks_removed: 1 };
  const client = new GideonDBClient("https://db.example", { apiKey: "secret", fetch: queuedFetch([
    jsonResponse(result, 201),
    jsonResponse(result, 201),
    jsonResponse(result, 201),
  ], requests) });
  const got = await client.ingestDocument("docs", "dir/guide.md", "# Guide\nhello", {
    id: "guide", namespace: "team", metadata: { lang: "en" }, chunkSize: 500, chunkOverlap: 50, contentType: "text/markdown",
  });
  assert.deepEqual(got, result);
  const { url, options } = requests[0];
  assert.equal(url, "https://db.example/v1/collections/docs/documents");
  assert.equal(options.method, "POST");
  assert.equal(options.headers["Content-Type"], undefined);
  assert.equal(options.headers.Authorization, "Bearer secret");
  assert.ok(options.body instanceof FormData);
  assert.equal(options.body.get("id"), "guide");
  assert.equal(options.body.get("namespace"), "team");
  assert.equal(options.body.get("metadata"), '{"lang":"en"}');
  assert.equal(options.body.get("chunk_size"), "500");
  assert.equal(options.body.get("chunk_overlap"), "50");
  const file = options.body.get("file");
  assert.equal(file.name, "guide.md");
  assert.equal(file.type, "text/markdown");
  assert.equal(await file.text(), "# Guide\nhello");
  // Encoded wire form carries the multipart boundary and file part.
  const encoded = new Request("https://x", { method: "POST", body: options.body });
  assert.match(encoded.headers.get("content-type"), /^multipart\/form-data; boundary=/);
  const wire = await encoded.text();
  assert.match(wire, /name="file"; filename="guide.md"\r\nContent-Type: text\/markdown/);

  await client.ingestDocument("docs", "a.pdf", new Uint8Array([37, 80, 68, 70]));
  const minimal = requests[1].options.body;
  assert.deepEqual([...minimal.keys()], ["file"]);
  assert.deepEqual(new Uint8Array(await minimal.get("file").arrayBuffer()), new Uint8Array([37, 80, 68, 70]));
  await client.ingestDocument("docs", "b.txt", new Blob(["hi"]));
  assert.equal(await requests[2].options.body.get("file").text(), "hi");
  await assert.rejects(client.ingestDocument("docs", "c.txt", 42), TypeError);
  await assert.rejects(client.ingestDocument("docs", "", "x"), TypeError);
});
