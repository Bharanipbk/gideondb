package io.gideondb.client;

import java.io.IOException;
import java.io.InputStream;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.time.Duration;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;

/** Dependency-free Java 17 client for GideonDB's versioned HTTP API. */
public final class GideonDBClient {
    private static final int MAX_RESPONSE_BYTES = 16 << 20;
    private final URI origin;
    private final String apiKey;
    private final Duration timeout;
    private final Transport transport;

    public GideonDBClient(String baseUrl) {
        this(baseUrl, "", Duration.ofSeconds(30));
    }

    public GideonDBClient(String baseUrl, String apiKey, Duration timeout) {
        this(baseUrl, apiKey, timeout, defaultTransport(timeout));
    }

    /** Advanced constructor for custom transports, testing, and controlled runtimes. */
    public GideonDBClient(String baseUrl, String apiKey, Duration timeout, Transport transport) {
        URI parsed;
        try { parsed = URI.create(Objects.requireNonNull(baseUrl).trim()); }
        catch (RuntimeException e) { throw new IllegalArgumentException("baseUrl must be an HTTP(S) origin", e); }
        if (!("http".equals(parsed.getScheme()) || "https".equals(parsed.getScheme()))
                || parsed.getHost() == null || parsed.getUserInfo() != null || parsed.getQuery() != null
                || parsed.getFragment() != null || !(parsed.getPath().isEmpty() || "/".equals(parsed.getPath()))) {
            throw new IllegalArgumentException("baseUrl must be an HTTP(S) origin");
        }
        if (timeout == null || timeout.isZero() || timeout.isNegative()) {
            throw new IllegalArgumentException("timeout must be positive");
        }
        this.origin = URI.create(parsed.getScheme() + "://" + parsed.getAuthority());
        this.apiKey = apiKey == null ? "" : apiKey;
        this.timeout = timeout;
        this.transport = Objects.requireNonNull(transport);
    }

    public void health() { request("GET", "/v1/health", null); }
    public void ready() { request("GET", "/v1/ready", null); }

    public List<Map<String, Object>> listCollections() {
        return objectList(requestObject("GET", "/v1/collections", null).get("collections"));
    }

    public Map<String, Object> createCollection(Map<String, Object> config) {
        return requestObject("POST", "/v1/collections", config);
    }

    public Map<String, Object> describeCollection(String name) {
        return requestObject("GET", collectionPath(name), null);
    }

    public void deleteCollection(String name) { request("DELETE", collectionPath(name), null); }

    public Map<String, Object> upsert(String collection, Map<String, Object> record) {
        return requestObject("POST", collectionPath(collection) + "/vectors", record);
    }

    public List<Map<String, Object>> batchUpsert(String collection, List<Map<String, Object>> records) {
        return objectList(requestObject("POST", collectionPath(collection) + "/vectors/batch",
                Map.of("records", records)).get("records"));
    }

    public Map<String, Object> get(String collection, String id) { return get(collection, id, ""); }

    public Map<String, Object> get(String collection, String id, String namespace) {
        return requestObject("GET", recordPath(collection, id, namespace), null);
    }

    public Map<String, Object> scroll(String collection, String namespace, int limit,
                                      String cursor, boolean includeVector) {
        return scrollPath(collectionPath(collection), namespace, limit, cursor, includeVector);
    }

    public Map<String, Object> distributedScroll(String collection, String namespace, int limit,
                                                  String cursor, boolean includeVector) {
        return scrollPath(clusterCollectionPath(collection), namespace, limit, cursor, includeVector);
    }

    private Map<String, Object> scrollPath(String basePath, String namespace, int limit,
                                           String cursor, boolean includeVector) {
        if (limit < 1 || limit > 200) throw new IllegalArgumentException("limit must be between 1 and 200");
        StringBuilder path = new StringBuilder(basePath).append("/vectors?limit=").append(limit);
        if (namespace != null && !namespace.isBlank()) path.append("&namespace=").append(encode(namespace));
        if (cursor != null && !cursor.isBlank()) path.append("&cursor=").append(encode(cursor));
        if (includeVector) path.append("&include_vector=true");
        return requestObject("GET", path.toString(), null);
    }

    public void delete(String collection, String id, String namespace) {
        request("DELETE", recordPath(collection, id, namespace), null);
    }

