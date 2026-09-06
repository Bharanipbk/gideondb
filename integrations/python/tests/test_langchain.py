import sys
import types
import unittest

from gideondb_integrations import create_langchain_vector_store


class FakeDocument:
    def __init__(self, page_content, metadata=None, id=None):
        self.page_content, self.metadata, self.id = page_content, metadata or {}, id


class FakeVectorStore:
    pass


class FakeEmbeddings:
    def embed_documents(self, texts): return [[float(len(text)), 1.0] for text in texts]
    def embed_query(self, text): return [float(len(text)), 1.0]


class FakeClient:
    def __init__(self): self.records, self.search_call, self.deleted = [], None, []
    def batch_upsert(self, collection, records): self.records=list(records); return self.records
    def search(self, collection, vector, k, **kwargs):
        self.search_call=(collection,vector,k,kwargs)
        return [{"id":"one","score":0.9,"metadata":{"kind":"guide"},"payload":{"text":"stored text"}}]
    def delete(self, collection, record_id, **kwargs): self.deleted.append((collection,record_id,kwargs))


class LangChainIntegrationTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.saved={name:sys.modules.get(name) for name in ("langchain_core","langchain_core.documents","langchain_core.vectorstores")}
        core=types.ModuleType("langchain_core"); documents=types.ModuleType("langchain_core.documents"); stores=types.ModuleType("langchain_core.vectorstores")
        documents.Document=FakeDocument; stores.VectorStore=FakeVectorStore
        sys.modules.update({"langchain_core":core,"langchain_core.documents":documents,"langchain_core.vectorstores":stores})

    @classmethod
    def tearDownClass(cls):
        for name,value in cls.saved.items():
            if value is None: sys.modules.pop(name,None)
            else: sys.modules[name]=value

    def test_add_search_score_and_delete_contract(self):
        client=FakeClient(); store=create_langchain_vector_store(client,"docs",FakeEmbeddings(),namespace="tenant")
        self.assertIsInstance(store,FakeVectorStore)
        self.assertEqual(store.add_texts(["hello"],[{"kind":"guide"}],ids=["one"]),["one"])
        self.assertEqual(client.records[0]["payload"]["text"],"hello")
        self.assertEqual(client.records[0]["namespace"],"tenant")
        results=store.similarity_search_with_score("query",2,filter={"kind":"guide"})
        self.assertEqual(results[0][0].page_content,"stored text"); self.assertEqual(results[0][0].id,"one")
        self.assertEqual(results[0][1],0.9); self.assertEqual(client.search_call[3]["namespace"],"tenant")
        self.assertTrue(store.delete(["one"])); self.assertEqual(client.deleted[0][1],"one")

    def test_validates_parallel_inputs_and_options(self):
        store=create_langchain_vector_store(FakeClient(),"docs",FakeEmbeddings())
        with self.assertRaises(ValueError): store.add_texts(["one"],[])
        with self.assertRaises(ValueError): store.add_texts(["one"],ids=["one","two"])
        with self.assertRaises(TypeError): store.similarity_search("query",unknown=True)
        with self.assertRaises(ValueError): store.delete()


if __name__ == "__main__": unittest.main()
