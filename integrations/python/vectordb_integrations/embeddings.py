"""LLM-agnostic text embedding and VectorDB ingestion helpers."""

from __future__ import annotations

import json
import math
from collections.abc import Callable, Mapping, Sequence
from dataclasses import dataclass
from typing import Any, Protocol
from urllib.error import HTTPError, URLError
from urllib.parse import quote, urlsplit
from urllib.request import Request, urlopen

MAX_RESPONSE_BYTES = 16 << 20


class Embedder(Protocol):
    """Minimal contract implemented by any batch text embedding provider."""

    def embed(self, texts: Sequence[str]) -> list[list[float]]: ...


@dataclass(frozen=True)
class Document:
    id: str
    text: str
    metadata: Mapping[str, Any] | None = None
    payload: Mapping[str, Any] | None = None
    namespace: str = ""


class OllamaEmbedder:
    """Dependency-free adapter for Ollama's native POST /api/embed endpoint."""

    def __init__(
        self,
        model: str,
        *,
        base_url: str = "http://127.0.0.1:11434",
        timeout: float = 30.0,
        dimensions: int | None = None,
        truncate: bool = True,
        opener: Callable[..., Any] | None = None,
    ) -> None:
        parsed = urlsplit(base_url.strip())
        if parsed.scheme not in {"http", "https"} or not parsed.netloc or parsed.username is not None or parsed.password is not None or parsed.path not in {"", "/"} or parsed.query or parsed.fragment:
            raise ValueError("base_url must be an HTTP(S) origin")
        if not model.strip():
            raise ValueError("model is required")
        if timeout <= 0:
            raise ValueError("timeout must be positive")
        if dimensions is not None and dimensions <= 0:
            raise ValueError("dimensions must be positive")
        self._url = f"{parsed.scheme}://{parsed.netloc}/api/embed"
        self._model, self._timeout, self._dimensions = model, timeout, dimensions
        self._truncate, self._opener = truncate, opener

    def embed(self, texts: Sequence[str]) -> list[list[float]]:
        values = list(texts)
        if not values or len(values) > 1000 or any(not isinstance(text, str) or not text for text in values):
            raise ValueError("texts must contain between 1 and 1000 non-empty strings")
        body: dict[str, Any] = {"model": self._model, "input": values, "truncate": self._truncate}
        if self._dimensions is not None:
            body["dimensions"] = self._dimensions
        request = Request(self._url, data=json.dumps(body, separators=(",", ":")).encode(), headers={"Accept":"application/json", "Content-Type":"application/json", "User-Agent":"vectordb-ollama/dev"}, method="POST")
        try:
            response = self._open(request)
            with response:
                payload = response.read(MAX_RESPONSE_BYTES + 1)
        except HTTPError as error:
            detail = error.read(4096).decode(errors="replace")
            raise RuntimeError(f"Ollama embedding request failed (HTTP {error.code}): {detail}") from error
        except (OSError, URLError) as error:
            raise RuntimeError(f"Ollama embedding request failed: {error}") from error
        if len(payload) > MAX_RESPONSE_BYTES:
            raise RuntimeError("Ollama embedding response exceeds 16 MiB")
        try:
            embeddings = json.loads(payload)["embeddings"]
        except (UnicodeDecodeError, json.JSONDecodeError, KeyError, TypeError) as error:
            raise RuntimeError("Ollama returned an invalid embedding response") from error
        if not isinstance(embeddings, list) or len(embeddings) != len(values):
            raise RuntimeError("Ollama returned a mismatched embedding count")
        result: list[list[float]] = []
        expected_dimension: int | None = self._dimensions
        for embedding in embeddings:
            if not isinstance(embedding, list) or not embedding:
                raise RuntimeError("Ollama returned an empty embedding")
            try:
                vector = [float(value) for value in embedding]
            except (TypeError, ValueError) as error:
                raise RuntimeError("Ollama returned a non-numeric embedding") from error
            if any(not math.isfinite(value) for value in vector):
                raise RuntimeError("Ollama returned a non-finite embedding")
            if expected_dimension is None:
                expected_dimension = len(vector)
            if len(vector) != expected_dimension:
                raise RuntimeError("Ollama returned inconsistent embedding dimensions")
            result.append(vector)
        return result

    def _open(self, request: Request) -> Any:
        if self._opener is not None:
            return self._opener(request, timeout=self._timeout)
        return urlopen(request, timeout=self._timeout)


