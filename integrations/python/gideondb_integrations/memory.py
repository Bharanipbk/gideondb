"""Agent memory compatible with the ``gideondb-mcp`` memory tools.

Memories use the same record layout as the MCP server's ``remember`` and
``recall`` tools, so an agent can write a memory through a framework adapter
and read it back through MCP, or the reverse: one collection, one namespace
per user, ``created_at``/``expires_at`` (Unix seconds) in metadata and the
text in ``payload.text``.
"""

from __future__ import annotations

import math
import time
import uuid
from collections.abc import Callable, Mapping
from typing import Any

from .embeddings import Embedder

CREATED_AT = "created_at"
EXPIRES_AT = "expires_at"
# The largest integer a float64 JSON number holds exactly, as in the MCP server.
NEVER_EXPIRES = (1 << 53) - 1
RECALL_OVERSAMPLING = 4


def _status(error: Exception) -> int | None:
    return getattr(error, "status_code", None)


class MemoryStore:
    """Per-user long-term memory with TTL and recency-weighted recall."""

    def __init__(
        self,
        client: Any,
        embedder: Embedder,
        *,
        collection: str = "agent_memory",
        clock: Callable[[], float] = time.time,
    ) -> None:
        if not collection:
            raise ValueError("collection is required")
        self._client, self._embedder, self._collection, self._clock = client, embedder, collection, clock
        self._ready = False

    @property
    def collection(self) -> str:
        return self._collection

    def _ensure_collection(self, dimension: int) -> None:
        if self._ready:
            return
        try:
            self._client.describe_collection(self._collection)
        except Exception as error:
            if _status(error) != 404:
                raise
            try:
                self._client.create_collection({
                    "name": self._collection, "dimension": dimension, "metric": "cosine",
                    "shard_count": 1, "index": {"type": "hnsw"},
                })
            except Exception as create_error:
                if _status(create_error) != 409:
                    raise
        self._ready = True

    def remember(
        self,
        user: str,
        text: str,
        *,
        metadata: Mapping[str, Any] | None = None,
        ttl_seconds: int | None = None,
        id: str | None = None,
    ) -> dict[str, Any]:
        if not user or not text:
            raise ValueError("user and text are required")
        if ttl_seconds is not None and ttl_seconds < 0:
            raise ValueError("ttl_seconds must not be negative")
        values = dict(metadata or {})
        for key in (CREATED_AT, EXPIRES_AT):
            if key in values:
                raise ValueError(f"metadata key {key!r} is reserved")
        vector = self._embedder.embed([text])[0]
        self._ensure_collection(len(vector))
        now = int(self._clock())
        expires_at = now + ttl_seconds if ttl_seconds else NEVER_EXPIRES
        values[CREATED_AT], values[EXPIRES_AT] = now, expires_at
        memory_id = id or uuid.uuid4().hex
        self._client.upsert(self._collection, {
            "id": memory_id, "vector": vector, "namespace": user,
            "metadata": values, "payload": {"text": text},
        })
        result: dict[str, Any] = {"id": memory_id, "created_at": now}
        if expires_at != NEVER_EXPIRES:
            result["expires_at"] = expires_at
        return result

    def recall(
        self,
        user: str,
        query: str,
        *,
        top_k: int = 5,
        recency_weight: float = 0.2,
        half_life_hours: float = 168.0,
        filter: Mapping[str, Any] | None = None,
    ) -> list[dict[str, Any]]:
        """Return memories ranked by
        ``(1 - recency_weight) * similarity + recency_weight * 0.5 ** (age_hours / half_life_hours)``.
        """
        if not user or not query:
            raise ValueError("user and query are required")
        if not 1 <= top_k <= 50:
            raise ValueError("top_k must be between 1 and 50")
        if not 0 <= recency_weight <= 1:
            raise ValueError("recency_weight must be between 0 and 1")
        if not half_life_hours > 0 or math.isinf(half_life_hours):
            raise ValueError("half_life_hours must be positive")
        vector = self._embedder.embed([query])[0]
        now = int(self._clock())
        condition: dict[str, Any] = {EXPIRES_AT: {"$gt": now}}
        if filter:
            condition = {"$and": [condition, dict(filter)]}
        try:
            results = self._client.search(
                self._collection, vector, min(top_k * RECALL_OVERSAMPLING, 200), namespace=user, filter=condition
            )
        except Exception as error:
            if _status(error) == 404:
                return []
            raise
        memories = []
        for result in results:
            metadata = dict(result.get("metadata") or {})
            created_at = int(metadata.pop(CREATED_AT, now))
            expires_at = int(metadata.pop(EXPIRES_AT, NEVER_EXPIRES))
            age_hours = max(0.0, (now - created_at) / 3600)
            similarity = float(result["score"])
            item: dict[str, Any] = {
                "id": result["id"], "text": (result.get("payload") or {}).get("text", ""),
                "score": (1 - recency_weight) * similarity + recency_weight * 2 ** (-age_hours / half_life_hours),
                "similarity": similarity, "created_at": created_at,
            }
            if expires_at != NEVER_EXPIRES:
                item["expires_at"] = expires_at
            if metadata:
                item["metadata"] = metadata
            memories.append(item)
        memories.sort(key=lambda memory: memory["score"], reverse=True)
        return memories[:top_k]

    def forget(self, user: str, id: str) -> None:
        if not user or not id:
            raise ValueError("user and id are required")
        self._client.delete(self._collection, id, namespace=user)

    def prune(self, user: str) -> int:
        """Delete the user's expired memories and return how many were removed."""
        if not user:
            raise ValueError("user is required")
        now, expired, cursor = int(self._clock()), [], ""
        while True:
            try:
                page = self._client.scroll(self._collection, namespace=user, limit=200, cursor=cursor)
            except Exception as error:
                if _status(error) == 404:
                    return 0
                raise
            for record in page.get("records", []):
                if record.get("namespace", "") == user and int((record.get("metadata") or {}).get(EXPIRES_AT, NEVER_EXPIRES)) <= now:
                    expired.append(record["id"])
            cursor = page.get("next_cursor", "")
            if not cursor:
                break
        for memory_id in expired:
            self._client.delete(self._collection, memory_id, namespace=user)
        return len(expired)


def memory_tools(memory: MemoryStore, user: str) -> list[Callable[..., Any]]:
    """Return plain ``remember`` / ``recall`` / ``forget`` functions bound to one user.

    They have type hints and docstrings, so agent frameworks that build tools
    from Python functions accept them directly, for example OpenAI Agents
    ``function_tool(fn)``, CrewAI ``tool(fn)`` or LangChain ``StructuredTool.from_function(fn)``.
    """
    if not user:
        raise ValueError("user is required")

    def remember(text: str, ttl_seconds: int = 0) -> str:
        """Store a fact about the user for later. ttl_seconds=0 keeps it forever."""
        return memory.remember(user, text, ttl_seconds=ttl_seconds or None)["id"]

    def recall(query: str, top_k: int = 5) -> list[str]:
        """Recall the user's memories most relevant to the query, newest favored."""
        return [item["text"] for item in memory.recall(user, query, top_k=top_k)]

    def forget(memory_id: str) -> str:
        """Delete one memory by the id that remember returned."""
        memory.forget(user, memory_id)
        return memory_id

    return [remember, recall, forget]
