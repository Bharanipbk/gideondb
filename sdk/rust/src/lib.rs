//! Synchronous Rust client for GideonDB's experimental REST API.

use serde::{Deserialize, Serialize, de::DeserializeOwned};
use serde_json::{Map, Value, json};
use std::{error::Error as StdError, fmt, sync::Arc, time::Duration};

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
        let response = self
            .transport
            .send(Request {
                method,
                url: format!("{}{}", self.origin, path),
                body,
                api_key: self.api_key.clone(),
                timeout: self.timeout,
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

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct CollectionConfig {
    pub name: String,
    pub dimension: usize,
    pub metric: String,
    pub shard_count: usize,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub index: Option<IndexConfig>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct IndexConfig {
    #[serde(rename = "type")]
    pub kind: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub m: Option<usize>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub ef_construction: Option<usize>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub ef_search: Option<usize>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct CollectionDescription {
    pub config: CollectionConfig,
    pub vector_count: usize,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Record {
    pub id: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub vector: Vec<f32>,
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

#[derive(Debug, Clone)]
pub struct SearchOptions {
    pub vector: Vec<f32>,
    pub top_k: usize,
    pub namespace: Option<String>,
    pub filter: Option<Value>,
    pub allow_partial: bool,
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
    let mut body = Map::new();
    body.insert(
        "vector".into(),
        serde_json::to_value(&options.vector).map_err(json_error)?,
    );
    body.insert("top_k".into(), Value::from(options.top_k));
    if let Some(namespace) = options.namespace.as_ref().filter(|value| !value.is_empty()) {
        body.insert("namespace".into(), Value::String(namespace.clone()));
    }
    if let Some(filter) = &options.filter {
        body.insert("filter".into(), filter.clone());
    }
    if distributed && options.allow_partial {
        body.insert("allow_partial".into(), Value::Bool(true));
    }
    serialize(&body)
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
                    .header("Content-Type", "application/json")
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
            metadata: Map::new(),
            payload: Map::new(),
            timestamp: None,
            version: None,
            namespace: None,
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
}
