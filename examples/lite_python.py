"""Use GideonDB from Python without running a server yourself.

LiteServer starts `gideondb -lite` on a free loopback port, hands you a
client, and stops the process on exit. Install the binary first:

    go install github.com/Bharanipbk/gideondb/cmd/gideondb@latest
"""

from gideondb_integrations import LiteServer

with LiteServer("./gideondb-data") as db:
    try:
        db.create_collection({"name": "notes", "dimension": 3, "metric": "cosine", "shard_count": 1})
    except Exception as error:
        if getattr(error, "status_code", None) != 409:
            raise
    db.batch_upsert("notes", [
        {"id": "1", "vector": [1, 0, 0], "metadata": {"topic": "go"}},
        {"id": "2", "vector": [0, 1, 0], "metadata": {"topic": "python"}},
    ])
    print(db.search("notes", [0.9, 0.1, 0], 1))
