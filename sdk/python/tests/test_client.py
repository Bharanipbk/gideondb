import io
import json
import unittest
from urllib.error import HTTPError

from gideondb import APIError, Client


class Response:
    def __init__(self, status=200, body=None):
        self.status = status
        self._body = json.dumps(body).encode() if body is not None else b""

    def read(self, _limit):
        return self._body

    def __enter__(self):
        return self

    def __exit__(self, *_args):
        return False


class QueueOpener:
    def __init__(self, responses):
        self.responses = list(responses)
        self.requests = []

    def __call__(self, request, *, timeout):
        self.requests.append((request, timeout))
        response = self.responses.pop(0)
        if isinstance(response, Exception):
            raise response
        return response


class ClientTest(unittest.TestCase):
    def test_lifecycle_auth_and_namespaces(self):
        opener = QueueOpener(
            [
                Response(body={"status": "ok"}),
                Response(201, {"name": "docs", "dimension": 2, "metric": "dot", "shard_count": 2}),
                Response(body={"id": "a/b", "vector": [1, 0], "version": 1}),
                Response(body={"id": "a/b", "vector": [1, 0], "version": 1}),
                Response(body={"results": [{"id": "a/b", "score": 1.0}]}),
                Response(204),
            ]
        )
        sdk = Client("https://db.example", api_key="secret", timeout=4, opener=opener)
        sdk.health()
        sdk.create_collection({"name": "docs", "dimension": 2, "metric": "dot", "shard_count": 2})
        sdk.upsert("docs", {"id": "a/b", "vector": [1, 0]})
        self.assertEqual(sdk.get("docs", "a/b", namespace="tenant one")["version"], 1)
        self.assertEqual(sdk.search("docs", [1, 0], 1)[0]["id"], "a/b")
        sdk.delete("docs", "a/b")
        request, timeout = opener.requests[3]
        self.assertEqual(timeout, 4)
        self.assertIn("a%2Fb?namespace=tenant+one", request.full_url)
        self.assertEqual(request.get_header("Authorization"), "Bearer secret")

    def test_distributed_shapes_and_typed_error(self):
        error_body = json.dumps({"code": "not_found", "message": "missing"}).encode()
        error = HTTPError("https://db.example/v1/collections/missing", 404, "", {}, io.BytesIO(error_body))
        opener = QueueOpener(
            [
                Response(body={"results": [], "partial": True, "failures": [{"shard_id": 1}], "metadata_epoch": 2, "authoritative_placement": True}),
                Response(207, {"outcomes": [{"shard_id": 0, "status": "unknown"}], "partial": True, "metadata_epoch": 2, "authoritative_placement": True}),
                error,
            ]
        )
        sdk = Client("http://db.example/", opener=opener)
        search = sdk.distributed_search("docs", [1, 0], 3, allow_partial=True)
        self.assertTrue(search["partial"])
        write = sdk.distributed_batch_upsert("docs", [{"id": "one", "vector": [1, 0]}], acknowledgement="all")
        self.assertEqual(write["outcomes"][0]["status"], "unknown")
        with self.assertRaises(APIError) as caught:
            sdk.describe_collection("missing")
        self.assertEqual(caught.exception.status_code, 404)
        self.assertEqual(caught.exception.code, "not_found")

    def test_rejects_invalid_origins_and_timeout(self):
        for value in ["ftp://db.example", "https://user@db.example", "https://db.example/path"]:
            with self.assertRaises(ValueError):
                Client(value)
        with self.assertRaises(ValueError):
            Client("https://db.example", timeout=0)

    def test_scroll_encodes_cursor_namespace_and_vector_choice(self):
        opener = QueueOpener([Response(body={"records": [{"id": "one"}], "next_cursor": "next/value", "vectors_included": False}),Response(body={"records": [], "next_cursor": "", "vectors_included": False, "metadata_epoch": 7, "authoritative_placement": True})])
        sdk = Client("https://db.example", opener=opener)
        page = sdk.scroll("docs", namespace="tenant one", limit=25, cursor="prior/value")
        self.assertEqual(page["next_cursor"], "next/value")
        self.assertIn("namespace=tenant+one", opener.requests[0][0].full_url)
        self.assertIn("cursor=prior%2Fvalue", opener.requests[0][0].full_url)
        cluster_page = sdk.distributed_scroll("docs", limit=25)
        self.assertEqual(cluster_page["metadata_epoch"], 7)
        self.assertIn("/v1/cluster/collections/docs/vectors?", opener.requests[1][0].full_url)
        with self.assertRaises(ValueError):
            sdk.scroll("docs", limit=201)
        with self.assertRaises(ValueError):
            sdk.distributed_scroll("docs", limit=201)


