//! Synchronous Rust client for GideonDB's experimental REST API.

use serde::{Deserialize, Serialize, de::DeserializeOwned};
use serde_json::{Map, Value, json};
use std::{collections::BTreeMap, error::Error as StdError, fmt, sync::Arc, time::Duration};

const MAX_RESPONSE_BYTES: usize = 16 << 20;

#[derive(Debug)]
pub enum Error {
    InvalidInput(String),
    Transport(String),
    Api {
        status: u16,
        code: String,
        message: String,
    },
    InvalidResponse(String),
    ResponseTooLarge,
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::InvalidInput(message) => write!(f, "gideondb: {message}"),
            Self::Transport(message) => write!(f, "gideondb: request failed: {message}"),
            Self::Api {
                status,
                code,
                message,
            } if !code.is_empty() => {
                write!(f, "gideondb: {code} (HTTP {status}): {message}")
            }
            Self::Api { status, .. } => write!(f, "gideondb: HTTP {status}"),
            Self::InvalidResponse(message) => write!(f, "gideondb: invalid response: {message}"),
            Self::ResponseTooLarge => write!(f, "gideondb: response exceeds 16 MiB"),
        }
    }
}

impl StdError for Error {}

#[derive(Debug, Clone)]
pub struct Request {
    pub method: &'static str,
    pub url: String,
    pub body: Option<Vec<u8>>,
    pub api_key: String,
    pub timeout: Duration,
    /// Body media type; `None` means `application/json`.
    pub content_type: Option<String>,
}

#[derive(Debug, Clone)]
pub struct Response {
    pub status: u16,
    pub body: Vec<u8>,
}

/// Injectable request transport for tests and controlled runtimes.
pub trait Transport: Send + Sync {
    fn send(&self, request: Request) -> Result<Response, Box<dyn StdError + Send + Sync>>;
}

#[derive(Clone)]
pub struct Client {
    origin: String,
    api_key: String,
    timeout: Duration,
    transport: Arc<dyn Transport>,
}

impl Client {
    pub fn new(base_url: &str) -> Result<Self, Error> {
        Self::with_options(base_url, "", Duration::from_secs(30))
    }

    pub fn with_options(base_url: &str, api_key: &str, timeout: Duration) -> Result<Self, Error> {
        if timeout.is_zero() {
            return Err(Error::InvalidInput("timeout must be positive".into()));
        }
        let origin = validate_origin(base_url)?;
        let transport = Arc::new(UreqTransport::new(timeout));
        Ok(Self {
            origin,
            api_key: api_key.into(),
            timeout,
            transport,
        })
    }

    pub fn with_transport(
        base_url: &str,
        api_key: &str,
        timeout: Duration,
        transport: Arc<dyn Transport>,
    ) -> Result<Self, Error> {
        if timeout.is_zero() {
            return Err(Error::InvalidInput("timeout must be positive".into()));
        }
        Ok(Self {
            origin: validate_origin(base_url)?,
            api_key: api_key.into(),
            timeout,
            transport,
        })
    }

    pub fn health(&self) -> Result<(), Error> {
        self.request::<Value>("GET", "/v1/health", None).map(|_| ())
    }
    pub fn ready(&self) -> Result<(), Error> {
        self.request::<Value>("GET", "/v1/ready", None).map(|_| ())
    }

    pub fn list_collections(&self) -> Result<Vec<CollectionConfig>, Error> {
        #[derive(Deserialize)]
        struct Envelope {
            collections: Vec<CollectionConfig>,
        }
        Ok(self
            .request::<Envelope>("GET", "/v1/collections", None)?
            .collections)
    }

    pub fn create_collection(&self, config: &CollectionConfig) -> Result<CollectionConfig, Error> {
        self.request("POST", "/v1/collections", Some(serialize(config)?))
    }

    pub fn describe_collection(&self, name: &str) -> Result<CollectionDescription, Error> {
        self.request("GET", &collection_path(name), None)
    }

    pub fn delete_collection(&self, name: &str) -> Result<(), Error> {
        self.request::<Value>("DELETE", &collection_path(name), None)
            .map(|_| ())
    }

    pub fn upsert(&self, collection: &str, record: &Record) -> Result<Record, Error> {
        self.request(
            "POST",
            &format!("{}/vectors", collection_path(collection)),
            Some(serialize(record)?),
        )
    }

    pub fn batch_upsert(&self, collection: &str, records: &[Record]) -> Result<Vec<Record>, Error> {
        #[derive(Deserialize)]
        struct Envelope {
            records: Vec<Record>,
        }
        let body = serialize(&json!({ "records": records }))?;
        Ok(self
            .request::<Envelope>(
                "POST",
                &format!("{}/vectors/batch", collection_path(collection)),
                Some(body),
            )?
            .records)
    }

    pub fn get(
        &self,
        collection: &str,
        id: &str,
        namespace: Option<&str>,
    ) -> Result<Record, Error> {
        self.request("GET", &record_path(collection, id, namespace), None)
    }

    pub fn delete(&self, collection: &str, id: &str, namespace: Option<&str>) -> Result<(), Error> {
        self.request::<Value>("DELETE", &record_path(collection, id, namespace), None)
            .map(|_| ())
    }

    pub fn scroll(&self, collection: &str, options: &ScrollOptions) -> Result<RecordPage, Error> {
        self.scroll_path(&collection_path(collection), options)
    }

    pub fn distributed_scroll(
        &self,
        collection: &str,
        options: &ScrollOptions,
    ) -> Result<RecordPage, Error> {
        self.scroll_path(&cluster_collection_path(collection), options)
    }

