import asyncio
import sys
import types
import unittest
from typing import Any, NamedTuple

from fake_server import FakeClient, KeywordEmbedder
from gideondb_integrations import create_langgraph_store


# Minimal mirrors of langgraph.store.base, field-for-field with LangGraph 1.x.
class GetOp(NamedTuple):
    namespace: tuple
    key: str
    refresh_ttl: bool = True


class PutOp(NamedTuple):
    namespace: tuple
    key: str
    value: Any
    index: Any = None
    ttl: Any = None


class SearchOp(NamedTuple):
    namespace_prefix: tuple
    filter: Any = None
    limit: int = 10
    offset: int = 0
    query: Any = None
    refresh_ttl: bool = True


class MatchCondition(NamedTuple):
    match_type: str
    path: tuple


class ListNamespacesOp(NamedTuple):
    match_conditions: Any = None
    max_depth: Any = None
    limit: int = 100
    offset: int = 0


class Item:
    def __init__(self, *, value, key, namespace, created_at, updated_at):
        self.value, self.key, self.namespace, self.created_at, self.updated_at = value, key, namespace, created_at, updated_at


class SearchItem(Item):
    def __init__(self, namespace, key, value, created_at, updated_at, score=None):
        super().__init__(value=value, key=key, namespace=namespace, created_at=created_at, updated_at=updated_at)
        self.score = score


class BaseStore:
    pass


class Clock:
    def __init__(self): self.now = 1_700_000_000.0
    def __call__(self): return self.now


class LangGraphStoreTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.saved = {name: sys.modules.get(name) for name in ("langgraph", "langgraph.store", "langgraph.store.base")}
        base = types.ModuleType("langgraph.store.base")
        for value in (BaseStore, GetOp, Item, ListNamespacesOp, PutOp, SearchItem, SearchOp):
            setattr(base, value.__name__, value)
        sys.modules.update({"langgraph": types.ModuleType("langgraph"), "langgraph.store": types.ModuleType("langgraph.store"), "langgraph.store.base": base})

    @classmethod
    def tearDownClass(cls):
        for name, value in cls.saved.items():
            if value is None:
                sys.modules.pop(name, None)
            else:
                sys.modules[name] = value

    def setUp(self):
        self.client, self.clock = FakeClient(), Clock()
        self.client.create_collection({"name": "lg", "dimension": 3})
        self.store = create_langgraph_store(self.client, "lg", KeywordEmbedder(), clock=self.clock)

    def put(self, namespace, key, value, **kwargs):
        self.assertEqual(self.store.batch([PutOp(namespace, key, value, **kwargs)]), [None])

    def test_put_get_update_delete(self):
        self.assertIsInstance(self.store, BaseStore)
        self.put(("users", "alice"), "pref", {"text": "drinks coffee"})
        first = self.store.batch([GetOp(("users", "alice"), "pref")])[0]
        self.assertEqual((first.namespace, first.key, first.value), (("users", "alice"), "pref", {"text": "drinks coffee"}))
        self.clock.now += 60
        self.put(("users", "alice"), "pref", {"text": "drinks tea"})
        second = self.store.batch([GetOp(("users", "alice"), "pref")])[0]
        self.assertEqual(second.created_at, first.created_at)
        self.assertGreater(second.updated_at, first.updated_at)
        self.assertIsNone(self.store.batch([GetOp(("users", "bob"), "pref")])[0])
        self.put(("users", "alice"), "pref", None)
        self.put(("users", "alice"), "pref", None)
        self.assertEqual(self.store.batch([GetOp(("users", "alice"), "pref")]), [None])

    def test_semantic_search_prefix_filter_and_ttl(self):
        self.put(("users", "alice"), "a", {"text": "coffee coffee", "kind": "food"})
        self.put(("users", "alice"), "b", {"text": "python tips", "kind": "code"})
        self.put(("users", "bob"), "c", {"text": "coffee", "kind": "food"})
        self.put(("users", "alice"), "d", {"text": "coffee soon gone", "kind": "food"}, ttl=1)
        self.clock.now += 120
        hits = self.store.batch([SearchOp(("users", "alice"), query="coffee", limit=5)])[0]
        self.assertEqual([hit.key for hit in hits][0], "a")
        self.assertNotIn("c", [hit.key for hit in hits])
        self.assertNotIn("d", [hit.key for hit in hits])
        self.assertIsInstance(hits[0].score, float)
        filtered = self.store.batch([SearchOp(("users",), filter={"kind": "code"}, query="coffee")])[0]
        self.assertEqual([hit.key for hit in filtered], ["b"])
        listing = self.store.batch([SearchOp(("users",), filter={"kind": {"$eq": "food"}})])[0]
        self.assertEqual(sorted(hit.key for hit in listing), ["a", "c"])
        self.assertIsNone(listing[0].score)

    def test_index_fields_choose_embedded_text(self):
        self.put(("docs",), "x", {"title": "python", "body": "coffee"}, index=["title"])
        record = next(iter(self.client.records.values()))
        self.assertEqual(record["vector"], KeywordEmbedder().embed(["python"])[0])

    def test_list_namespaces_and_async(self):
        self.put(("users", "alice", "notes"), "a", {"text": "x"})
        self.put(("users", "bob"), "b", {"text": "y"})
        self.put(("teams", "core"), "c", {"text": "z"})
        found = self.store.batch([ListNamespacesOp(match_conditions=(MatchCondition("prefix", ("users", "*")),))])[0]
        self.assertEqual(found, [("users", "alice", "notes"), ("users", "bob")])
        found = self.store.batch([ListNamespacesOp(max_depth=1)])[0]
        self.assertEqual(found, [("teams",), ("users",)])
        found = self.store.batch([ListNamespacesOp(match_conditions=(MatchCondition("suffix", ("core",)),))])[0]
        self.assertEqual(found, [("teams", "core")])
        result = asyncio.run(self.store.abatch([GetOp(("teams", "core"), "c")]))
        self.assertEqual(result[0].value, {"text": "z"})


if __name__ == "__main__":
    unittest.main()
