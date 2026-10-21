"""In-memory stand-in for gideondb.Client used by adapter tests."""

import math


class FakeAPIError(Exception):
    def __init__(self, status_code):
        super().__init__(f"HTTP {status_code}")
        self.status_code = status_code


def _match(metadata, condition):
    for key, expected in condition.items():
        if key == "$and":
            if not all(_match(metadata, part) for part in expected):
                return False
            continue
        actual = metadata.get(key)
        if isinstance(expected, dict):
            for op, operand in expected.items():
                if op == "$gt" and not (actual is not None and actual > operand):
                    return False
        elif actual != expected:
            return False
    return True


class FakeClient:
    def __init__(self):
        self.collections, self.records = {}, {}

    def describe_collection(self, name):
        if name not in self.collections:
            raise FakeAPIError(404)
        return self.collections[name]

    def create_collection(self, config):
        self.collections[config["name"]] = dict(config)
        return config

    def upsert(self, collection, record):
        if collection not in self.collections:
            raise FakeAPIError(404)
        self.records[(collection, record.get("namespace", ""), record["id"])] = dict(record)
        return record

    def get(self, collection, record_id, *, namespace=""):
        try:
            return self.records[(collection, namespace, record_id)]
        except KeyError:
            raise FakeAPIError(404) from None

    def delete(self, collection, record_id, *, namespace=""):
        if self.records.pop((collection, namespace, record_id), None) is None:
            raise FakeAPIError(404)

    def search(self, collection, vector, top_k, *, namespace="", filter=None):
        if collection not in self.collections:
            raise FakeAPIError(404)
        def cosine(a, b):
            return sum(x * y for x, y in zip(a, b)) / (math.sqrt(sum(x * x for x in a)) * math.sqrt(sum(y * y for y in b)) or 1)
        hits = [
            {**record, "score": cosine(vector, record["vector"])}
            for (name, ns, _), record in self.records.items()
            if name == collection and ns == namespace and _match(record.get("metadata", {}), filter or {})
        ]
        hits.sort(key=lambda hit: hit["score"], reverse=True)
        return hits[:top_k]

    def scroll(self, collection, *, namespace="", limit=50, cursor=""):
        if collection not in self.collections:
            raise FakeAPIError(404)
        rows = sorted(
            (record for (name, ns, _), record in self.records.items() if name == collection and (not namespace or ns == namespace)),
            key=lambda record: (record.get("namespace", ""), record["id"]),
        )
        start = int(cursor or 0)
        page = rows[start : start + limit]
        nxt = str(start + limit) if start + limit < len(rows) else ""
        return {"records": page, "next_cursor": nxt}


class KeywordEmbedder:
    """Deterministic 3-d embeddings: counts of three topic words."""

    WORDS = ("coffee", "python", "travel")

    def embed(self, texts):
        return [[float(text.lower().count(word)) + 0.01 for word in self.WORDS] for text in texts]