    public List<Map<String, Object>> search(String collection, List<? extends Number> vector, int topK,
                                             Map<String, Object> filter, String namespace) {
        Map<String, Object> body = searchBody(vector, topK, filter, namespace);
        return objectList(requestObject("POST", collectionPath(collection) + "/search", body).get("results"));
    }

    public Map<String, Object> distributedSearch(String collection, List<? extends Number> vector, int topK,
                                                  Map<String, Object> filter, String namespace,
                                                  boolean allowPartial) {
        Map<String, Object> body = searchBody(vector, topK, filter, namespace);
        if (allowPartial) body.put("allow_partial", true);
        return requestObject("POST", clusterCollectionPath(collection) + "/search", body);
    }

    /** Dense, lexical (text), sparse, or hybrid search described by {@link SearchOptions}. */
    public List<Map<String, Object>> search(String collection, SearchOptions options) {
        return objectList(requestObject("POST", collectionPath(collection) + "/search",
                Objects.requireNonNull(options, "options").toBody()).get("results"));
    }

    public Map<String, Object> distributedSearch(String collection, SearchOptions options, boolean allowPartial) {
        Map<String, Object> body = Objects.requireNonNull(options, "options").toBody();
        if (allowPartial) body.put("allow_partial", true);
        return requestObject("POST", clusterCollectionPath(collection) + "/search", body);
    }

    /** Full local search response: results plus facets and aggregations when requested. */
    public Map<String, Object> searchResponse(String collection, SearchOptions options) {
        return requestObject("POST", collectionPath(collection) + "/search",
                Objects.requireNonNull(options, "options").toBody());
    }

    /** Facet buckets per field ({@code field -> [{value, count}]}); empty when none were requested. */
    public static Map<String, Object> facets(Map<String, Object> response) { return objectField(response, "facets"); }

    /** Aggregation results by name ({@code name -> {count, min, max, sum, avg, buckets}}). */
    public static Map<String, Object> aggregations(Map<String, Object> response) { return objectField(response, "aggregations"); }

    /** Completes the last word of text; returns {@code completions [{text, word, count}]} and optional {@code did_you_mean}. */
    public Map<String, Object> suggest(String collection, String text, int size) {
        if (text == null || text.isEmpty()) throw new IllegalArgumentException("text is required");
        StringBuilder path = new StringBuilder(collectionPath(collection)).append("/suggest?text=").append(encode(text));
        if (size > 0) path.append("&size=").append(size);
        return requestObject("GET", path.toString(), null);
    }

    /** Suggest completions as a list of {@code {text, word, count}} objects. */
    public static List<Map<String, Object>> completions(Map<String, Object> suggestions) {
        Object value = suggestions.get("completions");
        return value == null ? List.of() : objectList(value);
    }

    /**
     * Uploads a text, Markdown, HTML, PDF or DOCX file as multipart/form-data; the server extracts, chunks and
     * embeds it. Returns {@code document_id, title, format, chunks, characters, replaced_chunks_removed}.
     */
    public Map<String, Object> ingestDocument(String collection, String filename, byte[] content, DocumentOptions options) {
        Objects.requireNonNull(content, "content");
        DocumentOptions o = options == null ? new DocumentOptions() : options;
        String name = Objects.requireNonNull(filename, "filename").replace('\\', '/');
        name = name.substring(name.lastIndexOf('/') + 1);
        String boundary = "gideondb-" + java.util.UUID.randomUUID().toString().replace("-", "");
        java.io.ByteArrayOutputStream out = new java.io.ByteArrayOutputStream();
        Map<String, String> fields = new LinkedHashMap<>();
        if (o.id != null && !o.id.isEmpty()) fields.put("id", o.id);
        if (o.namespace != null && !o.namespace.isEmpty()) fields.put("namespace", o.namespace);
        if (o.metadata != null) fields.put("metadata", Json.stringify(o.metadata));
        if (o.chunkSize != 0) fields.put("chunk_size", Integer.toString(o.chunkSize));
        if (o.chunkOverlap != 0) fields.put("chunk_overlap", Integer.toString(o.chunkOverlap));
        for (Map.Entry<String, String> field : fields.entrySet()) {
            writeAscii(out, "--" + boundary + "\r\nContent-Disposition: form-data; name=\"" + field.getKey() + "\"\r\n\r\n");
            out.writeBytes(field.getValue().getBytes(java.nio.charset.StandardCharsets.UTF_8));
            writeAscii(out, "\r\n");
        }
        String quoted = name.replace("\\", "\\\\").replace("\"", "\\\"").replace("\r", "").replace("\n", "");
        StringBuilder header = new StringBuilder("--").append(boundary)
                .append("\r\nContent-Disposition: form-data; name=\"file\"; filename=\"").append(quoted).append("\"\r\n");
        if (o.contentType != null && !o.contentType.isBlank()) header.append("Content-Type: ").append(o.contentType).append("\r\n");
        else header.append("Content-Type: application/octet-stream\r\n");
        out.writeBytes(header.append("\r\n").toString().getBytes(java.nio.charset.StandardCharsets.UTF_8));
        out.writeBytes(content);
        writeAscii(out, "\r\n--" + boundary + "--\r\n");
        Object value = send(new Request("POST", origin.resolve(collectionPath(collection) + "/documents"), null,
                apiKey, timeout, out.toByteArray(), "multipart/form-data; boundary=" + boundary));
        if (!(value instanceof Map<?, ?>)) throw new TransportException("response must be a JSON object");
        return castObject(value);
    }

