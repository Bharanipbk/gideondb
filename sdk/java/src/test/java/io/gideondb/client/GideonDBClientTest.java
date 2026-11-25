package io.gideondb.client;

import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;

public final class GideonDBClientTest {
    public static void main(String[] args) {
        lifecycleAndEncoding(); distributedAndErrors(); validationAndBounds(); hybridAndQuantization(); searchFeatures(); suggestAndDocuments();
        System.out.println("Java SDK tests passed");
    }
    private static void lifecycleAndEncoding() {
        QueueTransport transport = new QueueTransport();
        transport.add(200, "{\"status\":\"ok\"}");
        transport.add(201, "{\"name\":\"docs\",\"dimension\":2,\"metric\":\"dot\",\"shard_count\":2}");
        transport.add(200, "{\"records\":[{\"id\":\"one\"}],\"next_cursor\":\"next/value\",\"vectors_included\":false}");
        transport.add(200, "{\"records\":[],\"next_cursor\":\"\",\"vectors_included\":false,\"metadata_epoch\":7,\"authoritative_placement\":true}");
        transport.add(200, "{\"id\":\"a/b\",\"vector\":[1,0],\"version\":1}");
        transport.add(200, "{\"results\":[{\"id\":\"a/b\",\"score\":1.0}]}");
        GideonDBClient client = new GideonDBClient("https://db.example/", "secret", Duration.ofSeconds(2), transport);
        client.health(); client.createCollection(Map.of("name","docs","dimension",2,"metric","dot","shard_count",2));
        check("next/value".equals(client.scroll("docs", "tenant one", 25, "prior/value", true).get("next_cursor")), "scroll cursor");
        check(((Number)client.distributedScroll("docs", "", 25, "", false).get("metadata_epoch")).intValue() == 7, "distributed scroll epoch");
        check(((Number)client.get("docs", "a/b", "tenant one").get("version")).longValue() == 1, "record version");
        check("a/b".equals(client.search("docs", List.of(1,0), 1, null, "").get(0).get("id")), "search result");
        check(transport.requests.get(2).uri().toString().contains("cursor=prior%2Fvalue"), "encoded cursor");
        check(transport.requests.get(4).uri().toString().endsWith("a%2Fb?namespace=tenant%20one"), "encoded path");
        check(transport.requests.get(3).uri().toString().contains("/v1/cluster/collections/docs/vectors"), "cluster scroll path");
        check("secret".equals(transport.requests.get(4).apiKey()), "API key");
    }
    private static void distributedAndErrors() {
        QueueTransport transport = new QueueTransport();
        transport.add(207, "{\"outcomes\":[{\"shard_id\":0,\"status\":\"unknown\"}],\"partial\":true,\"metadata_epoch\":2,\"authoritative_placement\":true}");
        transport.add(404, "{\"code\":\"not_found\",\"message\":\"missing\"}");
        GideonDBClient client = new GideonDBClient("http://db.example", "", Duration.ofSeconds(2), transport);
        check(Boolean.TRUE.equals(client.distributedBatchUpsert("docs", List.of(Map.of("id","one","vector",List.of(1,0))), "all").get("partial")), "partial result");
        try { client.describeCollection("missing"); throw new AssertionError("expected API error"); }
        catch (GideonDBClient.ApiException e) { check(e.statusCode()==404 && "not_found".equals(e.code()), "typed API error"); }
    }
    private static void validationAndBounds() {
        QueueTransport transport = new QueueTransport(); transport.responses.add(new GideonDBClient.Response(200, new byte[(16 << 20) + 1]));
        GideonDBClient client = new GideonDBClient("https://db.example", "", Duration.ofSeconds(1), transport);
        try { client.health(); throw new AssertionError("expected bounded response error"); } catch (GideonDBClient.TransportException expected) {}
        try { new GideonDBClient("ftp://db.example"); throw new AssertionError("expected origin error"); } catch (IllegalArgumentException expected) {}
        try { new GideonDBClient("https://db.example/path"); throw new AssertionError("expected path error"); } catch (IllegalArgumentException expected) {}
    }
    private static void hybridAndQuantization() {
        QueueTransport transport = new QueueTransport();
        transport.add(201, "{\"name\":\"docs\"}");
        transport.add(200, "{\"id\":\"one\"}");
        transport.add(200, "{\"results\":[{\"id\":\"one\",\"score\":0.5}]}");
        transport.add(200, "{\"results\":[]}");
        GideonDBClient client = new GideonDBClient("https://db.example", "", Duration.ofSeconds(1), transport);
        Map<String, Object> config = new java.util.LinkedHashMap<>();
        config.put("name", "docs"); config.put("dimension", 2); config.put("vectorizer", "mini");
        config.put("index", new java.util.LinkedHashMap<>(Map.of("type", "flat", "quantization", "binary", "oversampling", 4)));
        client.createCollection(config);
        String created = transport.requests.get(0).body();
        check(created.contains("\"vectorizer\":\"mini\"") && created.contains("\"quantization\":\"binary\"") && created.contains("\"oversampling\":4"), "collection quantization body");
        client.upsert("docs", Map.of("id", "one", "text", "hello world", "sparse_vector", Map.of("hello", 1.5)));
        String record = transport.requests.get(1).body();
        check(record.contains("\"text\":\"hello world\"") && record.contains("\"sparse_vector\":{\"hello\":1.5}") && !record.contains("\"vector\""), "text record body");
        client.search("docs", new GideonDBClient.SearchOptions().text("hello").sparseVector(Map.of("hello", 2.0)).alpha(0.25).fusion("rrf").topK(3));
        check("{\"top_k\":3,\"text\":\"hello\",\"sparse_vector\":{\"hello\":2.0},\"alpha\":0.25,\"fusion\":\"rrf\"}".equals(transport.requests.get(2).body()), "hybrid search body: " + transport.requests.get(2).body());
        client.search("docs", new GideonDBClient.SearchOptions().vector(List.of(1, 0)).text("hello").topK(1));
        check(transport.requests.get(3).body().startsWith("{\"vector\":[1,0],\"top_k\":1,\"text\":\"hello\""), "dense plus text body");
        try { new GideonDBClient.SearchOptions().alpha(1.5).text("x").toBody(); throw new AssertionError("expected alpha error"); } catch (IllegalArgumentException expected) {}
        try { new GideonDBClient.SearchOptions().fusion("max").text("x").toBody(); throw new AssertionError("expected fusion error"); } catch (IllegalArgumentException expected) {}
        try { new GideonDBClient.SearchOptions().toBody(); throw new AssertionError("expected empty search error"); } catch (IllegalArgumentException expected) {}
    }
    private static void searchFeatures() {
        QueueTransport transport = new QueueTransport();
        transport.add(201, "{\"name\":\"docs\"}");
        transport.add(200, "{\"results\":[{\"id\":\"one\",\"score\":1.5,\"highlights\":[\"<b>hello</b> world\"]}],"
                + "\"facets\":{\"lang\":[{\"value\":\"en\",\"count\":3}]},"
                + "\"aggregations\":{\"price\":{\"count\":3,\"min\":1,\"max\":9.5,\"sum\":12,\"avg\":4}}}");
        transport.add(200, "{\"results\":[],\"partial\":false,\"facets\":{},\"aggregations\":{\"r\":{\"buckets\":[{\"key\":\"cheap\",\"count\":2}]}}}");
        transport.add(200, "{\"results\":[{\"id\":\"one\",\"highlights\":[\"x\"]}]}");
        GideonDBClient client = new GideonDBClient("https://db.example", "", Duration.ofSeconds(1), transport);
        Map<String, Object> config = new java.util.LinkedHashMap<>();
        config.put("name", "docs"); config.put("dimension", 2); config.put("text_analyzer", "english");
        config.put("synonyms", List.of(List.of("tv", "television")));
        client.createCollection(config);
        check("{\"name\":\"docs\",\"dimension\":2,\"text_analyzer\":\"english\",\"synonyms\":[[\"tv\",\"television\"]]}".equals(transport.requests.get(0).body()), "analyzer body");
        GideonDBClient.SearchOptions options = new GideonDBClient.SearchOptions().text("\"hello world\" +x").topK(2).syntax("simple")
                .highlight(new GideonDBClient.HighlightOptions().query("hello").preTag("<b>").postTag("</b>").fragmentSize(80).fragments(2).raw(true))
                .facet("lang", 5).facet("tag", 0)
                .aggregation("price", GideonDBClient.Aggregation.stats("price"))
                .aggregation("hist", GideonDBClient.Aggregation.histogram("price", 2.5))
                .aggregation("top", GideonDBClient.Aggregation.terms("tag", 3))
                .aggregation("r", GideonDBClient.Aggregation.range("price").addRange("cheap", null, 5.0).addRange("", 5.0, null))
                .sort("price", "desc").sort("_score", null).sortCandidates(200);
        Map<String, Object> response = client.searchResponse("docs", options);
        String expected = "{\"top_k\":2,\"text\":\"\\\"hello world\\\" +x\",\"syntax\":\"simple\","
                + "\"highlight\":{\"query\":\"hello\",\"pre_tag\":\"<b>\",\"post_tag\":\"</b>\",\"fragment_size\":80,\"fragments\":2,\"raw\":true},"
                + "\"facets\":[{\"field\":\"lang\",\"size\":5},{\"field\":\"tag\"}],"
                + "\"aggregations\":{\"price\":{\"type\":\"stats\",\"field\":\"price\"},\"hist\":{\"type\":\"histogram\",\"field\":\"price\",\"interval\":2.5},"
                + "\"top\":{\"type\":\"terms\",\"field\":\"tag\",\"size\":3},\"r\":{\"type\":\"range\",\"field\":\"price\",\"ranges\":[{\"key\":\"cheap\",\"to\":5.0},{\"from\":5.0}]}},"
                + "\"sort\":[{\"field\":\"price\",\"order\":\"desc\"},{\"field\":\"_score\"}],\"sort_candidates\":200}";
        check(expected.equals(transport.requests.get(1).body()), "search features body: " + transport.requests.get(1).body());
        check(transport.requests.get(1).uri().getPath().equals("/v1/collections/docs/search"), "search path");
        List<?> highlights = (List<?>) objectAt(response, "results", 0).get("highlights");
        check("<b>hello</b> world".equals(highlights.get(0)), "highlights");
        Map<?, ?> lang = (Map<?, ?>) ((List<?>) GideonDBClient.facets(response).get("lang")).get(0);
        check("en".equals(lang.get("value")) && ((Number) lang.get("count")).intValue() == 3, "facet bucket");
        Map<?, ?> price = (Map<?, ?>) GideonDBClient.aggregations(response).get("price");
        check(((Number) price.get("max")).doubleValue() == 9.5, "stats aggregation");
        Map<String, Object> cluster = client.distributedSearch("docs", new GideonDBClient.SearchOptions().text("x").facet("lang", 0), true);
        check(transport.requests.get(2).body().equals("{\"top_k\":10,\"text\":\"x\",\"facets\":[{\"field\":\"lang\"}],\"allow_partial\":true}"), "cluster facets body");
        check(transport.requests.get(2).uri().getPath().startsWith("/v1/cluster/"), "cluster path");
        check(GideonDBClient.aggregations(cluster).containsKey("r") && GideonDBClient.facets(cluster).isEmpty(), "cluster aggregations");
        List<Map<String, Object>> results = client.search("docs", new GideonDBClient.SearchOptions().vector(List.of(1, 0))
                .highlight(new GideonDBClient.HighlightOptions().query("q")));
        check(transport.requests.get(3).body().equals("{\"vector\":[1,0],\"top_k\":10,\"highlight\":{\"query\":\"q\"}}"), "minimal highlight");
        check(results.get(0).containsKey("highlights") && GideonDBClient.facets(Map.of()).isEmpty(), "legacy search keeps highlights");
        try { GideonDBClient.Aggregation.histogram("p", 0); throw new AssertionError("expected interval error"); } catch (IllegalArgumentException ignored) {}
    }
    private static void suggestAndDocuments() {
        QueueTransport transport = new QueueTransport();
        transport.add(200, "{\"completions\":[{\"text\":\"vector data\",\"word\":\"data\",\"count\":4}],\"did_you_mean\":\"vector database\"}");
        transport.add(200, "{\"completions\":[]}");
        transport.add(200, "{\"document_id\":\"guide\",\"title\":\"Guide\",\"format\":\"markdown\",\"chunks\":3,\"characters\":2400,\"replaced_chunks_removed\":1}");
        transport.add(200, "{\"document_id\":\"notes\",\"chunks\":1}");
        GideonDBClient client = new GideonDBClient("https://db.example", "k", Duration.ofSeconds(1), transport);
        Map<String, Object> s = client.suggest("my docs", "vectr da&b", 3);
        check(transport.requests.get(0).uri().toString().equals("https://db.example/v1/collections/my%20docs/suggest?text=vectr%20da%26b&size=3"), "suggest uri: " + transport.requests.get(0).uri());
        check("GET".equals(transport.requests.get(0).method()) && transport.requests.get(0).body() == null, "suggest method");
        Map<String, Object> c = GideonDBClient.completions(s).get(0);
        check("data".equals(c.get("word")) && ((Number) c.get("count")).intValue() == 4 && "vector database".equals(s.get("did_you_mean")), "suggest parse");
        client.suggest("docs", "x", 0);
        check(transport.requests.get(1).uri().toString().endsWith("/suggest?text=x") && GideonDBClient.completions(Map.of()).isEmpty(), "suggest default size");
        try { client.suggest("docs", "", 1); throw new AssertionError("expected text error"); } catch (IllegalArgumentException ignored) {}
        Map<String, Object> meta = new java.util.LinkedHashMap<>(); meta.put("team", "a");
        byte[] content = "# Guide\n\u00e9".getBytes(StandardCharsets.UTF_8);
        Map<String, Object> result = client.ingestDocument("docs", "dir/guide.md", content, new GideonDBClient.DocumentOptions()
                .id("guide").namespace("ns").metadata(meta).chunkSize(500).chunkOverlap(50).contentType("text/markdown"));
        GideonDBClient.Request upload = transport.requests.get(2);
        check("POST".equals(upload.method()) && upload.uri().getPath().equals("/v1/collections/docs/documents") && upload.body() == null && "k".equals(upload.apiKey()), "upload request");
        check(upload.contentType().startsWith("multipart/form-data; boundary=gideondb-"), "multipart content type");
        String b = upload.contentType().substring(upload.contentType().indexOf("boundary=") + 9);
        String expected = "--" + b + "\r\nContent-Disposition: form-data; name=\"id\"\r\n\r\nguide\r\n"
                + "--" + b + "\r\nContent-Disposition: form-data; name=\"namespace\"\r\n\r\nns\r\n"
                + "--" + b + "\r\nContent-Disposition: form-data; name=\"metadata\"\r\n\r\n{\"team\":\"a\"}\r\n"
                + "--" + b + "\r\nContent-Disposition: form-data; name=\"chunk_size\"\r\n\r\n500\r\n"
                + "--" + b + "\r\nContent-Disposition: form-data; name=\"chunk_overlap\"\r\n\r\n50\r\n"
                + "--" + b + "\r\nContent-Disposition: form-data; name=\"file\"; filename=\"guide.md\"\r\nContent-Type: text/markdown\r\n\r\n# Guide\n\u00e9\r\n"
                + "--" + b + "--\r\n";
        check(expected.equals(new String(upload.rawBody(), StandardCharsets.UTF_8)), "multipart body: " + new String(upload.rawBody(), StandardCharsets.UTF_8));
        check("markdown".equals(result.get("format")) && ((Number) result.get("chunks")).intValue() == 3
                && ((Number) result.get("characters")).intValue() == 2400 && ((Number) result.get("replaced_chunks_removed")).intValue() == 1, "document result");
        client.ingestDocument("docs", "notes.txt", new byte[] {1, 2}, null);
        GideonDBClient.Request second = transport.requests.get(3);
        String b2 = second.contentType().substring(second.contentType().indexOf("boundary=") + 9);
        check(!b2.equals(b), "random boundary");
        check(new String(second.rawBody(), StandardCharsets.ISO_8859_1).equals("--" + b2 + "\r\nContent-Disposition: form-data; name=\"file\"; filename=\"notes.txt\"\r\nContent-Type: application/octet-stream\r\n\r\n\u0001\u0002\r\n--" + b2 + "--\r\n"), "minimal multipart");
        GideonDBClient.Request legacy = new GideonDBClient.Request("GET", java.net.URI.create("https://x"), null, "", Duration.ofSeconds(1));
        check(legacy.rawBody() == null && legacy.contentType() == null, "legacy request constructor");
    }
    private static Map<?, ?> objectAt(Map<String, Object> response, String key, int index) { return (Map<?, ?>) ((List<?>) response.get(key)).get(index); }
    private static void check(boolean value, String message) { if (!value) throw new AssertionError(message); }
    private static final class QueueTransport implements GideonDBClient.Transport {
        final ArrayDeque<GideonDBClient.Response> responses = new ArrayDeque<>(); final List<GideonDBClient.Request> requests = new ArrayList<>();
        void add(int status, String body) { responses.add(new GideonDBClient.Response(status, body.getBytes(StandardCharsets.UTF_8))); }
        public GideonDBClient.Response send(GideonDBClient.Request request) { requests.add(request); return responses.remove(); }
    }
}
