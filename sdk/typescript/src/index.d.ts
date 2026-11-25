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
  /** "binary" is supported on flat indexes only. */
  quantization?: "binary";
  /** Candidate multiplier for rescoring quantized results (1-100, server default 4). */
  oversampling?: number;
}

export interface CollectionConfig {
  name: string;
  dimension: number;
  metric: Metric;
  shard_count: number;
  /** Name of a server embedding model used to embed record and query text. */
  vectorizer?: string;
  index?: IndexConfig;
  /** Full-text analyzer name, e.g. "standard" or "english". */
  text_analyzer?: string;
  /** Groups of equivalent terms expanded at query time. */
  synonyms?: string[][];
}

export interface VectorRecord {
  id: string;
  /** Optional when `text` is given and the collection has a vectorizer. */
  vector?: number[];
  /** Indexed for BM25 and embedded server-side when no vector is given. */
  text?: string;
  /** Term -> positive weight. */
  sparse_vector?: Record<string, number>;
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

export type Fusion = "weighted" | "rrf";

export interface SearchRequest {
  vector?: number[];
  topK: number;
  text?: string;
  sparseVector?: Record<string, number>;
  /** Dense weight 0..1 (server default 0.5). */
  alpha?: number;
  fusion?: Fusion;
  namespace?: string;
  filter?: Record<string, unknown>;
  allowPartial?: boolean;
  /** "simple" enables "phrases", +must, -must_not, fuzzy~, prefix* and ^boost in text. */
  syntax?: "simple";
  highlight?: HighlightRequest;
  facets?: FacetRequest[];
  aggregations?: Record<string, AggregationRequest>;
  sort?: SortField[];
  /** Relevance candidates that sort reorders (1-10000). */
  sortCandidates?: number;
}

export interface HighlightRequest {
  query?: string;
  pre_tag?: string;
  post_tag?: string;
  fragment_size?: number;
  fragments?: number;
  raw?: boolean;
}

export interface FacetRequest { field: string; size?: number; }
export type FacetResults = Record<string, Array<{ value: string | number | boolean; count: number }>>;

export interface AggregationRequest {
  type: "stats" | "histogram" | "range" | "terms";
  field: string;
  interval?: number;
  ranges?: Array<{ key?: string; from?: number; to?: number }>;
  size?: number;
}

export interface AggregationResult {
  count?: number;
  min?: number;
  max?: number;
  sum?: number;
  avg?: number;
  buckets?: Array<{ key: unknown; from?: number; to?: number; count: number }>;
}

export interface SortField { field: string; order?: "asc" | "desc"; }

export interface SearchResponse {
  results: SearchResult[];
  facets?: FacetResults;
  aggregations?: Record<string, AggregationResult>;
}

export interface Suggestions {
  completions: Array<{ text: string; word: string; count: number }>;
  did_you_mean?: string;
}

export interface DocumentOptions {
  /** Default: the file name without extension. */
  id?: string;
  namespace?: string;
  metadata?: Record<string, unknown>;
  /** Runes per chunk (server default 1000). */
  chunkSize?: number;
  /** Runes of overlap (server default 150). */
  chunkOverlap?: number;
  contentType?: string;
}

export interface DocumentResult {
  document_id: string;
  title: string;
  format: string;
  chunks: number;
  characters: number;
  replaced_chunks_removed: number;
}

export interface SearchResult {
  id: string;
  score: number;
  metadata?: Record<string, unknown>;
  payload?: Record<string, unknown>;
  namespace?: string;
  /** Record text snippets, present when highlight was requested. */
  highlights?: string[];
}

export interface DistributedSearchResponse {
  results: SearchResult[];
  facets?: FacetResults;
  aggregations?: Record<string, AggregationResult>;
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
  /** Like search, but returns the full response including facets and aggregations. */
  searchDetailed(collection: string, search: SearchRequest, options?: RequestOptions): Promise<SearchResponse>;
  suggest(collection: string, text: string, size?: number, options?: RequestOptions): Promise<Suggestions>;
  ingestDocument(collection: string, filename: string, content: Blob | Uint8Array | ArrayBuffer | string, settings?: DocumentOptions, options?: RequestOptions): Promise<DocumentResult>;
  distributedSearch(collection: string, search: SearchRequest, options?: RequestOptions): Promise<DistributedSearchResponse>;
  distributedBatchUpsert(collection: string, records: VectorRecord[], settings?: { acknowledgement?: Acknowledgement }, options?: RequestOptions): Promise<DistributedWriteResponse>;
}
