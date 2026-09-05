# WAL binary format

**Maturity:** Experimental  
**Byte order:** Little endian  
**Format version:** 1

Each file is a concatenation of records with no file-level header. Each record
has a fixed 28-byte header followed by `PayloadLength` bytes.

| Offset | Size | Field | Meaning |
|---:|---:|---|---|
| 0 | 4 | Magic | ASCII `VDBW` |
| 4 | 2 | Version | Unsigned format version, currently 1 |
| 6 | 2 | Flags | Reserved; must currently be zero |
| 8 | 4 | PayloadLength | Maximum 67,108,864 bytes |
| 12 | 8 | LSN | Per-file monotonically contiguous sequence, starting at 1 |
| 20 | 1 | Operation | 1=upsert, 2=delete, 3=batch upsert |
| 21 | 3 | Reserved | Must currently be zero |
| 24 | 4 | CRC32C | Castagnoli checksum |
| 28 | variable | Payload | UTF-8 JSON mutation envelope |

CRC32C covers header bytes `[4,24)` followed by the payload. It excludes magic
and the checksum field. Upsert payloads contain a complete record; delete
payloads contain exact namespace and ID.

Readers reject unknown versions, nonzero flags/reserved fields, payloads over
64 MiB, unknown operations, discontinuous LSNs, and
checksum mismatches. Batch-upsert payloads contain 1–10,000 complete records
that all route to the WAL's shard. A short final header or payload is the only automatically
recoverable corruption case. Major layout changes require a new version and a
migration/replay tool; fields will never be reinterpreted silently.
