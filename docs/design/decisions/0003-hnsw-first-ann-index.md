# ADR-0003: Use flat search as oracle and HNSW as the first ANN index

## Status

Proposed

## Context

The first release needs exact correctness validation and a practical ANN index
for memory-resident and mapped segments.

## Decision

Ship a flat index and a pluggable HNSW index with packed ordinal adjacency,
tombstone deletion and rebuild during compaction.

## Alternatives

IVF/PQ reduces memory but adds training and recall complexity. DiskANN-style
indexes better target disk-scale workloads but are substantially harder to
build and tune correctly.

## Advantages

HNSW is incremental, well understood and benchmarkable against flat search.
The index interface preserves future alternatives.

## Disadvantages

HNSW consumes significant RAM, construction is order-sensitive, and filtering
and concurrent mutation are subtle.

## Consequences

Recall, build time and bytes/vector are release metrics. Corrupt graph files are
rejected and rebuildable from segment vectors. Quantized and disk indexes need
new ADRs rather than changing HNSW's file format silently.
