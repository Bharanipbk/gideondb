"""Optional LangChain vector-store bridge."""

from __future__ import annotations

from collections.abc import Iterable, Mapping
from typing import Any
from uuid import uuid4


def create_langchain_vector_store(
    client: Any,
    collection: str,
    embeddings: Any,
    *,
    namespace: str = "",
) -> Any:
    """Create a LangChain ``VectorStore`` backed by an existing collection."""
    if not collection:
        raise ValueError("collection is required")
    try:
        from langchain_core.documents import Document
        from langchain_core.vectorstores import VectorStore
    except ImportError as error:
        raise ImportError("LangChain integration requires the optional 'langchain-core' package") from error

    class VectorDBVectorStore(VectorStore):
        def __init__(self) -> None:
            self._client = client
            self._collection = collection
            self._embeddings = embeddings
            self._namespace = namespace

        @property
        def embeddings(self) -> Any:
            return self._embeddings

        def add_texts(
            self,
            texts: Iterable[str],
            metadatas: list[dict[str, Any]] | None = None,
            *,
            ids: list[str] | None = None,
            **kwargs: Any,
        ) -> list[str]:
            values = list(texts)
            if not values or any(not isinstance(text, str) or not text for text in values):
                raise ValueError("texts must contain non-empty strings")
            if metadatas is not None and len(metadatas) != len(values):
                raise ValueError("metadatas must match texts")
            if ids is not None and len(ids) != len(values):
                raise ValueError("ids must match texts")
            record_ids = list(ids) if ids is not None else [str(uuid4()) for _ in values]
            if any(not value for value in record_ids) or len(set(record_ids)) != len(record_ids):
                raise ValueError("ids must be non-empty and unique")
            record_namespace = kwargs.pop("namespace", self._namespace)
            if kwargs:
                raise TypeError(f"unsupported options: {', '.join(sorted(kwargs))}")
            vectors = self._embeddings.embed_documents(values)
            if len(vectors) != len(values):
                raise RuntimeError("embedding count does not match texts")
            records = []
            for index, (record_id, text, vector) in enumerate(zip(record_ids, values, vectors, strict=True)):
                record: dict[str, Any] = {"id": record_id, "vector": vector, "payload": {"text": text}}
                if metadatas is not None:
                    record["metadata"] = dict(metadatas[index])
                if record_namespace:
                    record["namespace"] = record_namespace
                records.append(record)
            self._client.batch_upsert(self._collection, records)
            return record_ids

        def similarity_search(self, query: str, k: int = 4, **kwargs: Any) -> list[Any]:
            return [document for document, _score in self.similarity_search_with_score(query, k, **kwargs)]

        def similarity_search_with_score(self, query: str, k: int = 4, **kwargs: Any) -> list[tuple[Any, float]]:
            if k <= 0:
                raise ValueError("k must be positive")
            result_namespace = kwargs.pop("namespace", self._namespace)
            metadata_filter: Mapping[str, Any] | None = kwargs.pop("filter", None)
            if kwargs:
                raise TypeError(f"unsupported options: {', '.join(sorted(kwargs))}")
            vector = self._embeddings.embed_query(query)
            results = self._client.search(self._collection, vector, k, namespace=result_namespace, filter=metadata_filter)
            documents = []
            for result in results:
                payload = result.get("payload") or {}
                metadata = dict(result.get("metadata") or {})
                metadata["vectordb_id"] = result.get("id", "")
                documents.append((Document(page_content=str(payload.get("text", "")), metadata=metadata, id=result.get("id")), float(result["score"])))
            return documents

        def delete(self, ids: list[str] | None = None, **kwargs: Any) -> bool:
            if ids is None:
                raise ValueError("ids are required")
            delete_namespace = kwargs.pop("namespace", self._namespace)
            if kwargs:
                raise TypeError(f"unsupported options: {', '.join(sorted(kwargs))}")
            for record_id in ids:
                self._client.delete(self._collection, record_id, namespace=delete_namespace)
            return True

    return VectorDBVectorStore()
