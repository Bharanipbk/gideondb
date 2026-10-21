# GideonDB documentation

Use these guides to install, evaluate, integrate, and operate GideonDB. Start
with the quickstart, then choose the SDK or integration for your application.

## Start here

- [Quickstart](getting-started/quickstart.md)
- [Lite and embedded mode](getting-started/lite.md)
- [Staged adoption guide](getting-started/adoption.md)
- [Glossary](glossary.md)

## Use the API

- [REST API](api/rest.md)
- [Experimental HNSW index](indexing/hnsw.md)
- [Metadata filtering](indexing/filtering.md)
- [Sparse, keyword (BM25) and hybrid retrieval](indexing/sparse-hybrid.md)
- [Binary quantization](indexing/binary-quantization.md)
- [Product quantization](indexing/product-quantization.md)
- [DiskANN disk-resident index](indexing/diskann.md)
- [Named vectors and float16/bfloat16 storage](indexing/named-vectors.md)
- [Multi-vector (late interaction) search](indexing/multi-vector.md)
- [Reranking (MaxSim and cross-encoders)](indexing/reranking.md)

## Client SDKs

- [Go SDK](sdk/go.md)
- [Python SDK](sdk/python.md)
- [TypeScript SDK](sdk/typescript.md)
- [Java SDK](sdk/java.md)
- [Rust SDK](sdk/rust.md)
- [.NET SDK](sdk/dotnet.md)

## Embedding and framework integrations

- [Ollama semantic-search integration](integrations/ollama.md)
- [Hugging Face semantic-search integration](integrations/huggingface.md)
- [OpenAI-compatible semantic-search integration](integrations/openai-compatible.md)
- [Cohere semantic-search integration](integrations/cohere.md)
- [LangChain integration](integrations/langchain.md)
- [LlamaIndex integration](integrations/llamaindex.md)
- [MCP server for agents](integrations/mcp.md)
- [Agent frameworks: LangGraph, OpenAI Agents, Claude Agent SDK, CrewAI](integrations/agents.md)

## Deploy and operate

- [Docker](operations/docker.md)
- [Kubernetes](operations/kubernetes.md)
- [Helm chart](operations/helm.md)
- [Future AWS ECS storage plan](operations/aws-ecs-storage-plan.md)
- [Production deployment execution plan](operations/production-deployment-plan.md)
- [Configuration](operations/configuration.md)
- [Security](operations/security.md)
- [Backup and restore](operations/backup-restore.md)
- [Observability](operations/observability.md)
- [Administration dashboard](operations/dashboard.md)
- [Rolling upgrades](operations/rolling-upgrades.md)
- [Compatibility and data migration](operations/compatibility.md)
- [Network partitions](operations/network-partitions.md)

## Benchmarks and product planning

- [ANN recall and latency benchmark](benchmarks/ann-recall.md)
- [Gap analysis: open-source and cloud](product/gap-analysis.md)

## Develop and release

- [Continuous integration](development/continuous-integration.md)
- [Contributing](../CONTRIBUTING.md)
- [Project governance](../GOVERNANCE.md)

SDK maintainers: see the [package publishing guide](development/sdk-publishing.md)
for registry setup, validation, and releases.
