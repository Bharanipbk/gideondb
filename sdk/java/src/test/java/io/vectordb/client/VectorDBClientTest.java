package io.vectordb.client;

import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;

public final class VectorDBClientTest {
    public static void main(String[] args) {
        lifecycleAndEncoding(); distributedAndErrors(); validationAndBounds();
        System.out.println("Java SDK tests passed");
    }
    private static void lifecycleAndEncoding() {
        QueueTransport transport = new QueueTransport();
        transport.add(200, "{\"status\":\"ok\"}");
        transport.add(201, "{\"name\":\"docs\",\"dimension\":2,\"metric\":\"dot\",\"shard_count\":2}");
        transport.add(200, "{\"id\":\"a/b\",\"vector\":[1,0],\"version\":1}");
        transport.add(200, "{\"results\":[{\"id\":\"a/b\",\"score\":1.0}]}");
        VectorDBClient client = new VectorDBClient("https://db.example/", "secret", Duration.ofSeconds(2), transport);
        client.health(); client.createCollection(Map.of("name","docs","dimension",2,"metric","dot","shard_count",2));
        check(((Number)client.get("docs", "a/b", "tenant one").get("version")).longValue() == 1, "record version");
        check("a/b".equals(client.search("docs", List.of(1,0), 1, null, "").get(0).get("id")), "search result");
        check(transport.requests.get(2).uri().toString().endsWith("a%2Fb?namespace=tenant%20one"), "encoded path");
        check("secret".equals(transport.requests.get(2).apiKey()), "API key");
    }
    private static void distributedAndErrors() {
        QueueTransport transport = new QueueTransport();
        transport.add(207, "{\"outcomes\":[{\"shard_id\":0,\"status\":\"unknown\"}],\"partial\":true,\"metadata_epoch\":2,\"authoritative_placement\":true}");
        transport.add(404, "{\"code\":\"not_found\",\"message\":\"missing\"}");
        VectorDBClient client = new VectorDBClient("http://db.example", "", Duration.ofSeconds(2), transport);
        check(Boolean.TRUE.equals(client.distributedBatchUpsert("docs", List.of(Map.of("id","one","vector",List.of(1,0))), "all").get("partial")), "partial result");
        try { client.describeCollection("missing"); throw new AssertionError("expected API error"); }
        catch (VectorDBClient.ApiException e) { check(e.statusCode()==404 && "not_found".equals(e.code()), "typed API error"); }
    }
    private static void validationAndBounds() {
        QueueTransport transport = new QueueTransport(); transport.responses.add(new VectorDBClient.Response(200, new byte[(16 << 20) + 1]));
        VectorDBClient client = new VectorDBClient("https://db.example", "", Duration.ofSeconds(1), transport);
        try { client.health(); throw new AssertionError("expected bounded response error"); } catch (VectorDBClient.TransportException expected) {}
        try { new VectorDBClient("ftp://db.example"); throw new AssertionError("expected origin error"); } catch (IllegalArgumentException expected) {}
        try { new VectorDBClient("https://db.example/path"); throw new AssertionError("expected path error"); } catch (IllegalArgumentException expected) {}
    }
    private static void check(boolean value, String message) { if (!value) throw new AssertionError(message); }
    private static final class QueueTransport implements VectorDBClient.Transport {
        final ArrayDeque<VectorDBClient.Response> responses = new ArrayDeque<>(); final List<VectorDBClient.Request> requests = new ArrayList<>();
        void add(int status, String body) { responses.add(new VectorDBClient.Response(status, body.getBytes(StandardCharsets.UTF_8))); }
        public VectorDBClient.Response send(VectorDBClient.Request request) { requests.add(request); return responses.remove(); }
    }
}
