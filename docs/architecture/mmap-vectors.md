# Mapped immutable vectors

**Maturity:** Experimental production path

`OpenMappedVectors` opens a format-2 `VVEC` file read-only, maps the complete
file with `MAP_SHARED`, validates header fields, manifest dimension/count/LSN,
exact length and CRC32C, then exposes an immutable scoring source. Invalid data
is unmapped before an error is returned.

The mapped flat index stores only stable ordinal IDs on the Go heap. Search pins
the mapping with one read lease, scans vectors directly from mapped pages, and
uses the same bounded top-k heap and canonical distance kernels as heap-backed
flat search. Closing takes the write side of the lease, so unmapping cannot race
an active search. Fetching a vector explicitly returns a copy because callers
must not retain memory beyond the mapping lifetime.

## Zero-copy representation

The file format is little-endian and the payload begins at aligned offset 48.
On native little-endian systems with a 4-byte-aligned payload, a narrowly scoped
`unsafe.Slice` creates a read-only `[]float32` view over mapped bytes. The view
is never returned publicly, mutated, resized, or retained after `munmap`. Other
endianness/alignment cases use portable little-endian decoding.

Supported mmap builds are Darwin, Linux, FreeBSD, NetBSD, OpenBSD and DragonFly
BSD. Other targets compile with an explicit unsupported-feature error. mmap is
an optimization, not a persistent-format requirement.

## Accounting and limitations

Mapped index statistics report mapped bytes separately from owned vector heap
bytes. Virtual mapped size is not resident-set size; OS page residency must be
observed separately.

For format-3 flat collections, recovery reads record columns to resolve every
live key to a winning segment and row ordinal, applying later replacements and
tombstones without decoding vector columns. A composite mapped source opens all
referenced `VVEC` files and presents only those winning locations as one logical
flat index. Search holds read leases across the component mappings and scores
the selected rows in place. Explicit record fetch still copies only the
requested vector.

WAL replay and later writes populate a mutable delta. Active keys shadow the
composite base, tombstones suppress deleted base values, and searches merge both
result streams. Checkpoint publication resets the covered WAL before atomically
replacing the generation and clearing the delta. Reference-counted snapshot pins
keep every component mapping alive until its last reader releases it.

HNSW collections decode checkpoint vectors because their persisted full-view
graph currently owns a contiguous float32 arena.
