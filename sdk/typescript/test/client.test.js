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