class HuggingFaceEmbedder:
    """Adapter for Hugging Face Inference Providers feature extraction."""

    def __init__(
        self,
        model: str,
        token: str,
        *,
        endpoint_url: str = "",
        timeout: float = 30.0,
        normalize: bool = True,
        truncate: bool = True,
        prompt_name: str = "",
        opener: Callable[..., Any] | None = None,
    ) -> None:
        if not model.strip():
            raise ValueError("model is required")
        if not token.strip():
            raise ValueError("token is required")
        if timeout <= 0:
            raise ValueError("timeout must be positive")
        url = endpoint_url.strip() or f"https://router.huggingface.co/hf-inference/models/{quote(model, safe='/')}/pipeline/feature-extraction"
        parsed = urlsplit(url)
        if parsed.scheme != "https" or not parsed.netloc or parsed.username is not None or parsed.password is not None or parsed.query or parsed.fragment:
            raise ValueError("endpoint_url must be an HTTPS URL without credentials, query, or fragment")
        self._url, self._model, self._token, self._timeout = url, model, token, timeout
        self._normalize, self._truncate, self._prompt_name, self._opener = normalize, truncate, prompt_name, opener

    def embed(self, texts: Sequence[str]) -> list[list[float]]:
        values = list(texts)
        if not values or len(values) > 1000 or any(not isinstance(text, str) or not text for text in values):
            raise ValueError("texts must contain between 1 and 1000 non-empty strings")
        body: dict[str, Any] = {"inputs": values, "normalize": self._normalize, "truncate": self._truncate}
        if self._prompt_name:
            body["prompt_name"] = self._prompt_name
        request = Request(self._url, data=json.dumps(body, separators=(",", ":")).encode(), headers={"Accept":"application/json", "Content-Type":"application/json", "Authorization":f"Bearer {self._token}", "User-Agent":"vectordb-huggingface/dev"}, method="POST")
        try:
            response = self._open(request)
            with response:
                payload = response.read(MAX_RESPONSE_BYTES + 1)
        except HTTPError as error:
            detail = error.read(4096).decode(errors="replace")
            raise RuntimeError(f"Hugging Face embedding request failed (HTTP {error.code}): {detail}") from error
        except (OSError, URLError) as error:
            raise RuntimeError(f"Hugging Face embedding request failed: {error}") from error
        if len(payload) > MAX_RESPONSE_BYTES:
            raise RuntimeError("Hugging Face embedding response exceeds 16 MiB")
        try:
            embeddings = json.loads(payload)
        except (UnicodeDecodeError, json.JSONDecodeError) as error:
            raise RuntimeError("Hugging Face returned an invalid embedding response") from error
        if not isinstance(embeddings, list) or len(embeddings) != len(values):
            raise RuntimeError("Hugging Face returned a mismatched embedding count")
        result: list[list[float]] = []
        dimension: int | None = None
        for embedding in embeddings:
            if not isinstance(embedding, list) or not embedding or any(isinstance(value, list) for value in embedding):
                raise RuntimeError("Hugging Face did not return sentence-level embeddings")
            try:
                vector = [float(value) for value in embedding]
            except (TypeError, ValueError) as error:
                raise RuntimeError("Hugging Face returned a non-numeric embedding") from error
            if any(not math.isfinite(value) for value in vector):
                raise RuntimeError("Hugging Face returned a non-finite embedding")
            if dimension is None:
                dimension = len(vector)
            if len(vector) != dimension:
                raise RuntimeError("Hugging Face returned inconsistent embedding dimensions")
            result.append(vector)
        return result

    def _open(self, request: Request) -> Any:
        if self._opener is not None:
            return self._opener(request, timeout=self._timeout)
        return urlopen(request, timeout=self._timeout)


