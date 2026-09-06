package io.vectordb.client;

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

/** Dependency-free Java 17 client for VectorDB's versioned HTTP API. */
public final class VectorDBClient {
    private static final int MAX_RESPONSE_BYTES = 16 << 20;
    private final URI origin;
    private final String apiKey;
    private final Duration timeout;
    private final Transport transport;

    public VectorDBClient(String baseUrl) {
        this(baseUrl, "", Duration.ofSeconds(30));
    }

    public VectorDBClient(String baseUrl, String apiKey, Duration timeout) {
        this(baseUrl, apiKey, timeout, defaultTransport(timeout));
    }

    /** Advanced constructor for custom transports, testing, and controlled runtimes. */
    public VectorDBClient(String baseUrl, String apiKey, Duration timeout, Transport transport) {
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

    public Map<String, Object> distributedBatchUpsert(String collection,
            List<Map<String, Object>> records, String acknowledgement) {
        Map<String, Object> body = new LinkedHashMap<>();
        body.put("records", records);
        if (acknowledgement != null && !acknowledgement.isBlank()) body.put("acknowledgement", acknowledgement);
        return requestObject("POST", clusterCollectionPath(collection) + "/vectors/batch", body);
    }

    private Object request(String method, String path, Object body) {
        Request outgoing = new Request(method, origin.resolve(path), body == null ? null : Json.stringify(body),
                apiKey, timeout);
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
    private static List<Map<String, Object>> objectList(Object value) {
        if (!(value instanceof List<?> list)) throw new TransportException("response field must be an array");
        List<Map<String, Object>> result = new ArrayList<>();
        for (Object item : list) {
            if (!(item instanceof Map<?, ?>)) throw new TransportException("response array must contain objects");
            result.add(castObject(item));
        }
        return List.copyOf(result);
    }

    public record Request(String method, URI uri, String body, String apiKey, Duration timeout) {}
    public record Response(int statusCode, byte[] body) {}
    @FunctionalInterface public interface Transport { Response send(Request request) throws IOException, InterruptedException; }

    public static final class ApiException extends RuntimeException {
        private static final long serialVersionUID = 1L;
        private final int statusCode; private final String code;
        public ApiException(int statusCode, String code, String message) {
            super("vectordb: " + (code.isBlank() ? "HTTP " + statusCode : code + " (HTTP " + statusCode + "): " + message));
            this.statusCode = statusCode; this.code = code;
        }
        public int statusCode() { return statusCode; }
        public String code() { return code; }
    }
    public static final class TransportException extends RuntimeException {
        private static final long serialVersionUID = 1L;
        public TransportException(String message) { super("vectordb: " + message); }
        public TransportException(String message, Throwable cause) { super("vectordb: " + message, cause); }
    }

    private static final class JdkTransport implements Transport {
        private final HttpClient client;
        private JdkTransport(HttpClient client) { this.client = client; }
        public Response send(Request request) throws IOException, InterruptedException {
            HttpRequest.Builder builder = HttpRequest.newBuilder(request.uri()).timeout(request.timeout())
                    .header("Accept", "application/json").header("X-VectorDB-Client", "java/dev");
            if (!request.apiKey().isBlank()) builder.header("Authorization", "Bearer " + request.apiKey());
            if (request.body() == null) builder.method(request.method(), HttpRequest.BodyPublishers.noBody());
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
