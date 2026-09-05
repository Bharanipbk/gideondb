# ADR-0001: Use Go for the core server

## Status

Proposed

## Context

The server needs portable networking, concurrency, profiling and predictable
deployment while retaining access to low-allocation data structures.

## Decision

Implement the core server in Go. Use TypeScript for the dashboard and native
SDK languages for clients. Add assembly or Rust acceleration only behind a
portable Go path and only with benchmark evidence.

## Alternatives

Rust offers tighter memory control but a steeper contributor and development
cost. C++ offers mature SIMD libraries but increases memory-safety and build
risk. Java provides a mature runtime but conflicts with the desired deployment
and memory-control profile.

## Advantages

Simple binaries, strong concurrency/tooling, broad contributors, race detector
and mature network ecosystem.

## Disadvantages

Garbage collection and bounds checks require deliberate hot-path layouts; SIMD
support is less direct.

## Consequences

Performance work must measure allocations and GC. Unsafe code and assembly need
portable fallbacks, focused review and equivalence tests. Process crashes remain
possible, so durability never relies on memory safety alone.