class OpenAICompatibleEmbedder:
    """Dependency-free adapter for OpenAI-compatible POST /embeddings APIs."""

    def __init__(
        self,
        model: str,
        api_key: str,
        *,
        base_url: str = "https://api.openai.com/v1",
        timeout: float = 30.0,
        dimensions: int | None = None,
        user: str = "",
        opener: Callable[..., Any] | None = None,
    ) -> None:
        if not model.strip():
            raise ValueError("model is required")
        if not api_key.strip():
            raise ValueError("api_key is required")
        if timeout <= 0:
            raise ValueError("timeout must be positive")
        if dimensions is not None and dimensions <= 0:
            raise ValueError("dimensions must be positive")
        parsed = urlsplit(base_url.strip())
        loopback = parsed.hostname in {"localhost", "127.0.0.1", "::1"}
        if (parsed.scheme != "https" and not (parsed.scheme == "http" and loopback)) or not parsed.netloc or parsed.username is not None or parsed.password is not None or parsed.query or parsed.fragment:
            raise ValueError("base_url must be HTTPS, or HTTP on a loopback host, without credentials, query, or fragment")
        path = parsed.path.rstrip("/")
        self._url = f"{parsed.scheme}://{parsed.netloc}{path}/embeddings"
        self._model, self._api_key, self._timeout = model, api_key, timeout
        self._dimensions, self._user, self._opener = dimensions, user, opener

    def embed(self, texts: Sequence[str]) -> list[list[float]]:
        values = list(texts)
        if not values or len(values) > 2048 or any(not isinstance(value, str) or not value for value in values):
            raise ValueError("texts must contain between 1 and 2048 non-empty strings")
        body: dict[str, Any] = {"model": self._model, "input": values, "encoding_format": "float"}
        if self._dimensions is not None:
            body["dimensions"] = self._dimensions
        if self._user:
            body["user"] = self._user
        request = Request(self._url, data=json.dumps(body, separators=(",", ":")).encode(), headers={"Accept":"application/json", "Content-Type":"application/json", "Authorization":f"Bearer {self._api_key}", "User-Agent":"vectordb-openai-compatible/dev"}, method="POST")
        try:
            response = self._open(request)
            with response:
                payload = response.read(MAX_RESPONSE_BYTES + 1)
        except HTTPError as error:
            detail = error.read(4096).decode(errors="replace")
            raise RuntimeError(f"OpenAI-compatible embedding request failed (HTTP {error.code}): {detail}") from error
        except (OSError, URLError) as error:
            raise RuntimeError(f"OpenAI-compatible embedding request failed: {error}") from error
        if len(payload) > MAX_RESPONSE_BYTES:
            raise RuntimeError("OpenAI-compatible embedding response exceeds 16 MiB")
        try:
            data = json.loads(payload)["data"]
        except (UnicodeDecodeError, json.JSONDecodeError, KeyError, TypeError) as error:
            raise RuntimeError("OpenAI-compatible server returned an invalid embedding response") from error
        if not isinstance(data, list) or len(data) != len(values):
            raise RuntimeError("OpenAI-compatible server returned a mismatched embedding count")
        result: list[list[float] | None] = [None] * len(values)
        expected_dimension: int | None = self._dimensions
        for item in data:
            if not isinstance(item, dict) or type(item.get("index")) is not int or not isinstance(item.get("embedding"), list) or not item["embedding"]:
                raise RuntimeError("OpenAI-compatible server returned an invalid embedding item")
            index = item["index"]
            if index < 0 or index >= len(values) or result[index] is not None:
                raise RuntimeError("OpenAI-compatible server returned invalid embedding indices")
            try:
                vector = [float(value) for value in item["embedding"]]
            except (TypeError, ValueError) as error:
                raise RuntimeError("OpenAI-compatible server returned a non-numeric embedding") from error
            if any(not math.isfinite(value) for value in vector):
                raise RuntimeError("OpenAI-compatible server returned a non-finite embedding")
            if expected_dimension is None:
                expected_dimension = len(vector)
            if len(vector) != expected_dimension:
                raise RuntimeError("OpenAI-compatible server returned inconsistent embedding dimensions")
            result[index] = vector
        if any(vector is None for vector in result):
            raise RuntimeError("OpenAI-compatible server returned invalid embedding indices")
        return [vector for vector in result if vector is not None]

    def _open(self, request: Request) -> Any:
        if self._opener is not None:
            return self._opener(request, timeout=self._timeout)
        return urlopen(request, timeout=self._timeout)


