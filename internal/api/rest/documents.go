package rest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/ingest"
)

// maxDocumentBytes bounds one uploaded file.
const maxDocumentBytes = 32 << 20

// documentEmbedBatch is how many chunks are embedded per provider call.
const documentEmbedBatch = 64

var documentID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,199}$`)

// ingestDocument serves POST /v1/collections/{name}/documents: a multipart
// upload whose "file" part is extracted, chunked, embedded with the
// collection vectorizer and stored as one record per chunk. Re-uploading a
// document ID replaces its chunks.
func (s *Server) ingestDocument(w http.ResponseWriter, r *http.Request) {
	if s.rejectStandaloneDataPath(w) {
		return
	}
	name := r.PathValue("name")
	config, _, err := s.engine.DescribeCollection(name)
	if err != nil {
		writeError(w, err)
		return
	}
	if config.Vectorizer == "" {
		writeError(w, fmt.Errorf("%w: document ingestion needs a collection vectorizer to embed chunks", core.ErrInvalidArgument))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxDocumentBytes+1<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeError(w, fmt.Errorf("%w: expected multipart/form-data with a file part of at most %d bytes: %v", core.ErrInvalidArgument, maxDocumentBytes, err))
		return
	}
	defer r.MultipartForm.RemoveAll()
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, fmt.Errorf("%w: the file part is required", core.ErrInvalidArgument))
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxDocumentBytes+1))
	if err != nil {
		writeError(w, err)
		return
	}
	if len(data) > maxDocumentBytes || len(data) == 0 {
		writeError(w, fmt.Errorf("%w: file must be 1 to %d bytes", core.ErrInvalidArgument, maxDocumentBytes))
		return
	}
	id := r.FormValue("id")
	if id == "" {
		id = strings.TrimSuffix(filepath.Base(header.Filename), filepath.Ext(header.Filename))
	}
	if !documentID.MatchString(id) {
		writeError(w, fmt.Errorf("%w: document id must match %s", core.ErrInvalidArgument, documentID))
		return
	}
	namespace := r.FormValue("namespace")
	metadata := map[string]any{}
	if raw := r.FormValue("metadata"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
			writeError(w, fmt.Errorf("%w: metadata must be a JSON object", core.ErrInvalidArgument))
			return
		}
	}
	options := ingest.ChunkOptions{}
	for field, target := range map[string]*int{"chunk_size": &options.Size, "chunk_overlap": &options.Overlap} {
		if raw := r.FormValue(field); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil {
				writeError(w, fmt.Errorf("%w: %s must be an integer", core.ErrInvalidArgument, field))
				return
			}
			*target = value
		}
	}
	format, err := ingest.DetectFormat(header.Header.Get("Content-Type"), header.Filename, data)
	if err == nil {
		var document ingest.Document
		if document, err = ingest.Extract(format, data); err == nil {
			s.storeDocument(w, r, name, namespace, id, header.Filename, document, metadata, options)
			return
		}
	}
	if errors.Is(err, ingest.ErrUnsupported) {
		writeJSON(w, http.StatusUnsupportedMediaType, apiError{Code: "unsupported_document", Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "extraction_failed", Message: err.Error()})
}

func (s *Server) storeDocument(w http.ResponseWriter, r *http.Request, collection, namespace, id, filename string, document ingest.Document, metadata map[string]any, options ingest.ChunkOptions) {
	chunks, err := ingest.Split(document.Text, options)
	if err != nil {
		writeError(w, fmt.Errorf("%w: %v", core.ErrInvalidArgument, err))
		return
	}
	if len(chunks) == 0 {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "extraction_failed", Message: "the document contains no text"})
		return
	}
	stored := 0
	for first := 0; first < len(chunks); first += documentEmbedBatch {
		batch := chunks[first:min(first+documentEmbedBatch, len(chunks))]
		inputs := make([]textRecord, len(batch))
		for i, chunk := range batch {
			index := first + i
			chunkMetadata := make(map[string]any, len(metadata)+6)
			for key, value := range metadata {
				chunkMetadata[key] = value
			}
			chunkMetadata["document_id"] = id
			chunkMetadata["chunk_index"] = index
			chunkMetadata["chunk_count"] = len(chunks)
			chunkMetadata["content_format"] = string(document.Format)
			if document.Title != "" {
				chunkMetadata["title"] = document.Title
			}
			if filename != "" {
				chunkMetadata["source"] = filepath.Base(filename)
			}
			inputs[i].Record = core.Record{ID: chunkID(id, index), Namespace: namespace, Text: chunk.Text, Metadata: chunkMetadata,
				Payload: map[string]any{"char_start": chunk.Start, "char_end": chunk.End}}
		}
		records, ok := s.resolveRecords(r.Context(), w, collection, inputs)
		if !ok {
			return
		}
		if _, err := s.engine.BatchUpsert(collection, records); err != nil {
			writeError(w, fmt.Errorf("after storing %d of %d chunks: %w", stored, len(chunks), err))
			return
		}
		stored += len(records)
	}
	// Remove chunks left over from a longer earlier version.
	removed := 0
	for index := len(chunks); ; index++ {
		if err := s.engine.Delete(collection, namespace, chunkID(id, index)); err != nil {
			break
		}
		removed++
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"document_id": id, "title": document.Title, "format": document.Format, "chunks": len(chunks),
		"characters": ingest.RuneLen(document.Text), "replaced_chunks_removed": removed,
	})
}

// chunkID is the record ID of a document chunk; the fixed-width index keeps
// chunks in order when scrolled.
func chunkID(document string, index int) string { return fmt.Sprintf("%s#%05d", document, index) }

// DocumentIngestHandler exposes the document upload handler for the gRPC
// transport, which authenticates before calling it.
func (s *Server) DocumentIngestHandler() http.Handler { return http.HandlerFunc(s.ingestDocument) }
