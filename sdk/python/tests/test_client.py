import io
import json
import unittest
from urllib.error import HTTPError

from vectordb import APIError, Client


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
