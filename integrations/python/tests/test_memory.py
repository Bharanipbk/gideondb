import unittest

from fake_server import FakeClient, KeywordEmbedder
from gideondb_integrations import MemoryStore, memory_tools


class Clock:
    def __init__(self): self.now = 1_000_000
    def __call__(self): return self.now


class MemoryStoreTest(unittest.TestCase):
    def setUp(self):
        self.client, self.clock = FakeClient(), Clock()
        self.memory = MemoryStore(self.client, KeywordEmbedder(), clock=self.clock)

    def test_layout_matches_mcp_server(self):
        result = self.memory.remember("alice", "likes coffee", metadata={"source": "chat"}, ttl_seconds=60, id="m1")
        self.assertEqual(result, {"id": "m1", "created_at": 1_000_000, "expires_at": 1_000_060})
        self.assertEqual(self.client.collections["agent_memory"]["index"], {"type": "hnsw"})
        record = self.client.records[("agent_memory", "alice", "m1")]
        self.assertEqual(record["payload"], {"text": "likes coffee"})
        self.assertEqual(record["metadata"], {"source": "chat", "created_at": 1_000_000, "expires_at": 1_000_060})
        with self.assertRaises(ValueError):
            self.memory.remember("alice", "x", metadata={"created_at": 1})

    def test_recall_isolates_users_expires_and_prefers_recent(self):
        self.memory.remember("alice", "old coffee order", id="old")
        self.clock.now += 30 * 24 * 3600
        self.memory.remember("alice", "new coffee order", id="new")
        self.memory.remember("alice", "short coffee note", ttl_seconds=10, id="short")
        self.memory.remember("bob", "bob coffee", id="bob")
        self.clock.now += 20
        memories = self.memory.recall("alice", "coffee", top_k=5)
        self.assertEqual([m["id"] for m in memories], ["new", "old"])
        self.assertGreater(memories[0]["score"], memories[1]["score"])
        self.assertNotIn("expires_at", memories[0])
        self.assertEqual(self.memory.prune("alice"), 1)
        self.memory.forget("alice", "old")
        self.assertEqual([m["id"] for m in self.memory.recall("alice", "coffee")], ["new"])
        self.assertEqual(MemoryStore(FakeClient(), KeywordEmbedder()).recall("x", "coffee"), [])

    def test_tools_are_bound_to_one_user(self):
        remember, recall, forget = memory_tools(self.memory, "carol")
        self.assertEqual([fn.__name__ for fn in (remember, recall, forget)], ["remember", "recall", "forget"])
        memory_id = remember("loves python")
        self.assertEqual(recall("python"), ["loves python"])
        self.assertEqual(forget(memory_id), memory_id)
        self.assertEqual(recall("python"), [])
        self.assertTrue(remember.__doc__)


if __name__ == "__main__":
    unittest.main()
