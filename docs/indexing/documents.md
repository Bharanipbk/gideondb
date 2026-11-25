# Document ingestion

Upload whole documents and GideonDB extracts their text, splits it into
overlapping chunks, embeds each chunk with the collection's
[vectorizer](server-side-embedding.md) and stores one searchable record per
chunk. This is similar to Azure AI Search indexers with the text-split
skill.

```bash
curl -fsS -H "Authorization: Bearer $KEY" \
  -F file=@handbook.pdf -F id=handbook -F 'metadata={"team":"hr","year":2026}' \
  -F chunk_size=1000 -F chunk_overlap=150 \
  http://127.0.0.1:6333/v1/collections/kb/documents
```

```json
{"document_id": "handbook", "title": "Employee Handbook", "format": "pdf", "chunks": 48, "characters": 41207, "replaced_chunks_removed": 0}
```

| Format | Detected by | Extraction |
|---|---|---|
| Text | `text/plain`, `.txt`, valid UTF-8 | As is |
| Markdown | `text/markdown`, `.md` | Link targets, emphasis marks and code fences removed. The first heading becomes the title |
| HTML | `text/html`, `.html`, `<html` | Visible text with paragraph breaks. Scripts, styles and `<head>` are skipped. `<title>` becomes the title |
| PDF | `application/pdf`, `.pdf`, `%PDF-` | Text operators in page order, with word and line breaks from spacing. The document info title becomes the title |
| DOCX | Word MIME type, `.docx` | Paragraphs from `word/document.xml`. The core-properties title becomes the title |

- **Chunking:** chunks hold at most `chunk_size` characters (100–8,000,
  default 1,000). Each one is cut at the last paragraph break, sentence end or
  space in its window. The next chunk repeats the last `chunk_overlap`
  characters (default 150, at most half the size), starting on a word.
- **Records:** chunk IDs are `<id>#00000`, `<id>#00001`, and so on. Each
  chunk's metadata holds your `metadata` plus `document_id`, `chunk_index`,
  `chunk_count`, `content_format`, `title` and `source` (the file name).
  `payload` holds the chunk's `char_start` and `char_end` in the extracted
  text. Filter with `{"document_id": "handbook"}` to search one document.
- **Re-upload:** uploading the same `id` again replaces its chunks and deletes
  any extra chunks from a longer earlier version.
- **Requirements:** the collection must have a `vectorizer` (400 otherwise).
  Chunks are embedded 64 at a time. Combine with an
  [analyzer](full-text.md#analyzers), [query syntax](query-syntax.md) and
  [highlighting](full-text.md#highlighting) for hybrid retrieval over documents.
- **Limits:** files can be up to 32 MiB and 16 MiB of extracted text, with up to 5,000
  chunks. Scanned PDFs (images only) return 422, so run OCR first. An
  unknown binary format returns 415. Encrypted PDFs and legacy `.doc` files
  are not supported.
- **Not atomic:** a failure part-way, such as from the embedding provider,
  leaves the chunks stored so far. Re-upload to finish.
- **Cluster:** the endpoint stores through the local engine. With static routing
  enabled it returns 409, like the other single-node data endpoints.
