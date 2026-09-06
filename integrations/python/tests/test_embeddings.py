import json
import math
import unittest

from gideondb_integrations import CohereEmbedder, Document, HuggingFaceEmbedder, OllamaEmbedder, OpenAICompatibleEmbedder, SemanticStore


class Response:
    def __init__(self, value): self.value = json.dumps(value).encode()
    def read(self, limit): return self.value[:limit]
    def __enter__(self): return self
    def __exit__(self, *_args): return False


class Opener:
    def __init__(self, responses): self.responses, self.requests = list(responses), []
    def __call__(self, request, *, timeout): self.requests.append((request, timeout)); return self.responses.pop(0)


class FakeGideonDB:
    def __init__(self): self.batch, self.query = None, None
    def batch_upsert(self, collection, records): self.batch=(collection,records); return records
    def search(self, collection, vector, top_k, **options): self.query=(collection,vector,top_k,options); return [{"id":"one","score":1}]


class EmbeddingIntegrationTest(unittest.TestCase):
    def test_ollama_batch_contract_and_options(self):
        opener=Opener([Response({"embeddings":[[1,0],[0,1]]})])
        embedder=OllamaEmbedder("embeddinggemma",dimensions=2,truncate=False,timeout=4,opener=opener)
        self.assertEqual(embedder.embed(["one","two"]),[[1.0,0.0],[0.0,1.0]])
        request,timeout=opener.requests[0]; body=json.loads(request.data)
        self.assertEqual(timeout,4); self.assertEqual(request.full_url,"http://127.0.0.1:11434/api/embed")
        self.assertEqual(body,{"model":"embeddinggemma","input":["one","two"],"truncate":False,"dimensions":2})

    def test_rejects_bad_origins_counts_and_vectors(self):
        with self.assertRaises(ValueError): OllamaEmbedder("model",base_url="http://user@localhost:11434")
        with self.assertRaises(ValueError): OllamaEmbedder("")
        with self.assertRaises(ValueError): OllamaEmbedder("model",opener=Opener([])).embed([])
        for response in [{"embeddings":[]},{"embeddings":[[math.nan]]},{"embeddings":[[1,2],[1]]}]:
            with self.assertRaises(RuntimeError): OllamaEmbedder("model",opener=Opener([Response(response)])).embed(["one"] if len(response["embeddings"])<2 else ["one","two"])

    def test_provider_neutral_ingest_and_search(self):
        class Embedder:
            def embed(self,texts): return [[float(len(text)),1.0] for text in texts]
        client=FakeGideonDB(); store=SemanticStore(client,"docs",Embedder())
        stored=store.upsert_documents([Document("one","hello",{"kind":"guide"},namespace="tenant")])
        self.assertEqual(stored[0]["payload"]["text"],"hello"); self.assertEqual(client.batch[1][0]["namespace"],"tenant")
        self.assertEqual(store.search("query",3,filter={"kind":"guide"})[0]["id"],"one")
        self.assertEqual(client.query[2],3)

    def test_hugging_face_feature_extraction_contract(self):
        opener=Opener([Response([[1,0],[0,1]])])
        embedder=HuggingFaceEmbedder("org/model", "hf_secret", timeout=7, prompt_name="query", opener=opener)
        self.assertEqual(embedder.embed(["one","two"]),[[1.0,0.0],[0.0,1.0]])
        request,timeout=opener.requests[0]
        self.assertEqual(request.full_url,"https://router.huggingface.co/hf-inference/models/org/model/pipeline/feature-extraction")
        self.assertEqual(request.get_header("Authorization"),"Bearer hf_secret")
        self.assertEqual(timeout,7)
        self.assertEqual(json.loads(request.data),{"inputs":["one","two"],"normalize":True,"truncate":True,"prompt_name":"query"})

    def test_hugging_face_rejects_unsafe_and_token_level_responses(self):
        with self.assertRaises(ValueError): HuggingFaceEmbedder("model","",opener=Opener([]))
        with self.assertRaises(ValueError): HuggingFaceEmbedder("model","token",endpoint_url="http://example.com")
        with self.assertRaises(RuntimeError): HuggingFaceEmbedder("model","token",opener=Opener([Response([[[1,2],[3,4]]])])).embed(["one"])

    def test_openai_compatible_contract_and_index_order(self):
        opener=Opener([Response({"data":[{"index":1,"embedding":[0,1]},{"index":0,"embedding":[1,0]}]})])
        embedder=OpenAICompatibleEmbedder("text-embedding-3-small","secret",dimensions=2,user="tenant",timeout=6,opener=opener)
        self.assertEqual(embedder.embed(["one","two"]),[[1.0,0.0],[0.0,1.0]])
        request,timeout=opener.requests[0]
        self.assertEqual(request.full_url,"https://api.openai.com/v1/embeddings")
        self.assertEqual(request.get_header("Authorization"),"Bearer secret")
        self.assertEqual(timeout,6)
        self.assertEqual(json.loads(request.data),{"model":"text-embedding-3-small","input":["one","two"],"encoding_format":"float","dimensions":2,"user":"tenant"})

    def test_openai_compatible_rejects_unsafe_and_invalid_responses(self):
        with self.assertRaises(ValueError): OpenAICompatibleEmbedder("model","key",base_url="http://example.com/v1")
        with self.assertRaises(ValueError): OpenAICompatibleEmbedder("","key")
        with self.assertRaises(ValueError): OpenAICompatibleEmbedder("model","")
        OpenAICompatibleEmbedder("model","key",base_url="http://localhost:8080/v1")
        invalid = [
            {"data":[]},
            {"data":[{"index":1,"embedding":[1]}]},
            {"data":[{"index":0,"embedding":[math.inf]}]},
            {"data":[{"index":0,"embedding":[1]},{"index":0,"embedding":[2]}]},
        ]
        batches = [["one"],["one"],["one"],["one","two"]]
        for response,texts in zip(invalid,batches):
            with self.assertRaises(RuntimeError): OpenAICompatibleEmbedder("model","key",opener=Opener([Response(response)])).embed(texts)

    def test_cohere_uses_distinct_document_and_query_modes(self):
        documents=[[1.0]*256,[0.0]*256]; query=[[0.5]*256]
        opener=Opener([Response({"embeddings":{"float":documents}}),Response({"embeddings":{"float":query}})])
        embedder=CohereEmbedder("embed-v4.0","secret",output_dimension=256,truncate="NONE",timeout=8,opener=opener)
        self.assertEqual(embedder.embed_documents(["one","two"]),documents)
        self.assertEqual(embedder.embed_query("query"),query[0])
        first,second=opener.requests
        self.assertEqual(first[0].full_url,"https://api.cohere.com/v2/embed")
        self.assertEqual(first[0].get_header("Authorization"),"Bearer secret")
        self.assertEqual(first[0].get_header("X-client-name"),"gideondb")
        self.assertEqual(first[1],8)
        self.assertEqual(json.loads(first[0].data),{"model":"embed-v4.0","texts":["one","two"],"input_type":"search_document","embedding_types":["float"],"truncate":"NONE","output_dimension":256})
        self.assertEqual(json.loads(second[0].data)["input_type"],"search_query")

    def test_cohere_rejects_unsafe_options_and_invalid_vectors(self):
        with self.assertRaises(ValueError): CohereEmbedder("model","key",base_url="http://example.com")
        with self.assertRaises(ValueError): CohereEmbedder("model","key",output_dimension=100)
        with self.assertRaises(ValueError): CohereEmbedder("model","key",truncate="MIDDLE")
        with self.assertRaises(ValueError): CohereEmbedder("model","key",opener=Opener([])).embed(["x"]*97)
        with self.assertRaises(RuntimeError): CohereEmbedder("model","key",opener=Opener([Response({"embeddings":{"float":[[math.nan]]}})])).embed(["one"])

    def test_semantic_store_uses_specialized_embedding_modes(self):
        class RetrievalEmbedder:
            def embed(self,_texts): raise AssertionError("generic mode should not be used")
            def embed_documents(self,texts): return [[1.0,0.0] for _ in texts]
            def embed_query(self,_text): return [0.0,1.0]
        client=FakeGideonDB(); store=SemanticStore(client,"docs",RetrievalEmbedder())
        store.upsert_documents([Document("one","document")]); store.search("query",1)
        self.assertEqual(client.batch[1][0]["vector"],[1.0,0.0])
        self.assertEqual(client.query[1],[0.0,1.0])


if __name__ == "__main__": unittest.main()
