import os
import tempfile
import unittest

from gideondb_integrations import LiteServer
from gideondb_integrations.lite import find_binary


class LiteServerTest(unittest.TestCase):
    def test_command_is_loopback_lite(self):
        server = LiteServer("data", binary="/opt/gideondb", port=7000, extra_args=["-wal-sync", "async"])
        self.assertEqual(server.command(), ["/opt/gideondb", "-lite", "-data-path", os.path.abspath("data"), "-http-address", "127.0.0.1:7000", "-wal-sync", "async"])
        self.assertEqual(server.url, "http://127.0.0.1:7000")
        with self.assertRaises(ValueError):
            LiteServer("")

    def test_missing_binary_is_explained(self):
        saved_path, saved_binary = os.environ.get("PATH", ""), os.environ.pop("GIDEONDB_BINARY", None)
        os.environ["PATH"] = ""
        try:
            with self.assertRaisesRegex(FileNotFoundError, "GIDEONDB_BINARY"):
                find_binary()
        finally:
            os.environ["PATH"] = saved_path
            if saved_binary is not None:
                os.environ["GIDEONDB_BINARY"] = saved_binary

    @unittest.skipUnless(os.environ.get("GIDEONDB_BINARY"), "set GIDEONDB_BINARY to run against a real gideondb build")
    def test_real_binary_round_trip(self):
        with tempfile.TemporaryDirectory() as directory, LiteServer(directory) as db:
            db.create_collection({"name": "docs", "dimension": 2, "metric": "cosine", "shard_count": 1})
            db.batch_upsert("docs", [{"id": "a", "vector": [1, 0]}, {"id": "b", "vector": [0, 1]}])
            self.assertEqual(db.search("docs", [1, 0.1], 1)[0]["id"], "a")


if __name__ == "__main__":
    unittest.main()