    private static void writeAscii(java.io.ByteArrayOutputStream out, String text) {
        out.writeBytes(text.getBytes(java.nio.charset.StandardCharsets.UTF_8));
    }

    public Map<String, Object> distributedBatchUpsert(String collection,
            List<Map<String, Object>> records, String acknowledgement) {
        Map<String, Object> body = new LinkedHashMap<>();
        body.put("records", records);
        if (acknowledgement != null && !acknowledgement.isBlank()) body.put("acknowledgement", acknowledgement);
        return requestObject("POST", clusterCollectionPath(collection) + "/vectors/batch", body);
    }

    private Object request(String method, String path, Object body) {
        return send(new Request(method, origin.resolve(path), body == null ? null : Json.stringify(body),
                apiKey, timeout));
    }

    private Object send(Request outgoing) {
        Response response;
        try { response = transport.send(outgoing); }
        catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new TransportException("request interrupted", e);
        } catch (IOException | RuntimeException e) { throw new TransportException("request failed", e); }
        byte[] bytes = response.body() == null ? new byte[0] : response.body();
        if (bytes.length > MAX_RESPONSE_BYTES) throw new TransportException("response exceeds 16 MiB");
        String text = new String(bytes, java.nio.charset.StandardCharsets.UTF_8);
        Object payload = null;
        if (!text.isBlank()) {
            try { payload = Json.parse(text); }
            catch (IllegalArgumentException e) {
                if (response.statusCode() >= 200 && response.statusCode() < 300)
                    throw new TransportException("response is not valid JSON", e);
            }
        }
        if (response.statusCode() < 200 || response.statusCode() >= 300) {
            Map<String, Object> detail = payload instanceof Map<?, ?> ? castObject(payload) : Map.of();
            throw new ApiException(response.statusCode(), stringValue(detail.get("code")), stringValue(detail.get("message")));
        }
        return payload;
    }

    private Map<String, Object> requestObject(String method, String path, Object body) {
        Object value = request(method, path, body);
        if (!(value instanceof Map<?, ?>)) throw new TransportException("response must be a JSON object");
        return castObject(value);
    }

    private static Map<String, Object> searchBody(List<? extends Number> vector, int topK,
            Map<String, Object> filter, String namespace) {
        if (topK <= 0) throw new IllegalArgumentException("topK must be positive");
        Map<String, Object> body = new LinkedHashMap<>();
        body.put("vector", vector); body.put("top_k", topK);
        if (filter != null) body.put("filter", filter);
        if (namespace != null && !namespace.isBlank()) body.put("namespace", namespace);
        return body;
    }

    /**
     * Search request options. At least one of vector, text, or sparseVector is required. When text is
     * given without a vector, no vector is sent (the server embeds it if the collection has a vectorizer).
     */
    public static final class SearchOptions {
        private List<? extends Number> vector;
        private int topK = 10;
        private Map<String, Object> filter;
        private String namespace;
        private String text;
        private Map<String, ? extends Number> sparseVector;
        private Double alpha;
        private String fusion;
        private String syntax;
        private HighlightOptions highlight;
        private final List<Map<String, Object>> facets = new ArrayList<>();
        private final Map<String, Object> aggregations = new LinkedHashMap<>();
        private final List<Map<String, Object>> sort = new ArrayList<>();
        private Integer sortCandidates;

        public SearchOptions vector(List<? extends Number> value) { this.vector = value; return this; }
        public SearchOptions topK(int value) { this.topK = value; return this; }
        public SearchOptions filter(Map<String, Object> value) { this.filter = value; return this; }
        public SearchOptions namespace(String value) { this.namespace = value; return this; }
        public SearchOptions text(String value) { this.text = value; return this; }
        public SearchOptions sparseVector(Map<String, ? extends Number> value) { this.sparseVector = value; return this; }
        /** Dense weight from 0 to 1 for hybrid weighted fusion (server default 0.5). */
        public SearchOptions alpha(double value) { this.alpha = value; return this; }
        /** "weighted" (server default) or "rrf". */
        public SearchOptions fusion(String value) { this.fusion = value; return this; }
        /** "simple" enables phrases, +must, -must_not, fuzzy~, prefix* and ^boost in text. */
        public SearchOptions syntax(String value) { this.syntax = value; return this; }
        /** Returns per-result {@code highlights} snippets. */
        public SearchOptions highlight(HighlightOptions value) { this.highlight = value; return this; }
        /** Counts metadata values of field over every matching record; size 0 uses the server default. */
        public SearchOptions facet(String field, int size) {
            Map<String, Object> facet = new LinkedHashMap<>();
            facet.put("field", Objects.requireNonNull(field, "field"));
            if (size > 0) facet.put("size", size);
            facets.add(facet); return this;
        }
        public SearchOptions aggregation(String name, Aggregation value) {
            aggregations.put(Objects.requireNonNull(name, "name"), Objects.requireNonNull(value, "aggregation").toBody());
            return this;
        }
        /** Orders relevance candidates by a metadata field or "_score"; order is "asc", "desc" or null. */
        public SearchOptions sort(String field, String order) {
            Map<String, Object> entry = new LinkedHashMap<>();
            entry.put("field", Objects.requireNonNull(field, "field"));
            if (order != null && !order.isEmpty()) entry.put("order", order);
            sort.add(entry); return this;
        }
        public SearchOptions sortCandidates(int value) { this.sortCandidates = value; return this; }

        Map<String, Object> toBody() {
            if (topK <= 0) throw new IllegalArgumentException("topK must be positive");
            boolean hasText = text != null && !text.isBlank();
            boolean hasSparse = sparseVector != null && !sparseVector.isEmpty();
            if (vector == null && !hasText && !hasSparse)
                throw new IllegalArgumentException("search requires a vector, text, or sparseVector");
            if (alpha != null && (!(alpha >= 0 && alpha <= 1)))
                throw new IllegalArgumentException("alpha must be between 0 and 1");
            if (fusion != null && !fusion.isEmpty() && !"weighted".equals(fusion) && !"rrf".equals(fusion))
                throw new IllegalArgumentException("fusion must be \"weighted\" or \"rrf\"");
            Map<String, Object> body = new LinkedHashMap<>();
            if (vector != null) body.put("vector", vector);
            body.put("top_k", topK);
            if (filter != null) body.put("filter", filter);
            if (namespace != null && !namespace.isBlank()) body.put("namespace", namespace);
            if (hasText) body.put("text", text);
            if (hasSparse) body.put("sparse_vector", sparseVector);
            if (alpha != null) body.put("alpha", alpha);
            if (fusion != null && !fusion.isEmpty()) body.put("fusion", fusion);
            if (syntax != null && !syntax.isEmpty()) body.put("syntax", syntax);
            if (highlight != null) body.put("highlight", highlight.toBody());
            if (!facets.isEmpty()) body.put("facets", facets);
            if (!aggregations.isEmpty()) body.put("aggregations", aggregations);
            if (!sort.isEmpty()) body.put("sort", sort);
            if (sortCandidates != null) body.put("sort_candidates", sortCandidates);
            return body;
        }
    }

    /** Highlight settings; unset fields use server defaults. */
    public static final class HighlightOptions {
        private String query, preTag, postTag;
        private Integer fragmentSize, fragments;
        private boolean raw;
        public HighlightOptions query(String value) { this.query = value; return this; }
        public HighlightOptions preTag(String value) { this.preTag = value; return this; }
        public HighlightOptions postTag(String value) { this.postTag = value; return this; }
        public HighlightOptions fragmentSize(int value) { this.fragmentSize = value; return this; }
        public HighlightOptions fragments(int value) { this.fragments = value; return this; }
        /** Skip HTML-escaping the text around the tags. */
        public HighlightOptions raw(boolean value) { this.raw = value; return this; }
        Map<String, Object> toBody() {
            Map<String, Object> body = new LinkedHashMap<>();
            if (query != null && !query.isEmpty()) body.put("query", query);
            if (preTag != null && !preTag.isEmpty()) body.put("pre_tag", preTag);
            if (postTag != null && !postTag.isEmpty()) body.put("post_tag", postTag);
            if (fragmentSize != null) body.put("fragment_size", fragmentSize);
            if (fragments != null) body.put("fragments", fragments);
            if (raw) body.put("raw", true);
            return body;
        }
    }

    /** A stats, histogram, range or terms aggregation over a metadata field. */
    public static final class Aggregation {
        private final String type, field;
        private Double interval;
        private Integer size;
        private final List<Map<String, Object>> ranges = new ArrayList<>();
        private Aggregation(String type, String field) { this.type = type; this.field = Objects.requireNonNull(field, "field"); }
        public static Aggregation stats(String field) { return new Aggregation("stats", field); }
        public static Aggregation histogram(String field, double interval) {
            if (!(interval > 0) || !Double.isFinite(interval)) throw new IllegalArgumentException("interval must be positive");
            Aggregation a = new Aggregation("histogram", field); a.interval = interval; return a;
        }
        public static Aggregation terms(String field, int size) {
            Aggregation a = new Aggregation("terms", field); if (size > 0) a.size = size; return a;
        }
        public static Aggregation range(String field) { return new Aggregation("range", field); }
        /** Adds a range bucket; null from/to means open-ended (from inclusive, to exclusive). */
        public Aggregation addRange(String key, Double from, Double to) {
            Map<String, Object> r = new LinkedHashMap<>();
            if (key != null && !key.isEmpty()) r.put("key", key);
            if (from != null) r.put("from", from);
            if (to != null) r.put("to", to);
            ranges.add(r); return this;
        }
        Map<String, Object> toBody() {
            Map<String, Object> body = new LinkedHashMap<>();
            body.put("type", type); body.put("field", field);
            if (interval != null) body.put("interval", interval);
            if (!ranges.isEmpty()) body.put("ranges", ranges);
            if (size != null) body.put("size", size);
            return body;
        }
    }

    /** Optional multipart fields for {@link #ingestDocument}. */
    public static final class DocumentOptions {
        private String id, namespace, contentType;
        private Map<String, Object> metadata;
        private int chunkSize, chunkOverlap;
        /** Defaults to the file name without extension. */
        public DocumentOptions id(String value) { this.id = value; return this; }
        public DocumentOptions namespace(String value) { this.namespace = value; return this; }
        /** Content-Type of the file part (application/octet-stream when unset). */
        public DocumentOptions contentType(String value) { this.contentType = value; return this; }
        /** JSON object copied onto every chunk. */
        public DocumentOptions metadata(Map<String, Object> value) { this.metadata = value; return this; }
        public DocumentOptions chunkSize(int value) { this.chunkSize = value; return this; }
        public DocumentOptions chunkOverlap(int value) { this.chunkOverlap = value; return this; }
    }

    private static String collectionPath(String name) { return "/v1/collections/" + encode(name); }
    private static String clusterCollectionPath(String name) { return "/v1/cluster/collections/" + encode(name); }
    private static String recordPath(String collection, String id, String namespace) {
        String path = collectionPath(collection) + "/vectors/" + encode(id);
        return namespace == null || namespace.isBlank() ? path : path + "?namespace=" + encode(namespace);
    }
    private static String encode(String value) {
        return java.net.URLEncoder.encode(Objects.requireNonNull(value), java.nio.charset.StandardCharsets.UTF_8)
                .replace("+", "%20");
    }
    private static String stringValue(Object value) { return value instanceof String ? (String) value : ""; }
    private static Transport defaultTransport(Duration timeout) {
        if (timeout == null || timeout.isZero() || timeout.isNegative())
            throw new IllegalArgumentException("timeout must be positive");
        return new JdkTransport(HttpClient.newBuilder().connectTimeout(timeout)
                .followRedirects(HttpClient.Redirect.NEVER).build());
    }
    @SuppressWarnings("unchecked") private static Map<String, Object> castObject(Object value) { return (Map<String, Object>) value; }
    private static Map<String, Object> objectField(Map<String, Object> response, String key) {
        Object value = Objects.requireNonNull(response, "response").get(key);
        if (value == null) return Map.of();
        if (!(value instanceof Map<?, ?>)) throw new TransportException("response field " + key + " must be an object");
        return castObject(value);
    }
    private static List<Map<String, Object>> objectList(Object value) {
        if (!(value instanceof List<?> list)) throw new TransportException("response field must be an array");
        List<Map<String, Object>> result = new ArrayList<>();
        for (Object item : list) {
            if (!(item instanceof Map<?, ?>)) throw new TransportException("response array must contain objects");
            result.add(castObject(item));
        }
        return List.copyOf(result);
    }

    /**
     * Outgoing request. JSON requests carry {@code body}; multipart uploads carry {@code rawBody} with its
     * {@code contentType} (and a null {@code body}).
     */
    public record Request(String method, URI uri, String body, String apiKey, Duration timeout,
                          byte[] rawBody, String contentType) {
        public Request(String method, URI uri, String body, String apiKey, Duration timeout) {
            this(method, uri, body, apiKey, timeout, null, body == null ? null : "application/json");
        }
    }
    public record Response(int statusCode, byte[] body) {}
    @FunctionalInterface public interface Transport { Response send(Request request) throws IOException, InterruptedException; }

    public static final class ApiException extends RuntimeException {
        private static final long serialVersionUID = 1L;
        private final int statusCode; private final String code;
        public ApiException(int statusCode, String code, String message) {
            super("gideondb: " + (code.isBlank() ? "HTTP " + statusCode : code + " (HTTP " + statusCode + "): " + message));
            this.statusCode = statusCode; this.code = code;
        }
        public int statusCode() { return statusCode; }
        public String code() { return code; }
    }
    public static final class TransportException extends RuntimeException {
        private static final long serialVersionUID = 1L;
        public TransportException(String message) { super("gideondb: " + message); }
        public TransportException(String message, Throwable cause) { super("gideondb: " + message, cause); }
    }

    private static final class JdkTransport implements Transport {
        private final HttpClient client;
        private JdkTransport(HttpClient client) { this.client = client; }
        public Response send(Request request) throws IOException, InterruptedException {
            HttpRequest.Builder builder = HttpRequest.newBuilder(request.uri()).timeout(request.timeout())
                    .header("Accept", "application/json").header("X-GideonDB-Client", "java/dev");
            if (!request.apiKey().isBlank()) builder.header("Authorization", "Bearer " + request.apiKey());
            if (request.rawBody() != null) builder.header("Content-Type", request.contentType())
                    .method(request.method(), HttpRequest.BodyPublishers.ofByteArray(request.rawBody()));
            else if (request.body() == null) builder.method(request.method(), HttpRequest.BodyPublishers.noBody());
            else builder.header("Content-Type", "application/json").method(request.method(), HttpRequest.BodyPublishers.ofString(request.body()));
            HttpResponse<InputStream> response = client.send(builder.build(), HttpResponse.BodyHandlers.ofInputStream());
            try (InputStream input = response.body()) {
                return new Response(response.statusCode(), input.readNBytes(MAX_RESPONSE_BYTES + 1));
            }
        }
    }

    /** Small strict JSON codec keeps the SDK dependency-free. */
    private static final class Json {
        static String stringify(Object value) {
            if (value == null) return "null";
            if (value instanceof String s) return quote(s);
            if (value instanceof Boolean || value instanceof Byte || value instanceof Short || value instanceof Integer || value instanceof Long) return value.toString();
            if (value instanceof Number n) {
                double d = n.doubleValue(); if (!Double.isFinite(d)) throw new IllegalArgumentException("JSON numbers must be finite");
                return value.toString();
            }
            if (value instanceof Map<?, ?> map) {
                StringBuilder out = new StringBuilder("{"); boolean first = true;
                for (Map.Entry<?, ?> entry : map.entrySet()) {
                    if (!(entry.getKey() instanceof String)) throw new IllegalArgumentException("JSON object keys must be strings");
                    if (!first) out.append(','); first = false;
                    out.append(quote((String) entry.getKey())).append(':').append(stringify(entry.getValue()));
                }
                return out.append('}').toString();
            }
            if (value instanceof Iterable<?> items) {
                StringBuilder out = new StringBuilder("["); boolean first = true;
                for (Object item : items) { if (!first) out.append(','); first = false; out.append(stringify(item)); }
                return out.append(']').toString();
            }
            throw new IllegalArgumentException("unsupported JSON value: " + value.getClass().getName());
        }
        static Object parse(String text) { Parser p = new Parser(text); Object value = p.value(); p.space(); if (p.i != text.length()) p.fail(); return value; }
        private static String quote(String value) {
            StringBuilder out = new StringBuilder("\"");
            for (int i = 0; i < value.length(); i++) {
                char c = value.charAt(i);
                switch (c) { case '"' -> out.append("\\\""); case '\\' -> out.append("\\\\"); case '\b' -> out.append("\\b"); case '\f' -> out.append("\\f"); case '\n' -> out.append("\\n"); case '\r' -> out.append("\\r"); case '\t' -> out.append("\\t"); default -> { if (c < 0x20) out.append(String.format("\\u%04x", (int)c)); else out.append(c); } }
            }
            return out.append('"').toString();
        }
        private static final class Parser {
            final String s; int i; Parser(String s) { this.s = s; }
            void space() { while (i < s.length() && Character.isWhitespace(s.charAt(i))) i++; }
            void fail() { throw new IllegalArgumentException("invalid JSON at offset " + i); }
            Object value() { space(); if (i >= s.length()) { fail(); } char c=s.charAt(i); if(c=='{')return object(); if(c=='[')return array(); if(c=='"')return string(); if(c=='t'){literal("true");return true;} if(c=='f'){literal("false");return false;} if(c=='n'){literal("null");return null;} return number(); }
            Map<String,Object> object(){ i++; Map<String,Object> m=new LinkedHashMap<>(); space(); if(take('}'))return m; do{space(); if(i>=s.length()||s.charAt(i)!='"')fail(); String k=string(); space(); if(!take(':'))fail(); m.put(k,value()); space();}while(take(',')); if(!take('}'))fail(); return m; }
            List<Object> array(){ i++; List<Object> a=new ArrayList<>(); space(); if(take(']'))return a; do{a.add(value());space();}while(take(',')); if(!take(']'))fail(); return a; }
            String string(){ i++; StringBuilder o=new StringBuilder(); while(i<s.length()){char c=s.charAt(i++);if(c=='"')return o.toString();if(c=='\\'){if(i>=s.length())fail();char e=s.charAt(i++);switch(e){case '"','\\','/'->o.append(e);case'b'->o.append('\b');case'f'->o.append('\f');case'n'->o.append('\n');case'r'->o.append('\r');case't'->o.append('\t');case'u'->{if(i+4>s.length())fail();try{o.append((char)Integer.parseInt(s.substring(i,i+4),16));}catch(NumberFormatException x){fail();}i+=4;}default->fail();}}else{if(c<0x20)fail();o.append(c);}}fail();return null; }
            Number number(){int start=i;if(take('-')){}if(take('0')){}else{digits();}boolean decimal=false;if(take('.')){decimal=true;digits();}if(i<s.length()&&(s.charAt(i)=='e'||s.charAt(i)=='E')){decimal=true;i++;if(i<s.length()&&(s.charAt(i)=='+'||s.charAt(i)=='-'))i++;digits();}try{return decimal?Double.parseDouble(s.substring(start,i)):Long.parseLong(s.substring(start,i));}catch(NumberFormatException e){fail();return 0;} }
            void digits(){int start=i;while(i<s.length()&&Character.isDigit(s.charAt(i)))i++;if(start==i)fail();}
            void literal(String x){if(!s.startsWith(x,i))fail();i+=x.length();}
            boolean take(char c){if(i<s.length()&&s.charAt(i)==c){i++;return true;}return false;}
        }
    }
}
