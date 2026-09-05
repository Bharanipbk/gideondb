# Immutable checkpoint segment format

**Maturity:** Experimental  
**Byte order:** Little endian  
**Current manifest format:** 2

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

The record payload is a JSON array containing ID, metadata, payload, timestamp,
version and namespace—but no vectors. The vector payload is row-major IEEE-754
float32 data in little-endian order and must be exactly
`RecordCount × Dimension × 4` bytes. Record position is the join key between
columns.

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

Future metadata/index files require new magic/versioned formats and an atomic
manifest schema upgrade; existing fields will not be reinterpreted.
