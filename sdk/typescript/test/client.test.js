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