class CohereEmbedder:
    """Dependency-free Cohere v2 adapter with retrieval-specific input modes."""

    def __init__(
        self,
        model: str,
        api_key: str,
        *,
        base_url: str = "https://api.cohere.com",
        timeout: float = 30.0,
        output_dimension: int | None = None,
        truncate: str = "END",
        opener: Callable[..., Any] | None = None,
    ) -> None:
        if not model.strip():
            raise ValueError("model is required")
        if not api_key.strip():
            raise ValueError("api_key is required")
        if timeout <= 0:
            raise ValueError("timeout must be positive")
        if output_dimension is not None and output_dimension not in {256, 512, 1024, 1536}:
            raise ValueError("output_dimension must be one of 256, 512, 1024, or 1536")
        if truncate not in {"NONE", "START", "END"}:
            raise ValueError("truncate must be NONE, START, or END")
        parsed = urlsplit(base_url.strip())
        loopback = parsed.hostname in {"localhost", "127.0.0.1", "::1"}
        if (parsed.scheme != "https" and not (parsed.scheme == "http" and loopback)) or not parsed.netloc or parsed.username is not None or parsed.password is not None or parsed.query or parsed.fragment:
            raise ValueError("base_url must be HTTPS, or HTTP on a loopback host, without credentials, query, or fragment")
        self._url = f"{parsed.scheme}://{parsed.netloc}{parsed.path.rstrip('/')}/v2/embed"
        self._model, self._api_key, self._timeout = model, api_key, timeout
        self._output_dimension, self._truncate, self._opener = output_dimension, truncate, opener

    def embed(self, texts: Sequence[str]) -> list[list[float]]:
        return self.embed_documents(texts)

    def embed_documents(self, texts: Sequence[str]) -> list[list[float]]:
        return self._embed(texts, "search_document")

    def embed_query(self, text: str) -> list[float]:
        return self._embed([text], "search_query")[0]

    def _embed(self, texts: Sequence[str], input_type: str) -> list[list[float]]:
        values = list(texts)
        if not values or len(values) > 96 or any(not isinstance(value, str) or not value for value in values):
            raise ValueError("texts must contain between 1 and 96 non-empty strings")
        body: dict[str, Any] = {"model": self._model, "texts": values, "input_type": input_type, "embedding_types": ["float"], "truncate": self._truncate}
        if self._output_dimension is not None:
            body["output_dimension"] = self._output_dimension
        request = Request(self._url, data=json.dumps(body, separators=(",", ":")).encode(), headers={"Accept":"application/json", "Content-Type":"application/json", "Authorization":f"Bearer {self._api_key}", "X-Client-Name":"vectordb", "User-Agent":"vectordb-cohere/dev"}, method="POST")
        try:
            response = self._open(request)
            with response:
                payload = response.read(MAX_RESPONSE_BYTES + 1)
        except HTTPError as error:
            detail = error.read(4096).decode(errors="replace")
            raise RuntimeError(f"Cohere embedding request failed (HTTP {error.code}): {detail}") from error
        except (OSError, URLError) as error:
            raise RuntimeError(f"Cohere embedding request failed: {error}") from error
        if len(payload) > MAX_RESPONSE_BYTES:
            raise RuntimeError("Cohere embedding response exceeds 16 MiB")
        try:
            embeddings = json.loads(payload)["embeddings"]["float"]
        except (UnicodeDecodeError, json.JSONDecodeError, KeyError, TypeError) as error:
            raise RuntimeError("Cohere returned an invalid embedding response") from error
        if not isinstance(embeddings, list) or len(embeddings) != len(values):
            raise RuntimeError("Cohere returned a mismatched embedding count")
        result: list[list[float]] = []
        expected_dimension: int | None = self._output_dimension
        for embedding in embeddings:
            if not isinstance(embedding, list) or not embedding:
                raise RuntimeError("Cohere returned an empty embedding")
            try:
                vector = [float(value) for value in embedding]
            except (TypeError, ValueError) as error:
                raise RuntimeError("Cohere returned a non-numeric embedding") from error
            if any(not math.isfinite(value) for value in vector):
                raise RuntimeError("Cohere returned a non-finite embedding")
            if expected_dimension is None:
                expected_dimension = len(vector)
            if len(vector) != expected_dimension:
                raise RuntimeError("Cohere returned inconsistent embedding dimensions")
            result.append(vector)
        return result

    def _open(self, request: Request) -> Any:
        if self._opener is not None:
            return self._opener(request, timeout=self._timeout)
        return urlopen(request, timeout=self._timeout)


class SemanticStore:
    """Combines any Embedder with the stable VectorDB Python client surface."""

    def __init__(self, client: Any, collection: str, embedder: Embedder) -> None:
        if not collection:
            raise ValueError("collection is required")
        self.client, self.collection, self.embedder = client, collection, embedder

    def upsert_documents(self, documents: Sequence[Document]) -> list[dict[str, Any]]:
        docs = list(documents)
        if not docs:
            raise ValueError("documents are required")
        embed_documents = getattr(self.embedder, "embed_documents", self.embedder.embed)
        vectors = embed_documents([document.text for document in docs])
        records = []
        for document, vector in zip(docs, vectors, strict=True):
            record: dict[str, Any] = {"id": document.id, "vector": vector, "payload": {**dict(document.payload or {}), "text": document.text}}
            if document.metadata is not None:
                record["metadata"] = dict(document.metadata)
            if document.namespace:
                record["namespace"] = document.namespace
            records.append(record)
        return self.client.batch_upsert(self.collection, records)

    def search(self, text: str, top_k: int, *, namespace: str = "", filter: Mapping[str, Any] | None = None) -> list[dict[str, Any]]:
        embed_query = getattr(self.embedder, "embed_query", None)
        vector = embed_query(text) if embed_query is not None else self.embedder.embed([text])[0]
        return self.client.search(self.collection, vector, top_k, namespace=namespace, filter=filter)
