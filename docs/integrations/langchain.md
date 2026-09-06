# LangChain integration

`create_langchain_vector_store` creates a LangChain `VectorStore` backed by an
existing VectorDB collection. The integration is loaded lazily: the core Python
SDK and embedding helpers remain dependency-free, while applications that use
this bridge install `langchain-core` explicitly.

```sh
pip install langchain-core
```

```python
from langchain_openai import OpenAIEmbeddings
from vectordb import Client
from vectordb_integrations import create_langchain_vector_store

db = Client("http://127.0.0.1:6333", api_key="...")
store = create_langchain_vector_store(
    db,
    "documents",
    OpenAIEmbeddings(model="text-embedding-3-small"),
    namespace="tenant-a",
)

store.add_texts(
    ["VectorDB stores dense vectors."],
    [{"kind": "guide"}],
    ids=["intro"],
)
documents = store.similarity_search("What does VectorDB store?", k=4)
```

The bridge implements `add_texts`, `similarity_search`,
`similarity_search_with_score`, and ID-based `delete`. It preserves the source
text in payload, metadata in VectorDB metadata, and the record ID on returned
LangChain documents. `namespace` and VectorDB `filter` options can be supplied
to searches. Collection creation remains explicit so its vector dimension and
distance metric cannot silently disagree with the embedding model.

This surface follows LangChain's current
[`VectorStore` interface](https://github.com/langchain-ai/langchain/blob/master/libs/core/langchain_core/vectorstores/base.py).
