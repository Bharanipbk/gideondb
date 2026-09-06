# VectorDB documentation

This directory is a versioned part of the product. A change to architecture,
public APIs, configuration, persistent formats, protocols, guarantees, or
runtime behavior is incomplete until the corresponding documentation changes.

## Design-stage documents

- [Comprehensive technical design](technical-design.md)
- [Architecture decision records](design/decisions/README.md)
- [Roadmap](roadmap.md)
- [Glossary](glossary.md)

## Implemented documentation

- [Quickstart](getting-started/quickstart.md)
- [Staged adoption guide](getting-started/adoption.md)
- [Phase 1 architecture](architecture/phase-1.md)
- [REST API](api/rest.md)
- [OpenAPI description](api/openapi.yaml)
- [gRPC API contract](api/grpc.md)
- [Experimental HNSW index](indexing/hnsw.md)
- [WAL and recovery](architecture/wal.md)
- [WAL binary format](internals/wal-format.md)
- [Segment lifecycle](architecture/segments.md)
- [Segment binary format](internals/segment-format.md)
- [Metadata filtering](indexing/filtering.md)
- [Metadata filter optimization baseline](benchmarks/metadata-filtering.md)
- [Checkpoint column read baseline](benchmarks/checkpoint-columns.md)
- [Mapped vector architecture](architecture/mmap-vectors.md)
- [Mapped flat-search baseline](benchmarks/mapped-flat-search.md)
- [Three-node static-cluster functional gate](benchmarks/three-node-functional.md)
- [Replication architecture](architecture/replication.md)
- [Kubernetes deployment](operations/kubernetes.md)
- [Go SDK](sdk/go.md)
- [Python SDK](sdk/python.md)
- [TypeScript SDK](sdk/typescript.md)
- [Java SDK](sdk/java.md)
- [Rust SDK](sdk/rust.md)
- [.NET SDK](sdk/dotnet.md)
- [Administration dashboard](operations/dashboard.md)
- [Ollama semantic-search integration](integrations/ollama.md)
- [Hugging Face semantic-search integration](integrations/huggingface.md)
- [OpenAI-compatible semantic-search integration](integrations/openai-compatible.md)
- [Cohere semantic-search integration](integrations/cohere.md)
- [LangChain integration](integrations/langchain.md)
- [LlamaIndex integration](integrations/llamaindex.md)

Implementation-specific guides will be added with the implementation they
describe. Empty placeholder documents are deliberately avoided because they
create a false impression that behavior has been specified or implemented.

## Documentation policy

Statements use one of these maturity labels:

- **Implemented**: covered by tests in the current tree.
- **Experimental**: implemented, but compatibility is not promised.
- **Planned**: accepted design, not yet implemented.
- **Proposed**: under review.

Claims about performance must identify hardware, dimensions, dataset size,
index settings, concurrency, recall, storage settings, and benchmark method.
