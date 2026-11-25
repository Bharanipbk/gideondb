"""Dependency-free synchronous client for the experimental GideonDB REST API."""

from __future__ import annotations

import json
import os
import ssl
import uuid
from collections.abc import Callable, Mapping, Sequence
from typing import IO, Any
from urllib.error import HTTPError, URLError
from urllib.parse import quote, urlencode, urlsplit
from urllib.request import Request, urlopen

MAX_RESPONSE_BYTES = 16 << 20


class APIError(Exception):
    def __init__(self, status_code: int, code: str = "", message: str = "") -> None:
        self.status_code = status_code
        self.code = code
        self.message = message
        detail = f"{code}: {message}" if code else "request failed"
        super().__init__(f"gideondb: {detail} (HTTP {status_code})")


class TransportError(Exception):
    """The request did not produce a valid bounded GideonDB response."""


OpenFunction = Callable[..., Any]


class Client:
    def __init__(
        self,
        base_url: str,
        *,
        api_key: str = "",
        timeout: float = 30.0,
        ssl_context: ssl.SSLContext | None = None,
        opener: OpenFunction | None = None,
    ) -> None:
        parsed = urlsplit(base_url.strip())
        if (
            parsed.scheme not in {"http", "https"}
            or not parsed.netloc
            or parsed.username is not None
            or parsed.password is not None
            or parsed.path not in {"", "/"}
            or parsed.query
            or parsed.fragment
        ):
            raise ValueError("base_url must be an HTTP(S) origin")
        if timeout <= 0:
            raise ValueError("timeout must be positive")
        self._base_url = f"{parsed.scheme}://{parsed.netloc}"
        self._api_key = api_key
        self._timeout = timeout
        self._ssl_context = ssl_context
        self._opener = opener

    def health(self) -> None:
        self._request("GET", "/v1/health")

    def ready(self) -> None:
        self._request("GET", "/v1/ready")

    def list_collections(self) -> list[dict[str, Any]]:
        return self._request("GET", "/v1/collections")["collections"]

    def create_collection(self, config: Mapping[str, Any]) -> dict[str, Any]:
        return self._request("POST", "/v1/collections", config)

    def describe_collection(self, name: str) -> dict[str, Any]:
        return self._request("GET", self._collection_path(name))

    def delete_collection(self, name: str) -> None:
        self._request("DELETE", self._collection_path(name))

    def upsert(self, collection: str, record: Mapping[str, Any]) -> dict[str, Any]:
        return self._request("POST", f"{self._collection_path(collection)}/vectors", record)

    def batch_upsert(
        self, collection: str, records: Sequence[Mapping[str, Any]]
    ) -> list[dict[str, Any]]:
        response = self._request(
            "POST", f"{self._collection_path(collection)}/vectors/batch", {"records": records}
        )
        return response["records"]

    def get(self, collection: str, record_id: str, *, namespace: str = "") -> dict[str, Any]:
        path = f"{self._collection_path(collection)}/vectors/{quote(record_id, safe='')}"
        if namespace:
            path += "?" + urlencode({"namespace": namespace})
        return self._request("GET", path)

    def scroll(
        self, collection: str, *, namespace: str = "", limit: int = 50,
        cursor: str = "", include_vector: bool = False,
    ) -> dict[str, Any]:
        return self._scroll(self._collection_path(collection), namespace, limit, cursor, include_vector)

    def distributed_scroll(
        self, collection: str, *, namespace: str = "", limit: int = 50,
        cursor: str = "", include_vector: bool = False,
    ) -> dict[str, Any]:
        """Scroll one authoritative owner per shard with an epoch-fenced cursor."""
        return self._scroll(self._cluster_collection_path(collection), namespace, limit, cursor, include_vector)

    def _scroll(self, collection_path: str, namespace: str, limit: int, cursor: str, include_vector: bool) -> dict[str, Any]:
        if limit < 1 or limit > 200:
            raise ValueError("limit must be between 1 and 200")
        query: dict[str, Any] = {"limit": limit}
        if namespace:
            query["namespace"] = namespace
        if cursor:
            query["cursor"] = cursor
        if include_vector:
            query["include_vector"] = "true"
        return self._request("GET", f"{collection_path}/vectors?{urlencode(query)}")

    def delete(self, collection: str, record_id: str, *, namespace: str = "") -> None:
        path = f"{self._collection_path(collection)}/vectors/{quote(record_id, safe='')}"
        if namespace:
            path += "?" + urlencode({"namespace": namespace})
        self._request("DELETE", path)

    def search(
        self,
        collection: str,
        vector: Sequence[float] | None,
        top_k: int,
        *,
        namespace: str = "",
        filter: Mapping[str, Any] | None = None,
        text: str = "",
        sparse_vector: Mapping[str, float] | None = None,
        alpha: float | None = None,
        fusion: str = "",
        syntax: str = "",
        highlight: Mapping[str, Any] | None = None,
        facets: Sequence[Mapping[str, Any]] | None = None,
        aggregations: Mapping[str, Mapping[str, Any]] | None = None,
        sort: Sequence[Mapping[str, Any]] | None = None,
        sort_candidates: int | None = None,
    ) -> list[dict[str, Any]]:
        return self.search_response(
            collection, vector, top_k, namespace=namespace, filter=filter, text=text,
            sparse_vector=sparse_vector, alpha=alpha, fusion=fusion, syntax=syntax,
            highlight=highlight, facets=facets, aggregations=aggregations, sort=sort,
            sort_candidates=sort_candidates,
        )["results"]

    def search_response(
        self,
        collection: str,
        vector: Sequence[float] | None,
        top_k: int,
        *,
        namespace: str = "",
        filter: Mapping[str, Any] | None = None,
        text: str = "",
        sparse_vector: Mapping[str, float] | None = None,
        alpha: float | None = None,
        fusion: str = "",
        syntax: str = "",
        highlight: Mapping[str, Any] | None = None,
        facets: Sequence[Mapping[str, Any]] | None = None,
        aggregations: Mapping[str, Mapping[str, Any]] | None = None,
        sort: Sequence[Mapping[str, Any]] | None = None,
        sort_candidates: int | None = None,
    ) -> dict[str, Any]:
        """Search and return the whole response: results (with highlights), facets and aggregations."""
        body = self._search_body(vector, top_k, namespace, filter, text, sparse_vector, alpha, fusion)
        self._add_search_features(body, syntax, highlight, facets, aggregations, sort, sort_candidates)
        return self._request("POST", f"{self._collection_path(collection)}/search", body)

    def distributed_search(
        self,
        collection: str,
        vector: Sequence[float] | None,
        top_k: int,
        *,
        namespace: str = "",
        filter: Mapping[str, Any] | None = None,
        allow_partial: bool = False,
        text: str = "",
        sparse_vector: Mapping[str, float] | None = None,
        alpha: float | None = None,
        fusion: str = "",
        syntax: str = "",
        highlight: Mapping[str, Any] | None = None,
        facets: Sequence[Mapping[str, Any]] | None = None,
        aggregations: Mapping[str, Mapping[str, Any]] | None = None,
        sort: Sequence[Mapping[str, Any]] | None = None,
        sort_candidates: int | None = None,
    ) -> dict[str, Any]:
        body = self._search_body(vector, top_k, namespace, filter, text, sparse_vector, alpha, fusion)
        self._add_search_features(body, syntax, highlight, facets, aggregations, sort, sort_candidates)
        if allow_partial:
            body["allow_partial"] = True
        return self._request(
            "POST", f"{self._cluster_collection_path(collection)}/search", body
        )

    def distributed_batch_upsert(
        self,
        collection: str,
        records: Sequence[Mapping[str, Any]],
        *,
        acknowledgement: str = "",
    ) -> dict[str, Any]:
        body: dict[str, Any] = {"records": records}
        if acknowledgement:
            body["acknowledgement"] = acknowledgement
        return self._request(
            "POST", f"{self._cluster_collection_path(collection)}/vectors/batch", body
        )

    def suggest(self, collection: str, text: str, size: int | None = None) -> dict[str, Any]:
        """Complete the last word of text; returns completions and an optional did_you_mean."""
        query: dict[str, Any] = {"text": text}
        if size is not None:
            if size < 1:
                raise ValueError("size must be positive")
            query["size"] = size
        return self._request("GET", f"{self._collection_path(collection)}/suggest?{urlencode(query)}")

    def ingest_document(
        self,
        collection: str,
        filename: str,
        content: bytes | IO[bytes],
        *,
        id: str | None = None,
        namespace: str | None = None,
        metadata: Mapping[str, Any] | None = None,
        chunk_size: int | None = None,
        chunk_overlap: int | None = None,
        content_type: str | None = None,
    ) -> dict[str, Any]:
        """Upload a text, Markdown, HTML, PDF or DOCX file for server-side extraction and chunking."""
        data = content if isinstance(content, (bytes, bytearray)) else content.read()
        fields: dict[str, str] = {}
        if id:
            fields["id"] = id
        if namespace:
            fields["namespace"] = namespace
        if metadata is not None:
            fields["metadata"] = json.dumps(metadata, separators=(",", ":"))
        if chunk_size is not None:
            fields["chunk_size"] = str(chunk_size)
        if chunk_overlap is not None:
            fields["chunk_overlap"] = str(chunk_overlap)
        boundary = uuid.uuid4().hex
        body, form_type = self._multipart(boundary, fields, os.path.basename(filename), bytes(data), content_type)
        return self._request(
            "POST", f"{self._collection_path(collection)}/documents", body, content_type=form_type
        )

    @staticmethod
    def _multipart(
        boundary: str, fields: Mapping[str, str], filename: str, data: bytes, content_type: str | None
    ) -> tuple[bytes, str]:
        def quoted(value: str) -> str:
            return value.replace("\\", "\\\\").replace('"', '\\"').replace("\r", "").replace("\n", "")

        parts: list[bytes] = []
        for key, value in fields.items():
            parts.append(
                f'--{boundary}\r\nContent-Disposition: form-data; name="{key}"\r\n\r\n{value}\r\n'.encode()
            )
        header = f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="{quoted(filename)}"\r\n'
        if content_type:
            header += f"Content-Type: {content_type}\r\n"
        parts.append(header.encode() + b"\r\n" + data + b"\r\n")
        parts.append(f"--{boundary}--\r\n".encode())
        return b"".join(parts), f"multipart/form-data; boundary={boundary}"

    @staticmethod
    def _add_search_features(
        body: dict[str, Any],
        syntax: str,
        highlight: Mapping[str, Any] | None,
        facets: Sequence[Mapping[str, Any]] | None,
        aggregations: Mapping[str, Mapping[str, Any]] | None,
        sort: Sequence[Mapping[str, Any]] | None,
        sort_candidates: int | None,
    ) -> None:
        if syntax:
            if syntax != "simple":
                raise ValueError('syntax must be "simple"')
            body["syntax"] = syntax
        if highlight is not None:
            body["highlight"] = dict(highlight)
        if facets:
            body["facets"] = [dict(facet) for facet in facets]
        if aggregations:
            body["aggregations"] = {name: dict(spec) for name, spec in aggregations.items()}
        if sort:
            body["sort"] = [dict(field) for field in sort]
        if sort_candidates is not None:
            body["sort_candidates"] = sort_candidates

    @staticmethod
    def _search_body(
        vector: Sequence[float] | None,
        top_k: int,
        namespace: str,
        filter: Mapping[str, Any] | None,
        text: str = "",
        sparse_vector: Mapping[str, float] | None = None,
        alpha: float | None = None,
        fusion: str = "",
    ) -> dict[str, Any]:
        if vector is None and not text and not sparse_vector:
            raise ValueError("search requires a vector, text or sparse_vector")
        if alpha is not None and not 0 <= alpha <= 1:
            raise ValueError("alpha must be between 0 and 1")
        if fusion and fusion not in {"weighted", "rrf"}:
            raise ValueError('fusion must be "weighted" or "rrf"')
        body: dict[str, Any] = {"top_k": top_k}
        if vector is not None:
            body["vector"] = vector
        if text:
            body["text"] = text
        if sparse_vector:
            body["sparse_vector"] = dict(sparse_vector)
        if alpha is not None:
            body["alpha"] = alpha
        if fusion:
            body["fusion"] = fusion
        if namespace:
            body["namespace"] = namespace
        if filter is not None:
            body["filter"] = filter
        return body

    @staticmethod
    def _collection_path(name: str) -> str:
        return f"/v1/collections/{quote(name, safe='')}"

    @staticmethod
    def _cluster_collection_path(name: str) -> str:
        return f"/v1/cluster/collections/{quote(name, safe='')}"

    def _request(self, method: str, path: str, body: Any = None, *, content_type: str = "") -> Any:
        if content_type:
            encoded = body
        else:
            encoded = None if body is None else json.dumps(body, separators=(",", ":")).encode()
        headers = {"Accept": "application/json", "User-Agent": "gideondb-python/dev"}
        if encoded is not None:
            headers["Content-Type"] = content_type or "application/json"
        if self._api_key:
            headers["Authorization"] = f"Bearer {self._api_key}"
        request = Request(self._base_url + path, data=encoded, headers=headers, method=method)
        try:
            response = self._open(request)
            with response:
                payload = response.read(MAX_RESPONSE_BYTES + 1)
                status = response.status
        except HTTPError as error:
            payload = error.read(MAX_RESPONSE_BYTES + 1)
            if len(payload) > MAX_RESPONSE_BYTES:
                raise TransportError("error response exceeds 16 MiB") from error
            try:
                detail = json.loads(payload)
            except (UnicodeDecodeError, json.JSONDecodeError):
                detail = {}
            raise APIError(error.code, detail.get("code", ""), detail.get("message", "")) from error
        except (OSError, URLError) as error:
            raise TransportError(f"request failed: {error}") from error
        if len(payload) > MAX_RESPONSE_BYTES:
            raise TransportError("response exceeds 16 MiB")
        if status == 204 or not payload:
            return None
        try:
            return json.loads(payload)
        except (UnicodeDecodeError, json.JSONDecodeError) as error:
            raise TransportError("response is not valid JSON") from error

    def _open(self, request: Request) -> Any:
        if self._opener is not None:
            return self._opener(request, timeout=self._timeout)
        return urlopen(request, timeout=self._timeout, context=self._ssl_context)
