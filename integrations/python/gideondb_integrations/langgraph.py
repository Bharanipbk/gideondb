"""Optional LangGraph long-term memory store (``langgraph.store.base.BaseStore``)."""

from __future__ import annotations

import asyncio
import hashlib
import json
import time
from collections.abc import Iterable, Sequence
from datetime import datetime, timezone
from typing import Any

from .memory import NEVER_EXPIRES

_SCROLL_PAGE = 200


def _embed(embedder: Any, texts: list[str]) -> list[list[float]]:
    """Accept a GideonDB ``Embedder`` (``embed``) or LangChain ``Embeddings``."""
    if hasattr(embedder, "embed"):
        return embedder.embed(texts)
    return embedder.embed_documents(texts)


def _record_id(namespace: tuple[str, ...], key: str) -> str:
    digest = hashlib.sha256(json.dumps([list(namespace), key]).encode()).hexdigest()
    return digest[:32]


def _field_text(value: dict[str, Any], fields: Sequence[str]) -> str:
    parts = []
    for field in fields:
        if field == "$":
            parts.append(json.dumps(value, sort_keys=True, default=str))
            continue
        current: Any = value
        for segment in field.split("."):
            current = current.get(segment) if isinstance(current, dict) else None
        if current is not None:
            parts.append(current if isinstance(current, str) else json.dumps(current, default=str))
    return "\n".join(parts) or json.dumps(value, sort_keys=True, default=str)


def _matches(value: dict[str, Any], conditions: dict[str, Any] | None) -> bool:
    for name, expected in (conditions or {}).items():
        actual = value.get(name)
        if isinstance(expected, dict):
            for operator, operand in expected.items():
                checks = {
                    "$eq": lambda: actual == operand, "$ne": lambda: actual != operand,
                    "$gt": lambda: actual is not None and actual > operand, "$gte": lambda: actual is not None and actual >= operand,
                    "$lt": lambda: actual is not None and actual < operand, "$lte": lambda: actual is not None and actual <= operand,
                }
                if operator not in checks:
                    raise ValueError(f"unsupported filter operator {operator!r}")
                if not checks[operator]():
                    return False
        elif actual != expected:
            return False
    return True


def _match_path(namespace: tuple[str, ...], path: Sequence[str], suffix: bool) -> bool:
    if len(path) > len(namespace):
        return False
    window = namespace[-len(path):] if suffix and path else namespace[: len(path)]
    return all(want == "*" or want == got for want, got in zip(path, window))