    fn scroll_path(&self, base: &str, options: &ScrollOptions) -> Result<RecordPage, Error> {
        if !(1..=200).contains(&options.limit) {
            return Err(Error::InvalidInput(
                "limit must be between 1 and 200".into(),
            ));
        }
        let mut path = format!("{base}/vectors?limit={}", options.limit);
        query(&mut path, "namespace", options.namespace.as_deref());
        query(&mut path, "cursor", options.cursor.as_deref());
        if options.include_vector {
            path.push_str("&include_vector=true");
        }
        self.request("GET", &path, None)
    }

    pub fn search(
        &self,
        collection: &str,
        options: &SearchOptions,
    ) -> Result<Vec<SearchResult>, Error> {
        #[derive(Deserialize)]
        struct Envelope {
            results: Vec<SearchResult>,
        }
        Ok(self
            .request::<Envelope>(
                "POST",
                &format!("{}/search", collection_path(collection)),
                Some(search_body(options, false)?),
            )?
            .results)
    }

    /// Runs a search and returns results together with any requested
    /// `facets` and `aggregations`.
    pub fn search_response(
        &self,
        collection: &str,
        options: &SearchOptions,
    ) -> Result<SearchResponse, Error> {
        self.request(
            "POST",
            &format!("{}/search", collection_path(collection)),
            Some(search_body(options, false)?),
        )
    }

    /// Completes the last word of `text` and proposes a spelling fix.
    /// `size` of 0 uses the server default.
    pub fn suggest(&self, collection: &str, text: &str, size: usize) -> Result<Suggestions, Error> {
        if text.is_empty() {
            return Err(Error::InvalidInput("text must not be empty".into()));
        }
        let mut path = format!(
            "{}/suggest?text={}",
            collection_path(collection),
            encode(text)
        );
        if size > 0 {
            path.push_str(&format!("&size={size}"));
        }
        self.request("GET", &path, None)
    }

    /// Uploads a text, Markdown, HTML, PDF or DOCX file for server-side
    /// extraction, chunking and embedding.
    pub fn ingest_document(
        &self,
        collection: &str,
        filename: &str,
        content: &[u8],
        options: &DocumentOptions,
    ) -> Result<DocumentResult, Error> {
        let (body, content_type) = multipart_body(filename, content, options)?;
        self.send_request(
            "POST",
            &format!("{}/documents", collection_path(collection)),
            Some(body),
            Some(content_type),
        )
    }

    pub fn distributed_search(
        &self,
        collection: &str,
        options: &SearchOptions,
    ) -> Result<DistributedSearchResponse, Error> {
        self.request(
            "POST",
            &format!("{}/search", cluster_collection_path(collection)),
            Some(search_body(options, true)?),
        )
    }

    pub fn distributed_batch_upsert(
        &self,
        collection: &str,
        records: &[Record],
        acknowledgement: Option<&str>,
    ) -> Result<DistributedWriteResponse, Error> {
        let mut body = Map::new();
        body.insert(
            "records".into(),
            serde_json::to_value(records).map_err(json_error)?,
        );
        if let Some(value) = acknowledgement.filter(|value| !value.is_empty()) {
            body.insert("acknowledgement".into(), Value::String(value.into()));
        }
        self.request(
            "POST",
            &format!("{}/vectors/batch", cluster_collection_path(collection)),
            Some(serialize(&body)?),
        )
    }

    fn request<T: DeserializeOwned>(
        &self,
        method: &'static str,
        path: &str,
        body: Option<Vec<u8>>,
    ) -> Result<T, Error> {
        self.send_request(method, path, body, None)
    }

    fn send_request<T: DeserializeOwned>(
        &self,
        method: &'static str,
        path: &str,
        body: Option<Vec<u8>>,
        content_type: Option<String>,
    ) -> Result<T, Error> {
        let response = self
            .transport
            .send(Request {
                method,
                url: format!("{}{}", self.origin, path),
                body,
                api_key: self.api_key.clone(),
                timeout: self.timeout,
                content_type,
            })
            .map_err(|error| Error::Transport(error.to_string()))?;
        if response.body.len() > MAX_RESPONSE_BYTES {
            return Err(Error::ResponseTooLarge);
        }
        if !(200..300).contains(&response.status) {
            let detail: ApiErrorBody = serde_json::from_slice(&response.body).unwrap_or_default();
            return Err(Error::Api {
                status: response.status,
                code: detail.code,
                message: detail.message,
            });
        }
        if response.body.is_empty() {
            return serde_json::from_slice(b"null").map_err(json_error);
        }
        serde_json::from_slice(&response.body)
            .map_err(|error| Error::InvalidResponse(error.to_string()))
    }
}

#[derive(Debug, Clone, Default, Serialize, Deserialize, PartialEq)]
pub struct CollectionConfig {
    pub name: String,
    pub dimension: usize,
    pub metric: String,
    pub shard_count: usize,
    /// Name of a server-side embedding model used for records and searches given only `text`.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub vectorizer: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub index: Option<IndexConfig>,
    /// Analyzer turning text into BM25 terms (see docs/indexing/full-text.md).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub text_analyzer: Option<String>,
    /// Groups of interchangeable words for text queries.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub synonyms: Vec<Vec<String>>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize, PartialEq)]
pub struct IndexConfig {
    #[serde(rename = "type")]
    pub kind: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub m: Option<usize>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub ef_construction: Option<usize>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub ef_search: Option<usize>,
    /// `"binary"` enables binary quantization (flat index only).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub quantization: Option<String>,
    /// Rescoring candidate multiplier, 1-100 (server default 4).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub oversampling: Option<usize>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct CollectionDescription {
    pub config: CollectionConfig,
    pub vector_count: usize,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize, PartialEq)]
