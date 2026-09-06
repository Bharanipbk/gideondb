const MAX_RESPONSE_BYTES = 16 << 20;

export class VectorDBAPIError extends Error {
  constructor(statusCode, code = "", message = "") {
    super(code ? `vectordb: ${code} (HTTP ${statusCode}): ${message}` : `vectordb: HTTP ${statusCode}`);
    this.name = "VectorDBAPIError";
    this.statusCode = statusCode;
    this.code = code;
  }
}

export class VectorDBTransportError extends Error {
  constructor(message, options) {
    super(`vectordb: ${message}`, options);
    this.name = "VectorDBTransportError";
  }
}

export class VectorDBClient {
  constructor(baseURL, options = {}) {
    let parsed;
    try {
      parsed = new URL(baseURL.trim());
    } catch (error) {
      throw new TypeError("baseURL must be an HTTP(S) origin", { cause: error });
    }
    if (!["http:", "https:"].includes(parsed.protocol) || parsed.username || parsed.password || !parsed.host || !["", "/"].includes(parsed.pathname) || parsed.search || parsed.hash) {
      throw new TypeError("baseURL must be an HTTP(S) origin");
    }
    const timeoutMs = options.timeoutMs ?? 30_000;
    if (!Number.isFinite(timeoutMs) || timeoutMs <= 0) {
      throw new TypeError("timeoutMs must be positive");
    }
    this.baseURL = parsed.origin;
    this.apiKey = options.apiKey ?? "";
    this.timeoutMs = timeoutMs;
    this.fetch = options.fetch ?? globalThis.fetch;
    if (typeof this.fetch !== "function") {
      throw new TypeError("a fetch implementation is required");
    }
  }

  async health(options) { await this.request("GET", "/v1/health", undefined, options); }
  async ready(options) { await this.request("GET", "/v1/ready", undefined, options); }

  async listCollections(options) {
    return (await this.request("GET", "/v1/collections", undefined, options)).collections;
  }

  async createCollection(config, options) {
    return this.request("POST", "/v1/collections", config, options);
  }

  async describeCollection(name, options) {
    return this.request("GET", collectionPath(name), undefined, options);
  }

  async deleteCollection(name, options) {
    await this.request("DELETE", collectionPath(name), undefined, options);
  }

  async upsert(collection, record, options) {
    return this.request("POST", `${collectionPath(collection)}/vectors`, record, options);
  }

  async batchUpsert(collection, records, options) {
    return (await this.request("POST", `${collectionPath(collection)}/vectors/batch`, { records }, options)).records;
  }

  async get(collection, id, options = {}) {
    return this.request("GET", recordPath(collection, id, options.namespace), undefined, options);
  }

  async scroll(collection, settings = {}, options) {
	return this.scrollPath(collectionPath(collection), settings, options);
  }

  async distributedScroll(collection, settings = {}, options) {
	return this.scrollPath(clusterCollectionPath(collection), settings, options);
  }

  async scrollPath(basePath, settings = {}, options) {
    const limit = settings.limit ?? 50;
    if (!Number.isInteger(limit) || limit < 1 || limit > 200) throw new TypeError("limit must be between 1 and 200");
    const query = new URLSearchParams({ limit: String(limit) });
    if (settings.namespace) query.set("namespace", settings.namespace);
    if (settings.cursor) query.set("cursor", settings.cursor);
    if (settings.includeVector) query.set("include_vector", "true");
    return this.request("GET", `${basePath}/vectors?${query}`, undefined, options);
  }

  async delete(collection, id, options = {}) {
    await this.request("DELETE", recordPath(collection, id, options.namespace), undefined, options);
  }

  async search(collection, search, options) {
    return (await this.request("POST", `${collectionPath(collection)}/search`, searchBody(search, false), options)).results;
  }

  async distributedSearch(collection, search, options) {
    return this.request("POST", `${clusterCollectionPath(collection)}/search`, searchBody(search, true), options);
  }

  async distributedBatchUpsert(collection, records, settings = {}, options) {
    const body = { records };
    if (settings.acknowledgement) body.acknowledgement = settings.acknowledgement;
    return this.request("POST", `${clusterCollectionPath(collection)}/vectors/batch`, body, options);
  }

  async request(method, path, body, options = {}) {
    const headers = { Accept: "application/json", "X-VectorDB-Client": "typescript/dev" };
    if (body !== undefined) headers["Content-Type"] = "application/json";
    if (this.apiKey) headers.Authorization = `Bearer ${this.apiKey}`;
    const timeoutSignal = AbortSignal.timeout(this.timeoutMs);
    const signal = options.signal ? AbortSignal.any([options.signal, timeoutSignal]) : timeoutSignal;
    let response;
    try {
      response = await this.fetch(this.baseURL + path, {
        method,
        headers,
        body: body === undefined ? undefined : JSON.stringify(body),
        signal,
      });
    } catch (error) {
      throw new VectorDBTransportError("request failed", { cause: error });
    }
    const payload = await readBounded(response);
    if (!response.ok) {
      let detail = {};
      try { detail = payload.length ? JSON.parse(new TextDecoder().decode(payload)) : {}; } catch {}
      throw new VectorDBAPIError(response.status, detail.code, detail.message);
    }
    if (response.status === 204 || payload.length === 0) return undefined;
    try {
      return JSON.parse(new TextDecoder().decode(payload));
    } catch (error) {
      throw new VectorDBTransportError("response is not valid JSON", { cause: error });
    }
  }
}

function collectionPath(name) { return `/v1/collections/${encodeURIComponent(name)}`; }
function clusterCollectionPath(name) { return `/v1/cluster/collections/${encodeURIComponent(name)}`; }

function recordPath(collection, id, namespace) {
  const path = `${collectionPath(collection)}/vectors/${encodeURIComponent(id)}`;
  return namespace ? `${path}?${new URLSearchParams({ namespace })}` : path;
}

function searchBody(search, distributed) {
  const body = { vector: search.vector, top_k: search.topK };
  if (search.namespace) body.namespace = search.namespace;
  if (search.filter !== undefined) body.filter = search.filter;
  if (distributed && search.allowPartial) body.allow_partial = true;
  return body;
}

async function readBounded(response) {
  if (!response.body) return new Uint8Array();
  const reader = response.body.getReader();
  const chunks = [];
  let length = 0;
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      length += value.byteLength;
      if (length > MAX_RESPONSE_BYTES) {
        await reader.cancel();
        throw new VectorDBTransportError("response exceeds 16 MiB");
      }
      chunks.push(value);
    }
  } catch (error) {
    if (error instanceof VectorDBTransportError) throw error;
    throw new VectorDBTransportError("could not read response", { cause: error });
  }
  const result = new Uint8Array(length);
  let offset = 0;
  for (const chunk of chunks) {
    result.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return result;
}