def create_langgraph_store(
    client: Any,
    collection: str,
    embedder: Any,
    *,
    index_fields: Sequence[str] = ("$",),
    clock: Any = time.time,
) -> Any:
    """Create a LangGraph ``BaseStore`` backed by a GideonDB collection.

    Every item is embedded (GideonDB records always carry a vector): the
    fields named by ``PutOp.index`` or ``index_fields``, where ``"$"`` is the
    whole value as JSON. The collection must already exist with the
    embedder's dimension. Searches without a query, value filters and
    namespace listing scan the collection, so keep one store per agent rather
    than one huge shared collection.
    """
    if not collection:
        raise ValueError("collection is required")
    try:
        from langgraph.store.base import BaseStore, GetOp, Item, ListNamespacesOp, PutOp, SearchItem, SearchOp
    except ImportError as error:
        raise ImportError("LangGraph integration requires the optional 'langgraph' package") from error

    def now() -> float:
        return float(clock())

    def to_item(record: dict[str, Any], *, search: bool = False, score: float | None = None) -> Any:
        payload = record.get("payload") or {}
        fields = {
            "namespace": tuple(payload["namespace"]), "key": payload["key"], "value": payload["value"],
            "created_at": datetime.fromisoformat(payload["created_at"]),
            "updated_at": datetime.fromisoformat(payload["updated_at"]),
        }
        if not search:
            return Item(**fields)
        return SearchItem(**fields, score=score)

    def live(record: dict[str, Any]) -> bool:
        return int((record.get("metadata") or {}).get("expires_at", NEVER_EXPIRES)) > now()

    def scan() -> Iterable[dict[str, Any]]:
        cursor = ""
        while True:
            page = client.scroll(collection, limit=_SCROLL_PAGE, cursor=cursor)
            for record in page.get("records", []):
                if "key" in (record.get("payload") or {}) and live(record):
                    yield record
            cursor = page.get("next_cursor", "")
            if not cursor:
                return

    def fetch(namespace: tuple[str, ...], key: str) -> dict[str, Any] | None:
        try:
            record = client.get(collection, _record_id(namespace, key))
        except Exception as error:
            if getattr(error, "status_code", None) == 404:
                return None
            raise
        return record if live(record) else None

    def put(op: Any) -> None:
        namespace, record_id = tuple(op.namespace), _record_id(tuple(op.namespace), op.key)
        if op.value is None:
            try:
                client.delete(collection, record_id)
            except Exception as error:
                if getattr(error, "status_code", None) != 404:
                    raise
            return
        stamp = datetime.fromtimestamp(now(), timezone.utc).isoformat()
        existing = fetch(namespace, op.key)
        created = (existing or {}).get("payload", {}).get("created_at", stamp)
        fields = op.index if isinstance(op.index, list) else index_fields
        vector = _embed(embedder, [_field_text(op.value, fields)])[0]
        ttl = getattr(op, "ttl", None)
        metadata: dict[str, Any] = {f"ns_{depth}": part for depth, part in enumerate(namespace)}
        metadata["ns_depth"] = len(namespace)
        metadata["expires_at"] = int(now() + ttl * 60) if ttl else NEVER_EXPIRES
        client.upsert(collection, {
            "id": record_id, "vector": vector, "metadata": metadata,
            "payload": {"namespace": list(namespace), "key": op.key, "value": op.value, "created_at": created, "updated_at": stamp},
        })

    def search(op: Any) -> list[Any]:
        prefix = tuple(op.namespace_prefix)
        if op.query:
            conditions: list[dict[str, Any]] = [{"expires_at": {"$gt": int(now())}}]
            conditions += [{f"ns_{depth}": part} for depth, part in enumerate(prefix)]
            wanted = op.offset + op.limit
            fetch_k = min(max(wanted * (4 if op.filter else 1), wanted), 1000)
            vector = _embed(embedder, [op.query])[0]
            hits = client.search(collection, vector, fetch_k, filter={"$and": conditions})
            items = [to_item(hit, search=True, score=float(hit["score"])) for hit in hits if "key" in (hit.get("payload") or {})]
        else:
            items = [to_item(record, search=True) for record in scan()]
            items.sort(key=lambda item: item.updated_at, reverse=True)
        items = [item for item in items if tuple(item.namespace[: len(prefix)]) == prefix and _matches(item.value, op.filter)]
        return items[op.offset : op.offset + op.limit]

    def list_namespaces(op: Any) -> list[tuple[str, ...]]:
        found = set()
        for record in scan():
            namespace = tuple(record["payload"]["namespace"])
            if all(_match_path(namespace, tuple(c.path), c.match_type == "suffix") for c in (op.match_conditions or ())):
                found.add(namespace[: op.max_depth] if op.max_depth else namespace)
        return sorted(found)[op.offset : op.offset + op.limit]

    class GideonDBStore(BaseStore):
        def batch(self, ops: Iterable[Any]) -> list[Any]:
            results: list[Any] = []
            for op in ops:
                if isinstance(op, GetOp):
                    record = fetch(tuple(op.namespace), op.key)
                    results.append(to_item(record) if record else None)
                elif isinstance(op, PutOp):
                    results.append(put(op))
                elif isinstance(op, SearchOp):
                    results.append(search(op))
                elif isinstance(op, ListNamespacesOp):
                    results.append(list_namespaces(op))
                else:
                    raise TypeError(f"unsupported store operation {type(op).__name__}")
            return results

        async def abatch(self, ops: Iterable[Any]) -> list[Any]:
            return await asyncio.to_thread(self.batch, list(ops))

    return GideonDBStore()