if __name__ == "__main__":
    unittest.main()


class HybridTest(unittest.TestCase):
    def test_hybrid_text_sparse_and_quantized_vectorizer_bodies(self):
        opener = QueueOpener(
            [
                Response(201, {"name": "docs"}),
                Response(body={"records": []}),
                Response(body={"results": []}),
                Response(body={"results": []}),
                Response(body={"results": [], "partial": False}),
            ]
        )
        sdk = Client("https://db.example", opener=opener)
        config = {
            "name": "docs", "dimension": 384, "metric": "cosine", "shard_count": 1,
            "vectorizer": "minilm",
            "index": {"type": "flat", "quantization": "binary", "oversampling": 4},
        }
        sdk.create_collection(config)
        sdk.batch_upsert("docs", [{"id": "a", "text": "hello world", "sparse_vector": {"hello": 1.5}}])
        sdk.search("docs", None, 3, text="hello", alpha=0.3, fusion="rrf")
        sdk.search("docs", None, 1, sparse_vector={"hello": 2.0})
        sdk.distributed_search("docs", [1, 0], 2, text="hi", sparse_vector={"hi": 1.0}, fusion="weighted")
        bodies = [json.loads(r.data) for r, _ in opener.requests]
        self.assertEqual(bodies[0], config)
        self.assertEqual(bodies[1], {"records": [{"id": "a", "text": "hello world", "sparse_vector": {"hello": 1.5}}]})
        self.assertEqual(bodies[2], {"top_k": 3, "text": "hello", "alpha": 0.3, "fusion": "rrf"})
        self.assertEqual(bodies[3], {"top_k": 1, "sparse_vector": {"hello": 2.0}})
        self.assertEqual(bodies[4], {"top_k": 2, "vector": [1, 0], "text": "hi", "sparse_vector": {"hi": 1.0}, "fusion": "weighted"})
        with self.assertRaises(ValueError):
            sdk.search("docs", None, 1)
        with self.assertRaises(ValueError):
            sdk.search("docs", None, 1, text="x", alpha=1.5)
        with self.assertRaises(ValueError):
            sdk.search("docs", None, 1, text="x", fusion="max")
        self.assertEqual(len(opener.requests), 5)


