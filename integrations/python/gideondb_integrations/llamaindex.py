"""Optional LlamaIndex vector-store bridge."""

from __future__ import annotations

from collections.abc import Sequence
from typing import Any


def create_llamaindex_vector_store(client: Any, collection: str, *, namespace: str = "") -> Any:
    """Create a LlamaIndex vector store backed by an existing collection."""
    if not collection:
        raise ValueError("collection is required")
    try:
        from llama_index.core.schema import MetadataMode, TextNode
        from llama_index.core.vector_stores.types import VectorStoreQueryMode, VectorStoreQueryResult
    except ImportError as error:
        raise ImportError("LlamaIndex integration requires the optional 'llama-index-core' package") from error

    class GideonDBVectorStore:
        stores_text = True
        is_embedding_query = True

        def __init__(self) -> None:
            self._client = client
            self._collection = collection
            self._namespace = namespace
            self._ref_nodes: dict[str, set[str]] = {}

        @property
        def client(self) -> Any:
            return self._client

        def add(self, nodes: Sequence[Any], **kwargs: Any) -> list[str]:
            values = list(nodes)
            if not values:
                return []
            record_namespace = kwargs.pop("namespace", self._namespace)
            if kwargs:
                raise TypeError(f"unsupported options: {', '.join(sorted(kwargs))}")
            records, ids = [], []
            for node in values:
                node_id = str(node.node_id)
                if not node_id:
                    raise ValueError("node IDs must be non-empty")
                metadata = dict(node.metadata or {})
                ref_doc_id = node.ref_doc_id
                if ref_doc_id:
                    metadata["llama_ref_doc_id"] = ref_doc_id
                    self._ref_nodes.setdefault(str(ref_doc_id), set()).add(node_id)
                record: dict[str, Any] = {
                    "id": node_id,
                    "vector": node.get_embedding(),
                    "metadata": metadata,
                    "payload": {"text": node.get_content(metadata_mode=MetadataMode.NONE)},
                }
                if record_namespace:
                    record["namespace"] = record_namespace
                records.append(record); ids.append(node_id)
            self._client.batch_upsert(self._collection, records)
            return ids

        async def async_add(self, nodes: Sequence[Any], **kwargs: Any) -> list[str]:
            return self.add(nodes, **kwargs)

        def delete(self, ref_doc_id: str, **kwargs: Any) -> None:
            delete_namespace = kwargs.pop("namespace", self._namespace)
            if kwargs:
                raise TypeError(f"unsupported options: {', '.join(sorted(kwargs))}")
            node_ids = self._ref_nodes.pop(ref_doc_id, {ref_doc_id})
            for node_id in sorted(node_ids):
                self._client.delete(self._collection, node_id, namespace=delete_namespace)

        async def adelete(self, ref_doc_id: str, **kwargs: Any) -> None:
            self.delete(ref_doc_id, **kwargs)

        def query(self, query: Any, **kwargs: Any) -> Any:
            if query.mode != VectorStoreQueryMode.DEFAULT:
                raise ValueError("only the default dense query mode is supported")
            if query.query_embedding is None:
                raise ValueError("query_embedding is required")
            query_namespace = kwargs.pop("namespace", self._namespace)
            if kwargs:
                raise TypeError(f"unsupported options: {', '.join(sorted(kwargs))}")
            metadata_filter = None
            if query.filters is not None:
                metadata_filter = {item.key: item.value for item in query.filters.legacy_filters()}
            results = self._client.search(self._collection, query.query_embedding, query.similarity_top_k, namespace=query_namespace, filter=metadata_filter)
            nodes, similarities, ids = [], [], []
            for result in results:
                node_id = str(result.get("id", ""))
                metadata = dict(result.get("metadata") or {})
                metadata.pop("llama_ref_doc_id", None)
                nodes.append(TextNode(text=str((result.get("payload") or {}).get("text", "")), id_=node_id, metadata=metadata))
                similarities.append(float(result["score"])); ids.append(node_id)
            return VectorStoreQueryResult(nodes=nodes, similarities=similarities, ids=ids)

        async def aquery(self, query: Any, **kwargs: Any) -> Any:
            return self.query(query, **kwargs)

    return GideonDBVectorStore()