pub struct Record {
    pub id: String,
    /// Optional when `text` is given and the collection has a vectorizer.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub vector: Vec<f32>,
    /// Indexed for BM25 and embedded server-side when no vector is given.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub text: Option<String>,
    /// Positive per-term weights for sparse retrieval.
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub sparse_vector: BTreeMap<String, f32>,
    #[serde(default, skip_serializing_if = "Map::is_empty")]
    pub metadata: Map<String, Value>,
    #[serde(default, skip_serializing_if = "Map::is_empty")]
    pub payload: Map<String, Value>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub timestamp: Option<i64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub version: Option<u64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub namespace: Option<String>,
}

#[derive(Debug, Clone)]
pub struct ScrollOptions {
    pub namespace: Option<String>,
    pub limit: usize,
    pub cursor: Option<String>,
    pub include_vector: bool,
}

impl Default for ScrollOptions {
    fn default() -> Self {
        Self {
            namespace: None,
            limit: 50,
            cursor: None,
            include_vector: false,
        }
    }
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RecordPage {
    pub records: Vec<Record>,
    pub next_cursor: String,
    pub vectors_included: bool,
    #[serde(default)]
    pub metadata_epoch: u64,
    #[serde(default)]
    pub authoritative_placement: bool,
}

#[derive(Debug, Clone, Default)]
pub struct SearchOptions {
    /// Dense query vector; may be empty for text-only or sparse-only searches.
    pub vector: Vec<f32>,
    pub top_k: usize,
    pub namespace: Option<String>,
    pub filter: Option<Value>,
    pub allow_partial: bool,
    /// Query text for BM25 (and server-side embedding when no vector is given).
    pub text: Option<String>,
    pub sparse_vector: BTreeMap<String, f32>,
    /// Dense weight from 0 to 1 for weighted fusion (server default 0.5).
    pub alpha: Option<f32>,
    /// `"weighted"` (server default) or `"rrf"`.
    pub fusion: Option<String>,
    /// `"simple"` enables phrases, +must, -must_not, fuzzy~, prefix* and ^boost.
    pub syntax: Option<String>,
    /// Requests marked snippets of each result's record text.
    pub highlight: Option<HighlightOptions>,
    /// Metadata value counts over every matching record.
    pub facets: Vec<FacetRequest>,
    /// Named stats, histogram, range or terms aggregations.
    pub aggregations: BTreeMap<String, Aggregation>,
    /// Orders relevance candidates by metadata fields or `_score`.
    pub sort: Vec<SortField>,
    /// Size of the candidate pool `sort` reorders.
    pub sort_candidates: Option<usize>,
}

/// Highlight settings; unset fields use server defaults.
#[derive(Debug, Clone, Default, Serialize, Deserialize, PartialEq)]
pub struct HighlightOptions {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub query: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub pre_tag: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub post_tag: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub fragment_size: Option<usize>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub fragments: Option<usize>,
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub raw: bool,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize, PartialEq)]
pub struct FacetRequest {
    pub field: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub size: Option<usize>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct FacetBucket {
    pub value: Value,
    pub count: u64,
}

/// One aggregation; `kind` is `"stats"`, `"histogram"`, `"range"` or `"terms"`.
#[derive(Debug, Clone, Default, Serialize, Deserialize, PartialEq)]
pub struct Aggregation {
    #[serde(rename = "type")]
    pub kind: String,
    pub field: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub interval: Option<f64>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub ranges: Vec<AggregationRange>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub size: Option<usize>,
}

/// Half-open `[from, to)` range; `None` bounds are open.
#[derive(Debug, Clone, Default, Serialize, Deserialize, PartialEq)]
pub struct AggregationRange {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub key: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub from: Option<f64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub to: Option<f64>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize, PartialEq)]
pub struct AggregationResult {
    #[serde(default)]
    pub count: Option<u64>,
    #[serde(default)]
    pub min: Option<f64>,
    #[serde(default)]
    pub max: Option<f64>,
    #[serde(default)]
    pub sum: Option<f64>,
    #[serde(default)]
    pub avg: Option<f64>,
    #[serde(default)]
    pub buckets: Vec<AggregationBucket>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct AggregationBucket {
    #[serde(default)]
    pub key: Value,
    #[serde(default)]
    pub from: Option<f64>,
    #[serde(default)]
    pub to: Option<f64>,
    pub count: u64,
}

/// Orders results by one field; `order` is `"asc"` or `"desc"`.
#[derive(Debug, Clone, Default, Serialize, Deserialize, PartialEq)]
pub struct SortField {
    pub field: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub order: Option<String>,
}

/// Full node-local search response.
#[derive(Debug, Clone, Default, Serialize, Deserialize, PartialEq)]
pub struct SearchResponse {
    pub results: Vec<SearchResult>,
    #[serde(default)]
    pub facets: BTreeMap<String, Vec<FacetBucket>>,
    #[serde(default)]
    pub aggregations: BTreeMap<String, AggregationResult>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Completion {
    pub text: String,
    pub word: String,
    pub count: u64,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize, PartialEq)]
pub struct Suggestions {
    #[serde(default)]
    pub completions: Vec<Completion>,
    #[serde(default)]
    pub did_you_mean: Option<String>,
}

/// Upload options for `Client::ingest_document`; unset fields use server defaults.
#[derive(Debug, Clone, Default)]
pub struct DocumentOptions {
    /// Default: the file name without extension.
    pub id: Option<String>,
    pub namespace: Option<String>,
    pub metadata: Map<String, Value>,
    /// Chunk length in characters (server default 1000).
    pub chunk_size: Option<usize>,
    /// Chunk overlap in characters (server default 150).
    pub chunk_overlap: Option<usize>,
    /// Content-Type of the file part.
    pub content_type: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct DocumentResult {
    pub document_id: String,
    #[serde(default)]
    pub title: String,
    #[serde(default)]
    pub format: String,
    pub chunks: usize,
    #[serde(default)]
    pub characters: usize,
    #[serde(default)]
    pub replaced_chunks_removed: usize,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct SearchResult {
    pub id: String,
    pub score: f32,
    #[serde(default)]
    pub metadata: Map<String, Value>,
    #[serde(default)]
    pub payload: Map<String, Value>,
    #[serde(default)]
    pub namespace: Option<String>,
    /// Marked snippets, set when `SearchOptions::highlight` was requested.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub highlights: Vec<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ShardFailure {
    pub shard_id: u32,
    #[serde(default)]
    pub node_id: String,
    pub error: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct DistributedSearchResponse {
    pub results: Vec<SearchResult>,
    pub partial: bool,
    #[serde(default)]
    pub failures: Vec<ShardFailure>,
    pub metadata_epoch: u64,
    pub authoritative_placement: bool,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub facets: BTreeMap<String, Vec<FacetBucket>>,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub aggregations: BTreeMap<String, AggregationResult>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ShardWriteOutcome {
    pub shard_id: u32,
    #[serde(default)]
    pub node_id: String,
    pub status: String,
    #[serde(default)]
    pub error: String,
    #[serde(default)]
    pub replicas_acknowledged: usize,
    #[serde(default)]
    pub replication_factor: usize,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct DistributedWriteResponse {
    pub outcomes: Vec<ShardWriteOutcome>,
    pub partial: bool,
    pub metadata_epoch: u64,
    pub authoritative_placement: bool,
}

#[derive(Default, Deserialize)]
struct ApiErrorBody {
    #[serde(default)]
    code: String,
    #[serde(default)]
    message: String,
}

fn serialize<T: Serialize + ?Sized>(value: &T) -> Result<Vec<u8>, Error> {
    serde_json::to_vec(value).map_err(json_error)
}

fn json_error(error: serde_json::Error) -> Error {
    Error::InvalidInput(error.to_string())
}

fn search_body(options: &SearchOptions, distributed: bool) -> Result<Vec<u8>, Error> {
    if options.top_k == 0 {
        return Err(Error::InvalidInput("top_k must be positive".into()));
    }
    let text = options
        .text
        .as_ref()
        .filter(|value| !value.trim().is_empty());
    if let Some(alpha) = options.alpha {
        if !(0.0..=1.0).contains(&alpha) {
            return Err(Error::InvalidInput("alpha must be between 0 and 1".into()));
        }
    }
    let fusion = options.fusion.as_ref().filter(|value| !value.is_empty());
    if let Some(value) = fusion {
        if value != "weighted" && value != "rrf" {
            return Err(Error::InvalidInput(
                "fusion must be \"weighted\" or \"rrf\"".into(),
            ));
        }
    }
    let mut body = Map::new();
    // Text-only and sparse-only searches omit the vector; otherwise it is always sent.
    if !options.vector.is_empty() || (text.is_none() && options.sparse_vector.is_empty()) {
        body.insert(
            "vector".into(),
            serde_json::to_value(&options.vector).map_err(json_error)?,
        );
    }
    body.insert("top_k".into(), Value::from(options.top_k));
    if let Some(namespace) = options.namespace.as_ref().filter(|value| !value.is_empty()) {
        body.insert("namespace".into(), Value::String(namespace.clone()));
    }
    if let Some(filter) = &options.filter {
        body.insert("filter".into(), filter.clone());
    }
    if let Some(text) = text {
        body.insert("text".into(), Value::String(text.clone()));
    }
    if !options.sparse_vector.is_empty() {
        body.insert(
            "sparse_vector".into(),
            serde_json::to_value(&options.sparse_vector).map_err(json_error)?,
        );
    }
    if let Some(alpha) = options.alpha {
        body.insert("alpha".into(), Value::from(alpha));
    }
    if let Some(fusion) = fusion {
        body.insert("fusion".into(), Value::String(fusion.clone()));
    }
    if let Some(syntax) = options.syntax.as_ref().filter(|value| !value.is_empty()) {
        body.insert("syntax".into(), Value::String(syntax.clone()));
    }
    if let Some(highlight) = &options.highlight {
        body.insert(
            "highlight".into(),
            serde_json::to_value(highlight).map_err(json_error)?,
        );
    }
    if !options.facets.is_empty() {
        body.insert(
            "facets".into(),
            serde_json::to_value(&options.facets).map_err(json_error)?,
        );
    }
    if !options.aggregations.is_empty() {
        body.insert(
            "aggregations".into(),
            serde_json::to_value(&options.aggregations).map_err(json_error)?,
        );
    }
    if !options.sort.is_empty() {
        body.insert(
            "sort".into(),
            serde_json::to_value(&options.sort).map_err(json_error)?,
        );
    }
    if let Some(candidates) = options.sort_candidates {
        body.insert("sort_candidates".into(), Value::from(candidates));
    }
    if distributed && options.allow_partial {
        body.insert("allow_partial".into(), Value::Bool(true));
    }
    serialize(&body)
}

const MULTIPART_BOUNDARY: &str = "gideondb-rust-boundary-7d3f9a1c5e";

fn multipart_body(
    filename: &str,
    content: &[u8],
    options: &DocumentOptions,
) -> Result<(Vec<u8>, String), Error> {
    let base = filename.rsplit(['/', '\\']).next().unwrap_or_default();
    if base.is_empty() {
        return Err(Error::InvalidInput("filename must not be empty".into()));
    }
    if content
        .windows(MULTIPART_BOUNDARY.len())
        .any(|window| window == MULTIPART_BOUNDARY.as_bytes())
    {
        return Err(Error::InvalidInput(
            "document content contains the multipart boundary".into(),
        ));
    }
    let quote = |value: &str| -> Result<String, Error> {
        if value.contains(['\r', '\n']) {
            return Err(Error::InvalidInput(
                "multipart values must not contain line breaks".into(),
            ));
        }
        Ok(value.replace('\\', "\\\\").replace('"', "\\\""))
    };
    let mut fields: Vec<(&str, String)> = Vec::new();
    if let Some(id) = options.id.as_ref().filter(|value| !value.is_empty()) {
        fields.push(("id", id.clone()));
    }
    if let Some(namespace) = options.namespace.as_ref().filter(|value| !value.is_empty()) {
        fields.push(("namespace", namespace.clone()));
    }
    if !options.metadata.is_empty() {
        fields.push((
            "metadata",
            serde_json::to_string(&options.metadata).map_err(json_error)?,
        ));
    }
    if let Some(size) = options.chunk_size {
        fields.push(("chunk_size", size.to_string()));
    }
    if let Some(overlap) = options.chunk_overlap {
        fields.push(("chunk_overlap", overlap.to_string()));
    }
    let mut body = Vec::with_capacity(content.len() + 512);
    for (name, value) in fields {
        body.extend_from_slice(
            format!(
                "--{MULTIPART_BOUNDARY}\r\nContent-Disposition: form-data; name=\"{name}\"\r\n\r\n{value}\r\n"
            )
            .as_bytes(),
        );
    }
    body.extend_from_slice(
        format!(
            "--{MULTIPART_BOUNDARY}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"{}\"\r\n",
            quote(base)?
        )
        .as_bytes(),
    );
    if let Some(content_type) = options
        .content_type
        .as_deref()
        .filter(|value| !value.is_empty())
    {
        body.extend_from_slice(format!("Content-Type: {}\r\n", quote(content_type)?).as_bytes());
    }
    body.extend_from_slice(b"\r\n");
    body.extend_from_slice(content);
    body.extend_from_slice(format!("\r\n--{MULTIPART_BOUNDARY}--\r\n").as_bytes());
    Ok((
        body,
        format!("multipart/form-data; boundary={MULTIPART_BOUNDARY}"),
    ))
}

fn validate_origin(value: &str) -> Result<String, Error> {
    let value = value.trim();
    let uri: ureq::http::Uri = value
        .parse()
        .map_err(|_| Error::InvalidInput("base URL must be an HTTP(S) origin".into()))?;
    let valid = matches!(uri.scheme_str(), Some("http" | "https"))
        && uri.authority().is_some()
        && uri
            .authority()
            .is_some_and(|authority| !authority.as_str().contains('@'))
        && matches!(uri.path(), "" | "/")
        && uri.query().is_none();
    if !valid {
        return Err(Error::InvalidInput(
            "base URL must be an HTTP(S) origin".into(),
        ));
    }
    Ok(value.trim_end_matches('/').to_string())
}

fn encode(value: &str) -> String {
    const HEX: &[u8; 16] = b"0123456789ABCDEF";
    let mut out = String::new();
    for byte in value.bytes() {
        if byte.is_ascii_alphanumeric() || matches!(byte, b'-' | b'.' | b'_' | b'~') {
            out.push(byte as char);
        } else {
            out.push('%');
            out.push(HEX[(byte >> 4) as usize] as char);
            out.push(HEX[(byte & 15) as usize] as char);
        }
    }
    out
}

fn collection_path(name: &str) -> String {
    format!("/v1/collections/{}", encode(name))
}
fn cluster_collection_path(name: &str) -> String {
    format!("/v1/cluster/collections/{}", encode(name))
}
fn record_path(collection: &str, id: &str, namespace: Option<&str>) -> String {
    let mut path = format!("{}/vectors/{}", collection_path(collection), encode(id));
    if let Some(value) = namespace.filter(|value| !value.is_empty()) {
        path.push_str("?namespace=");
        path.push_str(&encode(value));
    }
    path
}
fn query(path: &mut String, name: &str, value: Option<&str>) {
    if let Some(value) = value.filter(|value| !value.is_empty()) {
        path.push('&');
        path.push_str(name);
        path.push('=');
        path.push_str(&encode(value));
    }
}

struct UreqTransport {
    agent: ureq::Agent,
}

impl UreqTransport {
    fn new(timeout: Duration) -> Self {
        let config = ureq::Agent::config_builder()
            .timeout_global(Some(timeout))
            .http_status_as_error(false)
            .build();
        Self {
            agent: ureq::Agent::new_with_config(config),
        }
    }
}

impl Transport for UreqTransport {
    fn send(&self, request: Request) -> Result<Response, Box<dyn StdError + Send + Sync>> {
        let mut builder = ureq::http::Request::builder()
            .method(request.method)
            .uri(&request.url)
            .header("Accept", "application/json")
            .header("X-GideonDB-Client", "rust/dev");
        if !request.api_key.is_empty() {
            builder = builder.header("Authorization", format!("Bearer {}", request.api_key));
        }
        let mut response = if let Some(body) = request.body {
            self.agent.run(
                builder
                    .header(
                        "Content-Type",
                        request
                            .content_type
                            .as_deref()
                            .unwrap_or("application/json"),
                    )
                    .body(body)?,
            )?
        } else {
            self.agent.run(builder.body(())?)?
        };
        let status = response.status().as_u16();
        let body = response
            .body_mut()
            .with_config()
            .limit((MAX_RESPONSE_BYTES + 1) as u64)
            .read_to_vec()?;
        Ok(Response { status, body })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::{collections::VecDeque, sync::Mutex};

    #[derive(Default)]
    struct QueueTransport {
        responses: Mutex<VecDeque<Response>>,
        requests: Mutex<Vec<Request>>,
    }
    impl QueueTransport {
        fn push(&self, status: u16, body: &str) {
            self.responses.lock().unwrap().push_back(Response {
                status,
                body: body.as_bytes().to_vec(),
            });
        }
    }
    impl Transport for QueueTransport {
        fn send(&self, request: Request) -> Result<Response, Box<dyn StdError + Send + Sync>> {
            self.requests.lock().unwrap().push(request);
            Ok(self.responses.lock().unwrap().pop_front().unwrap())
        }
    }

    fn client(transport: Arc<QueueTransport>) -> Client {
        Client::with_transport(
            "https://db.example/",
            "secret",
            Duration::from_secs(2),
            transport,
        )
        .unwrap()
    }

    #[test]
    fn lifecycle_scroll_and_encoding() {
        let transport = Arc::new(QueueTransport::default());
        transport.push(200, r#"{"status":"ok"}"#);
        transport.push(
            200,
            r#"{"records":[{"id":"one"}],"next_cursor":"next/value","vectors_included":false}"#,
        );
        transport.push(200, r#"{"records":[],"next_cursor":"","vectors_included":false,"metadata_epoch":7,"authoritative_placement":true}"#);
        transport.push(200, r#"{"id":"a/b","vector":[1.0,0.0]}"#);
        let client = client(transport.clone());
        client.health().unwrap();
        let options = ScrollOptions {
            namespace: Some("tenant one".into()),
            limit: 25,
            cursor: Some("prior/value".into()),
            include_vector: true,
        };
        assert_eq!(
            client.scroll("docs", &options).unwrap().next_cursor,
            "next/value"
        );
        assert_eq!(
            client
                .distributed_scroll("docs", &ScrollOptions::default())
                .unwrap()
                .metadata_epoch,
            7
        );
        assert_eq!(
            client.get("docs", "a/b", Some("tenant one")).unwrap().id,
            "a/b"
        );
        let requests = transport.requests.lock().unwrap();
        assert!(requests[1].url.contains("cursor=prior%2Fvalue"));
        assert!(
            requests[2]
                .url
                .contains("/v1/cluster/collections/docs/vectors")
        );
        assert!(requests[3].url.ends_with("a%2Fb?namespace=tenant%20one"));
        assert_eq!(requests[3].api_key, "secret");
    }

    #[test]
    fn distributed_calls_and_typed_errors() {
        let transport = Arc::new(QueueTransport::default());
        transport.push(207, r#"{"outcomes":[{"shard_id":0,"status":"unknown"}],"partial":true,"metadata_epoch":2,"authoritative_placement":true}"#);
        transport.push(404, r#"{"code":"not_found","message":"missing"}"#);
        let client = client(transport);
        let record = Record {
            id: "one".into(),
            vector: vec![1.0, 0.0],
            ..Default::default()
        };
        assert!(
            client
                .distributed_batch_upsert("docs", &[record], Some("all"))
                .unwrap()
                .partial
        );
        match client.describe_collection("missing").unwrap_err() {
            Error::Api {
                status: 404, code, ..
            } => assert_eq!(code, "not_found"),
            error => panic!("unexpected error: {error}"),
        }
    }

    #[test]
    fn hybrid_search_text_records_and_quantization() {
        let transport = Arc::new(QueueTransport::default());
        transport.push(
            201,
            r#"{"name":"docs","dimension":2,"metric":"cosine","shard_count":1}"#,
        );
        transport.push(200, r#"{"id":"one"}"#);
        transport.push(200, r#"{"results":[{"id":"one","score":0.5}]}"#);
        transport.push(200, r#"{"results":[]}"#);
        let client = client(transport.clone());
        client
            .create_collection(&CollectionConfig {
                name: "docs".into(),
                dimension: 2,
                metric: "cosine".into(),
                shard_count: 1,
                vectorizer: Some("mini".into()),
                index: Some(IndexConfig {
                    kind: "flat".into(),
                    quantization: Some("binary".into()),
                    oversampling: Some(4),
                    ..Default::default()
                }),
                ..Default::default()
            })
            .unwrap();
        client
            .upsert(
                "docs",
                &Record {
                    id: "one".into(),
                    text: Some("hello world".into()),
                    sparse_vector: BTreeMap::from([("hello".into(), 1.5)]),
                    ..Default::default()
                },
            )
            .unwrap();
        let hits = client
            .search(
                "docs",
                &SearchOptions {
                    top_k: 3,
                    text: Some("hello".into()),
                    sparse_vector: BTreeMap::from([("hello".into(), 2.0)]),
                    alpha: Some(0.25),
                    fusion: Some("rrf".into()),
                    ..Default::default()
                },
            )
            .unwrap();
        assert_eq!(hits[0].id, "one");
        client
            .search(
                "docs",
                &SearchOptions {
                    vector: vec![1.0, 0.0],
                    top_k: 1,
                    text: Some("hello".into()),
                    ..Default::default()
                },
            )
            .unwrap();
        let requests = transport.requests.lock().unwrap();
        let body = |index: usize| -> Value {
            serde_json::from_slice(requests[index].body.as_ref().unwrap()).unwrap()
        };
        assert_eq!(
            body(0),
            json!({"name":"docs","dimension":2,"metric":"cosine","shard_count":1,"vectorizer":"mini",
                   "index":{"type":"flat","quantization":"binary","oversampling":4}})
        );
        assert_eq!(
            body(1),
            json!({"id":"one","text":"hello world","sparse_vector":{"hello":1.5}})
        );
        assert_eq!(
            body(2),
            json!({"top_k":3,"text":"hello","sparse_vector":{"hello":2.0},"alpha":0.25,"fusion":"rrf"})
        );
        assert_eq!(
            body(3),
            json!({"vector":[1.0,0.0],"top_k":1,"text":"hello"})
        );
        drop(requests);
        let invalid_alpha = SearchOptions {
            top_k: 1,
            text: Some("x".into()),
            alpha: Some(1.5),
            ..Default::default()
        };
        assert!(matches!(
            client.search("docs", &invalid_alpha),
            Err(Error::InvalidInput(_))
        ));
        let invalid_fusion = SearchOptions {
            top_k: 1,
            text: Some("x".into()),
            fusion: Some("max".into()),
            ..Default::default()
        };
        assert!(matches!(
            client.search("docs", &invalid_fusion),
            Err(Error::InvalidInput(_))
        ));
    }

    #[test]
    fn validates_inputs_and_bounds_responses() {
        assert!(Client::new("ftp://db.example").is_err());
        assert!(Client::new("https://db.example/path").is_err());
        let transport = Arc::new(QueueTransport::default());
        transport.responses.lock().unwrap().push_back(Response {
            status: 200,
            body: vec![0; MAX_RESPONSE_BYTES + 1],
        });
        assert!(matches!(
            client(transport).health(),
            Err(Error::ResponseTooLarge)
        ));
    }
    #[test]
    fn text_analyzer_synonyms_and_search_features() {
        let transport = Arc::new(QueueTransport::default());
        transport.push(201, r#"{"name":"docs","dimension":2,"metric":"cosine","shard_count":1,"text_analyzer":"english","synonyms":[["car","auto"]]}"#);
        transport.push(200, r#"{"results":[{"id":"one","score":0.9,"highlights":["a <b>car</b>"]}],
            "facets":{"color":[{"value":"red","count":2},{"value":true,"count":1}]},
            "aggregations":{"price":{"count":3,"min":1.0,"max":9.0,"sum":12.0,"avg":4.0},
                            "bands":{"buckets":[{"key":"cheap","to":5.0,"count":2},{"key":"dear","from":5.0,"count":1}]}}}"#);
        transport.push(200, r#"{"results":[{"id":"one","score":0.5}],"partial":false,"failures":[],"metadata_epoch":3,"authoritative_placement":true,"facets":{"color":[{"value":"red","count":4}]},"aggregations":{"n":{"count":4}}}"#);
        transport.push(200, r#"{"results":[{"id":"two","score":0.1}]}"#);
        let client = client(transport.clone());
        let created = client
            .create_collection(&CollectionConfig {
                name: "docs".into(),
                dimension: 2,
                metric: "cosine".into(),
                shard_count: 1,
                text_analyzer: Some("english".into()),
                synonyms: vec![vec!["car".into(), "auto".into()]],
                ..Default::default()
            })
            .unwrap();
        assert_eq!(
            created.synonyms,
            vec![vec!["car".to_string(), "auto".to_string()]]
        );
        let options = SearchOptions {
            top_k: 2,
            text: Some("\"red car\" -used".into()),
            syntax: Some("simple".into()),
            highlight: Some(HighlightOptions {
                pre_tag: Some("<b>".into()),
                post_tag: Some("</b>".into()),
                fragment_size: Some(80),
                fragments: Some(2),
                ..Default::default()
            }),
            facets: vec![FacetRequest {
                field: "color".into(),
                size: Some(5),
            }],
            aggregations: BTreeMap::from([
                (
                    "price".into(),
                    Aggregation {
                        kind: "stats".into(),
                        field: "price".into(),
                        ..Default::default()
                    },
                ),
                (
                    "bands".into(),
                    Aggregation {
                        kind: "range".into(),
                        field: "price".into(),
                        ranges: vec![
                            AggregationRange {
                                key: Some("cheap".into()),
                                to: Some(5.0),
                                ..Default::default()
                            },
                            AggregationRange {
                                key: Some("dear".into()),
                                from: Some(5.0),
                                ..Default::default()
                            },
                        ],
                        ..Default::default()
                    },
                ),
            ]),
            sort: vec![
                SortField {
                    field: "price".into(),
                    order: Some("desc".into()),
                },
                SortField {
                    field: "_score".into(),
                    order: None,
                },
            ],
            sort_candidates: Some(200),
            ..Default::default()
        };
        let response = client.search_response("docs", &options).unwrap();
        assert_eq!(response.results[0].highlights, vec!["a <b>car</b>"]);
        assert_eq!(response.facets["color"][1].value, json!(true));
        assert_eq!(response.facets["color"][0].count, 2);
        assert_eq!(response.aggregations["price"].avg, Some(4.0));
        assert_eq!(
            response.aggregations["bands"].buckets[0].key,
            json!("cheap")
        );
        assert_eq!(response.aggregations["bands"].buckets[1].from, Some(5.0));
        let distributed = client
            .distributed_search(
                "docs",
                &SearchOptions {
                    allow_partial: true,
                    ..options.clone()
                },
            )
            .unwrap();
        assert_eq!(distributed.facets["color"][0].count, 4);
        assert_eq!(distributed.aggregations["n"].count, Some(4));
        let plain = client
            .search(
                "docs",
                &SearchOptions {
                    top_k: 1,
                    text: Some("car".into()),
                    highlight: Some(HighlightOptions {
                        raw: true,
                        ..Default::default()
                    }),
                    ..Default::default()
                },
            )
            .unwrap();
        assert!(plain[0].highlights.is_empty());
        let requests = transport.requests.lock().unwrap();
        let body = |index: usize| -> Value {
            serde_json::from_slice(requests[index].body.as_ref().unwrap()).unwrap()
        };
        assert_eq!(
            body(0),
            json!({"name":"docs","dimension":2,"metric":"cosine","shard_count":1,
                   "text_analyzer":"english","synonyms":[["car","auto"]]})
        );
        let expected = json!({
            "top_k":2,"text":"\"red car\" -used","syntax":"simple",
            "highlight":{"pre_tag":"<b>","post_tag":"</b>","fragment_size":80,"fragments":2},
            "facets":[{"field":"color","size":5}],
            "aggregations":{
                "bands":{"type":"range","field":"price","ranges":[{"key":"cheap","to":5.0},{"key":"dear","from":5.0}]},
                "price":{"type":"stats","field":"price"}},
            "sort":[{"field":"price","order":"desc"},{"field":"_score"}],
            "sort_candidates":200});
        assert_eq!(body(1), expected);
        assert!(requests[1].url.ends_with("/v1/collections/docs/search"));
        let mut distributed_expected = expected.clone();
        distributed_expected["allow_partial"] = json!(true);
        assert_eq!(body(2), distributed_expected);
        assert!(
            requests[2]
                .url
                .ends_with("/v1/cluster/collections/docs/search")
        );
        assert_eq!(
            body(3),
            json!({"top_k":1,"text":"car","highlight":{"raw":true}})
        );
        assert!(
            requests
                .iter()
                .all(|request| request.content_type.is_none())
        );
    }

    #[test]
    fn suggest_encodes_query_and_parses_completions() {
        let transport = Arc::new(QueueTransport::default());
        transport.push(200, r#"{"completions":[{"text":"vector datab","word":"database","count":7}],"did_you_mean":"vector database"}"#);
        transport.push(200, r#"{"completions":[]}"#);
        let client = client(transport.clone());
        let suggestions = client.suggest("my docs", "vectr datab&x", 3).unwrap();
        assert_eq!(suggestions.completions[0].word, "database");
        assert_eq!(suggestions.completions[0].count, 7);
        assert_eq!(suggestions.did_you_mean.as_deref(), Some("vector database"));
        assert_eq!(client.suggest("docs", "ve", 0).unwrap().did_you_mean, None);
        assert!(matches!(
            client.suggest("docs", "", 1),
            Err(Error::InvalidInput(_))
        ));
        let requests = transport.requests.lock().unwrap();
        assert_eq!(requests[0].method, "GET");
        assert!(
            requests[0]
                .url
                .ends_with("/v1/collections/my%20docs/suggest?text=vectr%20datab%26x&size=3")
        );
        assert!(
            requests[1]
                .url
                .ends_with("/v1/collections/docs/suggest?text=ve")
        );
        assert!(requests[0].body.is_none());
    }

    #[test]
    fn ingest_document_builds_multipart_body() {
        let transport = Arc::new(QueueTransport::default());
        transport.push(201, r#"{"document_id":"guide","title":"Guide","format":"markdown","chunks":3,"characters":2400,"replaced_chunks_removed":1}"#);
        transport.push(201, r#"{"document_id":"notes","chunks":1}"#);
        let client = client(transport.clone());
        let result = client
            .ingest_document(
                "docs",
                "dir/guide.md",
                b"# Guide\nbody",
                &DocumentOptions {
                    id: Some("guide".into()),
                    namespace: Some("team".into()),
                    metadata: Map::from_iter([("lang".to_string(), json!("en"))]),
                    chunk_size: Some(800),
                    chunk_overlap: Some(100),
                    content_type: Some("text/markdown".into()),
                },
            )
            .unwrap();
        assert_eq!(
            result,
            DocumentResult {
                document_id: "guide".into(),
                title: "Guide".into(),
                format: "markdown".into(),
                chunks: 3,
                characters: 2400,
                replaced_chunks_removed: 1,
            }
        );
        client
            .ingest_document("docs", "notes.txt", b"hi", &DocumentOptions::default())
            .unwrap();
        assert!(matches!(
            client.ingest_document("docs", "dir/", b"x", &DocumentOptions::default()),
            Err(Error::InvalidInput(_))
        ));
        let requests = transport.requests.lock().unwrap();
        let b = MULTIPART_BOUNDARY;
        assert_eq!(requests[0].method, "POST");
        assert!(requests[0].url.ends_with("/v1/collections/docs/documents"));
        assert_eq!(
            requests[0].content_type.as_deref(),
            Some(format!("multipart/form-data; boundary={b}").as_str())
        );
        let expected = format!(
            "--{b}\r\nContent-Disposition: form-data; name=\"id\"\r\n\r\nguide\r\n\
             --{b}\r\nContent-Disposition: form-data; name=\"namespace\"\r\n\r\nteam\r\n\
             --{b}\r\nContent-Disposition: form-data; name=\"metadata\"\r\n\r\n{{\"lang\":\"en\"}}\r\n\
             --{b}\r\nContent-Disposition: form-data; name=\"chunk_size\"\r\n\r\n800\r\n\
             --{b}\r\nContent-Disposition: form-data; name=\"chunk_overlap\"\r\n\r\n100\r\n\
             --{b}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"guide.md\"\r\n\
             Content-Type: text/markdown\r\n\r\n# Guide\nbody\r\n--{b}--\r\n"
        );
        assert_eq!(
            String::from_utf8(requests[0].body.clone().unwrap()).unwrap(),
            expected
        );
        let minimal = format!(
            "--{b}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"notes.txt\"\r\n\r\nhi\r\n--{b}--\r\n"
        );
        assert_eq!(
            String::from_utf8(requests[1].body.clone().unwrap()).unwrap(),
            minimal
        );
    }
}
