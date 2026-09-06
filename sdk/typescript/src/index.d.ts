export interface ClientOptions {
  apiKey?: string;
  timeoutMs?: number;
  fetch?: typeof globalThis.fetch;
}

export interface RequestOptions { signal?: AbortSignal; }
export type Metric = "cosine" | "dot" | "l2";
export type Acknowledgement = "leader" | "quorum" | "all";

export interface IndexConfig {
  type?: "flat" | "hnsw";
  m?: number;
  ef_construction?: number;
  ef_search?: number;
}

export interface CollectionConfig {
  name: string;
  dimension: number;
  metric: Metric;
  shard_count: number;
  index?: IndexConfig;
}

export interface VectorRecord {
  id: string;
  vector: number[];
  metadata?: Record<string, unknown>;
  payload?: Record<string, unknown>;
  timestamp?: number;
  version?: number;
  namespace?: string;
}

export interface RecordPage {
  records: VectorRecord[];
  next_cursor: string;
  vectors_included: boolean;
  metadata_epoch?: number;
  authoritative_placement?: boolean;
}

export interface SearchRequest {
  vector: number[];
  topK: number;
  namespace?: string;
  filter?: Record<string, unknown>;
  allowPartial?: boolean;
}

export interface SearchResult {
  id: string;
  score: number;
  metadata?: Record<string, unknown>;
  payload?: Record<string, unknown>;
  namespace?: string;
}

export interface DistributedSearchResponse {
  results: SearchResult[];
  partial: boolean;
  failures: Array<{ shard_id: number; node_id?: string; error: string }>;
  metadata_epoch: number;
  authoritative_placement: boolean;
}

export interface DistributedWriteResponse {
  outcomes: Array<{ shard_id: number; node_id?: string; status: string; error?: string; replicas_acknowledged?: number; replication_factor?: number }>;
  partial: boolean;
  metadata_epoch: number;
  authoritative_placement: boolean;
}

export class GideonDBAPIError extends Error {
  readonly statusCode: number;
  readonly code: string;
  constructor(statusCode: number, code?: string, message?: string);
}

export class GideonDBTransportError extends Error {}

export class GideonDBClient {
  constructor(baseURL: string, options?: ClientOptions);
  health(options?: RequestOptions): Promise<void>;
  ready(options?: RequestOptions): Promise<void>;
  listCollections(options?: RequestOptions): Promise<CollectionConfig[]>;
  createCollection(config: CollectionConfig, options?: RequestOptions): Promise<CollectionConfig>;
  describeCollection(name: string, options?: RequestOptions): Promise<{ config: CollectionConfig; vector_count: number }>;
  deleteCollection(name: string, options?: RequestOptions): Promise<void>;
  upsert(collection: string, record: VectorRecord, options?: RequestOptions): Promise<VectorRecord>;
  batchUpsert(collection: string, records: VectorRecord[], options?: RequestOptions): Promise<VectorRecord[]>;
  get(collection: string, id: string, options?: RequestOptions & { namespace?: string }): Promise<VectorRecord>;
  scroll(collection: string, settings?: { namespace?: string; limit?: number; cursor?: string; includeVector?: boolean }, options?: RequestOptions): Promise<RecordPage>;
  distributedScroll(collection: string, settings?: { namespace?: string; limit?: number; cursor?: string; includeVector?: boolean }, options?: RequestOptions): Promise<RecordPage>;
  delete(collection: string, id: string, options?: RequestOptions & { namespace?: string }): Promise<void>;
  search(collection: string, search: SearchRequest, options?: RequestOptions): Promise<SearchResult[]>;
  distributedSearch(collection: string, search: SearchRequest, options?: RequestOptions): Promise<DistributedSearchResponse>;
  distributedBatchUpsert(collection: string, records: VectorRecord[], settings?: { acknowledgement?: Acknowledgement }, options?: RequestOptions): Promise<DistributedWriteResponse>;
}
