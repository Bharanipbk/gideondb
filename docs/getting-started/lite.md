# Lite and embedded mode

**Maturity:** Experimental

GideonDB has two zero-setup ways to run on one machine, SQLite style:

- **Embedded Go library** (`pkg/lite`): the engine runs inside your Go
  process. No server, port or credentials.
- **Lite binary** (`gideondb -lite`): the normal server with no
  configuration, listening on loopback only. Any SDK, `curl` or the MCP server
  can use it, and Python can start it for you.

Both store data in the same on-disk format as a full server, so a directory
you prototype with can later be served by `gideondb -data-path <dir>`. Only
one process may open a data directory at a time.

## Embedded Go library

```bash
go get github.com/Bharanipbk/gideondb
```

```go
import "github.com/Bharanipbk/gideondb/pkg/lite"

db, err := lite.Open("./data", nil) // creates the directory if needed
if err != nil { return err }
defer db.Close()

err = db.CreateCollection(lite.Collection{Name: "notes", Dimension: 3, Metric: lite.Cosine})
err = db.Upsert("notes",
    lite.Record{ID: "1", Vector: []float32{1, 0, 0}, Text: "Go makes concurrency simple", Metadata: map[string]any{"topic": "go"}},
    lite.Record{ID: "2", Vector: []float32{0, 1, 0}, Text: "Python is great for notebooks"},
)

hits, err := db.Search("notes", lite.Query{Vector: []float32{1, 0.1, 0}, TopK: 5})            // dense
hits, err = db.Search("notes", lite.Query{Vector: v, TopK: 5, Filter: map[string]any{"topic": "go"}}) // filtered
hits, err = db.Search("notes", lite.Query{Text: "notebooks", TopK: 5})                          // BM25 keyword
hits, err = db.Search("notes", lite.Query{Vector: v, Text: "concurrency", Fusion: lite.FusionRRF}) // hybrid
```

A runnable version is in [`examples/lite-go`](../../examples/lite-go/main.go):

```bash
go run ./examples/lite-go
```

### API

| Call | Purpose |
|---|---|
| `Open(path, *Options)` / `Close()` | Open or create a data directory; close flushes the WAL |
| `CreateCollection`, `Collections`, `Count`, `DeleteCollection` | Collection lifecycle |
| `Upsert(collection, records...)` | Insert or replace up to 10,000 records atomically |
| `Get`, `Delete` | One record by namespace and ID |
| `Search(collection, Query)` | Dense, keyword, sparse or hybrid search, chosen by which of `Vector`, `Text`, `Sparse` are set |

- `Collection` defaults to one shard and an exact flat index; set
  `Index: lite.Index{Type: lite.IndexHNSW}` for HNSW, or
  `Quantization: "binary"` on a flat index.
- `Query.Filter` uses the REST [filter syntax](../indexing/filtering.md).
  `Query.Alpha` (dense weight, default 0.5) and `Query.Fusion` control hybrid
  ranking as in [sparse and hybrid retrieval](../indexing/sparse-hybrid.md).
- Larger `Result.Score` is always better.
- Errors wrap `lite.ErrNotFound`, `ErrAlreadyExists`, `ErrDimensionMismatch`,
  `ErrInvalidArgument` and `ErrClosed`; test with `errors.Is`.
- `Options{AsyncWAL: true}` skips the per-write fsync for bulk loads; a clean
  `Close` still persists everything.
- A `DB` is safe for concurrent use.

`pkg/lite` exposes no internal types, so it stays stable while the engine
changes. Server-only features (replication, authentication, server-side
embedding, the dashboard) are not part of it.

## Lite binary

```bash
go install github.com/Bharanipbk/gideondb/cmd/gideondb@latest
gideondb -lite
```

```text
GideonDB 0.1.0 — lite mode (single node, loopback only, no authentication)
  REST API   http://127.0.0.1:6333/v1   (try: curl http://127.0.0.1:6333/v1/health)
  Dashboard  off (set GIDEONDB_DASHBOARD_USERNAME and GIDEONDB_DASHBOARD_PASSWORD to enable)
  Data       /home/you/.gideondb/lite
```

`-lite` (or `GIDEONDB_LITE=true`) changes these defaults:

| Setting | Normal server | Lite |
|---|---|---|
| Data directory | `./data` | `~/.gideondb/lite` unless `-data-path` or `GIDEONDB_DATA_PATH` is set |
| Authentication | API key needed while the dashboard is on | none |
| Dashboard | always on; new directories need credentials | off unless dashboard credentials are configured or already stored |
| TLS | optional, `-tls-self-signed` in Docker | off (`-tls-self-signed` is ignored) |
| Listen address | refuses unauthenticated non-loopback | loopback only; anything else is an error |

Lite refuses `-peers`, an API key and a principals file, because each would
imply a network-facing deployment. Other flags (`-http-address
127.0.0.1:7000`, `-grpc-address 127.0.0.1:6334`, `-wal-sync async`) work as
usual.

## From Python

GideonDB is written in Go, so there is no in-process Python wheel. Instead,
`LiteServer` from the integrations package runs `gideondb -lite` as a child
process on a free loopback port and returns a normal client:

```python
from gideondb_integrations import LiteServer

with LiteServer("./gideondb-data") as db:   # gideondb.Client
    db.create_collection({"name": "notes", "dimension": 3, "metric": "cosine", "shard_count": 1})
    db.batch_upsert("notes", [{"id": "1", "vector": [1, 0, 0]}])
    print(db.search("notes", [1, 0, 0], 1))
```

It finds the binary from the `binary=` argument, `GIDEONDB_BINARY` or `PATH`,
waits for `/v1/ready`, and stops the process when the block exits. This suits
notebooks, tests and desktop tools. See
[`examples/lite_python.py`](../../examples/lite_python.py).

## When to move to a server

Use a regular server (Docker, [Helm](../operations/helm.md)) when anything
other than the local user must reach the data, when you need authentication
or TLS, or when you need replication.