class SearchFeaturesTest(unittest.TestCase):
    def test_collection_text_analyzer_and_synonyms_pass_through(self):
        opener = QueueOpener([Response(201, {"name": "docs", "text_analyzer": "english"})])
        sdk = Client("https://db.example", opener=opener)
        config = {"name": "docs", "dimension": 2, "metric": "cosine", "shard_count": 1,
                  "text_analyzer": "english", "synonyms": [["car", "auto"], ["tv", "television"]]}
        self.assertEqual(sdk.create_collection(config)["text_analyzer"], "english")
        self.assertEqual(json.loads(opener.requests[0][0].data), config)

    def test_search_features_body_and_rich_response(self):
        response = {
            "results": [{"id": "a", "score": 1.0, "highlights": {"text": ["<b>red</b> car"]}}],
            "facets": {"color": [{"value": "red", "count": 3}]},
            "aggregations": {"price_stats": {"count": 3, "min": 1, "max": 9}},
        }
        opener = QueueOpener([Response(body=response), Response(body=response),
                              Response(body=dict(response, partial=False)), Response(body={"results": []})])
        sdk = Client("https://db.example", opener=opener)
        kwargs = dict(
            text='"red car" -blue', syntax="simple",
            highlight={"pre_tag": "<b>", "post_tag": "</b>", "fragment_size": 80, "fragments": 2},
            facets=[{"field": "color", "size": 5}],
            aggregations={"price_stats": {"type": "stats", "field": "price"},
                          "bands": {"type": "range", "field": "price", "ranges": [{"key": "cheap", "to": 5}, {"from": 5}]}},
            sort=[{"field": "price", "order": "asc"}, {"field": "_score"}],
            sort_candidates=200,
        )
        full = sdk.search_response("docs", None, 5, **kwargs)
        self.assertEqual(full["facets"]["color"][0]["count"], 3)
        self.assertEqual(full["aggregations"]["price_stats"]["max"], 9)
        self.assertEqual(full["results"][0]["highlights"]["text"], ["<b>red</b> car"])
        results = sdk.search("docs", None, 5, **kwargs)
        self.assertEqual(results[0]["highlights"]["text"], ["<b>red</b> car"])
        cluster = sdk.distributed_search("docs", None, 5, allow_partial=True, **kwargs)
        self.assertEqual(cluster["facets"]["color"][0]["value"], "red")
        sdk.search("docs", None, 2, text="x", highlight={})
        expected = {
            "top_k": 5, "text": '"red car" -blue', "syntax": "simple",
            "highlight": {"pre_tag": "<b>", "post_tag": "</b>", "fragment_size": 80, "fragments": 2},
            "facets": [{"field": "color", "size": 5}],
            "aggregations": {"price_stats": {"type": "stats", "field": "price"},
                             "bands": {"type": "range", "field": "price", "ranges": [{"key": "cheap", "to": 5}, {"from": 5}]}},
            "sort": [{"field": "price", "order": "asc"}, {"field": "_score"}],
            "sort_candidates": 200,
        }
        bodies = [json.loads(r.data) for r, _ in opener.requests]
        self.assertEqual(bodies[0], expected)
        self.assertEqual(bodies[1], expected)
        self.assertEqual(bodies[2], dict(expected, allow_partial=True))
        self.assertEqual(bodies[3], {"top_k": 2, "text": "x", "highlight": {}})
        self.assertTrue(opener.requests[2][0].full_url.endswith("/v1/cluster/collections/docs/search"))
        with self.assertRaises(ValueError):
            sdk.search("docs", None, 1, text="x", syntax="lucene")

    def test_suggest_query_and_response(self):
        body = {"completions": [{"text": "vector data", "word": "data", "count": 4}], "did_you_mean": "vector database"}
        opener = QueueOpener([Response(body=body), Response(body={"completions": []})])
        sdk = Client("https://db.example", opener=opener)
        self.assertEqual(sdk.suggest("my docs", "vector dat", size=3), body)
        self.assertEqual(opener.requests[0][0].full_url,
                         "https://db.example/v1/collections/my%20docs/suggest?text=vector+dat&size=3")
        self.assertEqual(opener.requests[0][0].get_method(), "GET")
        self.assertEqual(sdk.suggest("docs", "x")["completions"], [])
        self.assertTrue(opener.requests[1][0].full_url.endswith("/suggest?text=x"))
        with self.assertRaises(ValueError):
            sdk.suggest("docs", "x", size=0)

    def test_ingest_document_multipart(self):
        result = {"document_id": "guide", "title": "Guide", "format": "markdown", "chunks": 2,
                  "characters": 120, "replaced_chunks_removed": 1}
        opener = QueueOpener([Response(201, result), Response(201, result)])
        sdk = Client("https://db.example", api_key="k", opener=opener)
        got = sdk.ingest_document(
            "docs", "/tmp/dir/guide.md", b"# Guide\nhello", id="guide", namespace="t1",
            metadata={"lang": "en"}, chunk_size=500, chunk_overlap=50, content_type="text/markdown",
        )
        self.assertEqual(got, result)
        request = opener.requests[0][0]
        self.assertTrue(request.full_url.endswith("/v1/collections/docs/documents"))
        self.assertEqual(request.get_method(), "POST")
        ctype = request.get_header("Content-type")
        self.assertTrue(ctype.startswith("multipart/form-data; boundary="))
        boundary = ctype.split("boundary=", 1)[1]
        expected = (
            f'--{boundary}\r\nContent-Disposition: form-data; name="id"\r\n\r\nguide\r\n'
            f'--{boundary}\r\nContent-Disposition: form-data; name="namespace"\r\n\r\nt1\r\n'
            f'--{boundary}\r\nContent-Disposition: form-data; name="metadata"\r\n\r\n{{"lang":"en"}}\r\n'
            f'--{boundary}\r\nContent-Disposition: form-data; name="chunk_size"\r\n\r\n500\r\n'
            f'--{boundary}\r\nContent-Disposition: form-data; name="chunk_overlap"\r\n\r\n50\r\n'
            f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="guide.md"\r\n'
            f'Content-Type: text/markdown\r\n\r\n# Guide\nhello\r\n'
            f'--{boundary}--\r\n'
        ).encode()
        self.assertEqual(request.data, expected)
        self.assertEqual(request.get_header("Authorization"), "Bearer k")
        import email.parser
        import email.policy
        sdk.ingest_document("docs", "notes.txt", io.BytesIO(b"plain"))
        request = opener.requests[1][0]
        message = email.parser.BytesParser(policy=email.policy.HTTP).parsebytes(
            b"Content-Type: " + request.get_header("Content-type").encode() + b"\r\n\r\n" + request.data)
        parts = list(message.iter_parts())
        self.assertEqual(len(parts), 1)
        self.assertEqual(parts[0].get_param("name", header="content-disposition"), "file")
        self.assertEqual(parts[0].get_filename(), "notes.txt")
        self.assertIsNone(parts[0].get("Content-Type"))
        self.assertEqual(parts[0].get_payload(decode=True), b"plain")
