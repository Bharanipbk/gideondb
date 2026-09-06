package io.gideondb.client;

import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;

public final class GideonDBClientTest {
    public static void main(String[] args) {
        lifecycleAndEncoding(); distributedAndErrors(); validationAndBounds();
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
    private static void check(boolean value, String message) { if (!value) throw new AssertionError(message); }
    private static final class QueueTransport implements GideonDBClient.Transport {
        final ArrayDeque<GideonDBClient.Response> responses = new ArrayDeque<>(); final List<GideonDBClient.Request> requests = new ArrayList<>();
        void add(int status, String body) { responses.add(new GideonDBClient.Response(status, body.getBytes(StandardCharsets.UTF_8))); }
        public GideonDBClient.Response send(GideonDBClient.Request request) { requests.add(request); return responses.remove(); }
    }
}
