import asyncio
import sys
import types
import unittest

from vectordb_integrations import create_llamaindex_vector_store


class MetadataMode: NONE="none"
class QueryMode: DEFAULT="default"; HYBRID="hybrid"
class QueryResult:
    def __init__(self, nodes=None, similarities=None, ids=None): self.nodes,self.similarities,self.ids=nodes,similarities,ids
class TextNode:
    def __init__(self,text="",id_="",metadata=None,embedding=None,ref_doc_id=None): self.text,self.node_id,self.metadata,self.embedding,self.ref_doc_id=text,id_,metadata or {},embedding,ref_doc_id
    def get_embedding(self): return self.embedding
    def get_content(self,metadata_mode=None): return self.text
class Query:
    def __init__(self,embedding,top_k=1,mode=QueryMode.DEFAULT,filters=None): self.query_embedding,self.similarity_top_k,self.mode,self.filters=embedding,top_k,mode,filters
class Filters:
    def legacy_filters(self): return [types.SimpleNamespace(key="kind",value="guide")]
class Client:
    def __init__(self): self.records,self.search_call,self.deleted=[],None,[]
    def batch_upsert(self,collection,records): self.records=list(records); return self.records
    def search(self,collection,vector,k,**kwargs): self.search_call=(collection,vector,k,kwargs); return [{"id":"node-1","score":0.8,"metadata":{"kind":"guide","llama_ref_doc_id":"doc-1"},"payload":{"text":"stored"}}]
    def delete(self,collection,record_id,**kwargs): self.deleted.append((collection,record_id,kwargs))


class LlamaIndexIntegrationTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        names=("llama_index","llama_index.core","llama_index.core.schema","llama_index.core.vector_stores","llama_index.core.vector_stores.types")
        cls.saved={name:sys.modules.get(name) for name in names}
        modules={name:types.ModuleType(name) for name in names}
        modules["llama_index.core.schema"].MetadataMode=MetadataMode; modules["llama_index.core.schema"].TextNode=TextNode
        modules["llama_index.core.vector_stores.types"].VectorStoreQueryMode=QueryMode; modules["llama_index.core.vector_stores.types"].VectorStoreQueryResult=QueryResult
        sys.modules.update(modules)
    @classmethod
    def tearDownClass(cls):
        for name,value in cls.saved.items():
            if value is None: sys.modules.pop(name,None)
            else: sys.modules[name]=value

    def test_add_query_delete_and_async_contract(self):
        client=Client(); store=create_llamaindex_vector_store(client,"docs",namespace="tenant")
        node=TextNode("hello","node-1",{"kind":"guide"},[1,0],"doc-1")
        self.assertEqual(store.add([node]),["node-1"]); self.assertEqual(client.records[0]["payload"]["text"],"hello")
        self.assertEqual(client.records[0]["metadata"]["llama_ref_doc_id"],"doc-1")
        result=store.query(Query([1,0],3,filters=Filters()))
        self.assertEqual(result.ids,["node-1"]); self.assertEqual(result.similarities,[0.8]); self.assertEqual(result.nodes[0].text,"stored")
        self.assertNotIn("llama_ref_doc_id",result.nodes[0].metadata); self.assertEqual(client.search_call[3]["filter"],{"kind":"guide"})
        store.delete("doc-1"); self.assertEqual(client.deleted[0][1],"node-1")
        self.assertEqual(asyncio.run(store.async_add([])),[])

    def test_rejects_unsupported_queries_and_options(self):
        store=create_llamaindex_vector_store(Client(),"docs")
        with self.assertRaises(ValueError): store.query(Query(None))
        with self.assertRaises(ValueError): store.query(Query([1],mode=QueryMode.HYBRID))
        with self.assertRaises(TypeError): store.add([TextNode("x","one",{},[1])],unknown=True)


if __name__ == "__main__": unittest.main()
