# ADR-0007: Use ordinal-based contiguous memory layouts

## Status

Proposed

## Context

Per-record Go objects and pointer-rich graphs impose GC, allocation and cache
cost at millions of records.

## Decision

Map external IDs to dense per-segment ordinals. Store vectors row-major and
HNSW adjacency in packed arrays with offsets. Store metadata column-wise with
dictionary and bitmap indexes.

## Alternatives

Object-per-record layouts are easier to code. Off-heap custom allocators give
more control but increase unsafe memory-management risk.

## Advantages

Fewer allocations, lower pointer chasing, efficient batched distance kernels
and mmap-compatible immutable data.

## Disadvantages

Updates require indirection/new versions, variable data needs side structures,
and codecs are more complex.

## Consequences

All offsets and lengths are validated before access. Memory accounting includes
heap and mmap. Layout changes require versioned formats and migration/rebuild,
while future SIMD can operate on the same contiguous representation.
