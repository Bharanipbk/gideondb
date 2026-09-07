# Immutable checkpoint segment format

**Maturity:** Experimental  
**Byte order:** Little endian  
**Current writer manifest format:** 3

Manifest format 2 references independent record and vector columns. Both use a
fixed 48-byte header followed by the declared payload.

## Column header

| Offset | Size | Field | Meaning |
|---:|---:|---|---|
| 0 | 4 | Magic | `VREC` for records or `VVEC` for vectors |
| 4 | 2 | Version | Column version, currently 1 |
| 6 | 2 | Flags | Reserved, must be zero |
| 8 | 4 | Dimension | Zero for records; collection dimension for vectors |
| 12 | 4 | Reserved | Must be zero |
| 16 | 8 | RecordCount | Number of live records |
| 24 | 8 | MaxLSN | Highest shard WAL LSN covered |
| 32 | 8 | PayloadLength | Exact bytes after the header |
| 40 | 4 | CRC32C | Castagnoli checksum |
| 44 | 4 | Reserved | Must be zero |
| 48 | variable | Payload | Column-specific bytes |

CRC32C covers header bytes `[4,40)`, header bytes `[44,48)`, and payload. Magic
and the checksum field are excluded. File size must equal `48 + PayloadLength`.

The record payload is a JSON array containing ID, optional sparse term weights,
metadata, payload, timestamp, version and namespace—but no dense vectors. The
vector payload is row-major IEEE-754
float32 data in little-endian order and must be exactly
`RecordCount × Dimension × 4` bytes. Record position is the join key between
columns.

Tombstone columns use magic `VTMB`, dimension zero, sorted unique
`namespace + NUL + id` keys encoded as JSON, and the same version-1 header and
CRC32C envelope. Their header count is the number of deletion keys.

`MANIFEST.json` format 2 contains basename-only `records_file` and
`vectors_file`, dimension, maximum LSN and record count. Both column files are
fully durable before the manifest is atomically published. Recovery requires
both columns and the manifest to agree.

## Legacy combined format 1

Legacy checkpoints remain readable. They use a fixed 40-byte `VSEG` header
followed by one JSON array containing complete records and vectors.

| Offset | Size | Field | Meaning |
|---:|---:|---|---|
| 0 | 4 | Magic | ASCII `VSEG` |
| 4 | 2 | Version | Unsigned format version, currently 1 |
| 6 | 2 | Flags | Reserved, must be zero |
| 8 | 8 | PayloadLength | JSON bytes following the header; maximum 16 GiB |
| 16 | 8 | RecordCount | Number of live records encoded |
| 24 | 8 | MaxLSN | Highest shard WAL LSN covered by the checkpoint |
| 32 | 4 | CRC32C | Castagnoli checksum |
| 36 | 4 | Reserved | Must be zero |
| 40 | variable | Payload | JSON array of complete vector records |

CRC32C covers header bytes `[4,32)`, header bytes `[36,40)`, and the payload in
that order. The magic and checksum field are excluded. Readers require EOF
immediately after the declared payload.

Legacy manifests contain a basename-only `segment_file`, maximum LSN and record
count. Both manifest versions use same-directory temporary file, file fsync,
rename and directory fsync. Unknown versions, nonzero reserved fields, unsafe
filenames, overflow, truncation, trailing bytes and checksum mismatch fail
recovery.

## Multi-segment manifest format 3

Format 3 keeps the top-level live record count, dimension, and maximum covered
LSN and adds an ordered `segments` array. Each entry declares basename-only
record/vector files, optional graph/filter/tombstone files, dimension, record
count, maximum LSN, and an optional measured byte size. Readers reject empty or
more-than-16-entry arrays, unsafe names, dimension mismatches, non-increasing
segment LSNs, entries beyond the manifest LSN, and a newest entry that does not
end exactly at the manifest LSN.

The checkpoint writer publishes format 3 with delta record/vector bundles and
sorted, checksummed tombstone columns. Recovery applies entries from oldest to
newest and validates the final live count. Flat recovery resolves winning
record locations first and maps their original vector columns through a
composite source, avoiding vector-payload copies. Existing format-2 checkpoints
are promoted by reference on their next flush or by the offline
`gideondb -migrate-data` command; format-1 checkpoints are rewritten into
current columns. Existing fields are never reinterpreted. The current reader
accepts manifest formats 1–3 and rejects unknown future formats. See the
[compatibility and migration contract](../operations/compatibility.md) for the
supported upgrade and downgrade policy.
